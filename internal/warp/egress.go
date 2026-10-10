package warp

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const traceURL = "https://www.cloudflare.com/cdn-cgi/trace"
const maxTraceSize = 16 << 10

// EgressResult describes an actual HTTPS request through the local SOCKS proxy.
// Reaching Cloudflare alone is not proof that the request used WARP.
type EgressResult struct {
	Verified bool   `json:"verified"`
	IP       string `json:"ip"`
	Location string `json:"location"`
	Colo     string `json:"colo"`
	WARP     string `json:"warp"`
	Proxy    string `json:"proxy"`
	Raw      string `json:"raw"`
}

// VerifyEgress is read-only: it neither starts nor reconnects the WARP client.
func (m *Manager) VerifyEgress(ctx context.Context) (EgressResult, error) {
	if !m.mu.TryLock() {
		return EgressResult{}, fmt.Errorf("WARP 操作正在执行，请完成后再验证出口")
	}
	defer m.mu.Unlock()
	if _, err := m.lookup("warp-cli"); err != nil {
		return EgressResult{}, fmt.Errorf("WARP 未安装，请先安装")
	}
	if !m.active(Unit) {
		return EgressResult{}, fmt.Errorf("WARP 服务未运行，请先连接")
	}
	settings, err := m.query("settings")
	if err != nil {
		return EgressResult{}, fmt.Errorf("读取代理设置失败：%w", err)
	}
	mode, port := parseSettings(settings)
	if mode != "proxy" || port == 0 {
		return EgressResult{}, fmt.Errorf("客户端尚未配置有效的 proxy 模式和端口，请先保存代理设置")
	}
	return m.trace(ctx, port)
}

func traceClient(port int) *http.Client {
	proxy := &url.URL{Scheme: "socks5h", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	// A dedicated transport always uses this proxy and resolves the target at
	// the SOCKS server. It never falls back to direct traffic or environment proxies.
	return &http.Client{
		Transport:     &http.Transport{Proxy: http.ProxyURL(proxy), DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second},
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func verifyTrace(ctx context.Context, port int) (EgressResult, error) {
	client := traceClient(port)
	defer client.CloseIdleConnections()
	return requestTrace(ctx, client, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

func requestTrace(ctx context.Context, client *http.Client, proxy string) (EgressResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	if err != nil {
		return EgressResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return EgressResult{}, fmt.Errorf("通过 SOCKS5 代理 %s 请求出口失败，请确认 WARP 已连接：%w", proxy, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EgressResult{}, fmt.Errorf("Cloudflare 出口验证返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTraceSize+1))
	if err != nil {
		return EgressResult{}, fmt.Errorf("读取出口验证响应失败：%w", err)
	}
	if len(body) > maxTraceSize {
		return EgressResult{}, fmt.Errorf("出口验证响应过大")
	}
	result, err := parseTrace(string(body))
	if err != nil {
		return EgressResult{}, err
	}
	result.Proxy = proxy
	return result, nil
}

func parseTrace(raw string) (EgressResult, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if _, exists := fields[key]; exists {
			return EgressResult{}, fmt.Errorf("Cloudflare trace 响应含重复字段")
		}
		fields[key] = strings.TrimSpace(value)
	}
	if net.ParseIP(fields["ip"]) == nil {
		return EgressResult{}, fmt.Errorf("Cloudflare trace 响应缺少有效的出口 IP")
	}
	flag := fields["warp"]
	if flag != "on" && flag != "plus" && flag != "off" {
		return EgressResult{}, fmt.Errorf("Cloudflare trace 响应缺少有效的 WARP 状态")
	}
	return EgressResult{Verified: flag == "on" || flag == "plus", IP: fields["ip"], Location: fields["loc"], Colo: fields["colo"], WARP: flag, Raw: strings.TrimSpace(raw)}, nil
}
