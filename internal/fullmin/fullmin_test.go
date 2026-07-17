package fullmin

import (
	"strings"
	"testing"
)

func TestCSS_Aggressive(t *testing.T) {
	// safe minify keeps "color: #ffffff"; full shortens the hex and drops the space.
	got, err := CSS(".a { color: #ffffff; }")
	if err != nil {
		t.Fatal(err)
	}
	if got != ".a{color:#fff}" {
		t.Fatalf("full CSS = %q, want %q", got, ".a{color:#fff}")
	}
}

func TestJS_Aggressive(t *testing.T) {
	// full mangles locals but keeps the top-level function name.
	got, err := JS("function add(a, b) {\n  const sum = a + b;\n  return sum;\n}")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" || len(got) >= len("function add(a, b) {\n  const sum = a + b;\n  return sum;\n}") {
		t.Fatalf("full JS did not shrink: %q", got)
	}
	// Top-level name must survive (it may be referenced from HTML).
	if !strings.Contains(got, "add") {
		t.Fatalf("full JS dropped the function name: %q", got)
	}
}

func TestJSON(t *testing.T) {
	got, err := JSON(`{ "exclude": "SELF-1" }`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"exclude":"SELF-1"}` {
		t.Fatalf("JSON minify = %q, want %q", got, `{"exclude":"SELF-1"}`)
	}
	// A bare integer (the hole sentinel) must survive verbatim.
	got2, _ := JSON(`{ "k": 909090900 }`)
	if got2 != `{"k":909090900}` {
		t.Fatalf("integer not preserved: %q", got2)
	}
}
