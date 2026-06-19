package gsx

import (
	"strings"
	"testing"
)

func TestWriteHTML(t *testing.T) {
	cases := map[string]string{
		`a & b`:          `a &amp; b`,
		`<script>`:       `&lt;script&gt;`,
		`" onmouseover=`: `&#34; onmouseover=`,
		`it's`:           `it&#39;s`,
		`plain`:          `plain`,
	}
	for in, want := range cases {
		var b strings.Builder
		if err := writeHTML(&b, in); err != nil {
			t.Fatal(err)
		}
		if b.String() != want {
			t.Fatalf("writeHTML(%q) = %q, want %q", in, b.String(), want)
		}
	}
}

func TestURLSanitize(t *testing.T) {
	safe := []string{
		"http://example.com/x",
		"https://example.com",
		"HTTPS://EXAMPLE.com", // scheme case-insensitive
		"mailto:a@b.com",
		"tel:+1234",
		"/relative/path",
		"../up",
		"#fragment",
		"?q=:colon",           // ':' after '?' is not a scheme
		"//cdn.example.com/x", // protocol-relative
	}
	for _, s := range safe {
		if got := urlSanitize(s); got != s {
			t.Fatalf("urlSanitize(%q) = %q, want unchanged", s, got)
		}
	}
	blocked := []string{
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"vbscript:msgbox",
		"data:text/html,<script>",
		"\tjavascript:alert(1)", // leading control char must not bypass
		" javascript:alert(1)",  // leading space must not bypass
		"java\tscript:alert(1)", // embedded tab in scheme must not bypass
	}
	for _, s := range blocked {
		if got := urlSanitize(s); got != blockedURL {
			t.Fatalf("urlSanitize(%q) = %q, want %q", s, got, blockedURL)
		}
	}
}

func TestWriteURLEscapesAfterSanitize(t *testing.T) {
	var b strings.Builder
	if err := writeURL(&b, `/x?a="b"&c`); err != nil {
		t.Fatal(err)
	}
	if b.String() != `/x?a=&#34;b&#34;&amp;c` {
		t.Fatalf("got %q", b.String())
	}
	b.Reset()
	if err := writeURL(&b, "javascript:alert(1)"); err != nil {
		t.Fatal(err)
	}
	if b.String() != blockedURL {
		t.Fatalf("blocked URL = %q, want %q", b.String(), blockedURL)
	}
}
