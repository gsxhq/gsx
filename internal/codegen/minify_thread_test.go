package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/fullmin"
)

// TestMinifyThreading verifies that CSSMinify/JSMinify/CSSMin/JSMin fields in
// Options are correctly threaded through Module.Generate: different minify
// configurations produce different output for a component with a static
// holeless <style> block containing collapsible whitespace.
//
// Three configs are tested:
//
//	builtinMin: CSSMinify=true, CSSMin=nil  → built-in safe whitespace pass
//	noMin:      CSSMinify=false             → no minification
//	fullMin:    CSSMinify=true, CSSMin=fullmin.CSS → aggressive tdewolff pass
func TestMinifyThreading(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod",
		"module example.com/mintest\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	// Static holeless <style> with collapsible whitespace: multiple spaces between
	// tokens so the built-in safe minifier and fullmin.CSS both transform it, but
	// differently. The built-in preserves spaces around non-delimiter bytes (e.g.
	// spaces around the colon), while fullmin removes them.
	pkgDir := filepath.Join(root, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gsxPath := filepath.Join(pkgDir, "views.gsx")
	writeFile(t, pkgDir, "views.gsx",
		"package views\n\ncomponent Page() {\n\t<style>.a {  color : red ;  }</style>\n}\n")

	generate := func(opts Options) string {
		opts.ModuleRoot = root
		opts.ModulePath = "example.com/mintest"
		opts.FilterPkgs = []string{StdImportPath}
		m, err := Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		out, diags, err := m.Generate(pkgDir)
		if err != nil {
			t.Fatalf("Generate: %v (diags=%v)", err, diags)
		}
		b := out[gsxPath]
		if b == nil {
			t.Fatalf("no output for %s; out=%v", gsxPath, out)
		}
		return string(b)
	}

	builtinMin := generate(Options{CSSMinify: true})
	noMin := generate(Options{CSSMinify: false})
	fullMin := generate(Options{CSSMinify: true, CSSMin: fullmin.CSS})

	// snip returns the line emitting the style body — the open tag itself is
	// split around the auto-injected CSP nonce guard (_gsxgw.Nonce(ctx)), so
	// the CSS content starts in a separate _gsxgw.S(">...") call.
	snip := func(gen, label string) string {
		start := strings.Index(gen, `_gsxgw.S(">`)
		if start == -1 {
			t.Fatalf("%s: cannot find _gsxgw.S(\">\") in generated output:\n%s", label, gen)
		}
		end := strings.Index(gen[start:], "\n")
		if end == -1 {
			return gen[start:]
		}
		return gen[start : start+end]
	}

	builtinSnip := snip(builtinMin, "builtinMin")
	noMinSnip := snip(noMin, "noMin")
	fullMinSnip := snip(fullMin, "fullMin")

	t.Logf("builtinMin: %s", builtinSnip)
	t.Logf("noMin:      %s", noMinSnip)
	t.Logf("fullMin:    %s", fullMinSnip)

	// noMin: original CSS preserved verbatim — double spaces intact, semicolon
	// before closing brace retained, spaces around colon preserved.
	const wantNoMin = `_gsxgw.S(">.a {  color : red ;  }</style>")`
	if !strings.Contains(noMin, wantNoMin) {
		t.Errorf("noMin: expected verbatim CSS; got snippet: %s", noMinSnip)
	}

	// builtinMin: safe whitespace pass — curly-brace-adjacent spaces dropped,
	// double space collapsed, trailing semicolon before } stripped. Spaces around
	// the colon are preserved (colon is not a CSS delimiter in this pass).
	const wantBuiltin = `_gsxgw.S(">.a{color : red}</style>")`
	if !strings.Contains(builtinMin, wantBuiltin) {
		t.Errorf("builtinMin: expected safe-minified CSS; got snippet: %s", builtinSnip)
	}

	// fullMin: aggressive tdewolff pass — all whitespace around property name,
	// colon, and value removed.
	const wantFull = `_gsxgw.S(">.a{color:red}</style>")`
	if !strings.Contains(fullMin, wantFull) {
		t.Errorf("fullMin: expected aggressively-minified CSS; got snippet: %s", fullMinSnip)
	}

	// Non-tautological: the three outputs must all differ.
	if noMin == builtinMin {
		t.Errorf("noMin and builtinMin produced identical output (CSSMinify not threaded?)")
	}
	if builtinMin == fullMin {
		t.Errorf("builtinMin and fullMin produced identical output (CSSMin func not threaded?)")
	}
	if noMin == fullMin {
		t.Errorf("noMin and fullMin produced identical output")
	}
}

// TestMinifyThreading_JS verifies that JSMin/JSONMin are threaded through
// Module.Generate for js`…` attribute values, mirroring TestMinifyThreading's
// CSS coverage: a holeless AND a holey JSON-shaped hx-vals value (parsed by
// htmx via JSON.parse) must stay valid JSON at every level, compacting ONLY
// once JSONMin is supplied (FULL level — see internal/jsmin/file.go's
// cascadeJS/minifyJSSegmentsHoley, which only route to the JSON minifier when
// m.JSON != nil). A non-JSON x-data control is minified as a JS expression at
// FULL level but left untouched at the built-in safe level (see
// internal/corpus/testdata/cases/jsattr/json_attr_minify.txtar for the
// built-in-level pin; the corpus never sets JSMin/JSONMin, so it cannot
// exercise the FULL/tdewolff path — that gap is closed here).
func TestMinifyThreading_JS(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod",
		"module example.com/jsmintest\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	pkgDir := filepath.Join(root, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Double intra-line spaces (around the colon/braces) so the built-in safe
	// pass, which collapses intra-line whitespace, actually differs from
	// noMin — a single-spaced literal would leave noMin==builtinMin
	// (tautologically "differ" but for the wrong reason: nothing to collapse).
	gsxPath := filepath.Join(pkgDir, "views.gsx")
	writeFile(t, pkgDir, "views.gsx", "package views\n\n"+
		"component Page(selfID string) {\n"+
		"\t<div hx-vals=js`{  \"exclude\":  \"SELF-1\"  }`></div>\n"+
		"\t<div hx-vals=js`{  \"exclude\":  @{selfID}  }`></div>\n"+
		"\t<div x-data=js`{  open:  false  }`></div>\n"+
		"}\n")

	generate := func(opts Options) string {
		opts.ModuleRoot = root
		opts.ModulePath = "example.com/jsmintest"
		opts.FilterPkgs = []string{StdImportPath}
		m, err := Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		out, diags, err := m.Generate(pkgDir)
		if err != nil {
			t.Fatalf("Generate: %v (diags=%v)", err, diags)
		}
		b := out[gsxPath]
		if b == nil {
			t.Fatalf("no output for %s; out=%v", gsxPath, out)
		}
		return string(b)
	}

	builtinMin := generate(Options{JSMinify: true})
	noMin := generate(Options{JSMinify: false})
	fullMin := generate(Options{JSMinify: true, JSMin: fullmin.JS, JSONMin: fullmin.JSON})

	t.Logf("builtinMin:\n%s", builtinMin)
	t.Logf("noMin:\n%s", noMin)
	t.Logf("fullMin:\n%s", fullMin)

	// noMin: source values preserved verbatim, double intra-line spaces intact
	// (indentation rebase only touches leading whitespace of the embedded
	// body, not this single-line value's interior).
	for _, want := range []string{
		`_gsxgw.S("<div hx-vals=\"{  &#34;exclude&#34;:  &#34;SELF-1&#34;  }\"></div>")`,
		`_gsxgw.S("<div x-data=\"{  open:  false  }\"></div>")`,
	} {
		if !strings.Contains(noMin, want) {
			t.Errorf("noMin: expected verbatim value %q; got:\n%s", want, noMin)
		}
	}

	// builtinMin (JSMinify: true, JSMin/JSONMin nil): the safe level. Both
	// hx-vals values stay valid, INTRA-LINE-COLLAPSED (double→single space)
	// but not compacted JSON — no JSON minifier runs when m.JSON is nil, so
	// jsmin.cascadeJS falls through to the built-in minifyJS, which collapses
	// whitespace but never removes it or rewrites values. x-data collapses the
	// same way (untouched otherwise — no `(…)`/`!1` rewrite at this level).
	for _, want := range []string{
		`_gsxgw.S("<div hx-vals=\"{ &#34;exclude&#34;: &#34;SELF-1&#34; }\"></div>")`,
		`_gsxgw.S("<div x-data=\"{ open: false }\"></div>")`,
	} {
		if !strings.Contains(builtinMin, want) {
			t.Errorf("builtinMin: expected value %q; got:\n%s", want, builtinMin)
		}
	}

	// fullMin (JSMin=fullmin.JS, JSONMin=fullmin.JSON): the holeless hx-vals
	// compacts to valid, whitespace-free JSON (quoted keys preserved — a JS
	// minifier would have unquoted them and broken JSON.parse). x-data, NOT
	// JSON-shaped, is minified as a JS object expression: `!1` for `false`,
	// wrapped in `(…)` since a single-property `{ open: false }` parses raw as
	// a labeled statement block (see cascadeJS's doc comment).
	for _, want := range []string{
		`_gsxgw.S("<div hx-vals=\"{&#34;exclude&#34;:&#34;SELF-1&#34;}\"></div>")`,
		`_gsxgw.S("<div x-data=\"({open:!1})\"></div>")`,
	} {
		if !strings.Contains(fullMin, want) {
			t.Errorf("fullMin: expected value %q; got:\n%s", want, fullMin)
		}
	}

	// The holey hx-vals (@{selfID} in a JSON value position) must ALSO compact
	// to valid JSON at full level, splitting cleanly back around the
	// _gsxgw.JSValAttr(selfID) call site — no leftover integer sentinel, no
	// stray whitespace either side of the hole.
	if !strings.Contains(fullMin, `_gsxgw.S("<div hx-vals=\"{&#34;exclude&#34;:")`) {
		t.Errorf("fullMin: expected compacted JSON prefix before the hole; got:\n%s", fullMin)
	}
	if !strings.Contains(fullMin, "_gsxgw.JSValAttr(selfID)") {
		t.Errorf("fullMin: expected JSValAttr(selfID) call for the holey hx-vals; got:\n%s", fullMin)
	}
	if !strings.Contains(fullMin, `_gsxgw.S("}\"></div>")`) {
		t.Errorf("fullMin: expected compacted JSON suffix after the hole; got:\n%s", fullMin)
	}

	// Non-tautological: the three outputs must all differ, and the
	// hx-vals/x-data values must be valid JSON / not garbled at every level.
	if noMin == builtinMin {
		t.Errorf("noMin and builtinMin produced identical output")
	}
	if builtinMin == fullMin {
		t.Errorf("builtinMin and fullMin produced identical output (JSMin/JSONMin not threaded?)")
	}
	if noMin == fullMin {
		t.Errorf("noMin and fullMin produced identical output")
	}
}
