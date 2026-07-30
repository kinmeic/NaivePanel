package sites

import (
	"strings"
	"testing"
)

var panel = PanelInfo{BasePath: "/manage-abc", Listen: "127.0.0.1:9000", ProxyToken: "secret-token-123"}

func TestRenderHostSiteFull(t *testing.T) {
	s := &Site{
		Domain: "example.com",
		ForwardProxy: ForwardProxy{
			Enabled:       true,
			Accounts:      []Account{{User: "abcdefg", Pass: "1234567890"}},
			UseBypassCore: true,
		},
		Web: Web{Type: WebPHP, Root: "/var/www/example.com", PHPSocket: "unix//run/php/php8.3-fpm.sock"},
		ExtraBlocks: []ExtraBlock{
			{Type: BlockHandlePath, Matcher: "/api/*", Content: "reverse_proxy 127.0.0.1:8080"},
		},
	}
	out, err := Render(s, panel, true, 1080)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		":443, example.com {",
		"handle /manage-abc/* {",
		"reverse_proxy 127.0.0.1:9000 {",
		"header_up X-NaivePanel-Key secret-token-123",
		"handle_path /api/* {",
		"forward_proxy {",
		"basic_auth abcdefg 1234567890",
		"hide_ip", "hide_via", "probe_resistance",
		"upstream socks5://127.0.0.1:1080",
		"php_fastcgi unix//run/php/php8.3-fpm.sock",
		"file_server",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Panel block must precede forward_proxy.
	if strings.Index(out, "handle /manage-abc") > strings.Index(out, "forward_proxy") {
		t.Error("panel block must render before forward_proxy")
	}
}

func TestRenderCustomUpstream(t *testing.T) {
	s := &Site{
		Domain: "a.com",
		ForwardProxy: ForwardProxy{
			Enabled:  true,
			Accounts: []Account{{User: "u", Pass: "p"}},
			Upstream: "https://x:y@up.com:443",
		},
		Web: Web{Type: WebReverseProxy, ProxyTo: "127.0.0.1:3000"},
	}
	out, err := Render(s, panel, false, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "upstream https://x:y@up.com:443") {
		t.Error("custom upstream missing")
	}
	if strings.Contains(out, "/manage-abc") {
		t.Error("non-host site must not contain panel block")
	}
	if !strings.Contains(out, "reverse_proxy 127.0.0.1:3000") {
		t.Error("reverse_proxy web type missing")
	}
}

func TestRenderValidation(t *testing.T) {
	s := &Site{Domain: "a.com", ForwardProxy: ForwardProxy{Enabled: true}}
	if _, err := Render(s, panel, false, 1080); err == nil {
		t.Error("expected error: forward_proxy without accounts")
	}
}

func TestRawModeHostGuard(t *testing.T) {
	s := &Site{Domain: "a.com", RawMode: true, Raw: "a.com {\n\trespond ok\n}\n"}
	if _, err := Render(s, panel, true, 1080); err == nil {
		t.Error("raw host site without panel path must be rejected")
	}
	// Panel path present but missing the shared-secret header: rejected.
	s.Raw = "a.com {\n\thandle /manage-abc/* {\n\t\treverse_proxy 127.0.0.1:9000\n\t}\n}\n"
	if _, err := Render(s, panel, true, 1080); err == nil {
		t.Error("raw host site without proxy token header must be rejected")
	}
	s.Raw = "a.com {\n\thandle /manage-abc/* {\n\t\treverse_proxy 127.0.0.1:9000 {\n\t\t\theader_up X-NaivePanel-Key secret-token-123\n\t\t}\n\t}\n}\n"
	if _, err := Render(s, panel, true, 1080); err != nil {
		t.Errorf("raw host site with panel block should pass: %v", err)
	}
}

func TestTokenQuoting(t *testing.T) {
	if got := token("plain"); got != "plain" {
		t.Errorf("plain token quoted: %s", got)
	}
	if got := token("with space"); got != `"with space"` {
		t.Errorf("space token not quoted: %s", got)
	}
}
