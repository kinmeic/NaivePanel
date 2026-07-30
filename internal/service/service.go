// Package service installs and uninstalls naivepanel as a system service
// across systemd (Linux), procd (OpenWrt) and launchd (macOS).
package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Platform is a supported service manager.
type Platform int

const (
	PlatformSystemd Platform = iota
	PlatformProcd
	PlatformLaunchd
)

func (p Platform) String() string {
	switch p {
	case PlatformSystemd:
		return "systemd"
	case PlatformProcd:
		return "procd (OpenWrt)"
	case PlatformLaunchd:
		return "launchd (macOS)"
	}
	return "unknown"
}

// Detect identifies the host's service manager.
func Detect() (Platform, error) {
	switch runtime.GOOS {
	case "darwin":
		return PlatformLaunchd, nil
	case "linux":
		if fileExists("/etc/openwrt_release") || fileExists("/sbin/procd") {
			return PlatformProcd, nil
		}
		if fileExists("/run/systemd/system") || commandExists("systemctl") {
			return PlatformSystemd, nil
		}
		return 0, errors.New("未识别的服务管理器（非 systemd、非 procd）")
	}
	return 0, fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
}

// InstallOptions controls service installation.
type InstallOptions struct {
	ConfigPath string // panel config file (must exist)
	BinPath    string // target binary path; empty = platform default
	Start      bool   // start the service after enabling
	DryRun     bool   // print actions without touching the system
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// defaultBinPath returns where the binary should live per platform.
func defaultBinPath(p Platform) string {
	if p == PlatformProcd {
		return "/usr/bin/naivepanel"
	}
	return "/usr/local/bin/naivepanel"
}

// defaultConfigPath returns the conventional config location per platform.
func defaultConfigPath() string {
	return "/etc/naivepanel/config.yaml"
}

// Install sets up naivepanel as a system service.
func Install(opts InstallOptions) error {
	plat, err := Detect()
	if err != nil {
		return err
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = defaultConfigPath()
	}
	confAbs, err := filepath.Abs(opts.ConfigPath)
	if err != nil {
		return err
	}
	if st, err := os.Stat(confAbs); err != nil || st.IsDir() {
		return fmt.Errorf("配置文件不存在: %s（请先运行 install.sh 或手动创建配置）", confAbs)
	}
	binPath := opts.BinPath
	if binPath == "" {
		binPath = defaultBinPath(plat)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)

	steps := []string{}
	run := func(desc string, fn func() error) error {
		steps = append(steps, desc)
		if opts.DryRun {
			fmt.Println("[dry-run]", desc)
			return nil
		}
		if err := fn(); err != nil {
			return fmt.Errorf("%s: %w", desc, err)
		}
		fmt.Println("[✓]", desc)
		return nil
	}

	// 1. Install binary if not already at the target path.
	if exe != binPath {
		if err := run("安装二进制到 "+binPath, func() error {
			return copyFileAtomic(exe, binPath, 0755)
		}); err != nil {
			return err
		}
	} else {
		fmt.Println("[✓] 二进制已位于", binPath)
	}

	// 2. Platform-specific unit installation.
	switch plat {
	case PlatformSystemd:
		unit := systemdUnit(binPath, confAbs)
		if err := run("写入 /etc/systemd/system/naivepanel.service", func() error {
			return os.WriteFile("/etc/systemd/system/naivepanel.service", []byte(unit), 0644)
		}); err != nil {
			return err
		}
		if opts.DryRun {
			fmt.Print(indentBlock(unit))
		}
		if err := run("systemctl daemon-reload", func() error {
			return exec.Command("systemctl", "daemon-reload").Run()
		}); err != nil {
			return err
		}
		if err := run("systemctl enable naivepanel", func() error {
			return exec.Command("systemctl", "enable", "naivepanel").Run()
		}); err != nil {
			return err
		}
		if opts.Start {
			if err := run("systemctl restart naivepanel", func() error {
				return exec.Command("systemctl", "restart", "naivepanel").Run()
			}); err != nil {
				return err
			}
		}

	case PlatformProcd:
		script := procdScript(binPath, confAbs)
		if err := run("写入 /etc/init.d/naivepanel", func() error {
			return os.WriteFile("/etc/init.d/naivepanel", []byte(script), 0755)
		}); err != nil {
			return err
		}
		if opts.DryRun {
			fmt.Print(indentBlock(script))
		}
		if err := run("/etc/init.d/naivepanel enable", func() error {
			return exec.Command("/etc/init.d/naivepanel", "enable").Run()
		}); err != nil {
			return err
		}
		if opts.Start {
			if err := run("/etc/init.d/naivepanel restart", func() error {
				return exec.Command("/etc/init.d/naivepanel", "restart").Run()
			}); err != nil {
				return err
			}
		}

	case PlatformLaunchd:
		plist := launchdPlist(binPath, confAbs)
		const plistPath = "/Library/LaunchDaemons/com.naivepanel.plist"
		if err := run("写入 "+plistPath, func() error {
			return os.WriteFile(plistPath, []byte(plist), 0644)
		}); err != nil {
			return err
		}
		if opts.DryRun {
			fmt.Print(indentBlock(plist))
		}
		if opts.Start {
			if err := run("launchctl bootstrap system/com.naivepanel", func() error {
				// Modern API first, fall back to legacy load.
				_ = exec.Command("launchctl", "bootout", "system/com.naivepanel").Run()
				if err := exec.Command("launchctl", "bootstrap", "system", plistPath).Run(); err != nil {
					return exec.Command("launchctl", "load", "-w", plistPath).Run()
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}

	fmt.Printf("\n服务已安装（%s）\n  二进制: %s\n  配置:   %s\n", plat, binPath, confAbs)
	if !opts.Start && !opts.DryRun {
		fmt.Println("服务已设为开机启动，但尚未启动；用 -start 安装并立即启动。")
	}
	return nil
}

// Uninstall removes the naivepanel service. purge also deletes the binary
// and the config directory.
func Uninstall(purge, dryRun bool) error {
	plat, err := Detect()
	if err != nil {
		return err
	}
	run := func(desc string, fn func() error) error {
		if dryRun {
			fmt.Println("[dry-run]", desc)
			return nil
		}
		if err := fn(); err != nil {
			return fmt.Errorf("%s: %w", desc, err)
		}
		fmt.Println("[✓]", desc)
		return nil
	}

	switch plat {
	case PlatformSystemd:
		_ = exec.Command("systemctl", "stop", "naivepanel").Run()
		if err := run("systemctl disable naivepanel", func() error {
			return exec.Command("systemctl", "disable", "naivepanel").Run()
		}); err != nil {
			return err
		}
		if err := run("删除 /etc/systemd/system/naivepanel.service", func() error {
			return os.RemoveAll("/etc/systemd/system/naivepanel.service")
		}); err != nil {
			return err
		}
		if err := run("systemctl daemon-reload", func() error {
			return exec.Command("systemctl", "daemon-reload").Run()
		}); err != nil {
			return err
		}

	case PlatformProcd:
		_ = exec.Command("/etc/init.d/naivepanel", "stop").Run()
		_ = exec.Command("/etc/init.d/naivepanel", "disable").Run()
		if err := run("删除 /etc/init.d/naivepanel", func() error {
			return os.RemoveAll("/etc/init.d/naivepanel")
		}); err != nil {
			return err
		}

	case PlatformLaunchd:
		_ = exec.Command("launchctl", "bootout", "system/com.naivepanel").Run()
		_ = exec.Command("launchctl", "unload", "-w", "/Library/LaunchDaemons/com.naivepanel.plist").Run()
		if err := run("删除 /Library/LaunchDaemons/com.naivepanel.plist", func() error {
			return os.RemoveAll("/Library/LaunchDaemons/com.naivepanel.plist")
		}); err != nil {
			return err
		}
	}

	if purge {
		bin := defaultBinPath(plat)
		if err := run("删除二进制 "+bin, func() error {
			return os.Remove(bin)
		}); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := run("删除配置目录 /etc/naivepanel", func() error {
			return os.RemoveAll("/etc/naivepanel")
		}); err != nil {
			return err
		}
	} else {
		fmt.Println("\n二进制与配置已保留；如需一并删除，使用 uninstall -purge")
	}
	fmt.Printf("服务已卸载（%s）\n", plat)
	return nil
}

// copyFileAtomic copies src to dst via a temp file + rename.
func copyFileAtomic(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func indentBlock(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("    | " + l + "\n")
	}
	return b.String()
}

func systemdUnit(bin, conf string) string {
	return fmt.Sprintf(`[Unit]
Description=NaivePanel server management panel
After=network-online.target caddy.service
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s -config %s
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
`, bin, conf)
}

func procdScript(bin, conf string) string {
	return fmt.Sprintf(`#!/bin/sh /etc/rc.common
# NaivePanel procd service

START=99
STOP=10
USE_PROCD=1

PROG=%s
CONF=%s

start_service() {
	procd_open_instance naivepanel
	procd_set_param command "$PROG" -config "$CONF"
	procd_set_param respawn 3600 5 5
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_set_param file "$CONF"
	procd_close_instance
}
`, bin, conf)
}

func launchdPlist(bin, conf string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.naivepanel</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>-config</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/var/log/naivepanel.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/naivepanel.log</string>
</dict>
</plist>
`, bin, conf)
}
