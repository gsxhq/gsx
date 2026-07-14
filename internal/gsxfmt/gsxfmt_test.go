package gsxfmt

import (
	"testing"

	"github.com/gsxhq/gsx/internal/pretty"
)

const messy = `package views



component   Hi(name string) {
    <p>{name}</p>
}
`

// TestFormatCanonicalizes: a messy file is rewritten to canonical form (collapsed
// blank lines, single space after `component`).
func TestFormatCanonicalizes(t *testing.T) {
	out, err := Format("hi.gsx", []byte(messy), 80)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if got == messy {
		t.Fatalf("Format did not change a non-canonical file:\n%s", got)
	}
	if want := "component Hi(name string)"; !contains(got, want) {
		t.Fatalf("formatted output missing %q:\n%s", want, got)
	}
}

// TestFormatIdempotent: formatting an already-canonical file is a no-op.
func TestFormatIdempotent(t *testing.T) {
	once, err := Format("hi.gsx", []byte(messy), 80)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Format("hi.gsx", once, 80)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Fatalf("Format is not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// TestFormatParseErrorReturnsError: invalid gsx yields an error, not silent
// truncation — callers decide whether to surface or ignore it.
func TestFormatParseErrorReturnsError(t *testing.T) {
	if _, err := Format("bad.gsx", []byte("package x\n\ncomponent Hi( {\n"), 80); err == nil {
		t.Fatal("expected a parse error for malformed gsx, got nil")
	}
}

// A multi-line embedded-attribute body is now re-indented by the configured
// JS/CSS formatter (printer: format multi-line js`/css` attribute values),
// superseding the verbatim-preservation behavior this test used to pin.
// jsfmt/cssfmt treat these bodies as flat top-level content (no outer
// nesting), so they get zero extra indent beyond the attribute's own depth;
// the opening tag and children break in symmetry once an attribute value
// forces a hard break.
func TestFormatReindentsMultilineEmbeddedAttrBody(t *testing.T) {
	src := "package p\n\n" +
		"component C(open bool) {\n" +
		"\t<div x-data=js`" + "\n" +
		"\t\t{ open: @{ open } }\n" +
		"\t` style=css`" + "\n" +
		"\t\tcolor : @{ color }\n" +
		"\t`>x</div>\n" +
		"}\n"
	want := "package p\n\n" +
		"component C(open bool) {\n" +
		"\t<div\n" +
		"\t\tx-data=js`\n" +
		"\t\t\t{ open: @{open} }\n" +
		"\t\t`\n" +
		"\t\tstyle=css`\n" +
		"\t\t\tcolor : @{color}\n" +
		"\t\t`\n" +
		"\t>\n" +
		"\t\tx\n" +
		"\t</div>\n" +
		"}\n"

	out, err := Format("embedded.gsx", []byte(src), 80)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != want {
		t.Fatalf("format mismatch:\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
	again, err := Format("embedded.gsx", out, 80)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(out) {
		t.Fatalf("Format is not idempotent:\nonce:\n%s\ntwice:\n%s", out, again)
	}
}

// Tab width changes where a line overflows, so the same source lays out
// differently at 2 and at 4. Nothing pinned this before: changing tabWidth
// broke zero tests, which measured coverage, not safety.
func TestFormatWithTabWidthChangesLayout(t *testing.T) {
	// A deeply-indented element whose line sits between the two budgets.
	src := []byte("package ui\n\ncomponent C() {\n\t<div>\n\t\t<span class=\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\">x</span>\n\t</div>\n}\n")

	at2, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: 2})
	if err != nil {
		t.Fatal(err)
	}
	at4, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: 4})
	if err != nil {
		t.Fatal(err)
	}
	if string(at2) == string(at4) {
		t.Errorf("tab width had no effect on layout:\n%s", at2)
	}
}

func TestFormatOptionsTabWidthZeroIsDefault(t *testing.T) {
	src := []byte("package ui\n\ncomponent C() {\n\t<p>hi</p>\n}\n")
	zero, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: 0})
	if err != nil {
		t.Fatal(err)
	}
	def, err := FormatWith("x.gsx", src, FormatOptions{Width: 80, TabWidth: pretty.DefaultTabWidth})
	if err != nil {
		t.Fatal(err)
	}
	if string(zero) != string(def) {
		t.Error("TabWidth 0 must mean DefaultTabWidth")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
