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

const (
	actionTimeout = 45 * time.Second
	queryTimeout  = 5 * time.Second
	statusTimeout = 10 * time.Second
	maxOutputSize = 4 << 20
)

type limitedBuffer struct {
	buf       bytes.Buffer
	remaining int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{remaining: limit}
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	if len(data) > b.remaining {
		data = data[:b.remaining]
		b.truncated = true
	}
	if len(data) > 0 {
		_, _ = b.buf.Write(data)
		b.remaining -= len(data)
	}
	return originalLength, nil
}

func (b *limitedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "\n（输出超过 4 MiB，已截断）"
	}
	return b.buf.String()
}

// Action runs systemctl <verb> <unit> and returns combined output on error.
func Action(verb, unit string) error {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", verb, unit)
	out := newLimitedBuffer(maxOutputSize)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("systemctl %s %s 超时", verb, unit)
		}
		return fmt.Errorf("systemctl %s %s: %v: %s", verb, unit, err, out.String())
	}
	return nil
}

// IsActive reports whether the unit is active.
func IsActive(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

// IsEnabled reports whether the unit is configured to start automatically.
func IsEnabled(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-enabled", "--quiet", unit).Run() == nil
}

// Status returns systemctl status text (may be non-zero exit for dead units).
func Status(unit string) string {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "status", "--no-pager", "-l", unit)
	out := newLimitedBuffer(maxOutputSize)
	cmd.Stdout, cmd.Stderr = out, out
	_ = cmd.Run()
	return out.String()
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
	out := newLimitedBuffer(maxOutputSize)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("读取日志超时（journalctl -u %s）", unit)
		}
		return "", fmt.Errorf("读取日志失败（journalctl -u %s）: %v: %s", unit, err, out.String())
	}
	if out.buf.Len() == 0 {
		return "（暂无日志）", nil
	}
	return out.String(), nil
}
