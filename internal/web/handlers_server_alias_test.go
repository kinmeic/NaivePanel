package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kinmeic/NaivePanel/internal/config"
	"github.com/kinmeic/NaivePanel/internal/systemstats"
)

func TestServerAliasSaveClearAndValidation(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, want string }{
		{"  上海主服务器  ", "上海主服务器"},
		{"bad\nname", "上海主服务器"},
		{strings.Repeat("别", 81), "上海主服务器"},
		{"", ""},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, authenticatedRequest(t, s, http.MethodPost, "/settings/server-alias", url.Values{"server_alias": {tc.input}}))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/manage-test/settings" {
			t.Fatalf("response: %d %s", w.Code, w.Header().Get("Location"))
		}
		if s.Cfg.GetServerAlias() != tc.want {
			t.Fatalf("in-memory alias=%q, want %q", s.Cfg.GetServerAlias(), tc.want)
		}
		loaded, err := config.Load(s.Cfg.Path())
		if err != nil {
			t.Fatal(err)
		}
		if loaded.GetServerAlias() != tc.want {
			t.Fatalf("persisted alias=%q err=%v", loaded.GetServerAlias(), err)
		}
	}
}

func TestDashboardAliasPositionAndEscaping(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"<上海主服务器>", ""} {
		w := httptest.NewRecorder()
		s.render(w, httptest.NewRequest(http.MethodGet, "/manage-test/", nil), "dashboard", "概览", map[string]any{
			"Host": systemstats.HostInfo{Hostname: "test-host", Distribution: "Example Linux"}, "ServerAlias": alias,
		})
		html := w.Body.String()
		if alias == "" {
			if strings.Contains(html, "服务器别名") {
				t.Fatal("empty alias row was rendered")
			}
			continue
		}
		hostAt, aliasAt, distroAt := strings.Index(html, "test-host"), strings.Index(html, "&lt;上海主服务器&gt;"), strings.Index(html, "Example Linux")
		if hostAt < 0 || aliasAt <= hostAt || aliasAt >= distroAt || strings.Contains(html, alias) {
			t.Fatalf("alias escaped/position: %s", html)
		}
	}
}
