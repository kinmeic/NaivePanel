package sites

import (
	"fmt"
	"strings"
)

// Parse parses a Caddyfile site snippet back into a Site model. basePath
// identifies the panel handle block (skipped during import); socksPort
// detects the BypassCore upstream. It returns an error when the snippet
// doesn't match the panel's own rendered shape — callers should then import
// the site in raw mode instead.
func Parse(snippet, basePath string, socksPort int) (*Site, error) {
	p := &snippetParser{lines: strings.Split(snippet, "\n")}

	// Site header: ":443, example.com {"
	header, ok := p.next()
	if !ok || !strings.HasSuffix(header, "{") {
		return nil, fmt.Errorf("无法识别的站点块头: %q", header)
	}
	domain := DomainFromHeader(snippet)
	if domain == "" {
		return nil, fmt.Errorf("无法识别站点地址: %q", header)
	}
	st := &Site{Domain: domain}

	// Panel-rendered sites use route { }, but ordinary hand-written
	// Caddyfiles commonly put handlers directly in the site block. Accept
	// both forms so importing a normal Caddyfile remains structured.
	line, ok := p.next()
	if !ok {
		return nil, fmt.Errorf("站点块为空或未闭合")
	}
	wrappedRoute := line == "route {"
	// Site-level options such as tls/log commonly precede a route block.
	// Consume those first, then parse the later route structurally instead
	// of treating the whole route as one opaque custom directive.
	for !wrappedRoute && line != "}" {
		tokens, err := splitTokens(line)
		if err != nil || len(tokens) == 0 || !isSiteOption(tokens[0]) {
			break
		}
		directive, err := p.collectDirective(line)
		if err != nil {
			return nil, err
		}
		st.SiteOptions = appendDirective(st.SiteOptions, directive)
		line, ok = p.next()
		if !ok {
			return nil, fmt.Errorf("意外的文件结尾（站点块未闭合）")
		}
		wrappedRoute = line == "route {"
	}

	var root, phpSock, proxyTo string
	fileServer := false
	for {
		if wrappedRoute || line == "" {
			line, ok = p.next()
		}
		if !ok {
			return nil, fmt.Errorf("意外的文件结尾（站点块未闭合）")
		}
		if line == "}" {
			break
		}
		tokens, err := splitTokens(line)
		if err != nil || len(tokens) == 0 {
			return nil, fmt.Errorf("无法解析行: %q", line)
		}
		consumed := true
		switch tokens[0] {
		case "handle", "handle_path":
			if len(tokens) < 2 || tokens[len(tokens)-1] != "{" {
				return nil, fmt.Errorf("无法解析块: %q", line)
			}
			matcher := ""
			if len(tokens) >= 3 {
				matcher = tokens[1]
			}
			if tokens[0] == "handle" && matcher == basePath+"/*" {
				p.skipBlock() // the panel block, not part of the site model
				line = ""
				continue
			}
			content, err := p.collectBlock()
			if err != nil {
				return nil, err
			}
			st.ExtraBlocks = append(st.ExtraBlocks, ExtraBlock{
				Type:    tokens[0],
				Matcher: matcher,
				Content: content,
			})
		case "redir":
			// Only discard the panel's own trailing-slash redirect. Other
			// redirects are user configuration and must survive import.
			if len(tokens) < 3 || tokens[1] != basePath || tokens[2] != basePath+"/" {
				consumed = false
			}
		case "forward_proxy":
			if tokens[len(tokens)-1] != "{" {
				return nil, fmt.Errorf("forward_proxy 必须使用块语法")
			}
			if err := p.parseForwardProxy(st, socksPort); err != nil {
				return nil, err
			}
		case "root":
			if tokens[len(tokens)-1] == "{" || len(tokens) < 2 {
				consumed = false
			} else {
				root = tokens[len(tokens)-1]
			}
		case "encode":
			// The panel renders gzip+zstd. Preserve non-standard encoder
			// settings instead of silently replacing them.
			if !sameTokenSet(tokens[1:], []string{"gzip", "zstd"}) {
				consumed = false
			}
		case "php_fastcgi":
			if len(tokens) != 2 {
				return nil, fmt.Errorf("php_fastcgi 缺少参数")
			}
			phpSock = tokens[1]
		case "file_server":
			if len(tokens) == 1 {
				fileServer = true
			} else {
				consumed = false
			}
		case "reverse_proxy":
			if len(tokens) == 2 {
				proxyTo = tokens[1]
			} else {
				// Multiple upstreams, matchers and transport blocks cannot
				// be represented by the simple Web model. Keep them in the
				// structured form's additional-directives section.
				consumed = false
			}
		default:
			consumed = false
		}
		if !consumed {
			directive, err := p.collectDirective(line)
			if err != nil {
				return nil, err
			}
			if !wrappedRoute && isSiteOption(tokens[0]) {
				st.SiteOptions = appendDirective(st.SiteOptions, directive)
			} else {
				st.ExtraDirectives = appendDirective(st.ExtraDirectives, directive)
			}
		}
		line = ""
	}

	if wrappedRoute {
		line, ok = p.next()
		if !ok || line != "}" {
			return nil, fmt.Errorf("站点块未正确闭合")
		}
	}
	if line, ok = p.next(); ok {
		return nil, fmt.Errorf("站点块后有多余内容: %q", line)
	}

	switch {
	case phpSock != "":
		st.Web = Web{Type: WebPHP, Root: root, PHPSocket: phpSock}
	case root != "" || fileServer:
		st.Web = Web{Type: WebStatic, Root: root}
	case proxyTo != "":
		st.Web = Web{Type: WebReverseProxy, ProxyTo: proxyTo}
	default:
		st.Web = Web{Type: WebNone}
	}
	if err := st.Validate(); err != nil {
		return nil, err
	}
	return st, nil
}

func sameTokenSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(got))
	for _, v := range got {
		seen[v] = true
	}
	for _, v := range want {
		if !seen[v] {
			return false
		}
	}
	return true
}

func isSiteOption(name string) bool {
	switch name {
	case "tls", "log", "bind", "handle_errors":
		return true
	default:
		return false
	}
}

func appendDirective(current, directive string) string {
	directive = strings.TrimSpace(directive)
	if current == "" {
		return directive
	}
	return strings.TrimSpace(current) + "\n\n" + directive
}

// DomainFromHeader extracts the site address (domain) from a snippet's first
// meaningful line, e.g. ":443, example.com {" → "example.com". Comment and
// blank lines are skipped.
func DomainFromHeader(snippet string) string {
	for _, line := range strings.Split(snippet, "\n") {
		line = stripComment(line)
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "{") {
			return ""
		}
		addr := strings.TrimSpace(strings.TrimSuffix(line, "{"))
		for _, a := range strings.Split(addr, ",") {
			a = strings.TrimSpace(strings.Trim(a, `"`))
			if a != "" && !strings.HasPrefix(a, ":") {
				return a
			}
		}
		return ""
	}
	return ""
}

// snippetParser walks a snippet line by line.
type snippetParser struct {
	lines []string
	pos   int
}

// next returns the next meaningful line: trimmed, with comments removed
// (both whole-line and trailing "# ..." comments outside quotes).
func (p *snippetParser) next() (string, bool) {
	for p.pos < len(p.lines) {
		line := stripComment(p.lines[p.pos])
		p.pos++
		if line != "" {
			return line, true
		}
	}
	return "", false
}

// stripComment trims whitespace and drops a trailing comment: everything
// after a '#' that starts a new token (at line start or preceded by
// whitespace) outside double quotes.
func stripComment(raw string) string {
	inQuote := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch == '\\' && inQuote:
			i++
		case ch == '"':
			inQuote = !inQuote
		case ch == '#' && !inQuote && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t'):
			return strings.TrimSpace(raw[:i])
		}
	}
	return strings.TrimSpace(raw)
}

// skipBlock consumes lines until the block opened by the previous line
// closes (balanced braces).
func (p *snippetParser) skipBlock() {
	depth := 1
	for p.pos < len(p.lines) && depth > 0 {
		line := p.lines[p.pos]
		p.pos++
		depth += strings.Count(line, "{") - strings.Count(line, "}")
	}
}

// collectBlock consumes a block like skipBlock but returns its content with
// the common tab indentation stripped.
func (p *snippetParser) collectBlock() (string, error) {
	depth := 1
	var content []string
	for p.pos < len(p.lines) {
		line := p.lines[p.pos]
		p.pos++
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth == 0 {
			return dedentLines(content), nil // closing brace of the block itself
		}
		content = append(content, line)
	}
	return "", fmt.Errorf("块未闭合")
}

// collectDirective returns line plus its block body when line opens a block.
// Simple directives are returned unchanged.
func (p *snippetParser) collectDirective(line string) (string, error) {
	tokens, err := splitTokens(line)
	if err != nil || len(tokens) == 0 || tokens[len(tokens)-1] != "{" {
		return line, err
	}
	content, err := p.collectBlock()
	if err != nil {
		return "", err
	}
	if content == "" {
		return line + "\n}", nil
	}
	// collectBlock removes the block body's common outer indentation. Add
	// one logical level back so nested site options/directives keep their
	// relative shape when the complete directive is rendered elsewhere.
	var nested strings.Builder
	for _, child := range strings.Split(content, "\n") {
		if strings.TrimSpace(child) != "" {
			nested.WriteString("\t")
		}
		nested.WriteString(child)
		nested.WriteString("\n")
	}
	return line + "\n" + strings.TrimSuffix(nested.String(), "\n") + "\n}", nil
}

// dedentLines strips the exact common whitespace prefix. Caddyfiles often
// mix tabs and spaces, and removing "all leading whitespace" would flatten
// nested blocks such as tls/log/handle_errors.
func dedentLines(content []string) string {
	common := ""
	for _, l := range content {
		if strings.TrimSpace(l) == "" {
			continue
		}
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if common == "" {
			common = indent
			continue
		}
		n := 0
		for n < len(common) && n < len(indent) && common[n] == indent[n] {
			n++
		}
		common = common[:n]
	}
	if common != "" {
		for i, l := range content {
			if strings.HasPrefix(l, common) {
				content[i] = l[len(common):]
			}
		}
	}
	return strings.TrimSpace(strings.Join(content, "\n"))
}

// parseForwardProxy consumes a forward_proxy block into the site model.
func (p *snippetParser) parseForwardProxy(st *Site, socksPort int) error {
	st.ForwardProxy.Enabled = true
	for {
		line, ok := p.next()
		if !ok {
			return fmt.Errorf("forward_proxy 块未闭合")
		}
		if line == "}" {
			return nil
		}
		tokens, err := splitTokens(line)
		if err != nil || len(tokens) == 0 {
			return fmt.Errorf("无法解析行: %q", line)
		}
		switch tokens[0] {
		case "basic_auth":
			if len(tokens) != 3 {
				return fmt.Errorf("basic_auth 需要两个参数: %q", line)
			}
			st.ForwardProxy.Accounts = append(st.ForwardProxy.Accounts,
				Account{User: tokens[1], Pass: tokens[2]})
		case "upstream":
			if len(tokens) != 2 {
				return fmt.Errorf("upstream 需要一个参数: %q", line)
			}
			if tokens[1] == fmt.Sprintf("socks5://127.0.0.1:%d", socksPort) {
				st.ForwardProxy.UseBypassCore = true
			} else {
				st.ForwardProxy.Upstream = tokens[1]
			}
		case "hide_ip", "hide_via", "probe_resistance":
			// fixed hardening directives, not part of the model
		default:
			return fmt.Errorf("forward_proxy 中无法识别的指令 %q", line)
		}
	}
}

// splitTokens splits a Caddyfile line into tokens, honoring double quotes
// (with \" escapes) and unquoting quoted tokens.
func splitTokens(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote, has := false, false
	flush := func() {
		if has || cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
			has = false
		}
	}
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case inQuote:
			if ch == '\\' && i+1 < len(line) && line[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else if ch == '"' {
				inQuote = false
			} else {
				cur.WriteByte(ch)
			}
		case ch == '"':
			inQuote = true
			has = true
		case ch == ' ' || ch == '\t':
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("未闭合的引号: %q", line)
	}
	flush()
	return out, nil
}
