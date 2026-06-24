package parser

import (
	"reflect"
	"strings"
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
		{`join(a |> b)`, []string{`join(a |> b)`}},                                 // |> inside parens: depth 1, not split
		{`"a |> b"`, []string{`"a |> b"`}},                                         // |> inside string literal
		{"flagsA | flagsB", []string{"flagsA | flagsB"}},                           // bitwise OR (no `>`): not a pipe
		{"a || b", []string{"a || b"}},                                             // logical OR: not a pipe
		{"a | > b", []string{"a | > b"}},                                           // `| >` with gap: not a pipe
		{"a |>= b", []string{"a |>= b"}},                                           // OR + GEQ: not a pipe
		{"a |>> b", []string{"a |>> b"}},                                           // OR + SHR: not a pipe
		{"`raw |> x`", []string{"`raw |> x`"}},                                     // |> inside a raw string literal
		{"items |> join(sep |> upper)", []string{"items ", " join(sep |> upper)"}}, // inner |> stays in the arg (depth 1)
		{"a |>|> b", []string{"a ", "", " b"}},                                     // empty middle segment (errors later)
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
	seed, stages, err := parsePipe("name |> upper |> truncate(20)")
	if err != nil {
		t.Fatal(err)
	}
	if seed != "name" {
		t.Fatalf("seed=%q, want \"name\"", seed)
	}
	want := []ast.PipeStage{{Name: "upper"}, {Name: "truncate", Args: "20", HasArgs: true}}
	if !reflect.DeepEqual(stages, want) {
		t.Fatalf("stages=%#v, want %#v", stages, want)
	}

	// No pipe → seed only, nil stages.
	seed, stages, err = parsePipe("greeting(name)")
	if err != nil || seed != "greeting(name)" || stages != nil {
		t.Fatalf("plain: seed=%q stages=%#v err=%v", seed, stages, err)
	}

	// The removed `?` try-marker is rejected — on the seed and on a stage.
	if _, _, err := parsePipe("name? |> upper"); err == nil {
		t.Fatal("expected error for `?` on the seed")
	}
	if _, _, err := parsePipe("name |> validate()? |> upper"); err == nil {
		t.Fatal("expected error for `?` on a stage")
	}
}

func TestParsePipeEdges(t *testing.T) {
	// Inner |> inside a filter argument stays opaque (spec A.4: nested pipelines
	// are NOT split; the arg is one Go string).
	seed, stages, err := parsePipe("items |> join(sep |> upper)")
	if err != nil {
		t.Fatal(err)
	}
	wantJoin := []ast.PipeStage{{Name: "join", Args: "sep |> upper", HasArgs: true}}
	if seed != "items" || !reflect.DeepEqual(stages, wantJoin) {
		t.Fatalf("nested arg: seed=%q stages=%#v", seed, stages)
	}

	// A `?` inside a seed string literal is not the try-marker (the literal ends
	// with `"`, not `?`), so it parses cleanly.
	seed, _, err = parsePipe(`"huh?" |> upper`)
	if err != nil || seed != `"huh?"` {
		t.Fatalf("string-? seed: seed=%q err=%v", seed, err)
	}

	// Empty middle stage is an error.
	if _, _, err := parsePipe("a |>|> b"); err == nil {
		t.Fatal("expected error for empty middle stage")
	}

	// Empty interpolation → seed "", nil stages.
	seed, stages, err = parsePipe("")
	if err != nil || seed != "" || stages != nil {
		t.Fatalf("empty: seed=%q stages=%#v err=%v", seed, stages, err)
	}
}

// FuzzSplitPipe asserts splitPipe never panics and is lossless: re-joining the
// segments with the "|>" delimiter reconstructs the input exactly (each split
// removes exactly the 2-byte operator). This catches any byte-offset bug.
func FuzzSplitPipe(f *testing.F) {
	for _, s := range []string{
		"", "name", "a |> b", "a |> b |> c(1)", "x |> truncate(20)",
		"join(a |> b)", `"a |> b"`, "a |>|> b", "a |>= b", "|>", "a|>b",
		"ünïcödé |> upper", "`raw |> x`", "a |>",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		segs := splitPipe(s) // MUST NOT PANIC
		if got := strings.Join(segs, "|>"); got != s {
			t.Fatalf("roundtrip broken: splitPipe(%q) = %#v, Join = %q", s, segs, got)
		}
	})
}

// FuzzParsePipe asserts parsePipe never panics for any input; malformed
// pipelines must return an error rather than crash.
func FuzzParsePipe(f *testing.F) {
	for _, s := range []string{
		"", "name", "name? |> upper", "a |> b |> c(1)",
		"validate()? |> x", "a |>|> b", "f( |> g", "items |> join(a |> b)",
		"|> upper", "x |> .y", "x |> 1",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _, _ = parsePipe(s) // MUST NOT PANIC; malformed → error
	})
}
