package agentcore

import (
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
	"gopkg.in/yaml.v3"
)

func TestInitialConfigTerminalSafety(t *testing.T) {
	for _, tc := range []struct {
		name, server string
		terminal     bool
		auth         string
	}{
		{"default", "{}", true, ""},
		{"loopback", "{host: 127.0.0.1, terminalEnabled: true}", true, ""},
		{"localhost", "{host: localhost}", true, ""},
		{"ipv6", "{host: '::1'}", true, ""},
		{"explicit false", "{host: 127.0.0.1, terminalEnabled: false}", false, ""},
		{"network", "{host: 0.0.0.0, terminalEnabled: true, auth_token: '${AUTH_TOKEN}'}", false, "${AUTH_TOKEN}"},
		{"network missing flag", "{host: 192.168.1.2, auth_token: secret}", false, "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(&config.Config{AgentCore: config.AgentCore{WorkDir: t.TempDir()}})
			data, err := m.initialConfig([]byte("server: " + tc.server + "\nllm: {context_window: 123456, max_output_tokens: 4096, reasoning_effort: high}\n"))
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]any
			if err := yaml.Unmarshal(data, &root); err != nil {
				t.Fatal(err)
			}
			server := root["server"].(map[string]any)
			if server["terminalEnabled"] != tc.terminal {
				t.Fatalf("terminalEnabled = %v", server["terminalEnabled"])
			}
			auth, _ := server["auth_token"].(string)
			if auth != tc.auth {
				t.Fatalf("auth_token = %q", auth)
			}
			llm := root["llm"].(map[string]any)
			if llm["context_window"] != 123456 || llm["max_output_tokens"] != 4096 || llm["reasoning_effort"] != "high" {
				t.Fatalf("model settings changed: %v", llm)
			}
		})
	}
}

func TestInitialConfigRejectsInvalidExample(t *testing.T) {
	m := New(&config.Config{})
	for _, input := range []string{"", "null", "[]", "server: ["} {
		if _, err := m.initialConfig([]byte(input)); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
