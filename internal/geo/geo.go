// Package geo downloads and verifies geoip.dat / geosite.dat for BypassCore.
package geo

import (
	"crypto/sha256"
	"encoding/hex"
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

var client = &http.Client{Timeout: 300 * time.Second}

// Info describes one local geodata file.
type Info struct {
	Name    string
	Exists  bool
	Size    int64
	ModTime time.Time
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

func url(mirror, name string) string {
	u := baseURL + "/" + name
	if mirror != "" {
		u = strings.TrimSuffix(mirror, "/") + "/" + strings.TrimPrefix(u, "https://")
	}
	return u
}

func download(u string) ([]byte, error) {
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载 %s 返回 %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
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
	for _, name := range Files {
		sumData, err := download(url(mirror, name+".sha256sum"))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		want, err := parseSHA256Sum(sumData)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		data, err := download(url(mirror, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		got := sha256.Sum256(data)
		if hex.EncodeToString(got[:]) != want {
			return fmt.Errorf("%s: sha256 校验失败（期望 %s，实际 %s）", name, want, hex.EncodeToString(got[:]))
		}
		tmp := filepath.Join(dir, "."+name+".tmp")
		if err := os.WriteFile(tmp, data, 0644); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return nil
}
