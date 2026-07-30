// Package sysd wraps systemctl service control.
package sysd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"
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

// IsEnabled reports whether the unit is configured to start automatically.
func IsEnabled(unit string) bool {
	return exec.Command("systemctl", "is-enabled", "--quiet", unit).Run() == nil
}

// Status returns systemctl status text (may be non-zero exit for dead units).
func Status(unit string) string {
	out, _ := exec.Command("systemctl", "status", "--no-pager", "-l", unit).CombinedOutput()
	return string(out)
}

// Log returns the last lines of the unit's journal (newest last). lines is
// clamped to [1, 1000]. Only call it with a whitelisted unit name — the value
// is passed to journalctl as-is.
func Log(unit string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	if lines > 1000 {
		lines = 1000
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", "-u", unit,
		"-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("读取日志失败（journalctl -u %s）: %v: %s", unit, err, out.String())
	}
	if out.Len() == 0 {
		return "（暂无日志）", nil
	}
	return out.String(), nil
}
