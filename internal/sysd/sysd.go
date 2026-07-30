// Package sysd wraps systemctl service control.
package sysd

import (
	"bytes"
	"fmt"
	"os/exec"
)

// Action runs systemctl <verb> <unit> and returns combined output on error.
func Action(verb, unit string) error {
	cmd := exec.Command("systemctl", verb, unit)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s %s: %v: %s", verb, unit, err, out.String())
	}
	return nil
}

// IsActive reports whether the unit is active.
func IsActive(unit string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
}

// Status returns systemctl status text (may be non-zero exit for dead units).
func Status(unit string) string {
	out, _ := exec.Command("systemctl", "status", "--no-pager", "-l", unit).CombinedOutput()
	return string(out)
}
