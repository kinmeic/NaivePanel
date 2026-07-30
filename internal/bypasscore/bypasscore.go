// Package bypasscore manages the BypassCore binary, its configuration and
// its Unix-socket control plane.
package bypasscore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kinmeic/NaivePanel/internal/config"
)

// Manager handles one BypassCore installation.
type Manager struct {
	cfg *config.Config
}

// New creates a Manager bound to the panel config.
func New(cfg *config.Config) *Manager { return &Manager{cfg: cfg} }

// Installed reports whether the binary exists.
func (m *Manager) Installed() bool {
	_, err := os.Stat(m.cfg.BypassCore.BinPath)
	return err == nil
}

// Version returns `bypasscore --version` output.
func (m *Manager) Version() string {
	out, err := exec.Command(m.cfg.BypassCore.BinPath, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// controlClient returns an HTTP client talking to the control Unix socket.
func (m *Manager) controlClient() *http.Client {
	sock := m.cfg.BypassCore.ControlSock
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// ControlGet fetches a control-plane endpoint and returns the raw body.
func (m *Manager) ControlGet(path string) ([]byte, error) {
	resp, err := m.controlClient().Get("http://control" + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("控制面 %s 返回 %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// controlPost posts a body to a control-plane endpoint.
func (m *Manager) controlPost(path string, body []byte) ([]byte, error) {
	resp, err := m.controlClient().Post("http://control"+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusConflict || resp.StatusCode >= 400 {
		return out, fmt.Errorf("控制面 %s 返回 %d: %s", path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Status returns /v1/status; err non-nil means the control plane is down.
func (m *Manager) Status() (json.RawMessage, error) {
	b, err := m.ControlGet("/v1/status")
	return json.RawMessage(b), err
}

// Ready returns /v1/ready.
func (m *Manager) Ready() (json.RawMessage, error) {
	b, err := m.ControlGet("/v1/ready")
	return json.RawMessage(b), err
}

// checkConfig validates config bytes with `bypasscore -check-config`.
func (m *Manager) checkConfig(path string) error {
	cmd := exec.Command(m.cfg.BypassCore.BinPath, "-check-config", "-config", path)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("配置校验失败: %v\n%s", err, out.String())
	}
	return nil
}

// ApplyConfig runs the full config-change pipeline for BypassCore:
// validate → backup → write → hot reload (restart when required) → probe.
func (m *Manager) ApplyConfig(content []byte) error {
	if !json.Valid(content) {
		return fmt.Errorf("不是合法的 JSON")
	}
	// Stage & validate.
	tmp, err := os.CreateTemp("", "bypasscore-config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := m.checkConfig(tmp.Name()); err != nil {
		return err
	}
	// Backup current.
	live := m.cfg.BypassCore.ConfigPath
	stamp := time.Now().Format("20060102-150405")
	backupDir := filepath.Join(m.cfg.BackupDir, "bypasscore")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return err
	}
	if cur, err := os.ReadFile(live); err == nil {
		if err := os.WriteFile(filepath.Join(backupDir, "config-"+stamp+".json"), cur, 0600); err != nil {
			return err
		}
	}
	// Write live.
	if err := os.MkdirAll(filepath.Dir(live), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(live, content, 0600); err != nil {
		return err
	}
	// Hot reload through the control plane when available.
	if _, statErr := os.Stat(m.cfg.BypassCore.ControlSock); statErr == nil {
		if out, err := m.controlPost("/v1/config/reload", content); err != nil {
			if strings.Contains(string(out), "restart_required") || strings.Contains(err.Error(), "restart_required") {
				return restartService()
			}
			return fmt.Errorf("热重载失败: %w", err)
		}
		if strings.Contains(string(mustReady(m)), "restart_required") {
			return restartService()
		}
		return nil
	}
	// No control socket: restart the service if it is running.
	return restartService()
}

func mustReady(m *Manager) []byte {
	b, _ := m.Ready()
	return b
}

func restartService() error {
	cmd := exec.Command("systemctl", "restart", "bypasscore")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("重启 bypasscore 失败: %v: %s", err, out.String())
	}
	return nil
}

// ReadConfig returns the live config content.
func (m *Manager) ReadConfig() ([]byte, error) {
	return os.ReadFile(m.cfg.BypassCore.ConfigPath)
}

// EnsureSocksInbound makes sure the config contains the local SOCKS5 inbound
// that receives Caddy forward_proxy traffic. An existing caddy-forward entry
// is updated in place when the port changed (never duplicated). A config
// without any outbound gets a default direct one (BypassCore refuses to
// validate otherwise) routed via routing.finalOutboundTag. It returns true
// when the config was changed (caller should ApplyConfig it).
func EnsureSocksInbound(content []byte, port int) ([]byte, bool, error) {
	var root map[string]any
	if len(bytes.TrimSpace(content)) == 0 {
		root = map[string]any{}
	} else if err := json.Unmarshal(content, &root); err != nil {
		return nil, false, fmt.Errorf("解析现有配置: %w", err)
	}
	changed := false
	inbounds, _ := root["inbounds"].([]any)
	found := false
	for _, ib := range inbounds {
		if m, ok := ib.(map[string]any); ok {
			if m["tag"] == "caddy-forward" && m["type"] == "socks" {
				found = true
				if p, ok := m["port"].(float64); !ok || int(p) != port {
					m["port"] = float64(port) // port changed: update in place
					changed = true
				}
			}
		}
	}
	if !found {
		inbounds = append(inbounds, map[string]any{
			"tag":     "caddy-forward",
			"type":    "socks",
			"listen":  "127.0.0.1",
			"port":    float64(port),
			"network": "tcp",
		})
		root["inbounds"] = inbounds
		changed = true
	}

	// BypassCore validation requires at least one outbound; default to a
	// direct freedom outbound and route everything through it unless the
	// operator already configured otherwise.
	if outbounds, _ := root["outbounds"].([]any); len(outbounds) == 0 {
		root["outbounds"] = []any{map[string]any{
			"tag":  "direct",
			"mode": "freedom",
		}}
		routing, _ := root["routing"].(map[string]any)
		if routing == nil {
			routing = map[string]any{}
		}
		if _, ok := routing["finalOutboundTag"]; !ok {
			routing["finalOutboundTag"] = "direct"
		}
		root["routing"] = routing
		changed = true
	}

	if !changed {
		return content, false, nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
