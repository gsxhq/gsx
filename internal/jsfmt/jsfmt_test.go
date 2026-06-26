// internal/jsfmt/jsfmt_test.go
package jsfmt

import (
	"strings"
	"testing"
)

func fmtJS(t *testing.T, in string) string {
	t.Helper()
	out, err := Format([]byte(in), 80)
	if err != nil {
		t.Fatalf("Format(%q) error: %v", in, err)
	}
	return string(out)
}

// TestCallbackPatternSingleIndent guards the bug that escaped to a user's file:
// `call(args, (e) => {` has an unclosed `(` AND an opening `{`. Counting both as
// indent levels put the callback BODY two levels deep (and the `});` one level
// too deep). Only the brace must count → exactly one level. This is the
// dominant real-world pattern (htmx/Alpine event handlers).
func TestCallbackPatternSingleIndent(t *testing.T) {
	in := "document.body.addEventListener('htmx:beforeRequest', (evt) => {\nconsole.log('HTMX Request:', evt.detail);\n});"
	want := "document.body.addEventListener('htmx:beforeRequest', (evt) => {\n\tconsole.log('HTMX Request:', evt.detail);\n});"
	if got := fmtJS(t, in); got != want {
		t.Fatalf("callback body over/under-indented:\ngot:  %q\nwant: %q", got, want)
	}
}

// realWorldJS are bodies harvested from one-learning/ui and his-project's
// design-system (Alpine + htmx). They are already hand/editor-formatted with
// tab indentation, so the re-indenter must reproduce them EXACTLY — re-indenting
// correctly-indented real code is a no-op. This is the coverage that was missing
// (synthetic single-nesting tests never exercised the callback/IIFE/nested
// patterns that dominate real code).
var realWorldJS = []string{
	// htmx event listeners (the exact shape that broke).
	"// Optional: Add some basic HTMX event listeners for debugging\n" +
		"document.body.addEventListener('htmx:beforeRequest', (evt) => {\n" +
		"\tconsole.log('HTMX Request:', evt.detail);\n" +
		"});\n" +
		"document.body.addEventListener('htmx:afterRequest', (evt) => {\n" +
		"\tconsole.log('HTMX Response:', evt.detail);\n" +
		"});",
	// Array .forEach with a nested addEventListener callback (two callback levels).
	"['dragenter', 'dragover', 'dragleave', 'drop'].forEach(eventName => {\n" +
		"\tdropZone.addEventListener(eventName, preventDefaults, false);\n" +
		"});\n" +
		"['dragenter', 'dragover'].forEach(eventName => {\n" +
		"\tdropZone.addEventListener(eventName, () => {\n" +
		"\t\tdropZone.classList.add('border-blue-500', 'bg-blue-50');\n" +
		"\t});\n" +
		"});",
	// setInterval callback + nested if, object-method style.
	"function startUploadProgress() {\n" +
		"\tlet progress = 0;\n" +
		"\tconst interval = setInterval(() => {\n" +
		"\t\tprogress += Math.random() * 15;\n" +
		"\t\tif (progress > 90) {\n" +
		"\t\t\tprogress = 90;\n" +
		"\t\t}\n" +
		"\t}, 200);\n" +
		"}",
	// IIFE with nested function + htmx.ajax object arg.
	"(function() {\n" +
		"\tfunction openDrawerFromUrl() {\n" +
		"\t\tvar id = new URLSearchParams(location.search).get('drawer');\n" +
		"\t\tif (!id) return;\n" +
		"\t\thtmx.ajax('GET', '/entities/' + encodeURIComponent(id) + '/drawer', {\n" +
		"\t\t\ttarget: '#entity-drawer-container'\n" +
		"\t\t});\n" +
		"\t}\n" +
		"})();",
	// Alpine x-data factory returning an object literal with methods.
	"function nptEditFormAlpineData() {\n" +
		"\treturn {\n" +
		"\t\tactiveTab: 'npt',\n" +
		"\t\tinit() {\n" +
		"\t\t\tthis.$watch('activeTab', v => history.replaceState(null, '', '#' + v));\n" +
		"\t\t}\n" +
		"\t};\n" +
		"}",
}

func TestRealWorldJSReproducedExactly(t *testing.T) {
	for i, src := range realWorldJS {
		got := fmtJS(t, src)
		if got != src {
			t.Errorf("case %d: re-indenting already-correct real JS changed it:\n--- want (input) ---\n%s\n--- got ---\n%s", i, src, got)
		}
		// And it must be idempotent regardless.
		if again := fmtJS(t, got); again != got {
			t.Errorf("case %d: not idempotent", i)
		}
	}
}

func TestReindentsToTabs(t *testing.T) {
	in := "function f() {\n      const x = 1;\n   if (x) {\nreturn x;\n   }\n}"
	want := "function f() {\n\tconst x = 1;\n\tif (x) {\n\t\treturn x;\n\t}\n}"
	if got := fmtJS(t, in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestKeepsNewlinesNoReflow(t *testing.T) {
	// A one-line body is NOT reflowed (ASI-preserving: line structure untouched).
	in := "const x=1;const y=2"
	if got := fmtJS(t, in); got != in {
		t.Fatalf("must not reflow/reformat a one-liner: got %q", got)
	}
}

func TestPreservesBlankLines(t *testing.T) {
	in := "a();\n\nb();"
	if got := fmtJS(t, in); got != in {
		t.Fatalf("blank line not preserved: got %q", got)
	}
}

func TestTemplateLiteralInteriorUntouched(t *testing.T) {
	in := "const t = `line1\n   line2`;"
	got := fmtJS(t, in)
	if !strings.Contains(got, "`line1\n   line2`") {
		t.Fatalf("template literal interior was re-indented:\n%s", got)
	}
}

func TestRegexNotMislexedAsDivision(t *testing.T) {
	// `/re/g` after `=` is a regex; must pass through verbatim, not break.
	in := "const re = /a\\/b/g;\nconst q = a / b;"
	got := fmtJS(t, in)
	if !strings.Contains(got, "/a\\/b/g") || !strings.Contains(got, "a / b") {
		t.Fatalf("regex/division mishandled:\n%s", got)
	}
}

func TestCommentInteriorUntouched(t *testing.T) {
	in := "function f() {\n\t/* a\n  b */\n\tx();\n}"
	got := fmtJS(t, in)
	if !strings.Contains(got, "/* a\n  b */") {
		t.Fatalf("block comment interior re-indented:\n%s", got)
	}
}

func TestIdempotent(t *testing.T) {
	once := fmtJS(t, "function f(){\n   const x=1\nif(x){\nreturn x\n}\n}")
	twice := fmtJS(t, once)
	if once != twice {
		t.Fatalf("not idempotent:\n--- once ---\n%q\n--- twice ---\n%q", once, twice)
	}
}

func TestTokenSignatureIgnoresWhitespace(t *testing.T) {
	// The two inputs must differ ONLY in whitespace — the re-indenter never
	// adds or removes a semicolon, so the signature (correctly) does not
	// normalize the optional one. Both have `return x;`.
	a := TokenSignature([]byte("const x=1;function f(){return x;}"))
	b := TokenSignature([]byte("const x = 1;\nfunction f() {\n\treturn x;\n}"))
	if a != b {
		t.Fatalf("whitespace changed the signature:\n%q\n%q", a, b)
	}
}

func TestNonLFLineTerminatorsNotDropped(t *testing.T) {
	// A lone \r, U+2028, U+2029 must NOT be dropped (that would fuse tokens and
	// break ASI). Each must become a real line break in the output.
	for _, in := range []string{"a = 1\rb = 2", "a = 1 b = 2", "a = 1 b = 2"} {
		got := fmtJS(t, in)
		if strings.Contains(got, "1b") || strings.Contains(got, "12") {
			t.Fatalf("line terminator dropped, tokens fused: %q -> %q", in, got)
		}
		if !strings.Contains(got, "a = 1\nb = 2") {
			t.Fatalf("expected a line break preserved (as \\n): %q -> %q", in, got)
		}
	}
}

func TestCRLFNormalizedToLF(t *testing.T) {
	got := fmtJS(t, "a = 1\r\nb = 2")
	if got != "a = 1\nb = 2" {
		t.Fatalf("CRLF not normalized to a single LF: %q", got)
	}
}
