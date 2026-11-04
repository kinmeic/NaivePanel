package agentcore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/kinmeic/NaivePanel/internal/config"
	"gopkg.in/yaml.v3"
)

const (
	repoAPI  = "https://api.github.com/repos/kinmeic/AgentCoreGo/releases/latest"
	unitPath = "/etc/systemd/system/agent-core.service"
)

var client = &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(r *http.Request, _ []*http.Request) error {
	if r.URL.Scheme != "https" {
		return errors.New("下载重定向必须使用 HTTPS")
	}
	return nil
}}
var skillName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type Manager struct{ cfg *config.Config }

func New(cfg *config.Config) *Manager { return &Manager{cfg: cfg} }
func (m *Manager) Installed() bool {
	st, err := os.Stat(m.cfg.AgentCore.BinPath)
	return err == nil && st.Mode().IsRegular()
}
func (m *Manager) VersionTag() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, m.cfg.AgentCore.BinPath, "version").CombinedOutput()
	if err != nil || ctx.Err() != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(v) {
		return "v" + v
	}
	return ""
}

type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func LatestRelease(mirror string) (*Release, error) {
	req, _ := http.NewRequest("GET", repoAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "naivepanel")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}
	var rel Release
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(rel.TagName) {
		return nil, errors.New("release 版本号异常")
	}
	if mirror != "" {
		for i := range rel.Assets {
			rel.Assets[i].URL = strings.TrimSuffix(mirror, "/") + "/" + strings.TrimPrefix(rel.Assets[i].URL, "https://")
		}
	}
	return &rel, nil
}
func fetch(url string, limit int64) ([]byte, error) {
	parsed, err := urlpkg.Parse(url)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("下载地址必须是 HTTPS URL")
	}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("下载文件超过大小限制")
	}
	return b, nil
}
func expectedSum(content []byte, name string) (string, error) {
	for _, line := range strings.Split(string(content), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(strings.TrimPrefix(f[1], "*"), "./") == name {
			if len(f[0]) == 64 {
				return strings.ToLower(f[0]), nil
			}
			break
		}
	}
	return "", fmt.Errorf("SHA256SUMS 缺少 %s", name)
}
func extractArchive(blob []byte) ([]byte, []byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var binary, example []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		switch filepath.Base(h.Name) {
		case "agent-core":
			binary, err = io.ReadAll(io.LimitReader(tr, (128<<20)+1))
		case "agent-core.yaml":
			example, err = io.ReadAll(io.LimitReader(tr, (1<<20)+1))
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if len(binary) == 0 || len(binary) > 128<<20 {
		return nil, nil, errors.New("压缩包缺少有效的 agent-core 二进制")
	}
	if len(example) > 1<<20 {
		return nil, nil, errors.New("示例配置过大")
	}
	return binary, example, nil
}
func (m *Manager) Install(mirror string) (string, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return "", errors.New("仅支持 Linux amd64/arm64")
	}
	rel, err := LatestRelease(mirror)
	if err != nil {
		return "", err
	}
	arch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	want := "agent-core-" + rel.TagName + "-linux-" + arch + ".tar.gz"
	var assetURL, sumsURL string
	for _, a := range rel.Assets {
		if a.Name == want {
			assetURL = a.URL
		}
		if a.Name == "SHA256SUMS" {
			sumsURL = a.URL
		}
	}
	if assetURL == "" || sumsURL == "" {
		return "", fmt.Errorf("release %s 缺少安装包或 SHA256SUMS", rel.TagName)
	}
	sums, err := fetch(sumsURL, 4<<20)
	if err != nil {
		return "", err
	}
	wantHash, err := expectedSum(sums, want)
	if err != nil {
		return "", err
	}
	blob, err := fetch(assetURL, 256<<20)
	if err != nil {
		return "", err
	}
	actual := sha256.Sum256(blob)
	if hex.EncodeToString(actual[:]) != wantHash {
		return "", errors.New("安装包 SHA256 校验失败")
	}
	binary, example, err := extractArchive(blob)
	if err != nil {
		return "", err
	}
	if err = m.ensureUserAndDirs(); err != nil {
		return "", err
	}
	dest := m.cfg.AgentCore.BinPath
	if err = os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}
	tmp := dest + ".new"
	if err = os.WriteFile(tmp, binary, 0755); err != nil {
		return "", err
	}
	if err = os.Chmod(tmp, 0755); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	out, runErr := exec.CommandContext(ctx, tmp, "version").CombinedOutput()
	cancel()
	if runErr != nil || strings.TrimSpace(string(out)) != strings.TrimPrefix(rel.TagName, "v") {
		return "", fmt.Errorf("新版本自检失败: %s %v", strings.TrimSpace(string(out)), runErr)
	}
	if _, err = os.Stat(m.cfg.AgentCore.ConfigPath); errors.Is(err, os.ErrNotExist) {
		if err = m.writeInitialConfig(example); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err = m.ValidateConfig(mustRead(m.cfg.AgentCore.ConfigPath), tmp); err != nil {
		return "", fmt.Errorf("现有配置校验失败: %w", err)
	}
	old := dest + ".previous"
	_ = os.Remove(old)
	hadOld := false
	if _, err = os.Stat(dest); err == nil {
		if err = os.Rename(dest, old); err != nil {
			return "", err
		}
		hadOld = true
	}
	if err = os.Rename(tmp, dest); err != nil {
		if hadOld {
			_ = os.Rename(old, dest)
		}
		return "", err
	}
	if err = m.writeUnit(); err != nil {
		if hadOld {
			_ = os.Rename(old, dest)
		} else {
			_ = os.Remove(dest)
		}
		return "", err
	}
	if out, e := exec.Command("systemctl", "enable", "agent-core").CombinedOutput(); e != nil {
		if hadOld {
			_ = os.Rename(old, dest)
		} else {
			_ = os.Remove(dest)
		}
		return "", fmt.Errorf("开启 AgentCore 自启失败: %v: %s", e, out)
	}
	if e := restartAndWait(); e != nil {
		if hadOld {
			_ = os.Rename(old, dest)
			_ = restartAndWait()
		} else {
			_ = os.Remove(dest)
			_, _ = exec.Command("systemctl", "disable", "--now", "agent-core").CombinedOutput()
		}
		return "", fmt.Errorf("启动 AgentCore 失败: %w", e)
	}
	_ = os.Remove(old)
	return rel.TagName, nil
}
func mustRead(path string) []byte { b, _ := os.ReadFile(path); return b }
func (m *Manager) ensureUserAndDirs() error {
	if out, err := exec.Command("id", "-u", "agent-core").CombinedOutput(); err != nil {
		if output, e := exec.Command("useradd", "--system", "--home-dir", m.cfg.AgentCore.WorkDir, "--shell", "/usr/sbin/nologin", "agent-core").CombinedOutput(); e != nil {
			return fmt.Errorf("创建 agent-core 用户: %v: %s (%s)", e, output, out)
		}
	}
	if err := os.MkdirAll(filepath.Dir(m.cfg.AgentCore.ConfigPath), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(m.cfg.AgentCore.WorkDir, 0755); err != nil {
		return err
	}
	if err := os.Chmod(m.cfg.AgentCore.WorkDir, 0755); err != nil {
		return err
	}
	if err := os.Chown(m.cfg.AgentCore.WorkDir, 0, 0); err != nil {
		return err
	}
	if err := os.MkdirAll(m.SkillDir(), 0755); err != nil {
		return err
	}
	if err := os.Chmod(m.SkillDir(), 0755); err != nil {
		return err
	}
	if err := os.Chown(m.SkillDir(), 0, 0); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(m.cfg.AgentCore.WorkDir, "data"), filepath.Join(m.cfg.AgentCore.WorkDir, "workspace")} {
		if err := os.MkdirAll(p, 0750); err != nil {
			return err
		}
		if out, err := exec.Command("chown", "-R", "agent-core:agent-core", p).CombinedOutput(); err != nil {
			return fmt.Errorf("设置目录权限: %v: %s", err, out)
		}
	}
	return nil
}
func (m *Manager) writeInitialConfig(example []byte) error {
	data, err := m.initialConfig(example)
	if err != nil {
		return err
	}
	return m.writeConfigFile(data)
}

func (m *Manager) initialConfig(example []byte) ([]byte, error) {
	if len(example) == 0 {
		return nil, errors.New("安装包缺少示例配置")
	}
	var root map[string]any
	if err := yaml.Unmarshal(example, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("示例配置必须是 YAML 对象")
	}
	set := func(section, key string, value any) {
		obj, _ := root[section].(map[string]any)
		if obj == nil {
			obj = map[string]any{}
			root[section] = obj
		}
		obj[key] = value
	}
	server, _ := root["server"].(map[string]any)
	host, _ := server["host"].(string)
	loopback := host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1"
	if enabled, exists := server["terminalEnabled"]; !loopback || !exists || enabled == nil {
		set("server", "terminalEnabled", loopback)
	}
	// Keep network-binding authentication; AgentCore rejects unauthenticated non-loopback hosts.
	if loopback {
		set("server", "auth_token", "")
	}
	set("llm", "api_key", "")
	set("workspace", "path", filepath.Join(m.cfg.AgentCore.WorkDir, "workspace"))
	set("storage", "dir", filepath.Join(m.cfg.AgentCore.WorkDir, "data"))
	set("storage", "sessionDbPath", filepath.Join(m.cfg.AgentCore.WorkDir, "data", "agent.db"))
	skills, _ := root["skills"].(map[string]any)
	if skills == nil {
		skills = map[string]any{}
		root["skills"] = skills
	}
	skills["roots"] = []string{m.SkillDir()}
	return yaml.Marshal(root)
}
func (m *Manager) writeConfigFile(data []byte) error {
	tmp := m.cfg.AgentCore.ConfigPath + ".new"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if out, err := exec.Command("chown", "root:agent-core", tmp).CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("设置配置权限: %v: %s", err, out)
	}
	if err := os.Chmod(tmp, 0640); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, m.cfg.AgentCore.ConfigPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
func (m *Manager) writeUnit() error {
	unit := fmt.Sprintf(`[Unit]
Description=AgentCore AI Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=agent-core
Group=agent-core
WorkingDirectory=%s
ExecStart=%s -config %s
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s

[Install]
WantedBy=multi-user.target
`, m.cfg.AgentCore.WorkDir, m.cfg.AgentCore.BinPath, m.cfg.AgentCore.ConfigPath, m.cfg.AgentCore.WorkDir)
	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return err
	}
	out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd 重载失败: %v: %s", err, out)
	}
	return nil
}
func (m *Manager) ReadConfig() ([]byte, error) { return os.ReadFile(m.cfg.AgentCore.ConfigPath) }
func (m *Manager) ValidateConfig(content []byte, bin string) error {
	if len(content) == 0 || len(content) > 1<<20 {
		return errors.New("配置为空或超过 1 MiB")
	}
	var node yaml.Node
	if err := yaml.Unmarshal(content, &node); err != nil {
		return fmt.Errorf("YAML 语法错误: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(m.cfg.AgentCore.ConfigPath), ".agent-core-validate-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err = temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "config", "validate", "--config", temp.Name())
	cmd.Dir = m.cfg.AgentCore.WorkDir
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("配置检查超时")
	}
	if err != nil {
		return fmt.Errorf("AgentCore 配置检查: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
func (m *Manager) SaveConfig(content []byte, active bool) error {
	if !m.Installed() {
		return errors.New("请先安装 AgentCore")
	}
	if err := m.ValidateConfig(content, m.cfg.AgentCore.BinPath); err != nil {
		return err
	}
	old, err := m.ReadConfig()
	if err != nil {
		return err
	}
	if err = m.writeConfigFile(content); err != nil {
		return err
	}
	if active {
		if e := restartAndWait(); e != nil {
			_ = m.writeConfigFile(old)
			_ = restartAndWait()
			return fmt.Errorf("重启失败，配置已回滚: %w", e)
		}
	}
	return nil
}

func restartAndWait() error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "restart", "agent-core").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart agent-core: %v: %s", err, out)
	}
	time.Sleep(time.Second)
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "agent-core").Run(); err != nil {
		return errors.New("服务重启后未保持运行，请查看 AgentCore 日志")
	}
	return nil
}
func (m *Manager) SkillDir() string { return filepath.Join(m.cfg.AgentCore.WorkDir, "skills") }

type Skill struct {
	Name    string
	Path    string
	Content string
	Enabled bool
}

func (m *Manager) ListSkills(overrides map[string]bool) ([]Skill, error) {
	entries, err := os.ReadDir(m.SkillDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() || !skillName.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(m.SkillDir(), e.Name(), "SKILL.md")
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		enabled, ok := overrides[p]
		if !ok {
			enabled = true
		}
		skills = append(skills, Skill{Name: e.Name(), Path: p, Content: string(b), Enabled: enabled})
	}
	return skills, nil
}
func (m *Manager) SaveSkill(name, content string) error {
	if !skillName.MatchString(name) {
		return errors.New("Skill 名称只能使用字母、数字、下划线和连字符，最长 64 字符")
	}
	if len(content) == 0 || len(content) > 100<<10 || !strings.HasPrefix(content, "---\n") {
		return errors.New("Skill 内容需以 YAML frontmatter 开头且不超过 100 KiB")
	}
	parts := strings.SplitN(content, "\n---\n", 2)
	if len(parts) != 2 {
		return errors.New("Skill frontmatter 缺少结束分隔符")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(parts[0], "---\n")), &metadata); err != nil {
		return fmt.Errorf("Skill frontmatter 无效: %w", err)
	}
	if metadata.Name != name || strings.TrimSpace(metadata.Description) == "" {
		return errors.New("Skill frontmatter 的 name 必须匹配目录名称，且 description 不能为空")
	}
	dir := filepath.Join(m.SkillDir(), name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, p := range []string{m.SkillDir(), dir, filepath.Join(dir, "SKILL.md"), filepath.Join(dir, "SKILL.md.new")} {
		if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return errors.New("Skill 路径不能是符号链接")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Chmod(dir, 0755); err != nil {
		return err
	}
	if err := os.Chown(dir, 0, 0); err != nil {
		return err
	}
	path := filepath.Join(dir, "SKILL.md")
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chown(tmp, 0, 0); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// EnsureSkillRoot makes managed skills discoverable in an existing installation.
func (m *Manager) EnsureSkillRoot(active bool) error {
	content, err := m.ReadConfig()
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(content, &root); err != nil {
		return err
	}
	skills, _ := root["skills"].(map[string]any)
	if skills == nil {
		skills = map[string]any{}
		root["skills"] = skills
	}
	var roots []string
	if values, ok := skills["roots"].([]any); ok {
		for _, v := range values {
			if s, ok := v.(string); ok {
				roots = append(roots, s)
			}
		}
	}
	for _, v := range roots {
		if v == m.SkillDir() {
			return nil
		}
	}
	roots = append(roots, m.SkillDir())
	skills["roots"] = roots
	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return m.SaveConfig(data, active)
}

func (m *Manager) SetSkillEnabled(name string, enabled, active bool) error {
	if !skillName.MatchString(name) {
		return errors.New("无效 Skill 名称")
	}
	path := filepath.Join(m.SkillDir(), name, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		return err
	}
	content, err := m.ReadConfig()
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(content, &root); err != nil {
		return err
	}
	entries, _ := root["skill"].(map[string]any)
	if entries == nil {
		entries = map[string]any{}
		root["skill"] = entries
	}
	entry, _ := entries[path].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
	}
	entry["enable"] = enabled
	entries[path] = entry
	data, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return m.SaveConfig(data, active)
}
func (m *Manager) DeleteSkill(name string) error {
	if !skillName.MatchString(name) {
		return errors.New("无效 Skill 名称")
	}
	return os.RemoveAll(filepath.Join(m.SkillDir(), name))
}
