package bypasscore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	repoAPI      = "https://api.github.com/repos/kinmeic/BypassCore/releases/latest"
	assetAMD64   = "bypasscore-linux-x86_64"
	assetARM64   = "bypasscore-linux-arm64"
	assetARM64OW = "bypasscore-openwrt-aarch64_cortex-a53" // static fallback
	serviceUnit  = "bypasscore"
)

var httpClient = &http.Client{Timeout: 120 * time.Second}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// LatestRelease queries GitHub for the newest BypassCore release.
func LatestRelease(mirror string) (*ghRelease, error) {
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
	// Apply mirror prefix to asset URLs (release page URL rewrite).
	if mirror != "" {
		for i := range rel.Assets {
			rel.Assets[i].URL = mirrorRewrite(mirror, rel.Assets[i].URL)
		}
	}
	return &rel, nil
}

func mirrorRewrite(mirror, url string) string {
	return strings.TrimSuffix(mirror, "/") + "/" + strings.TrimPrefix(url, "https://")
}

// assetName picks the right asset for the current architecture.
func assetName() string {
	if runtime.GOARCH == "arm64" {
		return assetARM64
	}
	return assetAMD64
}

// Install downloads the latest release binary to dest and (re)installs the
// systemd unit. Returns the installed version tag.
func (m *Manager) Install(mirror string) (string, error) {
	rel, err := LatestRelease(mirror)
	if err != nil {
		return "", fmt.Errorf("查询 release 失败: %w", err)
	}
	want := assetName()
	url := ""
	assetFile := ""
	for _, a := range rel.Assets {
		name := strings.TrimSuffix(a.Name, ".tar.gz")
		if name == want || (want == assetARM64 && name == assetARM64OW) {
			url = a.URL
			assetFile = a.Name
			if name == want {
				break
			}
		}
	}
	if url == "" {
		return "", fmt.Errorf("release %s 中没有适配 %s 的二进制", rel.TagName, runtime.GOARCH)
	}
	// Integrity: verify the tarball against the release's SHA256SUMS before
	// it gets installed and run as root.
	sumsURL := ""
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS" {
			sumsURL = a.URL
			break
		}
	}
	if sumsURL == "" {
		return "", fmt.Errorf("release %s 缺少 SHA256SUMS 校验文件，中止安装", rel.TagName)
	}
	sums, err := fetchBytes(sumsURL)
	if err != nil {
		return "", fmt.Errorf("下载 SHA256SUMS 失败: %w", err)
	}
	digest, err := sha256ForAsset(sums, assetFile)
	if err != nil {
		return "", err
	}
	if err := downloadAndExtract(url, digest, m.cfg.BypassCore.BinPath); err != nil {
		return "", err
	}
	if err := m.smokeCheck(); err != nil {
		return "", err
	}
	if err := m.writeUnit(); err != nil {
		return rel.TagName, err
	}
	return rel.TagName, nil
}

// smokeCheck runs the freshly installed binary once to catch corrupted or
// wrong-architecture downloads before the service is (re)started.
func (m *Manager) smokeCheck() error {
	cmd := exec.Command(m.cfg.BypassCore.BinPath, "--version")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("新二进制自检失败（已保留在 %s，请排查）: %v: %s",
			m.cfg.BypassCore.BinPath, err, strings.TrimSpace(out.String()))
	}
	return nil
}

// fetchBytes downloads a URL with a sane size cap.
func fetchBytes(url string) ([]byte, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载 %s 返回 %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// sha256ForAsset extracts the expected hex digest for filename from a
// coreutils-style SHA256SUMS file.
func sha256ForAsset(sums []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == filename {
			h := strings.ToLower(fields[0])
			if len(h) != 64 {
				break
			}
			return h, nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS 中找不到 %s 的校验值", filename)
}

// downloadAndExtract fetches a .tar.gz containing a single binary, verifies
// its sha256 against wantHex, and installs it atomically to dest with 0755.
func downloadAndExtract(url, wantHex, dest string) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	// 压缩包体积上限，防异常大包耗尽磁盘。
	blob, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return fmt.Errorf("读取下载内容失败: %w", err)
	}
	sum := sha256.Sum256(blob)
	if got := hex.EncodeToString(sum[:]); got != wantHex {
		return fmt.Errorf("SHA256 校验失败（期望 %s，实际 %s），已中止安装", wantHex, got)
	}
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var bin io.Reader
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && strings.HasPrefix(filepath.Base(hdr.Name), "bypasscore") {
			bin = tr
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("压缩包内未找到 bypasscore 二进制")
	}
	tmp := dest + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	// 解压上限，防 gzip bomb。
	if _, err := io.Copy(f, io.LimitReader(bin, 512<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

const unitTemplate = `[Unit]
Description=BypassCore routing core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s -run -config %s -log-level warning
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

// writeUnit installs the systemd unit for BypassCore.
func (m *Manager) writeUnit() error {
	unit := fmt.Sprintf(unitTemplate,
		m.cfg.BypassCore.WorkDir, m.cfg.BypassCore.BinPath, m.cfg.BypassCore.ConfigPath)
	if err := os.WriteFile("/etc/systemd/system/bypasscore.service", []byte(unit), 0644); err != nil {
		return err
	}
	return daemonReload()
}

func daemonReload() error {
	return runSystemctl("daemon-reload")
}

func runSystemctl(args ...string) error {
	out, err := execCombined("systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

func execCombined(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}
