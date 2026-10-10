package warp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStateDistinguishesUnavailableFromUnregistered(t *testing.T) {
	for _, tc := range []struct {
		name, registration string
		err                error
		known, registered  bool
	}{
		{"registered", "Registration:\n  ID: device-123\n  Account type: Unlimited", nil, true, true},
		{"missing", "Error: Missing registration. Try running warp-cli registration new", errors.New("exit 1"), true, false},
		{"daemon failure", "Unable to connect to daemon", errors.New("exit 1"), false, false},
		{"unknown format", "Success", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			known, registered, _, _ := parseRegistration(tc.registration, tc.err)
			if known != tc.known || registered != tc.registered {
				t.Fatalf("known=%v registered=%v", known, registered)
			}
		})
	}
	for _, tc := range []struct{ output, state string }{
		{"Status update: Connected", "connected"}, {"Status update: Disconnected\nReason: User disconnected", "disconnected"},
		{"Status update: Connecting", "connecting"}, {"Status update: Disconnecting", "disconnecting"}, {"Unexpected", ""},
	} {
		state, _, _ := parseConnection(tc.output)
		if state != tc.state {
			t.Fatalf("%q: got %q", tc.output, state)
		}
	}
	mode, port := parseSettings("(User set) Mode: WarpProxy on port 40000\nOther: true")
	if mode != "proxy" || port != 40000 {
		t.Fatalf("mode=%q port=%d", mode, port)
	}
}

func fakeManager(t *testing.T, command func([]string) (string, error)) *Manager {
	t.Helper()
	return &Manager{
		lookup: func(string) (string, error) { return "/usr/bin/warp-cli", nil },
		active: func(string) bool { return true }, enabled: func(string) bool { return true },
		action: func(string, string) error { return nil },
		run: func(_ context.Context, name string, args ...string) (string, error) {
			if name != "/usr/bin/warp-cli" || args[0] != "--accept-tos" {
				t.Errorf("unexpected CLI: %s %v", name, args)
			}
			return command(args[1:])
		},
	}
}

func TestConnectEnforcesProxyBeforeConnecting(t *testing.T) {
	var calls []string
	settingsCalls := 0
	m := fakeManager(t, func(args []string) (string, error) {
		cmd := strings.Join(args, " ")
		calls = append(calls, cmd)
		switch cmd {
		case "registration show":
			return "Account type: Free", nil
		case "settings":
			settingsCalls++
			if settingsCalls == 1 {
				return "Mode: WarpWithDnsOverHttps", nil
			}
			return "Mode: WarpProxy on port 40000", nil
		}
		return "Success", nil
	})
	if err := m.Connect(); err != nil {
		t.Fatal(err)
	}
	expected := []string{"registration show", "settings", "disconnect", "tunnel protocol set MASQUE", "mode proxy", "proxy port 40000", "settings", "connect"}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("commands: %v", calls)
	}
}

func TestConnectNeverConnectsOnRegistrationOrProxyFailure(t *testing.T) {
	for _, scenario := range []string{"unregistered", "mode failed", "policy override"} {
		t.Run(scenario, func(t *testing.T) {
			var calls []string
			m := fakeManager(t, func(args []string) (string, error) {
				cmd := strings.Join(args, " ")
				calls = append(calls, cmd)
				switch cmd {
				case "registration show":
					if scenario == "unregistered" {
						return "Missing registration", errors.New("missing")
					}
					return "Account type: Free", nil
				case "settings":
					return "Mode: WarpWithDnsOverHttps", nil
				case "mode proxy":
					if scenario == "mode failed" {
						return "", errors.New("mode failed")
					}
				}
				return "Success", nil
			})
			if err := m.Connect(); err == nil {
				t.Fatal("expected error")
			}
			for _, call := range calls {
				if call == "connect" {
					t.Fatal("connected despite failed prerequisite")
				}
			}
		})
	}
}

func TestSetPortPreservesConnectionIntent(t *testing.T) {
	for _, state := range []string{"Connected", "Disconnected", "Connecting"} {
		t.Run(state, func(t *testing.T) {
			var connected bool
			m := fakeManager(t, func(args []string) (string, error) {
				switch strings.Join(args, " ") {
				case "registration show":
					return "Account type: Free", nil
				case "settings":
					return "Mode: WarpProxy on port 40000", nil
				case "status":
					return "Status update: " + state, nil
				case "connect":
					connected = true
				}
				return "Success", nil
			})
			if err := m.SetPort(40000); err != nil {
				t.Fatal(err)
			}
			if connected != (state != "Disconnected") {
				t.Fatalf("connected=%v", connected)
			}
		})
	}
}

func TestRegisterDoesNotReplaceExistingRegistration(t *testing.T) {
	var replaced bool
	m := fakeManager(t, func(args []string) (string, error) {
		if strings.Join(args, " ") == "registration new" {
			replaced = true
		}
		return "Account type: Free", nil
	})
	if err := m.Register(); err == nil {
		t.Fatal("expected already-registered error")
	}
	if replaced {
		t.Fatal("replaced existing registration")
	}
}

func TestLicenseErrorsDoNotExposeKey(t *testing.T) {
	key := "abcd1234-EFGH5678-ijkl9012"
	m := fakeManager(t, func(args []string) (string, error) {
		if args[0] == "registration" && args[1] == "show" {
			return "Account type: Free", nil
		}
		return key, errors.New("invalid license " + key)
	})
	err := m.BindLicense(key)
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("unsafe error: %v", err)
	}
	if strings.Contains(redact("License: "+key), key) {
		t.Fatal("key was not redacted")
	}
}

func TestInstallScriptUsesSignedOfficialRepositories(t *testing.T) {
	for _, release := range []string{
		"ID=debian\nVERSION_CODENAME=bookworm", "ID=ubuntu\nVERSION_CODENAME=noble", "ID=fedora\nVERSION_ID=44", "ID=rocky\nVERSION_ID=9.6",
	} {
		script, err := installScript(release)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(script, "https://pkg.cloudflareclient.com/") || !strings.Contains(script, "cloudflare-warp") || strings.Contains(script, "dnf update") {
			t.Fatalf("unexpected script: %s", script)
		}
	}
	for _, release := range []string{"ID=alpine\nVERSION_ID=3.20", "ID=ubuntu\nVERSION_CODENAME=noble; touch /tmp/injected", "ID=debian\nVERSION_CODENAME=unknown"} {
		if _, err := installScript(release); err == nil {
			t.Fatalf("accepted unsupported release %q", release)
		}
	}
}
