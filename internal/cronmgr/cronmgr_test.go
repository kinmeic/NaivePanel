package cronmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) (*Manager, Paths) {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		StateFile: filepath.Join(root, "state.json"),
		ScriptDir: filepath.Join(root, "scripts"),
		CronFile:  filepath.Join(root, "cron.d"),
		LogFile:   filepath.Join(root, "cron.log"),
	}
	manager, err := New(paths)
	if err != nil {
		t.Fatal(err)
	}
	return manager, paths
}

func TestValidateSchedule(t *testing.T) {
	valid := []string{"* * * * *", "0 3 * * 1-5", "*/15 0-23/2 1,15 * 0,7"}
	for _, schedule := range valid {
		if err := ValidateSchedule(schedule); err != nil {
			t.Errorf("%q should be valid: %v", schedule, err)
		}
	}
	invalid := []string{
		"@daily", "* * * *", "60 * * * *", "* 24 * * *",
		"* * 0 * *", "* * * JAN *", "* * * * 8", "* * * * *\nroot id",
		"1/2 * * * *", "*/0 * * * *",
	}
	for _, schedule := range invalid {
		if err := ValidateSchedule(schedule); err == nil {
			t.Errorf("%q should be invalid", schedule)
		}
	}
}

func TestManagerCRUDAndCronRendering(t *testing.T) {
	manager, paths := testManager(t)
	task, err := manager.Create("每日备份", "0 3 * * *", "echo backup", true)
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(paths.ScriptDir, task.ID+".sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(script), "#!/bin/sh\nset -eu\n") {
		t.Fatalf("script not normalized: %q", script)
	}
	cronData, err := os.ReadFile(paths.CronFile)
	if err != nil {
		t.Fatal(err)
	}
	cron := string(cronData)
	if !strings.Contains(cron, "0 3 * * * root "+filepath.Join(paths.ScriptDir, task.ID+".sh")) {
		t.Fatalf("cron entry missing: %s", cron)
	}
	if strings.Contains(cron, "echo backup") {
		t.Fatal("inline user script leaked into cron file")
	}
	for path, want := range map[string]os.FileMode{
		paths.StateFile: 0o600,
		paths.CronFile:  0o644,
		paths.LogFile:   0o600,
		filepath.Join(paths.ScriptDir, task.ID+".sh"): 0o700,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode=%o want=%o", path, got, want)
		}
	}

	if err := manager.SetEnabled(task.ID, false); err != nil {
		t.Fatal(err)
	}
	cronData, _ = os.ReadFile(paths.CronFile)
	if strings.Contains(string(cronData), task.ID+".sh") {
		t.Fatal("disabled task remained in cron file")
	}

	if err := manager.Update(task.ID, "清理日志", "30 4 * * 0", "#!/bin/bash\necho clean\n", true); err != nil {
		t.Fatal(err)
	}
	got, ok := manager.Get(task.ID)
	if !ok || got.Name != "清理日志" || got.Schedule != "30 4 * * 0" {
		t.Fatalf("unexpected updated task: %#v", got)
	}

	if err := manager.Delete(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(paths.ScriptDir, task.ID+".sh")); !os.IsNotExist(err) {
		t.Fatalf("script should be removed, got %v", err)
	}
	reloaded, err := New(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.List()) != 0 {
		t.Fatal("deleted task survived reload")
	}
}

func TestRejectsCronInjectionAndOversizedScript(t *testing.T) {
	manager, _ := testManager(t)
	if _, err := manager.Create("bad", "* * * * *\n* * * * *", "id", true); err == nil {
		t.Fatal("expected schedule injection rejection")
	}
	if _, err := manager.Create("bad", "* * * * *\n", "id", true); err == nil {
		t.Fatal("expected trailing newline rejection")
	}
	if _, err := manager.Create("large", "* * * * *", strings.Repeat("x", maxScriptBytes+1), true); err == nil {
		t.Fatal("expected oversized script rejection")
	}
}

func TestInvalidStateDoesNotOverwriteOrBlockPanelStartup(t *testing.T) {
	manager, paths := testManager(t)
	bad := []byte("{not-json")
	if err := os.WriteFile(paths.StateFile, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(paths)
	if err != nil {
		t.Fatalf("optional feature should initialize in disabled state: %v", err)
	}
	if manager.LoadError() == nil {
		t.Fatal("invalid state was not reported")
	}
	if _, err := manager.Create("task", "* * * * *", "echo ok", true); err == nil {
		t.Fatal("mutation should be rejected while state is invalid")
	}
	got, err := os.ReadFile(paths.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bad) {
		t.Fatal("invalid state file was overwritten")
	}
}

func TestRunNowWritesLog(t *testing.T) {
	manager, _ := testManager(t)
	task, err := manager.Create("立即运行", "0 3 * * *", "printf 'run-now\\n'", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RunNow(task.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		logText, err := manager.ReadLog(4096)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logText, "run-now") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task output did not reach log: %q", logText)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
