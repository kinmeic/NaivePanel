// Package geo downloads and verifies geoip.dat / geosite.dat for BypassCore.
package geo

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const baseURL = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download"

// Files are the geodata files managed by the panel.
var Files = []string{"geoip.dat", "geosite.dat"}

var client = &http.Client{
	Timeout: 300 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("更新下载重定向次数过多")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("拒绝更新下载重定向到非 HTTPS 地址")
		}
		return nil
	},
}

// Info describes one local geodata file.
type Info struct {
	Name    string
	Exists  bool
	Size    int64
	ModTime time.Time
}

// Comparison describes local and remote metadata for one managed file.
// A metadata check never downloads or replaces the file itself.
type Comparison struct {
	Name               string
	LocalExists        bool
	LocalSize          int64
	LocalModTime       time.Time
	RemoteSize         int64
	RemoteSizeKnown    bool
	RemoteModTime      time.Time
	RemoteModTimeKnown bool
	Status             string
	StatusClass        string
	Error              string
}

// Stat returns info for each managed file in dir.
func Stat(dir string) []Info {
	out := make([]Info, 0, len(Files))
	for _, name := range Files {
		fi := Info{Name: name}
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
			fi.Exists = true
			fi.Size = st.Size()
			fi.ModTime = st.ModTime()
		}
		out = append(out, fi)
	}
	return out
}

// Check compares local file metadata with the latest remote release assets.
// Size is the most dependable HTTP metadata; modification time is shown as
// additional context and is not used alone to claim that an update exists.
func Check(dir, mirror string) []Comparison {
	local := Stat(dir)
	out := make([]Comparison, 0, len(Files))
	for i, name := range Files {
		item := Comparison{
			Name:         name,
			LocalExists:  local[i].Exists,
			LocalSize:    local[i].Size,
			LocalModTime: local[i].ModTime,
		}
		resp, err := client.Head(url(mirror, name))
		if err != nil {
			item.Error = err.Error()
			item.Status = "检查失败"
			item.StatusClass = "bad"
			out = append(out, item)
			continue
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if resp.StatusCode != http.StatusOK {
			item.Error = fmt.Sprintf("远端返回 %d", resp.StatusCode)
			item.Status = "检查失败"
			item.StatusClass = "bad"
			out = append(out, item)
			continue
		}
		if resp.ContentLength >= 0 {
			item.RemoteSize = resp.ContentLength
			item.RemoteSizeKnown = true
		}
		if value := resp.Header.Get("Last-Modified"); value != "" {
			if modified, parseErr := http.ParseTime(value); parseErr == nil {
				item.RemoteModTime = modified
				item.RemoteModTimeKnown = true
			}
		}
		switch {
		case !item.LocalExists:
			item.Status = "本地缺失"
			item.StatusClass = "warn"
		case item.RemoteSizeKnown && item.LocalSize != item.RemoteSize:
			item.Status = "大小不同"
			item.StatusClass = "warn"
		case item.RemoteSizeKnown:
			item.Status = "大小一致"
			item.StatusClass = "ok"
		default:
			item.Status = "远端大小未知"
			item.StatusClass = "warn"
		}
		out = append(out, item)
	}
	return out
}

func url(mirror, name string) string {
	u := baseURL + "/" + name
	if mirror != "" {
		u = strings.TrimSuffix(mirror, "/") + "/" + strings.TrimPrefix(u, "https://")
	}
	return u
}

func download(u string, maxBytes int64) ([]byte, error) {
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载 %s 返回 %d", u, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("下载 %s 超过大小上限", u)
	}
	return data, nil
}

// parseSHA256Sum extracts the hex digest from a "<hash>  <filename>" line.
func parseSHA256Sum(data []byte) (string, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("sha256sum 文件为空")
	}
	h := fields[0]
	if len(h) != 64 {
		return "", fmt.Errorf("sha256sum 格式异常: %q", h)
	}
	return strings.ToLower(h), nil
}

// Update downloads every managed file, verifies its sha256sum and atomically
// replaces the files in dir. mirror may be empty.
func Update(dir, mirror string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	type stagedFile struct {
		name string
		path string
	}
	var staged []stagedFile
	defer func() {
		for _, f := range staged {
			if f.path != "" {
				_ = os.Remove(f.path)
			}
		}
	}()
	for _, name := range Files {
		sumData, err := download(url(mirror, name+".sha256sum"), 1<<20)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		want, err := parseSHA256Sum(sumData)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		staged = append(staged, stagedFile{name: name, path: tmpPath})
		if err := tmp.Chmod(0644); err != nil {
			tmp.Close()
			return err
		}
		resp, err := client.Get(url(mirror, name))
		if err != nil {
			tmp.Close()
			return fmt.Errorf("%s: %w", name, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			tmp.Close()
			return fmt.Errorf("下载 %s 返回 %d", url(mirror, name), resp.StatusCode)
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(resp.Body, (512<<20)+1))
		closeErr := resp.Body.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil && n > 512<<20 {
			copyErr = fmt.Errorf("下载文件超过 512 MiB 上限")
		}
		if copyErr == nil {
			copyErr = tmp.Sync()
		}
		if closeTmpErr := tmp.Close(); copyErr == nil {
			copyErr = closeTmpErr
		}
		if copyErr != nil {
			return fmt.Errorf("%s: %w", name, copyErr)
		}
		got := fmt.Sprintf("%x", hash.Sum(nil))
		if got != want {
			return fmt.Errorf("%s: sha256 校验失败（期望 %s，实际 %s）", name, want, got)
		}
	}
	// Do not replace either live file until both downloads have passed their
	// checksums. This avoids a half-updated pair on network/checksum failure.
	for i := range staged {
		if err := os.Rename(staged[i].path, filepath.Join(dir, staged[i].name)); err != nil {
			return err
		}
		staged[i].path = ""
	}
	return nil
}
