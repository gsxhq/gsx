package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestRawJSRendererRegistrationParity pins Task 9's renderer-interaction
// finding for a case the shared txtar corpus CANNOT hold: registering a
// [renderers] entry for gsx's own gsx.RawJS passthrough type.
//
// Corpus-harness limitation (see .superpowers/sdd/task-9-report.md,
// "renderer_rawjs_registered" section): Options.Renderers is module-wide by
// design (no PerDir override, internal/codegen/module.go:60-64), and the
// corpus test harness compiles ALL cases in one shared batch with one
// renderer union (internal/corpus/batch.go). Registering a renderer for
// gsx.RawJS in a corpus case silently rewrites the rendered HTML of ~15
// unrelated pre-existing cases that also use RawJS (script/rawjs_passthrough,
// jsattr/click_rawjs, goexpr-js-literal/*, multispread/jscss_holes_hostile,
// components/embedded_prop_binding, ...) — pinning it there would misrepresent
// their output as intentionally changed. This isolated wire test (its own
// throwaway module, its own Options.Renderers) is the only place the behavior
// can be pinned without that collateral churn.
//
// What is pinned: a renderer registered for "github.com/gsxhq/gsx.RawJS"
// fires IDENTICALLY for (a) a whole-value ExprAttr directly constructing
// gsx.RawJS (`@click={ gsx.RawJS(...) }`) and (b) the expression-form js“
// literal path (`{{ h := js`...` }}` then `@click={h}`) — both are ordinary
// ExprAttrs by the time emitExprAttr sees them, so both hit the same
// applyRenderer call (internal/codegen/emit.go, emitExprAttr) before the
// attribute is written. This is coherent (neither form is silently
// exempted). DECIDED 2026-07-13: registrations targeting gsx.RawJS/RawCSS
// are allowed as intended power-user behavior (documented in the config
// guide's renderers section); this test pins the parity so any future
// change to either outcome is a deliberate, visible diff here.
func TestRawJSRendererRegistrationParity(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxrawjsparity\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	writeFile(t, tmp, "rend/rend.go", `package rend

import "github.com/gsxhq/gsx"

// Loud is a registered [renderers] entry for gsx.RawJS: it wraps the raw JS
// value's text in a visible marker so the wire test can see WHICH lowering
// (if any) it fires on, without depending on exact codegen call syntax.
func Loud(v gsx.RawJS) string { return "LOUD(" + string(v) + ")" }
`)

	dir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package views\n\n" +
		"import \"github.com/gsxhq/gsx\"\n\n" +
		"component Page(id string) {\n" +
		"\t<button @click={ gsx.RawJS(\"save('\" + id + \"')\") }>a</button>\n" +
		"\t{{ h := js`save('@{id}')` }}\n" +
		"\t<button @click={h}>b</button>\n" +
		"}\n"
	writeFile(t, dir, "views.gsx", src)

	res, err := GenerateDirs(tmp, []string{dir}, Options{
		FilterPkgs: []string{stdImportPath},
		CSSMinify:  true,
		JSMinify:   true,
		Renderers: []RendererAlias{
			{TypeKey: "github.com/gsxhq/gsx.RawJS", PkgPath: "gsxrawjsparity/rend", FuncName: "Loud"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dr := res[dir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("generate: unexpected errors: %v", dr.Diags)
	}
	var gen string
	for _, b := range dr.Files {
		gen += string(b)
	}

	// Every AttrValue-sink line whose inner expression routes through the
	// registered renderer: alias.Loud(( <inner expr> )). Two ExprAttrs
	// (the direct gsx.RawJS(...) construction, and the js`` literal's `h`
	// reference) should each produce exactly one such line.
	re := regexp.MustCompile(`_gsxgw\.AttrValue\(string\((_gsxf\d+)\.Loud\(\((.*)\)\)\)\)`)
	matches := re.FindAllStringSubmatch(gen, -1)
	if len(matches) != 2 {
		t.Fatalf("expected exactly 2 renderer-routed AttrValue lines (one per button), found %d, generated:\n%s", len(matches), gen)
	}

	aliasDirect, innerDirect := matches[0][1], matches[0][2]
	aliasExpr, innerExpr := matches[1][1], matches[1][2]

	// Same registered renderer entry resolved both times (same alias) — not
	// two independently-resolved registrations that happen to look similar.
	if aliasDirect != aliasExpr {
		t.Fatalf("renderer alias diverged between forms: direct=%s expr=%s\ngenerated:\n%s", aliasDirect, aliasExpr, gen)
	}

	// The direct-construction form's inner expression is the verbatim
	// gsx.RawJS(...) call text (Go-as-blob: attr expressions are copied
	// through, not reparsed).
	if wantInner := `gsx.RawJS("save('" + id + "')")`; innerDirect != wantInner {
		t.Errorf("direct-construction form: renderer arg = %q, want %q\ngenerated:\n%s", innerDirect, wantInner, gen)
	}
	// The expression-form's inner expression is the bare `h` reference — the
	// js`` literal's RawJS construction already happened at the `{{ h := ... }}`
	// statement above; applyRenderer only sees the plain identifier here.
	if wantInner := `h`; innerExpr != wantInner {
		t.Errorf("expression form: renderer arg = %q, want %q\ngenerated:\n%s", innerExpr, wantInner, gen)
	}
}
