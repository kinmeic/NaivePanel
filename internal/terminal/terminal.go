// Package terminal manages the ttyd web terminal: install, systemd unit and
// version probing. ttyd listens on localhost only and is exposed through the
// panel's authenticated reverse proxy under base_path/terminal/ui. It runs
// the system `login` program, so after the panel session gate users still
// authenticate with OS credentials before getting a shell.
//
// login(1) from util-linux calls vhangup(2) on the pty it was started on.
// ttyd's libuv read loop treats the resulting transient EIO as EOF and stops
// reading forever, which shows up as a blank terminal without the login
// prompt. Wrapping login in util-linux `script` gives it a nested pty, so the
// hangup hits the inner pty while ttyd keeps a healthy read side.
package terminal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/sysd"
)

const (
	repoAPI     = "https://api.github.com/repos/tsl0922/ttyd/releases/latest"
	serviceUnit = "ttyd"
	unitPath    = "/etc/systemd/system/ttyd.service"
)

var httpClient = &http.Client{Timeout: 120 * time.Second}

// Manager handles the local ttyd installation.
type Manager struct {
	cfg *config.Config
}

// New builds the manager from the panel config.
func New(cfg *config.Config) *Manager {
	return &Manager{cfg: cfg}
}

// Installed reports whether the ttyd binary exists.
func (m *Manager) Installed() bool {
	st, err := os.Stat(m.cfg.Terminal.BinPath)
	return err == nil && !st.IsDir()
}

// Version returns `ttyd --version` output, empty when unavailable.
func (m *Manager) Version() string {
	if !m.Installed() {
		return ""
	}
	out, err := exec.Command(m.cfg.Terminal.BinPath, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	// Output looks like "ttyd version 1.7.7-40e79c7".
	return strings.TrimSpace(string(out))
}

// UnitName exposes the systemd unit the panel controls.
func (m *Manager) UnitName() string { return serviceUnit }

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// assetName maps the Go architecture to the ttyd release asset name.
func assetName(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "ttyd.x86_64", nil
	case "arm64":
		return "ttyd.aarch64", nil
	case "arm":
		return "ttyd.armhf", nil
	case "386":
		return "ttyd.i686", nil
	}
	return "", fmt.Errorf("ttyd 没有适配 %s 架构的预编译二进制", goarch)
}

// checkDeps verifies the programs the unit's command line relies on.
func checkDeps() error {
	for _, name := range []string{"script", "login"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("缺少 %s 命令（Debian/Ubuntu 的 util-linux/bsdutils），无法承载终端登录: %w", name, err)
		}
	}
	return nil
}

// Install downloads the latest ttyd release binary and (re)installs the
// systemd unit. basePath is the panel base path so ttyd can serve under
// basePath/terminal/ui behind the panel's reverse proxy. Returns the
// installed version tag.
func (m *Manager) Install(mirror, basePath string) (string, error) {
	want, err := assetName(runtime.GOARCH)
	if err != nil {
		return "", err
	}
	if err := checkDeps(); err != nil {
		return "", err
	}
	rel, err := latestRelease(mirror)
	if err != nil {
		return "", fmt.Errorf("查询 release 失败: %w", err)
	}
	url := ""
	for _, a := range rel.Assets {
		if a.Name == want {
			url = a.URL
			break
		}
	}
	if url == "" {
		return "", fmt.Errorf("release %s 中没有 %s", rel.TagName, want)
	}
	// ttyd 官方 release 不提供校验文件，下载后以 --version 自检兜底。
	if err := downloadBinary(url, m.cfg.Terminal.BinPath); err != nil {
		return "", err
	}
	if err := m.smokeCheck(); err != nil {
		return "", err
	}
	if err := m.WriteUnit(basePath); err != nil {
		return rel.TagName, err
	}
	return rel.TagName, nil
}

// latestRelease queries GitHub for the newest ttyd release.
func latestRelease(mirror string) (*ghRelease, error) {
	req, err := http.NewRequest(http.MethodGet, repoAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "naivepanel")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if mirror != "" {
		for i := range rel.Assets {
			rel.Assets[i].URL = strings.TrimSuffix(mirror, "/") + "/" +
				strings.TrimPrefix(rel.Assets[i].URL, "https://")
		}
	}
	return &rel, nil
}

// downloadBinary fetches a raw executable and installs it atomically to dest
// with 0755.
func downloadBinary(url, dest string) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	tmp := dest + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	// 体积上限，防异常大包耗尽磁盘（ttyd 静态二进制约 3MB）。
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 64<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("读取下载内容失败: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// smokeCheck runs the fresh binary once to catch corrupted or wrong-arch
// downloads before the service is (re)started.
func (m *Manager) smokeCheck() error {
	cmd := exec.Command(m.cfg.Terminal.BinPath, "--version")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("新二进制自检失败（已保留在 %s，请排查）: %v: %s",
			m.cfg.Terminal.BinPath, err, strings.TrimSpace(out.String()))
	}
	return nil
}

const unitTemplate = `[Unit]
Description=ttyd web terminal (managed by NaivePanel)
After=network.target

[Service]
Type=simple
ExecStart=%s -i 127.0.0.1 -p %d -W -b %s script -qefc "exec login" /dev/null
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
`

// unitContent renders the systemd unit for ttyd. basePath is the panel base
// path; ttyd serves under basePath/terminal/ui so the panel reverse proxy
// can forward requests unchanged.
//
// login runs under `script` on purpose: script allocates a nested pty, so
// login's vhangup() cannot break ttyd's pty read side (see package comment).
// -q keeps script quiet, -e propagates login's exit status, -f flushes every
// write so the "login:" prompt (no trailing newline) is shown immediately.
// The command must be `exec login`: script runs -c through /bin/sh, and a
// plain `login` leaves it as a child of the shell, where login's setpgrp()
// moves it to a background process group and SIGTTIN stops it. `exec` replaces
// the shell, keeping login the session/pgrp leader with a foreground tty.
func (m *Manager) unitContent(basePath string) string {
	return fmt.Sprintf(unitTemplate, m.cfg.Terminal.BinPath, m.cfg.Terminal.Port,
		strings.TrimSuffix(basePath, "/")+"/terminal/ui")
}

// WriteUnit installs the systemd unit for ttyd.
func (m *Manager) WriteUnit(basePath string) error {
	if err := os.WriteFile(unitPath, []byte(m.unitContent(basePath)), 0644); err != nil {
		return err
	}
	out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v: %s", err, out)
	}
	return nil
}

// EnsureUnit rewrites the ttyd unit when its command line is stale (for
// example after a panel upgrade switched to the script-wrapped login) and
// restarts the unit if it is running, so the fix applies without a manual
// reinstall. A missing unit is left alone: Install creates it.
func (m *Manager) EnsureUnit(basePath string) error {
	if !m.Installed() {
		return nil
	}
	cur, err := os.ReadFile(unitPath)
	if err != nil {
		return nil
	}
	if string(cur) == m.unitContent(basePath) {
		return nil
	}
	if err := m.WriteUnit(basePath); err != nil {
		return err
	}
	if sysd.IsActive(serviceUnit) {
		return sysd.Action("restart", serviceUnit)
	}
	return nil
}
