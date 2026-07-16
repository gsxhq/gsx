package codegen

import (
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// writeDepFactsModule lays out a two-package module on disk:
// ui/card.gsx defines Card(title string) using { attrs... };
// pages/home.gsx imports ui.
func writeDepFactsModule(t *testing.T) (root, uiDir, pagesDir string) {
	t.Helper()
	root = t.TempDir()
	uiDir = filepath.Join(root, "ui")
	pagesDir = filepath.Join(root, "pages")
	for _, d := range []string{uiDir, pagesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, uiDir, "card.gsx", `package ui

import "github.com/gsxhq/gsx"

component Card(title string, attrs gsx.Attrs) {
	<div class="card" { attrs... }>{title}</div>
}
`)
	writeFile(t, pagesDir, "home.gsx", `package pages

import "example.com/app/ui"

component Home() {
	<ui.Card title="t" class="x"/>
}
`)
	return root, uiDir, pagesDir
}

func TestImportedPropFactsRunsCanonicalPreprocessor(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantCode   string
		wantSource string
	}{
		{
			name:       "malformed embedded markup",
			body:       `{ wrap(<Broken></Other>) }`,
			wantCode:   "parse-error",
			wantSource: "parser",
		},
		{
			// Binding/lvalue positions defer to emit-time type adjudication
			// (only a gsx.RawJS hole may splice there), so they are no longer a
			// classify-time preprocessor failure. Exercise a genuine, type-
			// independent JS classify failure instead: a @{ } inside a <script>
			// comment whose reconstructed text contains "</script".
			name:       "JavaScript failure after expansion",
			body:       "{ wrap(<script>const u = @{ id }; // x @{ \"</script>\" }\n</script>) }",
			wantCode:   "jsx-script-close",
			wantSource: "jsx",
		},
		{
			name:       "unsupported Go block element",
			body:       `{{ value := <div/> }}`,
			wantCode:   "unsupported-node",
			wantSource: "codegen",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			uiDir := filepath.Join(root, "ui")
			if err := os.MkdirAll(uiDir, 0o755); err != nil {
				t.Fatal(err)
			}
			repoRoot, _ := filepath.Abs("../..")
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
			writeFile(t, uiDir, "broken.gsx", "package ui\n\ncomponent Broken(value string) {\n\t"+tc.body+"\n}\n")

			m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.importedPropFacts(uiDir)
			if err == nil {
				t.Fatal("importedPropFacts succeeded; want canonical preprocessing failure")
			}
			diags, ok := diagnosticsFromSourceError(err)
			if !ok {
				t.Fatalf("importedPropFacts error = %T %v; want structured diagnostics", err, err)
			}
			if len(diags) != 1 || diags[0].Code != tc.wantCode || diags[0].Source != tc.wantSource {
				t.Fatalf("diagnostics = %+v; want one %s/%s diagnostic", diags, tc.wantSource, tc.wantCode)
			}
			if diags[0].Start.Filename != filepath.Join(uiDir, "broken.gsx") || diags[0].Start.Line == 0 {
				t.Fatalf("diagnostic is not positioned in dependency source: %+v", diags[0])
			}
		})
	}
}

func TestImportedPropFactsUseOnlyActiveCompanionSyntax(t *testing.T) {
	t.Setenv("GOFLAGS", "-tags=feature")
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	uiDir := filepath.Join(root, "ui")
	pagesDir := filepath.Join(root, "pages")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, uiDir, "active.go", "//go:build feature\n\npackage ui\n\ntype CardData struct { Title string }\n")
	writeFile(t, uiDir, "zz_inactive.go", "//go:build !feature\n\npackage ui\n\ntype CardData struct { Count int }\n")
	writeFile(t, uiDir, "card.gsx", "package ui\n\ncomponent Card(data CardData) { <article>{data.Title}</article> }\n")
	writeFile(t, pagesDir, "page.gsx", `package pages

import "example.com/app/ui"

component Page() { <ui.Card data={ui.CardData{Title: "hello"}}/> }
`)

	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.buildPackageSkeletons(pagesDir); err != nil {
		t.Fatalf("prewarm syntactic dependency facts: %v", err)
	}
	if got := m.externalLoads(); got != 0 {
		t.Fatalf("syntactic dependency facts triggered %d external loads", got)
	}
	_, diagnostics, err := m.Generate(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(diagnostics) {
		t.Fatalf("Generate diagnostics = %v", diagnostics)
	}
}

func TestComponentPreprocessFailurePreservesOnlyErrors(t *testing.T) {
	bag := diag.NewBag(token.NewFileSet())
	bag.Add(diag.Diagnostic{
		Severity: diag.Warning,
		Code:     "unrelated-warning",
		Source:   "codegen",
		Message:  "warning",
	})
	bag.Add(diag.Diagnostic{
		Severity: diag.Error,
		Code:     "preprocess-error",
		Source:   "codegen",
		Message:  "error",
	})

	err := componentPreprocessFailure("test", callSitePreprocessResult{syntaxOK: true, scriptsOK: true}, bag)
	diags, ok := diagnosticsFromSourceError(err)
	if !ok {
		t.Fatalf("componentPreprocessFailure error = %T %v; want structured diagnostics", err, err)
	}
	if len(diags) != 1 || diags[0].Severity != diag.Error || diags[0].Code != "preprocess-error" {
		t.Fatalf("diagnostics = %+v; want only the preprocessing error", diags)
	}
}

// TestFileScopedAliasNoCollision: pages/a.gsx imports app/ui as ui;
// pages/b.gsx imports app/widgets under the SAME alias ui. Both packages
// define Panel but with different props. Each file must split attrs against
// ITS OWN import, and output must be stable across repeated generation.
func TestFileScopedAliasNoCollision(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, src string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Dir(p), filepath.Base(p), src)
	}
	repoRoot, _ := filepath.Abs("../..")
	mk("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// ui.Panel has a Variant prop; widgets.Panel does NOT (variant falls to its bag).
	mk("ui/panel.gsx", `package ui

import "github.com/gsxhq/gsx"

component Panel(variant string, attrs gsx.Attrs, children gsx.Node) {
	<section data-variant={variant} { attrs... }>{children}</section>
}
`)
	mk("widgets/panel.gsx", `package widgets

import "github.com/gsxhq/gsx"

component Panel(attrs gsx.Attrs, children gsx.Node) {
	<aside { attrs... }>{children}</aside>
}
`)
	mk("pages/a.gsx", `package pages

import "example.com/app/ui"

component A() {
	<ui.Panel variant="big" class="x">a</ui.Panel>
}
`)
	mk("pages/b.gsx", `package pages

import ui "example.com/app/widgets"

component B() {
	<ui.Panel variant="big" class="x">b</ui.Panel>
}
`)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	pagesDir := filepath.Join(root, "pages")
	out, diags, err := m.Generate(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diags {
		t.Logf("diag: %+v", d)
	}
	aGen := string(out[filepath.Join(pagesDir, "a.gsx")])
	bGen := string(out[filepath.Join(pagesDir, "b.gsx")])
	// a.gsx: ui = app/ui, Panel HAS variant → first positional argument;
	// class falls through to attrs.
	if !strings.Contains(aGen, `ui.Panel("big",`) {
		t.Errorf("a.gsx should pass variant positionally to ui.Panel; got:\n%s", aGen)
	}
	// b.gsx: ui = app/widgets, Panel has NO variant → variant AND class fall
	// through to attrs.
	if !strings.Contains(bGen, `{Key: "variant", Value: "big"}`) {
		t.Errorf("b.gsx should send variant to the Attrs bag; got:\n%s", bGen)
	}
	// Determinism: a fresh Module over the same tree yields identical bytes.
	m2, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	out2, _, err := m2.Generate(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	for p := range out {
		if string(out[p]) != string(out2[p]) {
			t.Errorf("nondeterministic output for %s", p)
		}
	}
}

// A dependency analysis failure is a source error, not permission to guess its
// component contract. Preserve the dependency diagnostic and emit nothing.
func TestImportedPropFactsFailureFailsClosed(t *testing.T) {
	root, uiDir, pagesDir := writeDepFactsModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	m.SetOverride(filepath.Join(uiDir, "card.gsx"), []byte("package ui\n\ncomponent Card( {\n"))
	out, diags, err := m.Generate(pagesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("Generate emitted %d files after dependency analysis failed", len(out))
	}
	found := false
	for _, d := range diags {
		if d.Code == "parse-error" && d.Severity == diag.Error {
			found = true
			if d.Start.Filename != filepath.Join(uiDir, "card.gsx") || d.Start.Line == 0 {
				t.Errorf("dependency diagnostic should retain its source position; got %+v", d)
			}
		}
		if d.Code == "imported-props-unavailable" {
			t.Errorf("dependency failure was downgraded to compatibility warning: %+v", d)
		}
	}
	if !found {
		t.Fatalf("expected dependency parse diagnostic; diags = %+v", diags)
	}
}
