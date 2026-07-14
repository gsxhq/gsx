package printer

import (
	"strings"
	"testing"
)

// A multi-line js"…" attribute value: body one level under the attribute,
// brace-nested deeper, closing delimiter attached to the last line.
func TestEmbeddedAttrJSReindented(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nopen: false,\nitems() {\nreturn 1;\n}\n}\" class=\"c\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	// <form> at depth 1 → attrs at depth 2. Opening `{` attaches to js".
	if !strings.Contains(out, "x-data=js\"{") {
		t.Fatalf("opening delimiter+brace should attach:\n%s", out)
	}
	// body one level under the attribute (2 tabs base + 1 jsfmt = 3 tabs).
	if !strings.Contains(out, "\t\t\topen: false,") {
		t.Fatalf("body not one level under attribute:\n%s", out)
	}
	if !strings.Contains(out, "\t\t\t\treturn 1;") {
		t.Fatalf("nested body not two levels under attribute:\n%s", out)
	}
	// closing brace dedents to the attribute level and the delimiter attaches.
	if !strings.Contains(out, "\t\t}\"") {
		t.Fatalf("closing delimiter should attach at attribute indent:\n%s", out)
	}
	// following attribute survives.
	if !strings.Contains(out, "class=\"c\"") {
		t.Fatalf("trailing attribute lost:\n%s", out)
	}
}

func TestEmbeddedAttrIdempotent(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nopen: false,\n}\"/>\n}\n"
	once, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normPrint(t, once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestEmbeddedAttrHolePreserved(t *testing.T) {
	src := "package p\n\ncomponent C(id string) {\n\t<form x-data=js\"{\nurl: '@{id}',\nk: 1,\n}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "@{id}") || strings.Contains(out, "__gsxhole") {
		t.Fatalf("hole not preserved / sentinel leaked:\n%s", out)
	}
}

// A body containing the literal's own delimiter must round-trip (escaped).
func TestEmbeddedAttrDelimiterRoundTrip(t *testing.T) {
	// js"…" value whose JS contains a double-quoted string → the inner `"` is
	// escaped in source; after fmt it must still be escaped and re-parse.
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nmsg: \\\"hi\\\",\nk: 1,\n}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `msg: \"hi\",`) {
		t.Fatalf("inner delimiter not re-escaped:\n%s", out)
	}
	// re-parse must succeed (idempotence test covers structural stability).
	if _, err := normPrint(t, out); err != nil {
		t.Fatalf("reparse failed: %v\n%s", err, out)
	}
}

// A single-line js"…" value is left inline, unchanged.
func TestEmbeddedAttrSingleLineStaysInline(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{open:false}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x-data=js\"{open:false}\"") {
		t.Fatalf("single-line value should stay inline:\n%s", out)
	}
}

// CSS literal, backtick delimiter, multi-line. Brace-less CSS gets the block
// layout: opening backtick alone on the attribute line, body indented one
// level under the attribute, closing backtick alone at the attribute's own
// indent (never glued to the last declaration).
func TestEmbeddedAttrCSSReindented(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<div style=css`\ncolor: red;\nmargin: 0;\n`/>\n}\n"
	want := "package p\n\ncomponent C() {\n\t<div\n\t\tstyle=css`\n\t\t\tcolor: red;\n\t\t\tmargin: 0;\n\t\t`\n\t/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if out != want {
		t.Fatalf("css block layout mismatch:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

// End-to-end: the motivating Alpine x-data blob converted to js"…".
func TestEmbeddedAttrXDataEndToEnd(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nopen: false,\nactive: -1,\nmoveActive(d) {\nif (!this.open) return;\n},\n}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"x-data=js\"{",
		"\t\t\topen: false,",
		"\t\t\tmoveActive(d) {",
		"\t\t\t\tif (!this.open) return;",
		"\t\t\t},",
		"\t\t}\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// A multi-line template literal inside a js"…" attribute value must have its
// interior emitted VERBATIM (no injected indent) and be idempotent.
func TestEmbeddedAttrTemplateLiteralVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<form x-data=js\"{\nhtml: `<div>\nhi\n</div>`,\nk: 1,\n}\"/>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "`<div>\nhi\n</div>`") {
		t.Fatalf("template-literal interior re-indented (should be verbatim):\n%s", out)
	}
	twice, err := normPrint(t, out)
	if err != nil {
		t.Fatal(err)
	}
	if out != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", out, twice)
	}
}
