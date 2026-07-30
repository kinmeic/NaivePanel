// Package selfupdate checks GitHub for newer NaivePanel releases and
// replaces the running binary in place.
package selfupdate

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
	"strconv"
	"strings"
	"time"
)

const repoAPI = "https://api.github.com/repos/kinmeic/NaivePanel/releases/latest"

var httpClient = &http.Client{
	Timeout: 120 * time.Second,
	CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("拒绝更新下载重定向到非 HTTPS 地址")
		}
		return nil
	},
}

// Release is a GitHub release with its downloadable assets.
type Release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest queries GitHub for the newest NaivePanel release. mirror optionally
// prefixes asset URLs (e.g. a GitHub proxy like https://ghproxy.net).
func Latest(mirror string) (*Release, error) {
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
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
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

// Newer reports whether latest is a strictly newer version tag than current.
// Unparseable versions (e.g. "dev") never auto-update.
func Newer(current, latest string) bool {
	c, ok1 := parseVersion(current)
	l, ok2 := parseVersion(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// assetName returns the release asset matching this platform, or "" when the
// platform has no published binary (self-update is linux-only).
func assetName() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if runtime.GOARCH == "arm64" {
		return "naivepanel-linux-arm64.tar.gz"
	}
	return "naivepanel-linux-amd64.tar.gz"
}

// Apply downloads the latest release, verifies it against SHA256SUMS, smoke
// checks the binary and atomically replaces the running executable. It
// returns the installed tag. The caller is responsible for restarting the
// service afterwards.
func Apply(mirror string) (string, error) {
	want := assetName()
	if want == "" {
		return "", fmt.Errorf("当前平台 %s/%s 没有发布二进制，无法自动更新", runtime.GOOS, runtime.GOARCH)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}

	rel, err := Latest(mirror)
	if err != nil {
		return "", fmt.Errorf("查询 release 失败: %w", err)
	}
	var binURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			binURL = a.URL
		case "SHA256SUMS":
			sumsURL = a.URL
		}
	}
	if binURL == "" {
		return "", fmt.Errorf("release %s 中没有 %s", rel.TagName, want)
	}
	if sumsURL == "" {
		return "", fmt.Errorf("release %s 缺少 SHA256SUMS 校验文件，中止更新", rel.TagName)
	}
	sums, err := fetchBytes(sumsURL)
	if err != nil {
		return "", fmt.Errorf("下载 SHA256SUMS 失败: %w", err)
	}
	digest, err := sha256ForAsset(sums, want)
	if err != nil {
		return "", err
	}
	newBin, err := downloadVerified(binURL, digest)
	if err != nil {
		return "", err
	}
	defer os.Remove(newBin)

	// Smoke check the fresh binary before it replaces the running one.
	if out, err := exec.Command(newBin, "version").CombinedOutput(); err != nil {
		return "", fmt.Errorf("新二进制自检失败，已中止更新: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Atomic replace: copy into a same-dir temp then rename over the live
	// binary — rename across filesystems (/tmp → /usr) would fail.
	tmp := exe + ".new"
	if err := copyFile(newBin, tmp, 0755); err != nil {
		return "", fmt.Errorf("替换二进制失败: %w", err)
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("替换二进制失败: %w", err)
	}
	return rel.TagName, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// RestartSelf restarts the panel through the available service manager so the
// new binary takes over. As a last resort it exits, relying on the
// supervisor's restart policy.
func RestartSelf() {
	if _, err := exec.LookPath("systemctl"); err == nil &&
		exec.Command("systemctl", "is-active", "--quiet", "naivepanel").Run() == nil {
		_ = exec.Command("systemctl", "restart", "naivepanel").Run()
	}
	if _, err := os.Stat("/etc/init.d/naivepanel"); err == nil {
		_ = exec.Command("/etc/init.d/naivepanel", "restart").Run()
	}
	os.Exit(0)
}

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

func sha256ForAsset(sums []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == filename && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS 中找不到 %s 的校验值", filename)
}

// downloadVerified fetches the tarball, checks its sha256, and extracts the
// naivepanel binary into a temp file (0755). Caller removes it.
func downloadVerified(url, wantHex string) (string, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("下载返回 %d", resp.StatusCode)
	}
	// 压缩包体积上限，防异常大包耗尽磁盘。
	blob, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(blob)
	if got := hex.EncodeToString(sum[:]); got != wantHex {
		return "", fmt.Errorf("SHA256 校验失败（期望 %s，实际 %s），已中止更新", wantHex, got)
	}
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("压缩包内未找到 naivepanel 二进制")
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "naivepanel" {
			continue
		}
		tmp, err := os.CreateTemp("", "naivepanel-update-*")
		if err != nil {
			return "", err
		}
		// 解压上限，防 gzip bomb。
		if _, err := io.Copy(tmp, io.LimitReader(tr, 512<<20)); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return "", err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return "", err
		}
		if err := os.Chmod(tmp.Name(), 0755); err != nil {
			os.Remove(tmp.Name())
			return "", err
		}
		return tmp.Name(), nil
	}
}
