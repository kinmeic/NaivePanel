package web

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"text/template/parse"
)

func assertNonemptySVG(t *testing.T, markup string) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(markup))
	depth, roots, shapes := 0, 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid SVG: %v: %s", err, markup)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if token.Name.Local != "svg" {
					t.Fatalf("expected SVG root, got %s", token.Name.Local)
				}
			} else {
				switch token.Name.Local {
				case "path", "rect", "circle", "ellipse", "line", "polyline", "polygon":
					shapes++
				}
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if roots != 1 || shapes == 0 {
		t.Fatalf("expected one nonempty SVG, got %d roots and %d shapes: %s", roots, shapes, markup)
	}
}

func TestTemplateIconsRegistered(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var walk func(parse.Node)
	walk = func(node parse.Node) {
		switch node := node.(type) {
		case *parse.ListNode:
			if node != nil {
				for _, child := range node.Nodes {
					walk(child)
				}
			}
		case *parse.ActionNode:
			walk(node.Pipe)
		case *parse.IfNode:
			walk(node.Pipe)
			walk(node.List)
			walk(node.ElseList)
		case *parse.RangeNode:
			walk(node.Pipe)
			walk(node.List)
			walk(node.ElseList)
		case *parse.WithNode:
			walk(node.Pipe)
			walk(node.List)
			walk(node.ElseList)
		case *parse.TemplateNode:
			if node.Pipe != nil {
				walk(node.Pipe)
			}
		case *parse.PipeNode:
			for _, cmd := range node.Cmds {
				walk(cmd)
			}
		case *parse.CommandNode:
			if id, ok := node.Args[0].(*parse.IdentifierNode); ok && id.Ident == "icon" {
				if len(node.Args) != 2 {
					t.Errorf("unexpected icon call: %s", node)
					return
				}
				name, ok := node.Args[1].(*parse.StringNode)
				if !ok {
					t.Errorf("dynamic icon call needs explicit registry coverage: %s", node)
					return
				}
				if !seen[name.Text] {
					seen[name.Text] = true
					t.Run(name.Text, func(t *testing.T) {
						if strings.TrimSpace(iconSVGs[name.Text]) == "" {
							t.Fatalf("template icon %q is not registered or is empty", name.Text)
						}
						assertNonemptySVG(t, string(icon(name.Text)))
					})
				}
			}
			for _, arg := range node.Args {
				walk(arg)
			}
		}
	}
	for _, page := range s.pages {
		for _, tmpl := range page.Templates() {
			if tmpl.Tree != nil {
				walk(tmpl.Tree.Root)
			}
		}
	}
	for _, name := range []string{"copy", "arrow-down", "arrow-left"} {
		if !seen[name] {
			t.Errorf("regression icon %q was not found in templates", name)
		}
	}
}

func TestAgentCoreRenderedIcons(t *testing.T) {
	s, err := New(testConfig(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.render(w, httptest.NewRequest("GET", s.Cfg.BasePath+"/agentcore", nil), "agentcore", "AgentCore", map[string]any{"Installed": true, "Active": true})
	if w.Code != http.StatusOK {
		t.Fatalf("render status %d: %s", w.Code, w.Body)
	}
	for _, item := range []struct{ id, tag, icon string }{
		{"agent-chat-copy-icon", "template", "copy"},
		{"agent-chat-bottom", "button", "arrow-down"},
		{"agent-chat-delete-icon", "template", "trash-2"},
	} {
		t.Run(item.icon, func(t *testing.T) {
			pattern := `<` + item.tag + `\b[^>]*\bid="` + item.id + `"[^>]*>([\s\S]*?)</` + item.tag + `>`
			match := regexp.MustCompile(pattern).FindStringSubmatch(w.Body.String())
			if match == nil {
				t.Fatalf("missing rendered %s", item.id)
			}
			assertNonemptySVG(t, strings.TrimSpace(match[1]))
			if !strings.Contains(match[1], iconSVGs[item.icon]) {
				t.Errorf("%s rendered the wrong icon", item.id)
			}
		})
	}
}
