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

	// Everything the panel renders lives inside route { }.
	line, ok := p.next()
	if !ok || line != "route {" {
		return nil, fmt.Errorf("缺少 route 块，不是面板渲染格式")
	}

	var root, phpSock, proxyTo string
	for {
		line, ok = p.next()
		if !ok {
			return nil, fmt.Errorf("意外的文件结尾（route 块未闭合）")
		}
		if line == "}" {
			break // end of route
		}
		tokens, err := splitTokens(line)
		if err != nil || len(tokens) == 0 {
			return nil, fmt.Errorf("无法解析行: %q", line)
		}
		switch tokens[0] {
		case "handle", "handle_path":
			if len(tokens) < 3 || tokens[len(tokens)-1] != "{" {
				return nil, fmt.Errorf("无法解析块: %q", line)
			}
			matcher := tokens[1]
			if tokens[0] == "handle" && matcher == basePath+"/*" {
				p.skipBlock() // the panel block, not part of the site model
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
			// panel trailing-slash redirect, skip
		case "forward_proxy":
			if err := p.parseForwardProxy(st, socksPort); err != nil {
				return nil, err
			}
		case "root":
			root = tokens[len(tokens)-1]
		case "encode":
			// fixed directive, skip
		case "php_fastcgi":
			if len(tokens) < 2 {
				return nil, fmt.Errorf("php_fastcgi 缺少参数")
			}
			phpSock = tokens[1]
		case "file_server":
			// static site marker, nothing to record
		case "reverse_proxy":
			if len(tokens) < 2 {
				return nil, fmt.Errorf("reverse_proxy 缺少参数")
			}
			proxyTo = tokens[1]
		default:
			return nil, fmt.Errorf("无法识别的指令 %q", line)
		}
	}

	line, ok = p.next()
	if !ok || line != "}" {
		return nil, fmt.Errorf("站点块未正确闭合")
	}
	if line, ok = p.next(); ok {
		return nil, fmt.Errorf("站点块后有多余内容: %q", line)
	}

	switch {
	case phpSock != "":
		st.Web = Web{Type: WebPHP, Root: root, PHPSocket: phpSock}
	case root != "":
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

// DomainFromHeader extracts the site address (domain) from a snippet's first
// non-empty line, e.g. ":443, example.com {" → "example.com".
func DomainFromHeader(snippet string) string {
	for _, line := range strings.Split(snippet, "\n") {
		line = strings.TrimSpace(line)
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

// next returns the next non-empty trimmed line.
func (p *snippetParser) next() (string, bool) {
	for p.pos < len(p.lines) {
		line := strings.TrimSpace(p.lines[p.pos])
		p.pos++
		if line != "" {
			return line, true
		}
	}
	return "", false
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

// dedentLines strips the common leading-tab indentation.
func dedentLines(content []string) string {
	minTabs := -1
	for _, l := range content {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := 0
		for n < len(l) && l[n] == '\t' {
			n++
		}
		if minTabs < 0 || n < minTabs {
			minTabs = n
		}
	}
	if minTabs > 0 {
		for i, l := range content {
			if len(l) >= minTabs {
				content[i] = l[minTabs:]
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
