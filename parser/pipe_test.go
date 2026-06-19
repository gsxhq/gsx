package parser

import (
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/ast"
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
		{`join(a |> b)`, []string{`join(a |> b)`}},       // |> inside parens: depth 1, not split
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

func TestParsePipeStage(t *testing.T) {
	ok := []struct {
		in   string
		want ast.PipeStage
	}{
		{"upper", ast.PipeStage{Name: "upper"}},
		{" trim ", ast.PipeStage{Name: "trim"}},
		{"truncate(20)", ast.PipeStage{Name: "truncate", Args: "20", HasArgs: true}},
		{"f()", ast.PipeStage{Name: "f", Args: "", HasArgs: true}},
		{"strings.ToUpper", ast.PipeStage{Name: "strings.ToUpper"}},
		{"validate()?", ast.PipeStage{Name: "validate", HasArgs: true, Try: true}},
		{"join(\", \")", ast.PipeStage{Name: "join", Args: "\", \"", HasArgs: true}},
	}
	for _, c := range ok {
		got, err := parsePipeStage(c.in)
		if err != nil {
			t.Errorf("parsePipeStage(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePipeStage(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
	bad := []string{"", "  ", "?", "123", "a b", "f(", ".x", "f(x)y"}
	for _, in := range bad {
		if _, err := parsePipeStage(in); err == nil {
			t.Errorf("parsePipeStage(%q): expected error, got nil", in)
		}
	}
}

func TestParsePipe(t *testing.T) {
	seed, try, stages, err := parsePipe("name? |> upper |> truncate(20)")
	if err != nil {
		t.Fatal(err)
	}
	if seed != "name" || !try {
		t.Fatalf("seed=%q try=%v, want \"name\" true", seed, try)
	}
	want := []ast.PipeStage{{Name: "upper"}, {Name: "truncate", Args: "20", HasArgs: true}}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages=%#v, want %#v", stages, want)
	}

	// No pipe → seed only, nil stages (backward-compat shape).
	seed, try, stages, err = parsePipe("greeting(name)?")
	if err != nil || seed != "greeting(name)" || !try || stages != nil {
		t.Fatalf("plain: seed=%q try=%v stages=%#v err=%v", seed, try, stages, err)
	}
}
