package agentcore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"github.com/kinmeic/NaivePanel/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseChecksumAndExtraction(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gz)
	for name, content := range map[string]string{"agent-core-v0.4.0-linux-x86_64/agent-core": "binary", "agent-core-v0.4.0-linux-x86_64/agent-core.yaml": "server: {}\n"} {
		b := []byte(content)
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compressed.Bytes())
	name := "agent-core-v0.4.0-linux-x86_64.tar.gz"
	got, err := expectedSum([]byte(hex.EncodeToString(digest[:])+"  "+name+"\n"), name)
	if err != nil || got != hex.EncodeToString(digest[:]) {
		t.Fatalf("checksum=%q err=%v", got, err)
	}
	binary, example, err := extractArchive(compressed.Bytes())
	if err != nil || string(binary) != "binary" || !strings.Contains(string(example), "server") {
		t.Fatalf("extract binary=%q example=%q err=%v", binary, example, err)
	}
	if _, err := expectedSum([]byte("bad  other.tar.gz"), name); err == nil {
		t.Fatal("expected missing checksum error")
	}
}

func TestSaveConfigValidatesBeforeReplacing(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "agent-core")
	script := "#!/bin/sh\ncase \"$(cat \"$4\")\" in *invalid*) echo invalid >&2; exit 1;; esac\necho configuration valid\n"
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "agent-core.yaml")
	if err := os.WriteFile(path, []byte("server: {port: 3030}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AgentCore: config.AgentCore{BinPath: binary, ConfigPath: path, WorkDir: root}}
	manager := New(cfg)
	if err := manager.SaveConfig([]byte("server: {port: invalid}\n"), false); err == nil {
		t.Fatal("expected validator rejection")
	}
	content, _ := os.ReadFile(path)
	if string(content) != "server: {port: 3030}\n" {
		t.Fatalf("config changed after failed validation: %q", content)
	}
	// SaveConfig also adjusts ownership for the service account, so a full
	// successful write is exercised by deployment rather than this local test.
}

func TestSaveConfigReportsActualRollbackOutcome(t *testing.T) {
	for _, scenario := range []string{"restored", "write-failed", "restart-failed"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "agent-core.yaml")
			binary := filepath.Join(dir, "agent-core")
			t.Setenv("AGENTCORE_TEST_CONFIG", path)
			t.Setenv("AGENTCORE_TEST_STATE", filepath.Join(dir, "restarted"))
			t.Setenv("AGENTCORE_TEST_SCENARIO", scenario)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			files := map[string]string{
				path:                        "old: true\n",
				binary:                      "#!/bin/sh\nexit 0\n",
				filepath.Join(dir, "chown"): "#!/bin/sh\nexit 0\n",
				filepath.Join(dir, "systemctl"): `#!/bin/sh
if [ "$1" = restart ]; then
  if [ ! -e "$AGENTCORE_TEST_STATE" ]; then
    touch "$AGENTCORE_TEST_STATE"
    if [ "$AGENTCORE_TEST_SCENARIO" = write-failed ]; then
      mkdir "$AGENTCORE_TEST_CONFIG.new"
    fi
    echo initial-restart-failed
    exit 1
  fi
  if [ "$AGENTCORE_TEST_SCENARIO" = restart-failed ]; then
    echo recovery-restart-failed
    exit 1
  fi
fi
exit 0
`,
			}
			for p, content := range files {
				if err := os.WriteFile(p, []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			m := New(&config.Config{AgentCore: config.AgentCore{BinPath: binary, ConfigPath: path, WorkDir: dir}})
			err := m.SaveConfig([]byte("new: true\n"), true)
			if err == nil {
				t.Fatal("restart failure was hidden")
			}
			want := map[string]string{"restored": "配置已回滚", "write-failed": "配置回滚失败", "restart-failed": "服务恢复失败"}[scenario]
			if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "initial-restart-failed") {
				t.Fatalf("misleading rollback result: %v", err)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantContent := "old: true\n"
			if scenario == "write-failed" {
				wantContent = "new: true\n"
			}
			if string(content) != wantContent {
				t.Fatalf("config=%q, want %q", content, wantContent)
			}
		})
	}
}
