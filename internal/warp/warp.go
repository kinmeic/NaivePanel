// Package warp manages the official Cloudflare WARP Linux client in proxy mode.
package warp

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

const DefaultPort = 40000
const Unit = "warp-svc"

var licensePattern = regexp.MustCompile(`^[A-Za-z0-9]{8}-[A-Za-z0-9]{8}-[A-Za-z0-9]{8}$`)
var proxyModePattern = regexp.MustCompile(`(?i)\bWarpProxy\s+on\s+port\s+(\d+)\b`)

// State keeps failed queries distinct from a confirmed unregistered/disconnected client.
type State struct {
	Installed, Active, Enabled                   bool
	RegistrationKnown, Registered                bool
	AccountType, DeviceID                        string
	Connection, ConnectionLabel, ConnectionClass string
	Mode                                         string
	Port                                         int
	Errors                                       []string
}

type commandRunner func(context.Context, string, ...string) (string, error)

type Manager struct {
	mu              sync.Mutex
	run             commandRunner
	lookup          func(string) (string, error)
	active, enabled func(string) bool
	action          func(string, string) error
	install         func() error
	trace           func(context.Context, int) (EgressResult, error)
}

func New() *Manager {
	m := &Manager{run: runCommand, lookup: exec.LookPath, active: sysd.IsActive, enabled: sysd.IsEnabled, action: sysd.Action}
	m.install = m.installPackage
	m.trace = verifyTrace
	return m
}

// limitedOutput bounds package manager and CLI output without stopping pipe consumption.
type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if left := (64 << 10) - b.Len(); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "DEBIAN_FRONTEND=noninteractive")
	var out limitedOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return out.String(), fmt.Errorf("命令执行超时")
	}
	if err != nil {
		return out.String(), fmt.Errorf("%v: %s", err, redact(out.String()))
	}
	return out.String(), nil
}

// Never render a license from CLI settings or error output.
func redact(value string) string {
	re := regexp.MustCompile(`[A-Za-z0-9]{8}-[A-Za-z0-9]{8}-[A-Za-z0-9]{8}`)
	return re.ReplaceAllString(strings.TrimSpace(value), "[license 已隐藏]")
}

func (m *Manager) cli(timeout time.Duration, args ...string) (string, error) {
	path, err := m.lookup("warp-cli")
	if err != nil {
		return "", fmt.Errorf("WARP 未安装，请先安装")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return m.run(ctx, path, append([]string{"--accept-tos"}, args...)...)
}

func (m *Manager) query(args ...string) (string, error) { return m.cli(5*time.Second, args...) }
func (m *Manager) command(args ...string) error         { _, err := m.cli(25*time.Second, args...); return err }

func (m *Manager) State() State {
	s := State{ConnectionLabel: "未知", ConnectionClass: "muted"}
	_, err := m.lookup("warp-cli")
	s.Installed = err == nil
	if !s.Installed {
		s.ConnectionLabel = "未安装"
		return s
	}
	s.Active, s.Enabled = m.active(Unit), m.enabled(Unit)
	if !s.Active {
		s.ConnectionLabel = "服务未运行"
		return s
	}
	if !m.mu.TryLock() {
		s.Errors = append(s.Errors, "WARP 操作正在执行，完成后请刷新状态。")
		return s
	}
	defer m.mu.Unlock()
	out, err := m.query("registration", "show")
	s.RegistrationKnown, s.Registered, s.AccountType, s.DeviceID = parseRegistration(out, err)
	if !s.RegistrationKnown {
		s.Errors = append(s.Errors, "注册状态读取失败："+queryError(out, err))
	}
	out, err = m.query("settings")
	if err == nil {
		s.Mode, s.Port = parseSettings(out)
		if s.Mode == "" {
			s.Errors = append(s.Errors, "未识别客户端的模式和端口，请检查 WARP 版本。")
		}
	} else {
		s.Errors = append(s.Errors, "代理设置读取失败："+redact(err.Error()))
	}
	out, err = m.query("status")
	if err == nil {
		s.Connection, s.ConnectionLabel, s.ConnectionClass = parseConnection(out)
		if s.Connection == "" {
			s.Errors = append(s.Errors, "未识别连接状态："+redact(out))
		}
	} else {
		s.Errors = append(s.Errors, "连接状态读取失败："+redact(err.Error()))
	}
	return s
}

func queryError(out string, err error) string {
	if err != nil {
		return redact(err.Error())
	}
	return redact(out)
}

func parseRegistration(out string, err error) (known, registered bool, account, device string) {
	lower := strings.ToLower(out)
	for _, marker := range []string{"missing registration", "registration missing", "not registered", "no registration", "registration not found"} {
		if strings.Contains(lower, marker) {
			return true, false, "", ""
		}
	}
	if err != nil {
		return false, false, "", ""
	}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(key) {
		case "account type":
			account = value
		case "device id", "id":
			device = value
		}
	}
	return account != "" || device != "", account != "" || device != "", account, device
}

func parseSettings(out string) (mode string, port int) {
	for _, line := range strings.Split(out, "\n") {
		// Some clients prefix settings with their source, e.g. "(User set) Mode:".
		if at := strings.Index(line, "Mode:"); at >= 0 {
			mode = strings.TrimSpace(line[at+5:])
		}
	}
	if match := proxyModePattern.FindStringSubmatch(mode); len(match) == 2 {
		port, _ = strconv.Atoi(match[1])
		mode = "proxy"
	}
	if port < 1 || port > 65535 {
		port = 0
	}
	return mode, port
}

func parseConnection(out string) (state, label, class string) {
	for _, line := range strings.Split(out, "\n") {
		value := strings.TrimSpace(line)
		if _, v, ok := strings.Cut(value, "Status update:"); ok {
			value = strings.TrimSpace(v)
		}
		switch strings.ToLower(value) {
		case "connected":
			return "connected", "已连接", "ok"
		case "disconnected":
			return "disconnected", "未连接", "muted"
		case "connecting":
			return "connecting", "连接中", "warn"
		case "disconnecting":
			return "disconnecting", "断开中", "warn"
		}
	}
	return "", "未知", "warn"
}

func (m *Manager) ensureService() error {
	if _, err := m.lookup("warp-cli"); err != nil {
		return fmt.Errorf("WARP 未安装，请先安装")
	}
	if !m.active(Unit) {
		if err := m.action("start", Unit); err != nil {
			return fmt.Errorf("启动 WARP 服务失败：%w", err)
		}
		// The daemon may take a moment to create its IPC socket.
		for i := 0; i < 10; i++ {
			if _, err := m.query("settings"); err == nil {
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return fmt.Errorf("WARP 服务尚未就绪，请稍后重试")
	}
	return nil
}

func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("端口必须在 1–65535 之间")
	}
	return nil
}

func ValidateLicense(key string) error {
	if !licensePattern.MatchString(key) {
		return fmt.Errorf("license 格式错误，应为三组 8 位字母或数字，以连字符分隔")
	}
	return nil
}

func (m *Manager) registration() (bool, error) {
	out, err := m.query("registration", "show")
	known, registered, _, _ := parseRegistration(out, err)
	if !known {
		return false, fmt.Errorf("无法确认注册状态：%s", queryError(out, err))
	}
	return registered, nil
}

// Install enables the daemon, but leaves registration and connection to explicit actions.
func (m *Manager) Install() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.lookup("warp-cli"); err != nil {
		if err := m.install(); err != nil {
			return err
		}
	}
	if err := m.action("enable", Unit); err != nil {
		return fmt.Errorf("开启 WARP 自启失败：%w", err)
	}
	return m.ensureService()
}

func (m *Manager) Register() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureService(); err != nil {
		return err
	}
	registered, err := m.registration()
	if err != nil {
		return err
	}
	if registered {
		return fmt.Errorf("客户端已注册，无需重复注册")
	}
	if err := m.command("registration", "new"); err != nil {
		return fmt.Errorf("注册失败：%w", err)
	}
	// Registration alone must never activate a full-device tunnel.
	return m.configureProxy(DefaultPort, false)
}

// configureProxy disconnects before changing mode; failed settings never reconnect.
func (m *Manager) configureProxy(port int, connect bool) error {
	if err := m.command("disconnect"); err != nil {
		return fmt.Errorf("断开连接失败：%w", err)
	}
	for _, args := range [][]string{{"tunnel", "protocol", "set", "MASQUE"}, {"mode", "proxy"}, {"proxy", "port", strconv.Itoa(port)}} {
		if err := m.command(args...); err != nil {
			return fmt.Errorf("应用代理设置失败（连接保持断开）：%w", err)
		}
	}
	// Check effective values before issuing connect; managed policies can override CLI settings.
	out, err := m.query("settings")
	if err != nil {
		return fmt.Errorf("确认代理设置失败（连接保持断开）：%w", err)
	}
	mode, actualPort := parseSettings(out)
	if mode != "proxy" || actualPort != port {
		return fmt.Errorf("客户端未应用 proxy 模式或端口，连接保持断开，请检查客户端策略")
	}
	if connect {
		return m.command("connect")
	}
	return nil
}

func (m *Manager) Connect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureService(); err != nil {
		return err
	}
	registered, err := m.registration()
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("客户端尚未注册，请先注册")
	}
	out, err := m.query("settings")
	if err != nil {
		return fmt.Errorf("读取代理设置失败：%w", err)
	}
	_, port := parseSettings(out)
	if port == 0 {
		port = DefaultPort
	}
	return m.configureProxy(port, true)
}

func (m *Manager) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureService(); err != nil {
		return err
	}
	return m.command("disconnect")
}

func (m *Manager) SetPort(port int) error {
	if err := ValidatePort(port); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureService(); err != nil {
		return err
	}
	registered, err := m.registration()
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("客户端尚未注册，请先注册")
	}
	settings, err := m.query("settings")
	if err != nil {
		return fmt.Errorf("读取代理设置失败：%w", err)
	}
	mode, oldPort := parseSettings(settings)
	// Reject an occupied new port before interrupting the current connection.
	if mode != "proxy" || oldPort != port {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return fmt.Errorf("端口 %d 不可用：%w", port, err)
		}
		_ = listener.Close()
	}
	out, err := m.query("status")
	if err != nil {
		return fmt.Errorf("读取连接状态失败：%w", err)
	}
	state, _, _ := parseConnection(out)
	if state == "" {
		return fmt.Errorf("无法确认当前连接状态，未修改设置")
	}
	return m.configureProxy(port, state == "connected" || state == "connecting")
}

func (m *Manager) BindLicense(key string) error {
	if err := ValidateLicense(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ensureService(); err != nil {
		return err
	}
	registered, err := m.registration()
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("客户端尚未注册，请先注册")
	}
	if err := m.command("registration", "license", key); err != nil {
		// The CLI may echo a rejected license; never send that output to the browser/log.
		return fmt.Errorf("绑定/变更 license 失败，请检查密钥是否有效及设备数量限制")
	}
	return nil
}
