// parser/boundary_test.go
package parser

import "testing"

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
