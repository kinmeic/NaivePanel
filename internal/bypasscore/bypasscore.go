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
	"sync"
	"time"

	"github.com/kinmeic/NaivePanel/internal/config"
)

// Manager handles one BypassCore installation.
type Manager struct {
	cfg    *config.Config
	client *http.Client
	mu     sync.Mutex
}

// New creates a Manager bound to the panel config.
func New(cfg *config.Config) *Manager {
	sock := cfg.BypassCore.ControlSock
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	return &Manager{
		cfg:    cfg,
		client: &http.Client{Transport: tr, Timeout: 10 * time.Second},
	}
}

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

// ControlGet fetches a control-plane endpoint and returns the raw body.
func (m *Manager) ControlGet(path string) ([]byte, error) {
	resp, err := m.client.Get("http://control" + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取控制面响应失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("控制面 %s 返回 %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// controlPost posts a body to a control-plane endpoint.
func (m *Manager) controlPost(path string, body []byte) ([]byte, error) {
	resp, err := m.client.Post("http://control"+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return nil, fmt.Errorf("读取控制面响应失败: %w", readErr)
	}
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
	// Match the systemd unit's WorkingDirectory. BypassCore resolves relative
	// geodata references (geoip.dat / geosite.dat) from this directory.
	cmd.Dir = m.cfg.BypassCore.WorkDir
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
	m.mu.Lock()
	defer m.mu.Unlock()

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
	// Backup current and remember it for transactional rollback.
	live := m.cfg.BypassCore.ConfigPath
	old, oldErr := os.ReadFile(live)
	oldExists := oldErr == nil
	if oldErr != nil && !os.IsNotExist(oldErr) {
		return fmt.Errorf("读取当前配置失败: %w", oldErr)
	}
	stamp := time.Now().Format("20060102-150405.000000000")
	backupDir := filepath.Join(m.cfg.BackupDir, "bypasscore")
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return err
	}
	if oldExists {
		if err := os.WriteFile(filepath.Join(backupDir, "config-"+stamp+".json"), old, 0600); err != nil {
			return err
		}
	}
	// Write live atomically so a crash cannot leave truncated JSON.
	if err := writeFileAtomic(live, content, 0600); err != nil {
		return err
	}

	// Saving configuration must not unexpectedly start a stopped service.
	if !serviceActive() {
		return nil
	}

	var applyErr error
	// Hot reload through the control plane when available.
	if st, statErr := os.Stat(m.cfg.BypassCore.ControlSock); statErr == nil && st.Mode()&os.ModeSocket != 0 {
		if out, err := m.controlPost("/v1/config/reload", content); err != nil {
			if strings.Contains(string(out), "restart_required") || strings.Contains(err.Error(), "restart_required") {
				applyErr = restartService()
			} else {
				applyErr = fmt.Errorf("热重载失败: %w", err)
			}
		} else {
			_, applyErr = m.Ready()
		}
	} else {
		applyErr = restartService()
	}
	if applyErr == nil {
		if state, err := InspectControlConfig(content); err == nil &&
			state.Enabled && state.Socket == m.cfg.BypassCore.ControlSock {
			applyErr = m.waitReady(5 * time.Second)
		}
	}
	if applyErr == nil {
		return nil
	}

	// Restore both disk and runtime to the previously working revision.
	var rollbackErr error
	if oldExists {
		rollbackErr = writeFileAtomic(live, old, 0600)
	} else {
		rollbackErr = os.Remove(live)
		if os.IsNotExist(rollbackErr) {
			rollbackErr = nil
		}
	}
	if rollbackErr == nil {
		rollbackErr = restartService()
	}
	if rollbackErr != nil {
		return fmt.Errorf("应用配置失败: %v；回滚也失败: %v", applyErr, rollbackErr)
	}
	return fmt.Errorf("应用配置失败，已回滚到原配置: %w", applyErr)
}

func (m *Manager) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if _, err := m.Ready(); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("控制面探活超时: %w", last)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func serviceActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", "bypasscore").Run() == nil
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

// ControlConfigState describes whether BypassCore's config will create the
// control socket. This lets the UI distinguish a disabled control plane from
// a broken running service without exposing a low-level dial error.
type ControlConfigState struct {
	Configured bool
	Enabled    bool
	Socket     string
}

// InspectControlConfig reads the control section without changing content.
func InspectControlConfig(content []byte) (ControlConfigState, error) {
	var root map[string]any
	if err := json.Unmarshal(content, &root); err != nil {
		return ControlConfigState{}, fmt.Errorf("解析配置: %w", err)
	}
	raw, ok := root["control"]
	if !ok {
		return ControlConfigState{}, nil
	}
	control, ok := raw.(map[string]any)
	if !ok {
		return ControlConfigState{}, fmt.Errorf("control 必须是 JSON 对象")
	}
	state := ControlConfigState{Configured: true}
	state.Enabled, _ = control["enabled"].(bool)
	state.Socket, _ = control["socket"].(string)
	return state, nil
}

// EnsureControlPlane enables the local Unix-socket control API expected by
// NaivePanel while preserving every unrelated BypassCore setting.
func EnsureControlPlane(content []byte, socket string) ([]byte, bool, error) {
	var root map[string]any
	if len(bytes.TrimSpace(content)) == 0 {
		root = map[string]any{}
	} else if err := json.Unmarshal(content, &root); err != nil {
		return nil, false, fmt.Errorf("解析现有配置: %w", err)
	}
	control, _ := root["control"].(map[string]any)
	if control == nil {
		control = map[string]any{}
		root["control"] = control
	}
	changed := false
	if enabled, _ := control["enabled"].(bool); !enabled {
		control["enabled"] = true
		changed = true
	}
	if current, _ := control["socket"].(string); current != socket {
		control["socket"] = socket
		changed = true
	}
	if _, ok := control["mode"]; !ok {
		control["mode"] = "0660"
		changed = true
	}
	if !changed {
		return content, false, nil
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
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
