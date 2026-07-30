// Package sites defines the site model and Caddyfile snippet rendering.
package sites

import (
	"fmt"
	"strings"
)

// Web site types.
const (
	WebNone         = "none"
	WebStatic       = "static"
	WebPHP          = "php"
	WebReverseProxy = "reverse_proxy"
)

// Extra block types.
const (
	BlockHandle     = "handle"
	BlockHandlePath = "handle_path"
)

// Account is one forward_proxy basic_auth credential.
type Account struct {
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
}

// ForwardProxy holds the naive forward_proxy block settings.
type ForwardProxy struct {
	Enabled       bool      `yaml:"enabled"`
	Accounts      []Account `yaml:"accounts"`
	UseBypassCore bool      `yaml:"use_bypasscore"`
	Upstream      string    `yaml:"upstream"` // used when UseBypassCore is false; may be empty
}

// Web holds the site-building part of a site.
type Web struct {
	Type      string `yaml:"type"` // none | static | php | reverse_proxy
	Root      string `yaml:"root"`
	PHPSocket string `yaml:"php_socket"`
	ProxyTo   string `yaml:"proxy_to"`
}

// ExtraBlock is a user-defined handle / handle_path block rendered before
// forward_proxy, in list order.
type ExtraBlock struct {
	Type    string `yaml:"type"` // handle | handle_path
	Matcher string `yaml:"matcher"`
	Content string `yaml:"content"`
}

// Site is one managed domain.
type Site struct {
	Domain       string       `yaml:"domain"`
	ForwardProxy ForwardProxy `yaml:"forward_proxy"`
	Web          Web          `yaml:"web"`
	ExtraBlocks  []ExtraBlock `yaml:"extra_blocks"`
	RawMode      bool         `yaml:"raw_mode"`
	Raw          string       `yaml:"raw"`
}

// ProxyTokenHeader is the shared-secret header Caddy injects when reverse
// proxying to the panel. The panel rejects any request without it, so the
// panel is only reachable through Caddy's HTTPS listener.
const ProxyTokenHeader = "X-NaivePanel-Key"

// PanelInfo carries what site rendering needs to know about the panel itself.
type PanelInfo struct {
	BasePath   string // e.g. /manage-x7k2q9
	Listen     string // e.g. 127.0.0.1:9000
	ProxyToken string // shared secret injected via header_up
}

// token quotes a Caddyfile token when it contains whitespace or quotes.
// Newlines are escaped rather than quoted so a value can never break out of
// its line and inject extra Caddyfile directives.
func token(s string) string {
	if s == "" {
		return `""`
	}
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	if strings.ContainsAny(s, " \t\"'") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func indent(b *strings.Builder, level int, s string) {
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(strings.Repeat("\t", level))
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// singleLine rejects values containing CR/LF — they would break out of the
// one-token-per-line structure the renderer emits.
func singleLine(field, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("%s 不能包含换行", field)
	}
	return nil
}

// Validate performs basic sanity checks on a site before rendering.
func (s *Site) Validate() error {
	if s.Domain == "" {
		return fmt.Errorf("域名不能为空")
	}
	if strings.ContainsAny(s.Domain, " \t\n/{}") {
		return fmt.Errorf("域名 %q 含非法字符", s.Domain)
	}
	if s.RawMode {
		if strings.TrimSpace(s.Raw) == "" {
			return fmt.Errorf("高级模式下原始配置不能为空")
		}
		return nil
	}
	if s.ForwardProxy.Enabled && len(s.ForwardProxy.Accounts) == 0 {
		return fmt.Errorf("forward_proxy 启用时至少需要一个 basic_auth 账号")
	}
	for _, a := range s.ForwardProxy.Accounts {
		if a.User == "" || a.Pass == "" {
			return fmt.Errorf("basic_auth 账号和密码不能为空")
		}
		if err := singleLine("basic_auth 账号", a.User); err != nil {
			return err
		}
		if err := singleLine("basic_auth 密码", a.Pass); err != nil {
			return err
		}
	}
	if err := singleLine("forward_proxy 上游", s.ForwardProxy.Upstream); err != nil {
		return err
	}
	switch s.Web.Type {
	case "", WebNone:
		s.Web.Type = WebNone
	case WebStatic:
		if s.Web.Root == "" {
			return fmt.Errorf("静态站需要指定站点目录")
		}
	case WebPHP:
		if s.Web.Root == "" || s.Web.PHPSocket == "" {
			return fmt.Errorf("PHP 站需要指定站点目录和 php-fpm socket")
		}
	case WebReverseProxy:
		if s.Web.ProxyTo == "" {
			return fmt.Errorf("反向代理站需要指定上游地址")
		}
	default:
		return fmt.Errorf("未知的站点类型 %q", s.Web.Type)
	}
	for _, v := range []struct{ field, val string }{
		{"站点目录", s.Web.Root},
		{"php-fpm socket", s.Web.PHPSocket},
		{"上游地址", s.Web.ProxyTo},
	} {
		if err := singleLine(v.field, v.val); err != nil {
			return err
		}
	}
	for i, eb := range s.ExtraBlocks {
		if eb.Type != BlockHandle && eb.Type != BlockHandlePath {
			return fmt.Errorf("自定义块 #%d 类型必须是 handle 或 handle_path", i+1)
		}
		if eb.Matcher == "" {
			return fmt.Errorf("自定义块 #%d 的匹配路径不能为空", i+1)
		}
		if err := singleLine(fmt.Sprintf("自定义块 #%d 的匹配路径", i+1), eb.Matcher); err != nil {
			return err
		}
		if strings.TrimSpace(eb.Content) == "" {
			return fmt.Errorf("自定义块 #%d 的内容不能为空", i+1)
		}
	}
	return nil
}

// Render produces the Caddyfile snippet for this site. hostSite marks the
// site that hosts the panel; socksPort is the local BypassCore SOCKS5 port.
func Render(s *Site, panel PanelInfo, hostSite bool, socksPort int) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.RawMode {
		raw := strings.TrimRight(s.Raw, "\n") + "\n"
		if hostSite {
			// Raw-mode host sites must keep the panel block themselves.
			if !strings.Contains(raw, panel.BasePath) {
				return "", fmt.Errorf("该站点是面板寄宿站点，高级模式的原始配置中必须保留面板路径 %s 的反向代理配置", panel.BasePath)
			}
			if panel.ProxyToken != "" && !strings.Contains(raw, ProxyTokenHeader) {
				return "", fmt.Errorf("高级模式下面板反代必须注入共享密钥头，请在面板的 reverse_proxy 块中加入:\n\t\t\t\theader_up %s %s", ProxyTokenHeader, panel.ProxyToken)
			}
		}
		return raw, nil
	}

	var b strings.Builder
	b.WriteString(":443, " + token(s.Domain) + " {\n\troute {\n")

	if hostSite {
		renderPanelBlock(&b, panel, 2)
	}

	for _, eb := range s.ExtraBlocks {
		indent(&b, 2, eb.Type+" "+token(eb.Matcher)+" {")
		indent(&b, 3, strings.TrimSpace(eb.Content))
		indent(&b, 2, "}")
		b.WriteString("\n")
	}

	if s.ForwardProxy.Enabled {
		indent(&b, 2, "forward_proxy {")
		for _, a := range s.ForwardProxy.Accounts {
			indent(&b, 3, "basic_auth "+token(a.User)+" "+token(a.Pass))
		}
		// Fixed hardening directives, never shown in the UI.
		indent(&b, 3, "hide_ip")
		indent(&b, 3, "hide_via")
		indent(&b, 3, "probe_resistance")
		upstream := s.ForwardProxy.Upstream
		if s.ForwardProxy.UseBypassCore {
			upstream = fmt.Sprintf("socks5://127.0.0.1:%d", socksPort)
		}
		if upstream != "" {
			indent(&b, 3, "upstream "+token(upstream))
		}
		indent(&b, 2, "}")
		b.WriteString("\n")
	}

	switch s.Web.Type {
	case WebStatic, WebPHP:
		indent(&b, 2, "root * "+token(s.Web.Root))
		indent(&b, 2, "encode gzip zstd")
		if s.Web.Type == WebPHP {
			indent(&b, 2, "php_fastcgi "+token(s.Web.PHPSocket))
		}
		indent(&b, 2, "file_server")
	case WebReverseProxy:
		indent(&b, 2, "reverse_proxy "+token(s.Web.ProxyTo))
	case WebNone:
		// no web part
	}

	b.WriteString("\t}\n}\n")
	return b.String(), nil
}

// renderPanelBlock renders the panel reverse_proxy handle at indent level n.
// The shared-secret header makes the panel unreachable except through this
// HTTPS reverse proxy.
func renderPanelBlock(b *strings.Builder, panel PanelInfo, n int) {
	indent(b, n, "handle "+panel.BasePath+"/* {")
	if panel.ProxyToken != "" {
		indent(b, n+1, "reverse_proxy "+panel.Listen+" {")
		indent(b, n+2, "header_up "+ProxyTokenHeader+" "+token(panel.ProxyToken))
		indent(b, n+1, "}")
	} else {
		indent(b, n+1, "reverse_proxy "+panel.Listen)
	}
	indent(b, n, "}")
	indent(b, n, "redir "+panel.BasePath+" "+panel.BasePath+"/ 308")
	b.WriteString("\n")
}
