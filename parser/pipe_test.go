package parser

import (
	"reflect"
	"testing"
)

func TestSplitPipe(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"name", []string{"name"}},
		{"name |> upper", []string{"name ", " upper"}},
		{"a |> b |> c", []string{"a ", " b ", " c"}},
		{"x |> truncate(20)", []string{"x ", " truncate(20)"}},
		{`join(a |> b)`, []string{`join(a |> b)`}},      // |> inside parens: depth 1, not split
		{`"a |> b"`, []string{`"a |> b"`}},               // |> inside string literal
		{"flagsA | flagsB", []string{"flagsA | flagsB"}}, // bitwise OR (no `>`): not a pipe
		{"a || b", []string{"a || b"}},                   // logical OR: not a pipe
		{"a | > b", []string{"a | > b"}},                 // `| >` with gap: not a pipe
	}
	for _, c := range cases {
		got := splitPipe(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitPipe(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
