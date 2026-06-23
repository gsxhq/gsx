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

func TestStyleValue(t *testing.T) {
	// RawCSS passthrough: value is emitted verbatim (no filtering).
	if got := StyleValue(RawCSS("color:blue")); got != "color:blue" {
		t.Errorf("StyleValue(RawCSS) = %q, want %q", got, "color:blue")
	}
	// RawCSS with hostile content still passes through (author vouched).
	const hostile = "color:red; background:url(javascript:alert(1))"
	if got := StyleValue(RawCSS(hostile)); got != hostile {
		t.Errorf("StyleValue(RawCSS hostile) = %q, want %q", got, hostile)
	}
	// Plain string: filtered (hostile input must become cssFailsafe).
	filtered := cssValueFilter("width:1px; x:url(javascript:alert(1))")
	if got := StyleValue("width:1px; x:url(javascript:alert(1))"); got != filtered {
		t.Errorf("StyleValue(string) = %q, want filtered %q", got, filtered)
	}
	if got := StyleValue("width:1px; x:url(javascript:alert(1))"); got == "width:1px; x:url(javascript:alert(1))" {
		t.Errorf("StyleValue(string) must filter hostile CSS, got %q", got)
	}
	// Safe plain string: passes through filter unchanged.
	if got := StyleValue("red"); got != "red" {
		t.Errorf("StyleValue(safe string) = %q, want %q", got, "red")
	}
	// StyleValue with a plain string == FilterCSS (byte-identical).
	for _, s := range []string{"red", "1px solid blue", "url(javascript:alert(1))", ""} {
		want := FilterCSS(s)
		if got := StyleValue(s); got != want {
			t.Errorf("StyleValue(%q) = %q, want FilterCSS result %q", s, got, want)
		}
	}
}

func TestCSSValueFilter(t *testing.T) {
	tests := []struct{ css, want string }{
		{"", ""},
		{"foo", "foo"},
		{"0", "0"},
		{"0px", "0px"},
		{"-5px", "-5px"},
		{"1.25in", "1.25in"},
		{"+.33em", "+.33em"},
		{"100%", "100%"},
		{".foo", ".foo"},
		{"#bar", "#bar"},
		{"-moz-corner-radius", "-moz-corner-radius"},
		{"#123456", "#123456"},
		{"U+00-FF, U+980-9FF", "U+00-FF, U+980-9FF"},
		{"color: red", "color: red"},
		{"<!--", "ZgotmplZ"},
		{"-->", "ZgotmplZ"},
		{"</style", "ZgotmplZ"},
		{`"`, "ZgotmplZ"},
		{`'`, "ZgotmplZ"},
		{"`", "ZgotmplZ"},
		{"\x00", "ZgotmplZ"},
		{"/* foo */", "ZgotmplZ"},
		{"//", "ZgotmplZ"},
		{"rgb(1,2,3)", "ZgotmplZ"},
		{"expression(alert(1337))", "ZgotmplZ"},
		{"EXPRESSION", "ZgotmplZ"},
		{"-moz-binding", "ZgotmplZ"},
		{`-express\69on(alert(1337))`, "ZgotmplZ"},
		{`-expre\0000073sion`, "-expre\x073sion"},
		{"@import url evil.css", "ZgotmplZ"},
		{"<", "ZgotmplZ"},
		{">", "ZgotmplZ"},
		// cold decode branches: hexDecode uppercase A-F
		{"\\3C script\\3E", "ZgotmplZ"},   // uppercase hex \3C \3E -> "<script>" (hexDecode A-F)
		// cold decode branches: skipCSSSpace whitespace variants after hex escape
		{"expr\\65\tssion", "ZgotmplZ"},    // tab after \65 -> "expression" (skipCSSSpace \t)
		{"expr\\65\nssion", "ZgotmplZ"},    // newline (skipCSSSpace \n)
		{"expr\\65\fssion", "ZgotmplZ"},    // form feed (skipCSSSpace \f)
		{"expr\\65\rssion", "ZgotmplZ"},    // CR (skipCSSSpace \r)
		{"expr\\65\r\nssion", "ZgotmplZ"},  // CRLF (skipCSSSpace \r\n two-byte branch)
		// cold decode branches: decodeCSS len<2 and >MaxRune clamp
		{"foo\\", "foo"},       // trailing lone backslash dropped (decodeCSS len<2 break)
		{"\\110000", "𑀀0"},   // hex > utf8.MaxRune: r/16=U+11000 (𑀀) + remaining "0" (decodeCSS clamp)
	}
	for _, tt := range tests {
		if got := cssValueFilter(tt.css); got != tt.want {
			t.Errorf("cssValueFilter(%q) = %q, want %q", tt.css, got, tt.want)
		}
	}
}
