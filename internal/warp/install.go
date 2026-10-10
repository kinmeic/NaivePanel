package warp

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// installScript uses the official signed package repositories. os-release is
// parsed as data, never sourced as root shell code.
func installScript(release string) (string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(release, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	id, codename := values["ID"], values["VERSION_CODENAME"]
	if id == "debian" || id == "ubuntu" {
		allowed := map[string]string{"debian:bookworm": "12", "debian:trixie": "13", "ubuntu:jammy": "22.04", "ubuntu:noble": "24.04", "ubuntu:resolute": "26.04"}
		if _, ok := allowed[id+":"+codename]; !ok {
			return "", fmt.Errorf("自动安装不支持此发行版：%s %s，请通过 Cloudflare 官方仓库安装 WARP", id, values["VERSION_ID"])
		}
		return `set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl gnupg
install -d -m 0755 /usr/share/keyrings /etc/apt/sources.list.d
curl --retry 2 --connect-timeout 15 --max-time 60 -fsSL https://pkg.cloudflareclient.com/pubkey.gpg | gpg --batch --yes --dearmor --output /usr/share/keyrings/cloudflare-warp-archive-keyring.gpg
chmod 0644 /usr/share/keyrings/cloudflare-warp-archive-keyring.gpg
printf '%s\n' 'deb [signed-by=/usr/share/keyrings/cloudflare-warp-archive-keyring.gpg] https://pkg.cloudflareclient.com/ ` + codename + ` main' > /etc/apt/sources.list.d/cloudflare-client.list
apt-get update
apt-get install -y cloudflare-warp
`, nil
	}
	version := values["VERSION_ID"]
	rhel := id == "rhel" || id == "centos" || id == "rocky" || id == "almalinux"
	major, _, _ := strings.Cut(version, ".")
	if (rhel && (major == "9" || major == "10")) || (id == "fedora" && (version == "43" || version == "44")) {
		epel := ""
		if rhel {
			epel = "dnf install -y epel-release\n"
		}
		return `set -euo pipefail
dnf install -y ca-certificates curl
` + epel + `install -d -m 0755 /etc/yum.repos.d
curl --retry 2 --connect-timeout 15 --max-time 60 -fsSL https://pkg.cloudflareclient.com/cloudflare-warp-ascii.repo -o /etc/yum.repos.d/cloudflare-warp.repo
rpm --import https://pkg.cloudflareclient.com/pubkey.gpg
dnf install -y --refresh cloudflare-warp
`, nil
	}
	return "", fmt.Errorf("自动安装不支持此发行版：%s %s，请通过 Cloudflare 官方仓库安装 WARP", id, version)
}

func (m *Manager) installPackage() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("WARP 自动安装仅支持 Linux + systemd")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return fmt.Errorf("WARP 自动安装仅支持 amd64/arm64")
	}
	release, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return fmt.Errorf("读取系统发行版失败：%w", err)
	}
	script, err := installScript(string(release))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := m.run(ctx, "bash", "-c", script); err != nil {
		return fmt.Errorf("从官方仓库安装 WARP 失败：%w", err)
	}
	return nil
}
