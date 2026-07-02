package gsx

import (
	"strings"
	"testing"
)

// cssBreakoutBytes are the bytes that could let a value escape a CSS value
// context (end the declaration/rule, open a string/comment/url, or close the
// <style> element). cssValueFilter must guarantee its output contains NONE of
// them — it returns either the inert placeholder or a value that passed the
// reject scan, so a forbidden byte in the output is a security defect.
const cssBreakoutBytes = "\x00\"'()/;@[\\]`{}<>"

// refreshURLRef is an independent port of the WHATWG "shared declarative
// refresh steps" (https://html.spec.whatwg.org/multipage/document-lifecycle.html#shared-declarative-refresh-steps),
// reduced to URL extraction: it reports the URL a conforming browser would
// navigate to for a meta refresh content value, and false when the value names
// no URL. It is the differential oracle for refreshContentSanitize and must be
// ported from the spec, never from the implementation under test.
func refreshURLRef(s string) (string, bool) {
	p := 0
	skip := func() {
		for p < len(s) && isASCIIWhitespaceByte(s[p]) {
			p++
		}
	}
	skip()
	start := p
	for p < len(s) && '0' <= s[p] && s[p] <= '9' {
		p++
	}
	if p == start && (p >= len(s) || s[p] != '.') {
		return "", false
	}
	for p < len(s) && (('0' <= s[p] && s[p] <= '9') || s[p] == '.') {
		p++
	}
	if p >= len(s) {
		return "", false
	}
	if c := s[p]; c != ';' && c != ',' && !isASCIIWhitespaceByte(c) {
		return "", false
	}
	skip()
	if p < len(s) && (s[p] == ';' || s[p] == ',') {
		p++
	}
	skip()
	if p >= len(s) {
		return "", false
	}
	// Optional `url` [ws] `=` [ws] prefix; each failed check jumps to the
	// quote-skipping step with position left where the check failed.
	if s[p] == 'u' || s[p] == 'U' {
		p++
		if p < len(s) && (s[p] == 'r' || s[p] == 'R') {
			p++
			if p < len(s) && (s[p] == 'l' || s[p] == 'L') {
				p++
				skip()
				if p < len(s) && s[p] == '=' {
					p++
					skip()
				}
			}
		}
	}
	if p < len(s) && (s[p] == '\'' || s[p] == '"') {
		quote := s[p]
		rest := s[p+1:]
		if before, _, ok := strings.Cut(rest, string(quote)); ok {
			return before, true
		}
		return rest, true
	}
	return s[p:], true
}

// FuzzRefreshContentSanitize hardens the meta refresh content parser. Seeds
// come from the OWASP XSS filter-evasion sheet (META REFRESH vectors) plus the
// unit-test grammar corpus. Invariants: (1) the URL a WHATWG-conforming
// browser extracts from the SANITIZED value always passes urlSanitize's scheme
// policy unchanged — no evasion of the quote/whitespace/url= grammar can smuggle
// a blocked scheme past the sanitizer; (2) sanitizing is idempotent.
func FuzzRefreshContentSanitize(f *testing.F) {
	for _, s := range []string{
		"", "0", "5.25", "0; refresh", "0;url", "url=javascript:alert(1)",
		"0;url=/next", "0; URL=https://example.com/a?b=c", "0, url='?q=a:b'",
		"0;url=javascript:alert(1)", "0;URL=JavaScript:alert(1)",
		"0; URL='javascript:alert(1)'", `0;url="javascript:alert(1)"`,
		"0;url='javascript:alert(1)", "0; url= \tjavascript:alert(1)",
		"0;url=java\tscript:alert(1)", "0;url=java\nscript:alert(1)",
		"0;url=java\rscript:alert(1)", " 0 ; url = jAvAsCrIpT:alert(1)",
		".5;url=javascript:alert(1)", "0.5.5;url=javascript:alert(1)",
		"0,url=vbscript:msgbox(1)", "0 url=javascript:alert(1)",
		"0;;url=javascript:alert(1)", "0;'javascript:alert(1)'",
		"0;urljavascript:alert(1)", "0;url'javascript:alert(1)'",
		"0;u'javascript:alert(1)'", "0;url =javascript:alert(1)",
		"0;url=\x00javascript:alert(1)", "0;url=\x01javascript:alert(1)",
		"0;url=%6A%61%76%61script:alert(1)", "0;url=jav&#x0A;ascript:alert(1)",
		"0;url=data:text/html,<script>alert(1)</script>",
		"0;url=DaTa:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"9999999999999999999;url=javascript:alert(1)",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := refreshContentSanitize(s)
		if u, ok := refreshURLRef(out); ok {
			if urlSanitize(u) != u {
				t.Fatalf("refreshContentSanitize(%q) = %q still navigates to policy-blocked URL %q", s, out, u)
			}
		}
		if again := refreshContentSanitize(out); again != out {
			t.Fatalf("refreshContentSanitize not idempotent: %q -> %q -> %q", s, out, again)
		}
	})
}

func FuzzCSSValueFilter(f *testing.F) {
	for _, s := range []string{
		"", "foo", "color: red", "10px", "#fff", "rgb(1,2,3)", "<!--", "-->",
		"</style", "expression(alert(1))", "EXPRESSION", "-moz-binding",
		`\3c script`, `-expre\69on`, "url(javascript:alert(1))", "a;b}c{d",
		"--x: ;", "1.25in", "U+00-FF", "\x00", "`backtick`",
		"\\3C script\\3E", "expr\\65\tssion", "expr\\65\nssion",
		"expr\\65\fssion", "expr\\65\rssion", "expr\\65\r\nssion",
		"foo\\", "\\110000",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := cssValueFilter(s)
		// Security invariant: output never carries a CSS breakout byte.
		if i := strings.IndexAny(out, cssBreakoutBytes); i >= 0 {
			t.Fatalf("cssValueFilter(%q) = %q leaks breakout byte %q at %d", s, out, out[i], i)
		}
		// Idempotence: filtering an already-filtered value is stable.
		if again := cssValueFilter(out); again != out {
			t.Fatalf("cssValueFilter not idempotent: %q -> %q -> %q", s, out, again)
		}
		// FilterCSS is the exported alias and must agree exactly.
		if fc := FilterCSS(s); fc != out {
			t.Fatalf("FilterCSS(%q)=%q != cssValueFilter=%q", s, fc, out)
		}
	})
}
