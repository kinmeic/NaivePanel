package sites

import (
	"path/filepath"
	"strings"
)

// MainSite is a site block found inline in the main Caddyfile (as opposed to
// an imported snippet file).
type MainSite struct {
	Domain  string // parsed from the block header
	Content string // full block text, header through closing brace
}

// SplitMain splits a main Caddyfile into its preserved head (global options
// block, comments, snippet definitions, import and other top-level lines,
// kept verbatim) and any inline site blocks.
func SplitMain(content string) (head string, list []MainSite) {
	lines := strings.Split(content, "\n")
	var headLines []string
	for i := 0; i < len(lines); {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			headLines = append(headLines, raw)
			i++
			continue
		}
		if line == "{" || strings.HasSuffix(line, "{") {
			block, n := consumeTopBlock(lines, i)
			header := strings.TrimSpace(strings.TrimSuffix(line, "{"))
			switch {
			case header == "":
				// Global options block.
				headLines = append(headLines, block...)
			case strings.HasPrefix(header, "("):
				// Named snippet definition — keep for the site blocks that
				// may reference it.
				headLines = append(headLines, block...)
			default:
				text := strings.Join(block, "\n")
				list = append(list, MainSite{Domain: DomainFromHeader(text), Content: text})
			}
			i += n
			continue
		}
		// import lines and any other top-level directives.
		headLines = append(headLines, raw)
		i++
	}
	return strings.TrimSpace(strings.Join(headLines, "\n")), list
}

// consumeTopBlock returns the lines of the block starting at index i (which
// opens with a "{") through its matching closing brace, and how many lines
// were consumed.
func consumeTopBlock(lines []string, i int) ([]string, int) {
	depth := 0
	j := i
	for ; j < len(lines); j++ {
		line := lines[j]
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			j++
			break
		}
	}
	return lines[i:j], j - i
}

// RenderMainPreserve renders the main Caddyfile, keeping the operator's
// original head content (global options, comments, named snippets, other
// imports) and making sure exactly one import line for sitesDir exists.
func RenderMainPreserve(head, email, sitesDir string, needsForwardProxyOrder bool) string {
	importLine := "import " + filepath.Join(sitesDir, "*.caddy")
	var kept []string
	for _, l := range strings.Split(head, "\n") {
		if strings.TrimSpace(l) == importLine {
			continue // re-appended below, exactly once
		}
		kept = append(kept, l)
	}
	body := strings.TrimSpace(strings.Join(kept, "\n"))
	if !hasGlobalBlock(body) {
		global := "{\n\temail " + token(email)
		if needsForwardProxyOrder {
			global += "\n\torder forward_proxy before file_server"
		}
		global += "\n}"
		if body == "" {
			body = global
		} else {
			body = global + "\n\n" + body
		}
	} else if needsForwardProxyOrder {
		body = ensureForwardProxyOrder(body)
	}
	return body + "\n\n" + importLine + "\n"
}

// ensureForwardProxyOrder adds the ordering recommended by the NaiveProxy
// project without replacing an operator's existing forward_proxy order.
func ensureForwardProxyOrder(head string) string {
	lines := strings.Split(head, "\n")
	for _, line := range lines {
		fields, err := splitTokens(stripComment(line))
		if err == nil && len(fields) >= 2 && fields[0] == "order" && fields[1] == "forward_proxy" {
			return head
		}
	}

	inGlobal := false
	depth := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inGlobal {
			if trimmed != "{" {
				continue
			}
			inGlobal = true
			depth = 1
			continue
		}
		nextDepth := depth + strings.Count(line, "{") - strings.Count(line, "}")
		if nextDepth == 0 {
			lines = append(lines[:i], append([]string{"\torder forward_proxy before file_server"}, lines[i:]...)...)
			return strings.Join(lines, "\n")
		}
		depth = nextDepth
	}
	return head
}

// hasGlobalBlock reports whether the head's first meaningful line opens a
// global options block (comments and blank lines may precede it).
func hasGlobalBlock(head string) bool {
	for _, l := range strings.Split(head, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return t == "{"
	}
	return false
}
