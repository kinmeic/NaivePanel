package service

import (
	"os"
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	u := systemdUnit("/usr/local/bin/naivepanel", "/etc/naivepanel/config.yaml")
	for _, want := range []string{
		"[Unit]", "[Service]", "[Install]",
		"ExecStart=/usr/local/bin/naivepanel -config /etc/naivepanel/config.yaml",
		"Restart=always",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("systemd unit 缺少 %q", want)
		}
	}
}

func TestProcdScript(t *testing.T) {
	s := procdScript("/usr/bin/naivepanel", "/etc/naivepanel/config.yaml")
	for _, want := range []string{
		"#!/bin/sh /etc/rc.common",
		"USE_PROCD=1",
		"procd_open_instance naivepanel",
		`procd_set_param command "$PROG" -config "$CONF"`,
		"PROG=/usr/bin/naivepanel",
		"CONF=/etc/naivepanel/config.yaml",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("procd 脚本缺少 %q", want)
		}
	}
}

func TestLaunchdPlist(t *testing.T) {
	p := launchdPlist("/usr/local/bin/naivepanel", "/etc/naivepanel/config.yaml")
	for _, want := range []string{
		"<string>com.naivepanel</string>",
		"<string>/usr/local/bin/naivepanel</string>",
		"<string>/etc/naivepanel/config.yaml</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("launchd plist 缺少 %q", want)
		}
	}
}

func TestDefaultBinPath(t *testing.T) {
	if got := defaultBinPath(PlatformProcd); got != "/usr/bin/naivepanel" {
		t.Errorf("OpenWrt bin path = %s", got)
	}
	if got := defaultBinPath(PlatformSystemd); got != "/usr/local/bin/naivepanel" {
		t.Errorf("systemd bin path = %s", got)
	}
	if got := defaultBinPath(PlatformLaunchd); got != "/usr/local/bin/naivepanel" {
		t.Errorf("launchd bin path = %s", got)
	}
}

func TestCopyFileAtomic(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/src"
	dst := dir + "/sub/dst"
	if err := os.WriteFile(src, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := copyFileAtomic(src, dst, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("内容不符: %q", data)
	}
}
