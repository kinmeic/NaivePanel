package bypasscore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
)

func TestEnsureSocksInboundEmpty(t *testing.T) {
	out, changed, err := EnsureSocksInbound(nil, 1080)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	ibs, _ := root["inbounds"].([]any)
	if len(ibs) != 1 {
		t.Fatalf("expected 1 inbound, got %d", len(ibs))
	}
	m := ibs[0].(map[string]any)
	if m["tag"] != "caddy-forward" || m["type"] != "socks" || int(m["port"].(float64)) != 1080 {
		t.Fatalf("unexpected inbound: %v", m)
	}
	obs, _ := root["outbounds"].([]any)
	if len(obs) != 1 {
		t.Fatalf("expected 1 default outbound, got %d", len(obs))
	}
	ob := obs[0].(map[string]any)
	if ob["tag"] != "direct" || ob["mode"] != "freedom" {
		t.Fatalf("unexpected default outbound: %v", ob)
	}
	rt, _ := root["routing"].(map[string]any)
	if rt["finalOutboundTag"] != "direct" {
		t.Fatalf("routing should default to direct: %v", rt)
	}
}

func TestEnsureSocksInboundIdempotent(t *testing.T) {
	in := `{"inbounds":[{"tag":"caddy-forward","type":"socks","listen":"127.0.0.1","port":1080,"network":"tcp"}],"outbounds":[{"tag":"direct","mode":"freedom"}],"routing":{"finalOutboundTag":"direct"}}`
	_, changed, err := EnsureSocksInbound([]byte(in), 1080)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("should be idempotent for existing matching inbound")
	}
}

// An existing outbound / routing setup is never touched.
func TestEnsureSocksInboundKeepsOutbounds(t *testing.T) {
	in := `{"inbounds":[{"tag":"caddy-forward","type":"socks","port":1080}],"outbounds":[{"tag":"caddy-exit","mode":"proxy","upstream":{"server":"exit.example.com:443"}}],"routing":{"finalOutboundTag":"caddy-exit"}}`
	out, changed, err := EnsureSocksInbound([]byte(in), 1080)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("config with outbounds must not be modified")
	}
	if !strings.Contains(string(out), "caddy-exit") {
		t.Fatalf("existing outbound lost: %s", out)
	}
}

func TestEnsureSocksInboundAppends(t *testing.T) {
	in := `{"inbounds":[{"tag":"other","type":"dns","port":1053}]}`
	out, changed, err := EnsureSocksInbound([]byte(in), 2080)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(out), `"caddy-forward"`) || !strings.Contains(string(out), `"other"`) {
		t.Fatalf("missing content: %s", out)
	}
}

func TestEnsureControlPlane(t *testing.T) {
	in := []byte(`{"inbounds":[],"control":{"enabled":false},"custom":{"keep":true}}`)
	out, changed, err := EnsureControlPlane(in, "/run/bypasscore/control.sock")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	state, err := InspectControlConfig(out)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Configured || !state.Enabled || state.Socket != "/run/bypasscore/control.sock" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if !strings.Contains(string(out), `"keep": true`) {
		t.Fatalf("unrelated config lost: %s", out)
	}
	out2, changed, err := EnsureControlPlane(out, "/run/bypasscore/control.sock")
	if err != nil || changed || string(out2) != string(out) {
		t.Fatalf("must be idempotent: changed=%v err=%v", changed, err)
	}
}

func TestInspectControlConfigMissing(t *testing.T) {
	state, err := InspectControlConfig([]byte(`{"inbounds":[]}`))
	if err != nil || state.Configured || state.Enabled {
		t.Fatalf("unexpected state=%+v err=%v", state, err)
	}
}

func TestSHA256ForAsset(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte(sum + "  ./bypasscore-linux-x86_64.tar.gz\n" +
		strings.Repeat("cd", 32) + " *bypasscore-linux-arm64.tar.gz\n")
	got, err := sha256ForAsset(sums, "bypasscore-linux-x86_64.tar.gz")
	if err != nil || got != sum {
		t.Fatalf("find-style ./ prefix entry: got %q err %v", got, err)
	}
	got, err = sha256ForAsset(sums, "bypasscore-linux-arm64.tar.gz")
	if err != nil || got != strings.Repeat("cd", 32) {
		t.Fatalf("binary-marker * entry: got %q err %v", got, err)
	}
	if _, err := sha256ForAsset(sums, "bypasscore-darwin-arm64.tar.gz"); err == nil {
		t.Fatal("missing asset must error")
	}
}

func TestCheckConfigUsesBypassCoreWorkDir(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "geosite.dat"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(workDir, "bypasscore")
	script := `#!/bin/sh
set -eu
test -f geosite.dat
test "$1" = "-check-config"
test "$2" = "-config"
test -f "$3"
`
	if err := os.WriteFile(fakeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte(`{"routing":{"rules":[{"domain":["geosite:cn"]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	manager := New(&config.Config{
		BypassCore: config.BypassCore{
			BinPath: fakeBin,
			WorkDir: workDir,
		},
	})
	if err := manager.checkConfig(configPath); err != nil {
		t.Fatalf("checkConfig did not use bypasscore.work_dir: %v", err)
	}
}
