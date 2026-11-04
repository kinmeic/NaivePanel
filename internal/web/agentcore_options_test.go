package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAgentCoreOptionsExposeOnlyComposerSettings(t *testing.T) {
	s := agentCoreProxyServer(t, "http://127.0.0.1:3030")
	for _, explicit := range []bool{false, true} {
		config := "server: {auth_token: secret-token}\nllm: {model: test-model, api_key: secret-key}\n"
		if explicit {
			config = "server: {auth_token: secret-token}\nllm: {model: test-model, api_key: secret-key, reasoning_effort: high, context_window: 64000, max_output_tokens: 8000}\ntools: {approval_mode: plan}\n"
		}
		if err := os.WriteFile(s.Cfg.AgentCore.ConfigPath, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		s.handleAgentCoreOptions(w, httptest.NewRequest("GET", "/panel/agentcore/api/options", nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response: %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "api_key") || strings.Contains(w.Body.String(), "auth_token") {
			t.Fatal("secret exposed")
		}
		var data map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		context, output, mode, effort := float64(200000), float64(32000), "build", ""
		if explicit {
			context, output, mode, effort = 64000, 8000, "plan", "high"
		}
		if data["model"] != "test-model" || data["reasoning_effort"] != effort || data["context_window"] != context || data["max_output_tokens"] != output || data["approval_mode"] != mode {
			t.Fatalf("settings: %#v", data)
		}
	}
}
