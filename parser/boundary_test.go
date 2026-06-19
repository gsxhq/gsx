// parser/boundary_test.go
package parser

import (
	"strings"
	"testing"
)

func TestGoExprEnd(t *testing.T) {
	cases := []struct {
		src   string
		open  int
		close int
		ok    bool
	}{
		{`{x}`, 0, 2, true},
		{`{ a < b && c > d }`, 0, 17, true},        // comparison ops, not markup
		{`{ m[string]int{"a": 1} }`, 0, 23, true},  // nested braces
		{`{ "string with } brace" }`, 0, 24, true}, // brace in string
		{"{ `raw } string` }", 0, 17, true},        // brace in raw string
		{`{ '}' }`, 0, 6, true},                    // brace in rune literal
		{`{ a /* } */ b }`, 0, 14, true},           // brace in comment
		{`{ unbalanced`, 0, 0, false},
	}
	for _, c := range cases {
		got, ok := goExprEnd(c.src, c.open)
		if ok != c.ok || (ok && got != c.close) {
			t.Errorf("goExprEnd(%q) = (%d,%v), want (%d,%v)", c.src, got, ok, c.close, c.ok)
		}
	}
}

func TestParenEnd(t *testing.T) {
	cases := []struct {
		src   string
		open  int
		close int
		ok    bool
	}{
		{`(p UsersPage)`, 0, 12, true},
		{`(a string, b func(int) int)`, 0, 26, true}, // nested parens
		{`( ")" )`, 0, 6, true},                      // paren in string
		{`(unbalanced`, 0, 0, false},
	}
	for _, c := range cases {
		got, ok := parenEnd(c.src, c.open)
		if ok != c.ok || (ok && got != c.close) {
			t.Errorf("parenEnd(%q) = (%d,%v), want (%d,%v)", c.src, got, ok, c.close, c.ok)
		}
	}
}

func TestGoExprEndIgnoresPrecedingProse(t *testing.T) {
	// An apostrophe BEFORE the brace must not desync the scanner: goExprEnd is
	// asked to match the brace at `open`, and only the region from `open` on
	// (pure Go) should be tokenized.
	src := `Today's items: {n}`
	open := strings.IndexByte(src, '{')
	end, ok := goExprEnd(src, open)
	if !ok {
		t.Fatalf("goExprEnd returned ok=false; want match at the closing brace")
	}
	if src[end] != '}' || end != len(src)-1 {
		t.Fatalf("end=%d (src[end]=%q), want %d (the final '}')", end, string(src[end]), len(src)-1)
	}
}

func TestParenEndIgnoresPrecedingProse(t *testing.T) {
	src := `it's (a, b)`
	open := strings.IndexByte(src, '(')
	end, ok := parenEnd(src, open)
	if !ok || src[end] != ')' || end != len(src)-1 {
		t.Fatalf("parenEnd end=%d ok=%v, want final ')'", end, ok)
	}
}
