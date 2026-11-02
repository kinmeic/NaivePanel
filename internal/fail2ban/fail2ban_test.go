package fail2ban

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m := New()
	m.rulesPath = filepath.Join(t.TempDir(), "jail.d", "zz-naivepanel.local")
	m.logPath = filepath.Join(t.TempDir(), "fail2ban.log")
	m.installed = func() bool { return true }
	m.active = func(string) bool { return true }
	m.enabled = func(string) bool { return true }
	m.run = func(time.Duration, string, ...string) (string, error) { return "", nil }
	return m
}

func TestApplyRulesValidatesReloadsAndRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		exists, active       bool
		failTest, failReload bool
	}{
		{"running", true, true, false, false},
		{"stopped", true, false, false, false},
		{"invalid existing", true, true, true, false},
		{"invalid new", false, true, true, false},
		{"reload failure", true, true, false, true},
		{"new reload failure", false, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t)
			m.active = func(string) bool { return tc.active }
			const old = "[sshd]\nenabled = false\n"
			const next = "[sshd]\nenabled = true\nport = 2222\n"
			if tc.exists {
				if err := atomicWrite(m.rulesPath, []byte(old), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var commands []string
			m.run = func(_ time.Duration, name string, args ...string) (string, error) {
				if name != "fail2ban-client" {
					t.Fatalf("unexpected command %s", name)
				}
				commands = append(commands, strings.Join(args, " "))
				if args[0] == "-t" {
					b, _ := os.ReadFile(m.rulesPath)
					if string(b) != next {
						t.Fatalf("test did not see candidate: %q", b)
					}
					if tc.failTest {
						return "", errors.New("invalid configuration")
					}
				}
				if args[0] == "reload" && tc.failReload && len(commands) == 2 {
					return "", errors.New("reload failed")
				}
				return "", nil
			}
			err := m.ApplyRules(next)
			failed := tc.failTest || tc.failReload
			if (err != nil) != failed {
				t.Fatalf("err=%v, want failure=%v", err, failed)
			}
			got, readErr := os.ReadFile(m.rulesPath)
			switch {
			case failed && !tc.exists:
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("new override not removed: %v", readErr)
				}
			case failed:
				if string(got) != old {
					t.Fatalf("rollback got %q", got)
				}
			default:
				if string(got) != next {
					t.Fatalf("saved %q", got)
				}
			}
			if tc.exists {
				st, _ := os.Stat(m.rulesPath)
				if st.Mode().Perm() != 0600 {
					t.Fatal("file permissions changed")
				}
			}
			want := []string{"-t"}
			if !tc.failTest && tc.active {
				want = append(want, "reload")
			}
			if tc.failReload {
				want = append(want, "reload")
			}
			if !reflect.DeepEqual(commands, want) {
				t.Fatalf("commands=%v, want %v", commands, want)
			}
		})
	}
}

func TestApplyRulesReportsFailedRecovery(t *testing.T) {
	m := testManager(t)
	m.run = func(_ time.Duration, _ string, args ...string) (string, error) {
		if args[0] == "reload" {
			return "", errors.New("offline")
		}
		return "", nil
	}
	if err := m.ApplyRules(DefaultRules); err == nil || !strings.Contains(err.Error(), "重新加载失败") {
		t.Fatalf("lost recovery error: %v", err)
	}
}

func TestReadRulesDefaultAndSizeLimit(t *testing.T) {
	m := testManager(t)
	file, err := m.ReadRuleFile()
	if err != nil || file.Saved || file.Path != m.rulesPath {
		t.Fatalf("draft state: %#v %v", file, err)
	}
	got, err := m.ReadRules()
	if err != nil || got != DefaultRules {
		t.Fatalf("default: %q %v", got, err)
	}
	if _, err := os.Stat(m.rulesPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reading defaults created a file")
	}
	if err := atomicWrite(m.rulesPath, []byte(strings.Repeat("x", maxRulesSize+10)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadRules(); err == nil {
		t.Fatal("oversized file was accepted")
	}
	if err := m.ApplyRules(strings.Repeat("x", maxRulesSize+1)); err == nil {
		t.Fatal("oversized submission was accepted")
	}
	if err := m.ApplyRules("[sshd]\x00"); err == nil {
		t.Fatal("NUL accepted")
	}
}

func TestReadRuleFileSavedStateAndUnboundedHistory(t *testing.T) {
	m := testManager(t)
	if err := atomicWrite(m.rulesPath, []byte("[sshd]\nenabled = false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file, err := m.ReadRuleFile()
	if err != nil || !file.Saved || file.Content != "[sshd]\nenabled = false\n" {
		t.Fatalf("saved state: %#v %v", file, err)
	}
	var events []banEvent
	for i := 0; i < 250; i++ {
		events = append(events, banEvent{jail: "sshd", ip: "192.0.2.1", action: "Ban", at: time.Unix(int64(i*2), 0)}, banEvent{jail: "sshd", ip: "192.0.2.1", action: "Unban", at: time.Unix(int64(i*2+1), 0)})
	}
	if records := buildRecords(events, nil, 0); len(records) != 250 {
		t.Fatalf("pagination lost records: %d", len(records))
	}
}

func TestServiceActionsWhitelistAndValidateBeforeStarting(t *testing.T) {
	m := testManager(t)
	var calls []string
	m.action = func(verb, unit string) error { calls = append(calls, verb+" "+unit); return nil }
	m.run = func(_ time.Duration, _ string, args ...string) (string, error) {
		if !reflect.DeepEqual(args, []string{"-t"}) {
			t.Fatalf("unexpected args: %v", args)
		}
		return "", errors.New("bad rules")
	}
	for _, verb := range []string{"start", "restart", "stop; reboot", "--help"} {
		if err := m.Action(verb); err == nil {
			t.Fatalf("accepted %q", verb)
		}
	}
	for _, verb := range []string{"stop", "enable", "disable"} {
		if err := m.Action(verb); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"stop fail2ban", "enable fail2ban", "disable fail2ban"}) {
		t.Fatalf("unexpected actions: %v", calls)
	}
}

func TestBanHistoryCyclesRestoresAndLiveState(t *testing.T) {
	const log = `2026-11-02 09:00:00,100 fail2ban.actions [12]: NOTICE [sshd] Ban 192.0.2.1
2026-11-02 09:00:00,100 fail2ban.actions [12]: NOTICE [sshd] Ban 192.0.2.1
2026-11-02 09:01:00,000 fail2ban.actions [12]: NOTICE [sshd] Restore Ban 192.0.2.1
2026-11-02 10:00:00,000 fail2ban.actions [12]: NOTICE [sshd] Unban 192.0.2.1
2026-11-02 11:00:00,000 fail2ban.actions [12]: NOTICE [sshd] Ban 192.0.2.1
2026-11-02 11:01:00,000 fail2ban.actions [12]: NOTICE [nginx] Ban 192.0.2.1
2026-11-02 11:02:00,000 fail2ban.actions [12]: NOTICE [sshd] Restore Ban 2001:db8::1
2026-11-02 11:03:00,000 fail2ban.actions [12]: NOTICE [sshd] Unban 192.0.2.9
2026-11-02 11:04:00,000 fail2ban.filter [12]: INFO [sshd] Found 192.0.2.5
`
	var events []banEvent
	for _, line := range strings.Split(log, "\n") {
		if e, ok := parseEvent(line, time.Time{}); ok {
			events = append(events, e)
		}
	}
	records := buildRecords(events, map[string][]string{"sshd": {"192.0.2.1", "2001:db8::1", "192.0.2.99"}}, 200)
	if len(records) != 6 {
		t.Fatalf("got %d records: %#v", len(records), records)
	}
	var current, closed, missing, notBanned int
	for _, r := range records {
		if r.Current {
			current++
			if !r.UnbannedAt.IsZero() || r.State != "封禁中" {
				t.Fatalf("bad active record: %#v", r)
			}
		}
		if r.IP == "192.0.2.1" && !r.UnbannedAt.IsZero() {
			closed++
			if r.BannedAt.Hour() != 9 || r.UnbannedAt.Hour() != 10 {
				t.Fatalf("cycle merged incorrectly: %#v", r)
			}
		}
		if r.BannedAt.IsZero() {
			missing++
		}
		if r.Jail == "nginx" {
			notBanned++
			if !r.UnbannedAt.IsZero() || r.State != "已不在封禁列表" {
				t.Fatalf("invented unban: %#v", r)
			}
		}
	}
	if current != 3 || closed != 1 || missing != 3 || notBanned != 1 {
		t.Fatalf("counts: %d %d %d %d", current, closed, missing, notBanned)
	}
	if got := buildRecords(events, nil, 200); got[0].State == "封禁中" {
		t.Fatal("unknown runtime reported as banned")
	}
	if got := buildRecords(events, map[string][]string{}, 2); len(got) != 2 {
		t.Fatal("record limit ignored")
	}
}

func TestStartConfirmsControlSocketReadiness(t *testing.T) {
	m := testManager(t)
	var steps []string
	m.run = func(_ time.Duration, _ string, args ...string) (string, error) {
		steps = append(steps, args[0])
		return "", nil
	}
	m.action = func(verb, unit string) error { steps = append(steps, verb+" "+unit); return nil }
	if err := m.Action("start"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(steps, []string{"-t", "start fail2ban", "ping"}) {
		t.Fatalf("steps=%v", steps)
	}
}

func TestSnapshotCombinesCustomLogsAndJournal(t *testing.T) {
	m := testManager(t)
	custom := filepath.Join(t.TempDir(), "custom.log")
	ban := "2026-11-02 09:00:00,000 fail2ban.actions [12]: NOTICE [sshd] Ban 192.0.2.1"
	if err := os.WriteFile(custom+".1", []byte(ban), 0600); err != nil {
		t.Fatal(err)
	}
	m.run = func(_ time.Duration, name string, args ...string) (string, error) {
		if name == "journalctl" {
			return `{"MESSAGE":"2026-11-02 09:00:00,000 fail2ban.actions [12]: NOTICE [sshd] Ban 192.0.2.1","__REALTIME_TIMESTAMP":"1793600000000000"}`, nil
		}
		switch strings.Join(args, " ") {
		case "banned":
			return "[{'sshd': ['192.0.2.1']}, {'nginx': []}]", nil
		case "get logtarget":
			return "Current logging target is:\n`- " + custom, nil
		}
		return "", errors.New("unexpected command")
	}
	s := m.Snapshot()
	if s.StatusError != "" || s.HistoryError != "" || !s.Active || !s.Enabled || s.CurrentCount != 1 || len(s.Records) != 1 || !s.Records[0].Current || len(s.Jails) != 2 {
		t.Fatalf("snapshot: %#v", s)
	}
}

func TestParseBannedAndJournalFallback(t *testing.T) {
	for _, input := range []string{"[]", "[{'sshd': []}]", "[{'sshd': ['192.0.2.1', '2001:db8::1']}]", "WARNING allowipv6 defaults to auto\n[{'sshd': []}]\n"} {
		if _, err := parseBanned(input); err != nil {
			t.Fatalf("%s: %v", input, err)
		}
	}
	for _, input := range []string{"error", "[{'sshd': ['not-an-ip']}]"} {
		if _, err := parseBanned(input); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	now := time.Now()
	e, ok := parseEvent("NOTICE [sshd] Ban 2001:db8::1", now)
	if !ok || !e.at.Equal(now) {
		t.Fatal("journal timestamp fallback lost")
	}
	if _, ok := parseEvent("NOTICE [sshd] Ban 192.0.2.1", time.Time{}); ok {
		t.Fatal("invented timestamp")
	}
}
