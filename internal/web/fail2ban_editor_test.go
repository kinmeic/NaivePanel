package web

import (
	"os/exec"
	"testing"
)

func TestFail2banStructuredEditor(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js unavailable; run node --test testdata/fail2ban-config.test.cjs to exercise the editor")
	}
	if output, err := exec.Command(node, "--test", "testdata/fail2ban-config.test.cjs").CombinedOutput(); err != nil {
		t.Fatalf("Fail2ban editor tests: %v\n%s", err, output)
	}
}
