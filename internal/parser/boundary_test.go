// internal/parser/boundary_test.go
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
		{`{ a < b && c > d }`, 0, 17, true},
		{`{ m[string]int{"a": 1} }`, 0, 23, true},     // nested braces
		{`{ "string with } brace" }`, 0, 24, true},     // brace in string
		{"{ `raw } string` }", 0, 17, true},            // brace in raw string
		{`{ '}' }`, 0, 6, true},                         // brace in rune literal
		{`{ a /* } */ b }`, 0, 14, true},               // brace in comment
		{`{ unbalanced`, 0, 0, false},
	}
	for _, c := range cases {
		got, ok := goExprEnd(c.src, c.open)
		if ok != c.ok || (ok && got != c.close) {
			t.Errorf("goExprEnd(%q) = (%d,%v), want (%d,%v)", c.src, got, ok, c.close, c.ok)
		}
	}
}
