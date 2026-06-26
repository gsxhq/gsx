package interp

import (
	"strings"
	"testing"
)

// TestPlaygroundTransformRendersHTML is the combined transform+render proof:
// generate Go from gsx and interpret+render it to HTML, all in-process (no Go
// toolchain). `{name |> upper}` with name "World" must yield "<p>Hello WORLD!</p>".
func TestPlaygroundTransformRendersHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping transform+render test in -short mode")
	}
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const src = `package main

component Greeting(name string) {
	<p>Hello { name |> upper }!</p>
}
`
	out := p.Transform(src, `Greeting(GreetingProps{Name: "World"})`)
	if len(out.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", out.Diagnostics)
	}
	if !strings.Contains(out.Code, "Upper(") {
		t.Fatalf("generated code missing std.Upper call:\n%s", out.Code)
	}
	if out.HTML != "<p>Hello WORLD!</p>" {
		t.Fatalf("rendered HTML = %q, want \"<p>Hello WORLD!</p>\"", out.HTML)
	}
}

// TestPlaygroundTransformReportsTypeError proves a generation-time type error is
// returned as a diagnostic with no HTML (the interpreter is never run on code
// that did not type-check).
func TestPlaygroundTransformReportsTypeError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping transform test in -short mode")
	}
	p, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	const src = `package main

component Greeting(count int) {
	<p>{ count |> upper }</p>
}
`
	out := p.Transform(src, `Greeting(GreetingProps{Count: 1})`)
	if out.HTML != "" {
		t.Fatalf("expected no HTML for a type error, got %q", out.HTML)
	}
	if len(out.Diagnostics) == 0 {
		t.Fatal("expected a type-error diagnostic")
	}
	if !strings.Contains(out.Diagnostics[0].Message, "as string value in argument") {
		t.Fatalf("unexpected diagnostic: %+v", out.Diagnostics[0])
	}
}
