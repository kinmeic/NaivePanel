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

// The shape a real server carries inline in /etc/caddy/Caddyfile: PHP site
// with forward_proxy upstream and the panel handle block.
func TestParseServerShape(t *testing.T) {
	snippet := `:443, hk.example.com {
	route {
		forward_proxy {
			basic_auth u1 p1
			hide_ip
			hide_via
			probe_resistance
			upstream https://u1:p1@sg2.example.com:443
		}

		handle /manage-abc/* {
			reverse_proxy 127.0.0.1:9000 {
				header_up X-NaivePanel-Key secret
			}
		}

		root * /var/www/html
		encode gzip zstd
		php_fastcgi unix//run/php/php8.3-fpm.sock
		file_server
	}
}`
	st, err := Parse(snippet, "/manage-abc", 1080)
	if err != nil {
		t.Fatal(err)
	}
	if st.Web.Type != WebPHP || st.Web.PHPSocket != "unix//run/php/php8.3-fpm.sock" {
		t.Fatalf("web mismatch: %+v", st.Web)
	}
	if !st.ForwardProxy.Enabled || st.ForwardProxy.Upstream != "https://u1:p1@sg2.example.com:443" {
		t.Fatalf("forward_proxy mismatch: %+v", st.ForwardProxy)
	}
	if len(st.ExtraBlocks) != 0 {
		t.Fatalf("panel handle block must be skipped, got %+v", st.ExtraBlocks)
	}
}

// Comments (whole-line and trailing) must not break parsing.
func TestParseToleratesComments(t *testing.T) {
	snippet := `# 站点说明
:443, hk.example.com {
	route {
		# 代理解锁
		forward_proxy {
			basic_auth u1 p1 # 账号
			hide_ip
			hide_via
			probe_resistance
			upstream https://u1:p1@sg2.example.com:443
		}

		root * /var/www/html # 站点目录
		encode gzip zstd
		php_fastcgi unix//run/php/php8.3-fpm.sock
		file_server
	}
}`
	st, err := Parse(snippet, "/manage-abc", 1080)
	if err != nil {
		t.Fatal(err)
	}
	if st.Web.Type != WebPHP || st.Web.Root != "/var/www/html" {
		t.Fatalf("web mismatch: %+v", st.Web)
	}
	if len(st.ForwardProxy.Accounts) != 1 || st.ForwardProxy.Accounts[0].Pass != "p1" {
		t.Fatalf("accounts mismatch: %+v", st.ForwardProxy.Accounts)
	}
}

// '#' inside a quoted token is not a comment.
func TestStripCommentQuoted(t *testing.T) {
	if got := stripComment(`basic_auth user "pa#ss"`); got != `basic_auth user "pa#ss"` {
		t.Fatalf("quoted hash mangled: %q", got)
	}
	if got := stripComment("root * /var/www # dir"); got != "root * /var/www" {
		t.Fatalf("trailing comment not stripped: %q", got)
	}
	if got := stripComment("# full line"); got != "" {
		t.Fatalf("full-line comment not stripped: %q", got)
	}
}
