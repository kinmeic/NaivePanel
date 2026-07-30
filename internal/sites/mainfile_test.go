package sites

import (
	"strings"
	"testing"
)

const sampleMain = `# 主配置
{
	email admin@example.com
}

:443, a.example.com {
	route {
		forward_proxy {
			basic_auth u1 p1
			hide_ip
			hide_via
			probe_resistance
		}
		root * /var/www/a
		encode zstd gzip
		file_server
	}
}

(common) {
	header X-Frame-Options DENY
}

:443, b.example.com {
	route {
		reverse_proxy 127.0.0.1:8080
	}
}
`

func TestSplitMain(t *testing.T) {
	head, list := SplitMain(sampleMain)
	if len(list) != 2 {
		t.Fatalf("expected 2 inline sites, got %d", len(list))
	}
	if list[0].Domain != "a.example.com" || list[1].Domain != "b.example.com" {
		t.Fatalf("unexpected domains: %q, %q", list[0].Domain, list[1].Domain)
	}
	if !strings.Contains(list[0].Content, "forward_proxy") {
		t.Fatalf("inline content lost body: %q", list[0].Content)
	}
	// Head keeps global block, comments and named snippet definitions.
	for _, want := range []string{"# 主配置", "email admin@example.com", "(common)"} {
		if !strings.Contains(head, want) {
			t.Fatalf("head missing %q:\n%s", want, head)
		}
	}
	if strings.Contains(head, "a.example.com") || strings.Contains(head, "b.example.com") {
		t.Fatalf("head contains site blocks:\n%s", head)
	}
}

func TestSplitMainEmpty(t *testing.T) {
	head, list := SplitMain("")
	if head != "" || len(list) != 0 {
		t.Fatalf("expected empty split, got head=%q sites=%d", head, len(list))
	}
}

func TestRenderMainPreserveKeepsHead(t *testing.T) {
	head, _ := SplitMain(sampleMain)
	out := RenderMainPreserve(head, "admin@panel.example", "/etc/caddy/sites")
	for _, want := range []string{
		"email admin@example.com", // original global block wins
		"(common)",
		"import /etc/caddy/sites/*.caddy",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "admin@panel.example") {
		t.Fatalf("panel email must not override existing global block:\n%s", out)
	}
	if strings.Count(out, "import /etc/caddy/sites/*.caddy") != 1 {
		t.Fatalf("import line must appear exactly once:\n%s", out)
	}
}

func TestRenderMainPreserveEmptyHead(t *testing.T) {
	out := RenderMainPreserve("", "admin@panel.example", "/etc/caddy/sites")
	if !strings.HasPrefix(out, "{\n\temail admin@panel.example\n}") {
		t.Fatalf("expected synthesized global block:\n%s", out)
	}
	if !strings.Contains(out, "import /etc/caddy/sites/*.caddy") {
		t.Fatalf("missing import line:\n%s", out)
	}
}

func TestRenderMainPreserveDedupesImport(t *testing.T) {
	head := "{\n\temail a@b.c\n}\n\nimport /etc/caddy/sites/*.caddy"
	out := RenderMainPreserve(head, "x@y.z", "/etc/caddy/sites")
	if strings.Count(out, "import /etc/caddy/sites/*.caddy") != 1 {
		t.Fatalf("duplicate import lines:\n%s", out)
	}
}

// Round-trip: a split + preserve render must keep every inline block body
// available for migration and produce a main file that parses again.
func TestSplitMainRoundTrip(t *testing.T) {
	head, list := SplitMain(sampleMain)
	out := RenderMainPreserve(head, "admin@panel.example", "/etc/caddy/sites")
	head2, list2 := SplitMain(out)
	if len(list2) != 0 {
		t.Fatalf("rendered main file should have no inline sites, got %d", len(list2))
	}
	_ = head2
	// Migrated snippets must themselves parse as site blocks.
	for _, ms := range list {
		if got := DomainFromHeader(ms.Content); got != ms.Domain {
			t.Fatalf("domain mismatch: %q vs %q", got, ms.Domain)
		}
	}
}
