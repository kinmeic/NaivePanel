package terminal

import (
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
)

func TestAssetName(t *testing.T) {
	cases := map[string]string{
		"amd64": "ttyd.x86_64",
		"arm64": "ttyd.aarch64",
		"arm":   "ttyd.armhf",
		"386":   "ttyd.i686",
	}
	for goarch, want := range cases {
		got, err := assetName(goarch)
		if err != nil || got != want {
			t.Fatalf("%s: got %q err %v", goarch, got, err)
		}
	}
	if _, err := assetName("riscv64"); err == nil {
		t.Fatal("unsupported arch should error")
	}
}

func TestUnitContent(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetDefaults()
	m := New(cfg)
	unit := m.unitContent("/manage-x/")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/ttyd -i 127.0.0.1 -p 7681 -W -b /manage-x/terminal/ui bash",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "/manage-x//terminal/ui") {
		t.Fatalf("double slash in base path:\n%s", unit)
	}
}

func TestInstalledAndVersion(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetDefaults()
	cfg.Terminal.BinPath = t.TempDir() + "/ttyd"
	m := New(cfg)
	if m.Installed() {
		t.Fatal("missing binary must report not installed")
	}
	if m.Version() != "" {
		t.Fatal("missing binary must report empty version")
	}
	if m.UnitName() != "ttyd" {
		t.Fatalf("unit name %q", m.UnitName())
	}
}
