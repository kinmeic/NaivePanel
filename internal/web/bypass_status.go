package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type bypassControlStatus struct {
	Running        bool                   `json:"running"`
	Ready          bool                   `json:"ready"`
	StartedAt      string                 `json:"startedAt"`
	ConfigHash     string                 `json:"configHash"`
	ConfigRevision uint64                 `json:"configRevision"`
	GeodataLoaded  bool                   `json:"geodataLoaded"`
	Inbounds       []bypassInboundStatus  `json:"inbounds"`
	Outbounds      []bypassOutboundStatus `json:"outbounds"`
	DNSUpstreams   []json.RawMessage      `json:"dnsUpstreams"`
	LastReload     bypassLastReloadStatus `json:"lastReload"`
}

type bypassInboundStatus struct {
	Tag       string `json:"tag"`
	Type      string `json:"type"`
	Listen    string `json:"listen"`
	State     string `json:"state"`
	Running   bool   `json:"running"`
	LastError string `json:"lastError"`
	UpdatedAt string `json:"updatedAt"`
}

type bypassOutboundStatus struct {
	Tag         string `json:"tag"`
	LastSuccess string `json:"lastSuccess"`
	LastFailure string `json:"lastFailure"`
	LastError   string `json:"lastError"`
}

type bypassLastReloadStatus struct {
	Success bool   `json:"success"`
	At      string `json:"at"`
	Error   string `json:"error"`
}

type bypassStatusView struct {
	HealthLabel      string
	HealthClass      string
	Uptime           string
	StartedAt        string
	ConfigRevision   string
	ConfigHash       string
	GeodataLabel     string
	GeodataClass     string
	DNSUpstreamCount int
	Inbounds         []bypassInboundView
	Outbounds        []bypassOutboundView
	LastReload       string
	LastReloadClass  string
	LastReloadError  string
}

type bypassInboundView struct {
	Tag         string
	Description string
	Listen      string
	State       string
	StateClass  string
	LastError   string
}

type bypassOutboundView struct {
	Tag         string
	State       string
	StateClass  string
	LastSuccess string
	LastFailure string
	LastError   string
}

func newBypassStatusView(raw json.RawMessage, now time.Time) (bypassStatusView, error) {
	var status bypassControlStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return bypassStatusView{}, fmt.Errorf("解析控制面状态: %w", err)
	}

	view := bypassStatusView{
		HealthLabel:      "未就绪",
		HealthClass:      "warn",
		ConfigRevision:   fmt.Sprintf("第 %d 版", status.ConfigRevision),
		ConfigHash:       abbreviateHash(status.ConfigHash),
		GeodataLabel:     "未加载",
		GeodataClass:     "bad",
		DNSUpstreamCount: len(status.DNSUpstreams),
		LastReload:       "本次启动后尚未热重载",
		LastReloadClass:  "muted",
	}
	if status.Running && status.Ready {
		view.HealthLabel = "运行正常"
		view.HealthClass = "ok"
	} else if status.Running {
		view.HealthLabel = "正在运行，部分组件未就绪"
	}
	if status.GeodataLoaded {
		view.GeodataLabel = "已加载"
		view.GeodataClass = "ok"
	}
	if started, ok := parseControlTime(status.StartedAt); ok {
		view.StartedAt = started.Local().Format("2006-01-02 15:04:05")
		if now.After(started) {
			view.Uptime = formatChineseDuration(now.Sub(started))
		}
	}
	if view.StartedAt == "" {
		view.StartedAt = "未知"
	}
	if view.Uptime == "" {
		view.Uptime = "未知"
	}

	for _, inbound := range status.Inbounds {
		state, class := friendlyInboundState(inbound.State, inbound.Running)
		view.Inbounds = append(view.Inbounds, bypassInboundView{
			Tag:         fallbackLabel(inbound.Tag, "未命名入站"),
			Description: friendlyInboundType(inbound.Type),
			Listen:      fallbackLabel(inbound.Listen, "未报告"),
			State:       state,
			StateClass:  class,
			LastError:   inbound.LastError,
		})
	}
	for _, outbound := range status.Outbounds {
		lastSuccess, successOK := parseControlTime(outbound.LastSuccess)
		lastFailure, failureOK := parseControlTime(outbound.LastFailure)
		state := "等待首次连接"
		class := "muted"
		switch {
		case failureOK && (!successOK || lastFailure.After(lastSuccess)):
			state, class = "最近连接失败", "bad"
		case successOK:
			state, class = "最近连接成功", "ok"
		}
		view.Outbounds = append(view.Outbounds, bypassOutboundView{
			Tag:         fallbackLabel(outbound.Tag, "未命名出站"),
			State:       state,
			StateClass:  class,
			LastSuccess: formatControlTime(lastSuccess, successOK),
			LastFailure: formatControlTime(lastFailure, failureOK),
			LastError:   outbound.LastError,
		})
	}
	if reloaded, ok := parseControlTime(status.LastReload.At); ok {
		view.LastReload = "最近热重载：" + reloaded.Local().Format("2006-01-02 15:04:05")
		if status.LastReload.Success {
			view.LastReload += "，已成功生效"
			view.LastReloadClass = "ok"
		} else {
			view.LastReload += "，执行失败"
			view.LastReloadClass = "bad"
			view.LastReloadError = status.LastReload.Error
		}
	}
	return view, nil
}

func friendlyInboundState(state string, running bool) (string, string) {
	switch strings.ToLower(state) {
	case "running":
		return "正常监听", "ok"
	case "starting":
		return "正在启动", "warn"
	case "failed":
		return "运行失败", "bad"
	case "stopped":
		return "已停止", "muted"
	}
	if running {
		return "正常监听", "ok"
	}
	if strings.TrimSpace(state) == "" {
		return "状态未知", "muted"
	}
	return state, "warn"
}

func friendlyInboundType(value string) string {
	switch strings.ToLower(value) {
	case "socks":
		return "SOCKS 代理"
	case "http":
		return "HTTP 代理"
	case "dns":
		return "DNS 服务"
	case "tproxy":
		return "透明代理"
	default:
		return fallbackLabel(value, "未知类型")
	}
}

func parseControlTime(value string) (time.Time, bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil && !parsed.IsZero()
}

func formatControlTime(value time.Time, ok bool) string {
	if !ok {
		return "无记录"
	}
	return value.Local().Format("01-02 15:04:05")
}

func formatChineseDuration(value time.Duration) string {
	if value < 0 {
		return "未知"
	}
	days := int(value / (24 * time.Hour))
	value %= 24 * time.Hour
	hours := int(value / time.Hour)
	value %= time.Hour
	minutes := int(value / time.Minute)
	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%d 分钟", minutes)
	default:
		return "不到 1 分钟"
	}
}

func abbreviateHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return fallbackLabel(value, "未知")
}

func fallbackLabel(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
