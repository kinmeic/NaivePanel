package agentcore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
)

func TestActivateInstallRestoresBinaryUnitAndServiceState(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			for _, stage := range []string{"reload", "enable", "restart", "success"} {
				name := stage
				if existing {
					name += "/upgrade"
				} else {
					name += "/fresh"
				}
				if enabled {
					name += "/enabled"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					dest, tmp, unit := filepath.Join(dir, "agent-core"), filepath.Join(dir, "agent-core.new"), filepath.Join(dir, "agent-core.service")
					oldUnit := "[Service]\nUser=agent-core\nGroup=agent-core\n"
					write := func(path, value string, mode os.FileMode) {
						t.Helper()
						if err := os.WriteFile(path, []byte(value), mode); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(path, mode); err != nil {
							t.Fatal(err)
						}
					}
					if existing {
						write(dest, "old binary", 0755)
						write(unit, oldUnit, 0600)
					}
					write(tmp, "new binary", 0755)
					m := New(&config.Config{AgentCore: config.AgentCore{BinPath: dest, WorkDir: dir}})
					isEnabled, isActive, failed, restarts := enabled, existing, false, 0
					failure := errors.New("injected " + stage + " failure")
					control := func(args ...string) error {
						switch args[0] {
						case "is-enabled":
							if !isEnabled {
								return errors.New("disabled")
							}
						case "is-active":
							if !isActive {
								return errors.New("inactive")
							}
						case "enable":
							isEnabled = true // Even a failed enable can leave a symlink.
						case "disable":
							if _, err := os.Stat(unit); err != nil {
								t.Fatal("unit removed before systemd could disable it")
							}
							isEnabled = false
						case "stop":
							isActive = false
						}
						if !failed && (stage == "reload" && args[0] == "daemon-reload" || stage == "enable" && args[0] == "enable") {
							failed = true
							return failure
						}
						return nil
					}
					read := func(path string) string {
						t.Helper()
						b, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						return string(b)
					}
					failureErr := m.activateInstall(tmp, unit, control, func() error {
						restarts++
						isActive = true
						if restarts == 1 {
							if read(dest) != "new binary" || !strings.Contains(read(unit), "\nUser=root\n") {
								t.Fatal("activation did not use new binary and root unit")
							}
							if stage == "restart" {
								return failure
							}
						} else if read(dest) != "old binary" || read(unit) != oldUnit {
							t.Fatal("previous service restarted without restoring binary AND unit")
						}
						return nil
					})
					if stage == "success" {
						if failureErr != nil || !isActive || !isEnabled || read(dest) != "new binary" {
							t.Fatalf("activation failed: %v", failureErr)
						}
					} else {
						if !errors.Is(failureErr, failure) || isEnabled != enabled || isActive != existing {
							t.Fatalf("rollback err=%v enabled=%v active=%v", failureErr, isEnabled, isActive)
						}
						if existing {
							if read(dest) != "old binary" || read(unit) != oldUnit {
								t.Fatal("rollback did not restore files")
							}
							st, err := os.Stat(unit)
							if err != nil || st.Mode().Perm() != 0600 {
								t.Fatalf("unit permissions not restored: %v %v", st, err)
							}
						} else {
							for _, path := range []string{dest, unit} {
								if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
									t.Fatalf("fresh install left %s: %v", path, err)
								}
							}
						}
					}
					if _, err := os.Stat(dest + ".previous"); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("binary backup not cleaned up: %v", err)
					}
				})
			}
		}
	}
}

func TestActivateInstallPreservesInactiveServiceOnFailure(t *testing.T) {
	dir := t.TempDir()
	dest, tmp, unit := filepath.Join(dir, "agent-core"), filepath.Join(dir, "agent-core.new"), filepath.Join(dir, "agent-core.service")
	for path, data := range map[string]string{dest: "old", tmp: "new", unit: "User=agent-core\n"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(&config.Config{AgentCore: config.AgentCore{BinPath: dest, WorkDir: dir}})
	restarts, stopped := 0, false
	err := m.activateInstall(tmp, unit, func(args ...string) error {
		if args[0] == "is-active" || args[0] == "is-enabled" {
			return errors.New("inactive and disabled")
		}
		if args[0] == "stop" {
			stopped = true
		}
		return nil
	}, func() error {
		restarts++
		return errors.New("start failed")
	})
	if err == nil || restarts != 1 || !stopped {
		t.Fatalf("previously inactive service restarted: %v, restarts=%d, stopped=%v", err, restarts, stopped)
	}
}

func TestActivateInstallReportsRollbackFailureWithoutRestarting(t *testing.T) {
	dir := t.TempDir()
	dest, tmp, unit := filepath.Join(dir, "agent-core"), filepath.Join(dir, "agent-core.new"), filepath.Join(dir, "agent-core.service")
	for path, data := range map[string]string{dest: "old", tmp: "new", unit: "User=agent-core\n"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(&config.Config{AgentCore: config.AgentCore{BinPath: dest, WorkDir: dir}})
	reloads, restarts := 0, 0
	rollbackFailure := errors.New("rollback daemon-reload failed")
	err := m.activateInstall(tmp, unit, func(args ...string) error {
		if args[0] == "daemon-reload" {
			reloads++
			if reloads > 1 {
				return rollbackFailure
			}
		}
		return nil
	}, func() error {
		restarts++
		return errors.New("start failed")
	})
	if !errors.Is(err, rollbackFailure) || !strings.Contains(err.Error(), "回滚失败") || restarts != 1 {
		t.Fatalf("rollback failure hidden or unsafe restart: %v, restarts=%d", err, restarts)
	}
}
