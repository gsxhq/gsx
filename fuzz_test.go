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
