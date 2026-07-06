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

func TestRefreshContentSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"reload integer", "0", "0"},
		{"reload decimal", "5.25", "5.25"},
		{"safe relative", "0;url=/next", "0;url=/next"},
		{"safe absolute", "0; URL=https://example.com/a?b=c", "0; URL=https://example.com/a?b=c"},
		{"safe quoted query", "0, url='?q=a:b'", "0, url='?q=a:b'"},
		{"unsafe unquoted", "0;url=javascript:alert(1)", "0;url=" + blockedURL},
		{"unsafe mixed case", "0;URL=JavaScript:alert(1)", "0;URL=" + blockedURL},
		{"unsafe after url whitespace", "0; url= \tjavascript:alert(1)", "0; url= \t" + blockedURL},
		{"unsafe embedded tab", "0;url=java\tscript:alert(1)", "0;url=" + blockedURL},
		{"unsafe double quoted", `0;url="javascript:alert(1)"`, `0;url="` + blockedURL + `"`},
		{"unsafe single quoted trailing", "0; url='javascript:alert(1)'; ignored", "0; url='" + blockedURL + "'; ignored"},
		{"no url assignment", "0; refresh", "0; refresh"},
		{"non refresh grammar", "url=javascript:alert(1)", "url=javascript:alert(1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := refreshContentSanitize(tt.in); got != tt.want {
				t.Fatalf("refreshContentSanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriterRefreshContentEscapesAfterSanitize(t *testing.T) {
	var b strings.Builder
	gw := W(&b)
	gw.RefreshContent(`0;url=/x?a="b"&c`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if b.String() != `0;url=/x?a=&#34;b&#34;&amp;c` {
		t.Fatalf("safe refresh content = %q", b.String())
	}

	b.Reset()
	gw = W(&b)
	gw.RefreshContent(`0;url="javascript:alert(1)"`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if b.String() != `0;url=&#34;`+blockedURL+`&#34;` {
		t.Fatalf("blocked refresh content = %q, want quoted blocked URL", b.String())
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

func TestURLSanitizeImage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// allowed: standard schemes and relative still pass through
		{"http", "http://example.com/a.png", "http://example.com/a.png"},
		{"relative", "/img/a.png", "/img/a.png"},
		// allowed: image data URLs (raster + svg)
		{"png", "data:image/png;base64,iVBORw0KGgo=", "data:image/png;base64,iVBORw0KGgo="},
		{"jpeg", "data:image/jpeg;base64,/9j/4AAQ==", "data:image/jpeg;base64,/9j/4AAQ=="},
		{"webp upper mime", "data:IMAGE/WEBP;base64,UklGRg==", "data:IMAGE/WEBP;base64,UklGRg=="},
		{"svg", "data:image/svg+xml;base64,PHN2Zz4=", "data:image/svg+xml;base64,PHN2Zz4="},
		// allowed: an extra parameter before the final ;base64 marker doesn't
		// change the MIME or the marker, so it's still accepted unchanged.
		{"png with charset param", "data:image/png;charset=utf-8;base64,iVBORw0KGgo=", "data:image/png;charset=utf-8;base64,iVBORw0KGgo="},
		// blocked: non-image data URLs
		{"html", "data:text/html;base64,PHNjcmlwdD4=", blockedURL},
		{"js", "data:application/javascript;base64,YWxlcnQ=", blockedURL},
		{"no mime", "data:;base64,AAAA", blockedURL},
		{"image no base64 marker", "data:image/png,rawbytes", blockedURL},
		// blocked: marker must be exactly "base64", not a lookalike suffix
		{"almost base64 marker", "data:image/png;base64x,AAAA", blockedURL},
		// blocked: other dangerous schemes
		{"javascript", "javascript:alert(1)", blockedURL},
		{"vbscript", "vbscript:msgbox(1)", blockedURL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := urlSanitizeImage(c.in); got != c.want {
				t.Fatalf("urlSanitizeImage(%q) = %q, want %q", c.in, got, c.want)
			}
		})
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
		{"\\3C script\\3E", "ZgotmplZ"}, // uppercase hex \3C \3E -> "<script>" (hexDecode A-F)
		// cold decode branches: skipCSSSpace whitespace variants after hex escape
		{"expr\\65\tssion", "ZgotmplZ"},   // tab after \65 -> "expression" (skipCSSSpace \t)
		{"expr\\65\nssion", "ZgotmplZ"},   // newline (skipCSSSpace \n)
		{"expr\\65\fssion", "ZgotmplZ"},   // form feed (skipCSSSpace \f)
		{"expr\\65\rssion", "ZgotmplZ"},   // CR (skipCSSSpace \r)
		{"expr\\65\r\nssion", "ZgotmplZ"}, // CRLF (skipCSSSpace \r\n two-byte branch)
		// cold decode branches: decodeCSS len<2 and >MaxRune clamp
		{"foo\\", "foo"},   // trailing lone backslash dropped (decodeCSS len<2 break)
		{"\\110000", "𑀀0"}, // hex > utf8.MaxRune: r/16=U+11000 (𑀀) + remaining "0" (decodeCSS clamp)
	}
	for _, tt := range tests {
		if got := cssValueFilter(tt.css); got != tt.want {
			t.Errorf("cssValueFilter(%q) = %q, want %q", tt.css, got, tt.want)
		}
	}
}
