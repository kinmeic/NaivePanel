package bypasscore

import (
	"encoding/json"
	"strings"
	"testing"
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
}

func TestEnsureSocksInboundIdempotent(t *testing.T) {
	in := `{"inbounds":[{"tag":"caddy-forward","type":"socks","listen":"127.0.0.1","port":1080,"network":"tcp"}]}`
	_, changed, err := EnsureSocksInbound([]byte(in), 1080)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("should be idempotent for existing matching inbound")
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
