package web

import (
	"encoding/json"
	"fmt"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentCorePreferences(t *testing.T) {
	for _, tc := range []struct {
		name, mode, stage, response string
		status, want                int
	}{
		{name: "plan", mode: "plan", want: 200},
		{name: "build", mode: "build", want: 200},
		{name: "edit", mode: "edit", want: 200},
		{name: "guarded", mode: "guarded", want: 200},
		{name: "yolo", mode: "yolo", want: 200},
		{name: "missing mode", want: 400},
		{name: "smart", mode: "smart", want: 400},
		{name: "auto", mode: "auto", want: 400},
		{name: "injection", mode: "plan\n/new", want: 400},
		{name: "missing", mode: "plan", stage: "initial", status: 404, want: 404},
		{name: "archived", mode: "plan", stage: "initial", response: `{"id":"abc","archived_at":"2026-01-01","metadata":{}}`, want: 409},
		{name: "bad conversation", mode: "plan", stage: "initial", response: `null`, want: 502},
		{name: "running", mode: "plan", stage: "runs", response: `[{"status":"running"}]`, want: 409},
		{name: "pending", mode: "plan", stage: "runs", response: `[{"status":"pending"}]`, want: 409},
		{name: "approval", mode: "plan", stage: "runs", response: `[{"status":"waiting_approval"}]`, want: 409},
		{name: "clarification", mode: "plan", stage: "runs", response: `[{"status":"waiting_clarification"}]`, want: 409},
		{name: "runs failure", mode: "plan", stage: "runs", status: 500, want: 502},
		{name: "command failure", mode: "plan", stage: "command", status: 500, want: 502},
		{name: "redirect", mode: "plan", stage: "command", status: 307, want: 502},
		{name: "not command", mode: "plan", stage: "command", response: `{"run_id":"unexpected"}`, want: 502},
		{name: "bad JSON", mode: "plan", stage: "command", response: `{`, want: 502},
		{name: "oversized", mode: "plan", stage: "command", response: strings.Repeat("x", (1<<20)+1), want: 502},
		{name: "verification failure", mode: "plan", stage: "verify", status: 500, want: 502},
		{name: "not updated", mode: "plan", stage: "verify", response: `{"id":"abc","metadata":{"mode":"build"}}`, want: 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			updated := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer config-secret" || r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" || r.URL.RawQuery != "" && r.URL.RawQuery != "limit=500" {
					t.Errorf("unsafe request: %s %v", r.URL, r.Header)
				}
				stage := "initial"
				switch {
				case r.Method == "GET" && r.URL.RequestURI() == "/v1/conversations/abc/runs?limit=500":
					stage = "runs"
				case r.Method == "POST" && r.URL.RequestURI() == "/v1/chat":
					stage = "command"
					var got map[string]string
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
						t.Error(err)
					}
					if len(got) != 3 || got["conversation_id"] != "abc" || got["message"] != "/mode "+tc.mode || got["source"] != "web" {
						t.Errorf("unexpected command: %v", got)
					}
					updated = true
				case r.Method == "GET" && r.URL.RequestURI() == "/v1/conversations/abc":
					if updated {
						stage = "verify"
					}
				default:
					t.Errorf("unexpected upstream: %s %s", r.Method, r.URL)
				}
				if stage == tc.stage {
					if tc.status != 0 {
						w.Header().Set("Location", "/secret-destination")
						w.WriteHeader(tc.status)
						fmt.Fprint(w, "config-secret")
						return
					}
					fmt.Fprint(w, tc.response)
					return
				}
				mode := "build"
				if updated {
					mode = tc.mode
				}
				conversation := fmt.Sprintf(`{"id":"abc","title":"Keep title","metadata":{"mode":%q,"model":"keep-model","effort":"high","locale":"zh-CN","custom":"keep"}}`, mode)
				if stage == "runs" {
					fmt.Fprint(w, `[{"status":"completed"}]`)
				} else if stage == "command" {
					fmt.Fprintf(w, `{"command":"mode","action":"preference","data":%s,"reply":"Conversation preference updated"}`, conversation)
				} else {
					fmt.Fprint(w, conversation)
				}
			}))
			defer upstream.Close()
			s := agentCoreProxyServer(t, upstream.URL)
			body, _ := json.Marshal(map[string]string{"mode": tc.mode, "message": "DO NOT FORWARD", "source": "evil"})
			r := httptest.NewRequest("POST", "/panel/agentcore/api/conversations/abc/preferences?token=evil", strings.NewReader(string(body)))
			r.SetPathValue("id", "abc")
			r.Header.Set("Cookie", "browser-secret")
			r.Header.Set("X-CSRF-Token", "browser-secret")
			w := httptest.NewRecorder()
			s.handleAgentCorePreferences(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body)
			}
			if strings.Contains(w.Body.String(), "config-secret") || w.Header().Get("Location") != "" {
				t.Fatal("leaked upstream secret/redirect")
			}
			if tc.want == 400 && calls != 0 {
				t.Fatal("invalid mode contacted upstream")
			}
			if tc.want == 200 {
				if calls != 4 {
					t.Fatalf("calls=%d", calls)
				}
				var got agentCorePreferenceConversation
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.ID != "abc" || got.Metadata["mode"] != tc.mode || got.Metadata["model"] != "keep-model" || got.Metadata["effort"] != "high" || got.Metadata["locale"] != "zh-CN" || got.Metadata["custom"] != "keep" {
					t.Fatalf("lost metadata: %+v", got)
				}
			} else if tc.stage == "initial" || tc.stage == "runs" {
				if updated {
					t.Fatal("rejected request changed preferences")
				}
			}
		})
	}
}

func TestAgentCorePreferencesProtection(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	token, session, err := s.Sessions.Create("admin", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cookie, csrf string
		want               int
	}{
		{"anonymous", "", "", 303},
		{"missing CSRF", token, "", 403},
		{"wrong CSRF", token, "wrong", 403},
		{"authenticated", token, session.CSRF, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", s.Cfg.BasePath+"/agentcore/api/conversations/abc/preferences", strings.NewReader(`{"mode":"invalid"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-CSRF-Token", tc.csrf)
			if tc.cookie != "" {
				r.AddCookie(&http.Cookie{Name: "np_session", Value: tc.cookie})
			}
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
	for _, body := range []string{`null`, `{`, `{"mode":"plan"} {}`, strings.Repeat("x", 4097)} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		r.SetPathValue("id", "abc")
		w := httptest.NewRecorder()
		s.handleAgentCorePreferences(w, r)
		if w.Code != 400 && w.Code != 413 {
			t.Fatalf("body accepted: %d", w.Code)
		}
	}

}
