// Package fail2ban manages the distribution's Fail2ban installation and a
// panel-owned jail override. It never replaces jail.conf or jail.local.
package fail2ban

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kinmeic/NaivePanel/internal/sysd"
)

const RulesPath = "/etc/fail2ban/jail.d/zz-naivepanel.local"
const maxRulesSize = 128 << 10

// The initial editor is a suggestion only. Installation does not overwrite or
// enable SSH rules before the administrator has chosen the correct SSH port.
const DefaultRules = `# NaivePanel 管理的规则；其他配置文件保持不变。
# bantime：封禁时长；findtime：统计窗口；maxretry：窗口内失败次数。
# 时间支持 s/m/h/d，ignoreip 为以空格分隔的可信 IP 或 CIDR。
[DEFAULT]
bantime = 1h
findtime = 10m
maxretry = 5
ignoreip = 127.0.0.1/8 ::1

# 确认 SSH 端口后，将 enabled 改为 true 开启 SSH 防护。
[sshd]
enabled = false
port = 22
backend = systemd
`

type Manager struct {
	mu        sync.Mutex
	rulesPath string
	logPath   string
	run       func(time.Duration, string, ...string) (string, error)
	installed func() bool
	active    func(string) bool
	enabled   func(string) bool
	action    func(string, string) error
}

func New() *Manager {
	return &Manager{
		rulesPath: RulesPath, logPath: "/var/log/fail2ban.log", run: runCommand,
		installed: func() bool { _, err := exec.LookPath("fail2ban-client"); return err == nil },
		active:    sysd.IsActive, enabled: sysd.IsEnabled, action: sysd.Action,
	}
}

func (m *Manager) Installed() bool { return m.installed() }

// cappedOutput bounds subprocess output while still draining the pipe.
type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if left := (2 << 20) - b.Len(); left > 0 {
		if len(p) > left {
			p = p[:left]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func runCommand(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	var out cappedOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s 执行超时", name)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

func (m *Manager) client(args ...string) (string, error) {
	return m.run(10*time.Second, "fail2ban-client", args...)
}

// Install uses the same Debian/Ubuntu package management supported by the
// panel installer. It preserves existing jail configuration and service state.
func (m *Manager) Install() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Installed() {
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("fail2ban 自动安装仅支持 Linux + systemd")
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		return errors.New("自动安装仅支持 Debian/Ubuntu；其他发行版请先通过系统软件源安装 fail2ban")
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return errors.New("缺少 systemctl，无法管理 fail2ban 服务")
	}
	if _, err := m.run(3*time.Minute, "apt-get", "-o", "DPkg::Lock::Timeout=60", "update"); err != nil {
		return err
	}
	if _, err := m.run(5*time.Minute, "apt-get", "-o", "DPkg::Lock::Timeout=60", "install", "-y", "fail2ban", "python3-systemd"); err != nil {
		return err
	}
	if !m.Installed() {
		return errors.New("软件包安装完成，但未找到 fail2ban-client")
	}
	return nil
}

func (m *Manager) Action(verb string) error {
	switch verb {
	case "start", "stop", "restart", "enable", "disable":
	default:
		return errors.New("未知 fail2ban 操作")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.Installed() {
		return errors.New("fail2ban 尚未安装")
	}
	if verb == "start" || verb == "restart" {
		if _, err := m.client("-t"); err != nil {
			return fmt.Errorf("规则检查失败: %w", err)
		}
	}
	if err := m.action(verb, "fail2ban"); err != nil {
		return err
	}
	if verb == "start" || verb == "restart" {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := m.client("ping"); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("启动命令已完成，但 fail2ban 控制接口未就绪，请检查服务日志")
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	return nil
}

type RuleFile struct {
	Content string
	Path    string
	Saved   bool
}

// ReadRuleFile distinguishes a proposed template from a persisted override.
// Neither is a representation of all merged configuration or runtime state.
func (m *Manager) ReadRuleFile() (RuleFile, error) {
	file := RuleFile{Path: m.rulesPath}
	b, err := readTail(m.rulesPath, maxRulesSize+1)
	if errors.Is(err, os.ErrNotExist) {
		file.Content = DefaultRules
		return file, nil
	}
	if err != nil {
		return file, err
	}
	file.Saved = true
	if len(b) > maxRulesSize {
		return file, errors.New("规则文件超过 128 KiB，请在服务器上编辑")
	}
	file.Content = string(b)
	return file, nil
}

func (m *Manager) ReadRules() (string, error) {
	file, err := m.ReadRuleFile()
	return file.Content, err
}

func atomicWrite(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".naivepanel-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// ApplyRules validates the combined distribution configuration. A failed
// validation or reload restores the old override; failed recovery is reported.
func (m *Manager) ApplyRules(content string) error {
	if len(content) > maxRulesSize || strings.ContainsRune(content, 0) {
		return errors.New("规则不能超过 128 KiB 或包含空字符")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.Installed() {
		return errors.New("fail2ban 尚未安装")
	}
	old, err := os.ReadFile(m.rulesPath)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	mode := os.FileMode(0644)
	if st, err := os.Stat(m.rulesPath); err == nil {
		mode = st.Mode().Perm()
	}
	if err := atomicWrite(m.rulesPath, []byte(content), mode); err != nil {
		return err
	}
	restore := func(cause error, reload bool) error {
		var err error
		if existed {
			err = atomicWrite(m.rulesPath, old, mode)
		} else {
			err = os.Remove(m.rulesPath)
		}
		if err != nil {
			return fmt.Errorf("%w；恢复原规则失败: %v", cause, err)
		}
		if reload {
			if _, err := m.client("reload"); err != nil {
				return fmt.Errorf("%w；原规则已恢复，但重新加载失败: %v", cause, err)
			}
		}
		return fmt.Errorf("%w（已恢复原规则）", cause)
	}
	if _, err := m.client("-t"); err != nil {
		return restore(fmt.Errorf("规则校验失败: %w", err), false)
	}
	if m.active("fail2ban") {
		if _, err := m.client("reload"); err != nil {
			return restore(fmt.Errorf("规则应用失败: %w", err), true)
		}
	}
	return nil
}

func readTail(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("日志或配置不是普通文件")
	}
	if st.Size() > limit {
		if _, err := f.Seek(-limit, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, limit))
}
