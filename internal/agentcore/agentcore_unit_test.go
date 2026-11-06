package agentcore

import (
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
)

func TestAgentCoreUnitRunsAsRootWithFilesystemIsolation(t *testing.T) {
	m := New(&config.Config{AgentCore: config.AgentCore{
		BinPath: "/usr/local/bin/agent-core", ConfigPath: "/etc/agent-core/agent-core.yaml", WorkDir: "/var/lib/agent-core",
	}})
	unit := m.unitContent()
	for _, line := range []string{
		"User=root", "Group=root", "WorkingDirectory=/var/lib/agent-core",
		"ExecStart=/usr/local/bin/agent-core -config /etc/agent-core/agent-core.yaml",
		"NoNewPrivileges=true", "PrivateTmp=true", "ProtectSystem=strict", "ProtectHome=true",
		"ReadWritePaths=/var/lib/agent-core",
	} {
		if !strings.Contains(unit, "\n"+line+"\n") {
			t.Errorf("unit missing %q", line)
		}
	}
	for _, line := range []string{"User=agent-core", "Group=agent-core", "NoNewPrivileges=false", "ProtectSystem=false", "ProtectHome=false"} {
		if strings.Contains(unit, "\n"+line+"\n") {
			t.Errorf("unexpected unit setting %q", line)
		}
	}
}
