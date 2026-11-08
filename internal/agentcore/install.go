package agentcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runSystemctl(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

func writeUnitFile(path string, content []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-core-unit-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(content); err == nil {
		err = f.Chmod(mode)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Activation is a transaction over the binary, unit and service state. In
// particular, a failed migration must not run the previous binary as root.
func (m *Manager) activateInstall(tmp, servicePath string, control func(...string) error, restart func() error) (err error) {
	var oldUnit []byte
	var unitMode os.FileMode
	if st, e := os.Lstat(servicePath); e == nil {
		if !st.Mode().IsRegular() {
			return errors.New("AgentCore 服务文件必须是普通文件，请先解除屏蔽或符号链接")
		}
		unitMode = st.Mode().Perm()
		oldUnit, err = os.ReadFile(servicePath)
		if err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	wasEnabled := control("is-enabled", "--quiet", "agent-core") == nil
	wasActive := control("is-active", "--quiet", "agent-core") == nil
	dest := m.cfg.AgentCore.BinPath
	old := dest + ".previous"
	if err = os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hadOld := false
	if err = os.Rename(dest, old); err == nil {
		hadOld = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	unitChanged, enableAttempted, restartAttempted := false, false, false
	defer func() {
		if err == nil {
			_ = os.Remove(old)
			return
		}
		var rollbackErr error
		if restartAttempted {
			rollbackErr = control("stop", "agent-core")
		}
		// Disable while the installed unit still exists so systemd can remove
		// its WantedBy links, including after a partially successful enable.
		if enableAttempted && !wasEnabled {
			rollbackErr = errors.Join(rollbackErr, control("disable", "agent-core"))
		}
		var binaryErr error
		if hadOld {
			binaryErr = os.Rename(old, dest)
		} else {
			binaryErr = os.Remove(dest)
			if errors.Is(binaryErr, os.ErrNotExist) {
				binaryErr = nil
			}
		}
		var unitErr error
		if unitChanged {
			if oldUnit != nil {
				unitErr = writeUnitFile(servicePath, oldUnit, unitMode)
			} else {
				unitErr = os.Remove(servicePath)
			}
			if unitErr == nil {
				unitErr = control("daemon-reload")
			}
		}
		rollbackErr = errors.Join(rollbackErr, binaryErr, unitErr)
		if enableAttempted && wasEnabled {
			rollbackErr = errors.Join(rollbackErr, control("enable", "agent-core"))
		}
		// Do not restart with a partially restored binary or unit.
		if wasActive && restartAttempted && binaryErr == nil && unitErr == nil {
			rollbackErr = errors.Join(rollbackErr, restart())
		}
		if rollbackErr != nil {
			err = fmt.Errorf("AgentCore 安装失败: %w；回滚失败: %w", err, rollbackErr)
		}
	}()
	if err = os.Rename(tmp, dest); err != nil {
		return err
	}
	if err = writeUnitFile(servicePath, []byte(m.unitContent()), 0644); err != nil {
		return err
	}
	unitChanged = true
	if err = control("daemon-reload"); err != nil {
		return err
	}
	enableAttempted = true
	if err = control("enable", "agent-core"); err != nil {
		return err
	}
	restartAttempted = true
	err = restart()
	return err
}
