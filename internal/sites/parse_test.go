package sites

import (
	"strings"
	"testing"
)

// roundTrip renders s and parses it back, returning the parsed model.
func roundTrip(t *testing.T, s *Site, host bool) *Site {
	t.Helper()
	out, err := Render(s, panel, host, 1080)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := Parse(out, panel.BasePath, 1080)
	if err != nil {
		t.Fatalf("Parse 解析面板自身渲染结果失败: %v\n%s", err, out)
	}
	return got
}

func TestParseRoundTripFull(t *testing.T) {
	s := &Site{
		Domain: "example.com",
		ForwardProxy: ForwardProxy{
			Enabled:       true,
			Accounts:      []Account{{User: "abcdefg", Pass: "1234567890"}, {User: "u2", Pass: "p 2"}},
			UseBypassCore: true,
		},
		Web: Web{Type: WebPHP, Root: "/var/www/example.com", PHPSocket: "unix//run/php/php8.3-fpm.sock"},
		ExtraBlocks: []ExtraBlock{
			{Type: BlockHandlePath, Matcher: "/api/*", Content: "reverse_proxy 127.0.0.1:8080"},
			{Type: BlockHandle, Matcher: "/static/*", Content: "root * /var/www/static\nfile_server"},
		},
	}
	got := roundTrip(t, s, true)

	if got.Domain != s.Domain {
		t.Errorf("domain = %q", got.Domain)
	}
	if got.RawMode {
		t.Error("不应为高级模式")
	}
	if !got.ForwardProxy.Enabled || !got.ForwardProxy.UseBypassCore {
		t.Error("forward_proxy / UseBypassCore 未还原")
	}
	if len(got.ForwardProxy.Accounts) != 2 ||
		got.ForwardProxy.Accounts[0] != s.ForwardProxy.Accounts[0] ||
		got.ForwardProxy.Accounts[1] != s.ForwardProxy.Accounts[1] {
		t.Errorf("accounts = %+v", got.ForwardProxy.Accounts)
	}
	if got.Web != s.Web {
		t.Errorf("web = %+v, want %+v", got.Web, s.Web)
	}
	if len(got.ExtraBlocks) != 2 {
		t.Fatalf("extra blocks = %+v", got.ExtraBlocks)
	}
	if got.ExtraBlocks[0] != s.ExtraBlocks[0] {
		t.Errorf("extra[0] = %+v", got.ExtraBlocks[0])
	}
	if got.ExtraBlocks[1] != s.ExtraBlocks[1] {
		t.Errorf("extra[1] = %+v, want %+v", got.ExtraBlocks[1], s.ExtraBlocks[1])
	}
}

func TestParseRoundTripVariants(t *testing.T) {
	cases := []struct {
		name string
		site *Site
	}{
		{"static no fp", &Site{Domain: "a.com", Web: Web{Type: WebStatic, Root: "/var/www/a"}}},
		{"reverse proxy", &Site{Domain: "b.com", Web: Web{Type: WebReverseProxy, ProxyTo: "127.0.0.1:3000"}}},
		{"pure proxy custom upstream", &Site{
			Domain:       "c.com",
			ForwardProxy: ForwardProxy{Enabled: true, Accounts: []Account{{User: "u", Pass: "p"}}, Upstream: "https://x:y@up.com:443"},
		}},
		{"fp direct no upstream", &Site{
			Domain:       "d.com",
			ForwardProxy: ForwardProxy{Enabled: true, Accounts: []Account{{User: "u", Pass: "p"}}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := roundTrip(t, tc.site, false)
			if got.Domain != tc.site.Domain {
				t.Errorf("domain = %q", got.Domain)
			}
			if got.Web != tc.site.Web {
				t.Errorf("web = %+v, want %+v", got.Web, tc.site.Web)
			}
			if got.ForwardProxy.Enabled != tc.site.ForwardProxy.Enabled {
				t.Errorf("fp enabled = %v", got.ForwardProxy.Enabled)
			}
			if got.ForwardProxy.Upstream != tc.site.ForwardProxy.Upstream {
				t.Errorf("upstream = %q, want %q", got.ForwardProxy.Upstream, tc.site.ForwardProxy.Upstream)
			}
			if got.ForwardProxy.UseBypassCore {
				t.Error("UseBypassCore 误判")
			}
		})
	}
}

func TestParseSkipsPanelBlock(t *testing.T) {
	s := &Site{Domain: "example.com", Web: Web{Type: WebStatic, Root: "/var/www/x"}}
	got := roundTrip(t, s, true)
	if len(got.ExtraBlocks) != 0 {
		t.Errorf("面板块不应进入 extra blocks: %+v", got.ExtraBlocks)
	}
}

func TestParseRejectsForeignSnippet(t *testing.T) {
	foreign := "example.com {\n\trespond ok\n}\n"
	if _, err := Parse(foreign, panel.BasePath, 1080); err == nil {
		t.Error("非面板渲染格式应报错（调用方回退高级模式导入）")
	}
}

func TestDomainFromHeader(t *testing.T) {
	if got := DomainFromHeader(":443, example.com {\n"); got != "example.com" {
		t.Errorf("got %q", got)
	}
	if got := DomainFromHeader("example.com {\n"); got != "example.com" {
		t.Errorf("got %q", got)
	}
	if got := DomainFromHeader("respond ok\n"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestSplitTokens(t *testing.T) {
	toks, err := splitTokens(`basic_auth user "pass word"`)
	if err != nil || len(toks) != 3 || toks[2] != "pass word" {
		t.Errorf("tokens = %v, err = %v", toks, err)
	}
	toks, err = splitTokens(`basic_auth user "pa\"ss"`)
	if err != nil || toks[2] != `pa"ss` {
		t.Errorf("tokens = %v, err = %v", toks, err)
	}
	if _, err := splitTokens(`basic_auth user "unclosed`); err == nil {
		t.Error("未闭合引号应报错")
	}
	if !strings.Contains(strings.Join(mustTokens(t, "handle /api/* {"), " "), "/api/*") {
		t.Error("handle tokens")
	}
}

func mustTokens(t *testing.T, line string) []string {
	t.Helper()
	toks, err := splitTokens(line)
	if err != nil {
		t.Fatal(err)
	}
	return toks
}
