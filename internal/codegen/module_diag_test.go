package codegen

import (
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func TestModulePackageSurfacesTypeErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// `nope` is undefined → a type error inside the interpolation.
	writeFile(t, pkgDir, "page.gsx", "package page\n\ncomponent Home() {\n\t<div>{ nope() }</div>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "nope") && strings.HasSuffix(d.Start.Filename, ".gsx") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a .gsx-positioned type-error diagnostic mentioning 'nope'; got %+v", pr.Diags)
	}
}

// TestModulePackageSurfacesWalkTimeDiagnostics is the P2 perf-hunt regression
// test: Package's diagnostics-only generateFile call (module.go, gated on
// len(a.typeErrs)==0 && !a.bag.HasErrors()) now passes diagnosticsOnly=true,
// returning right after the component/decl walk instead of running the full
// emit (import assembly, coalesceStaticWrites, format.Source). An unknown
// pipe filter is a WALK-time diagnostic (filters.go's lowerPipe, added to bag
// during that same walk, well before the skipped output-shaping stages) —
// exactly the class of diagnostic diagnosticsOnly's early return must still
// surface. The source here type-checks cleanly (bogusFilter is undefined only
// as a FILTER, not a Go identifier — `x` alone is a valid string expression),
// so this exercises the diagnosticsOnly branch, not the type-error branch
// TestModulePackageSurfacesTypeErrors already covers.
func TestModulePackageSurfacesWalkTimeDiagnostics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "page.gsx", "package page\n\ncomponent Home(x string) {\n\t<div>{ x |> bogusFilter }</div>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "unknown filter") && strings.Contains(d.Message, "bogusFilter") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an 'unknown filter \"bogusFilter\"' diagnostic on the Package path; got %+v", pr.Diags)
	}
}

// TestPackageOmitsSecondaryDiagsOnTypeError proves Package surfaces ONLY the
// type-error diagnostic for a package that fails to type-check — not the spurious
// secondary "could not resolve type of interpolation" diagnostics that running
// generateFile on a type-error package would add. Mirrors Generate's gate.
func TestPackageOmitsSecondaryDiagsOnTypeError(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxa\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// {missing} references an undefined identifier -> a single type error. Running
	// generateFile anyway would add a secondary "could not resolve type" diag.
	writeFile(t, pkgDir, "views.gsx", "package views\n\ncomponent A() {\n\t<div>{missing}</div>\n}\n")

	m, err := Open(Options{ModuleRoot: tmp, ModulePath: "gsxa", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	var errCount, secondary int
	for _, d := range pr.Diags {
		if d.Severity != diag.Error {
			continue
		}
		errCount++
		if strings.Contains(d.Message, "could not resolve type") {
			secondary++
		}
	}
	if errCount == 0 {
		t.Fatal("expected at least one type-error diagnostic")
	}
	if secondary != 0 {
		t.Fatalf("Package emitted %d secondary 'could not resolve type' diagnostics; want 0\nall diags: %v", secondary, pr.Diags)
	}
}

func TestAttrsBoundariesUseNativeTypeDiagnostics(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")

	tests := []struct {
		name       string
		src        string
		wantText   string
		wantCount  int
		wantFiles  bool
		forbidText string
	}{
		{
			name: "child prop rejects map without helper leak",
			src: `package views

import "github.com/gsxhq/gsx"

component Card(bag gsx.Attrs) {
	<div></div>
}

component Page(m map[string]any) {
	<Card bag={m}/>
}
`,
			wantText:   "to github.com/gsxhq/gsx.Attrs",
			wantCount:  1,
			forbidText: "_gsxbag",
		},
		{
			name: "element spread rejects map and suppresses output",
			src: `package views

component Page(m map[string]any) {
	<div { m... }></div>
}
`,
			wantText:   "as gsx.Attrs",
			wantCount:  1,
			forbidText: "_gsx",
		},
		{
			name: "element spread rejects unconverted AttrMap",
			src: `package views

import "github.com/gsxhq/gsx"

component Page(bag gsx.AttrMap) {
	<div { bag... }></div>
}
`,
			wantText:   "as gsx.Attrs",
			wantCount:  1,
			forbidText: "_gsx",
		},
		{
			name: "element spread accepts nil",
			src: `package views

component Page() {
	<div { nil... }></div>
}
`,
			wantFiles: true,
		},
		{
			name: "undefined element spread reports once",
			src: `package views

component Page() {
	<div { missing... }></div>
}
`,
			wantText:   "undefined: missing",
			wantCount:  1,
			forbidText: "_gsx",
		},
		{
			name: "element spread accepts Attrs",
			src: `package views

import "github.com/gsxhq/gsx"

component Page(bag gsx.Attrs) {
	<div { bag... }></div>
}
`,
			wantFiles: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
			pkgDir := filepath.Join(root, "views")
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, pkgDir, "views.gsx", tc.src)

			m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
			if err != nil {
				t.Fatal(err)
			}
			files, diags, err := m.Generate(pkgDir)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if got := len(files) > 0; got != tc.wantFiles {
				t.Errorf("generated files present = %v, want %v", got, tc.wantFiles)
			}
			if tc.wantText == "" {
				if len(diags) != 0 {
					t.Fatalf("unexpected diagnostics: %+v", diags)
				}
				return
			}
			if len(diags) != tc.wantCount {
				t.Fatalf("diagnostic count = %d, want %d; all diagnostics: %+v", len(diags), tc.wantCount, diags)
			}
			var matches int
			for _, d := range diags {
				if strings.Contains(d.Message, tc.wantText) {
					matches++
				}
				if tc.forbidText != "" && strings.Contains(d.Message, tc.forbidText) {
					t.Errorf("diagnostic leaked %q: %s", tc.forbidText, d.Message)
				}
			}
			if matches != tc.wantCount {
				t.Fatalf("diagnostics containing %q = %d, want %d; all diagnostics: %+v", tc.wantText, matches, tc.wantCount, diags)
			}
		})
	}
}

func TestModuleInvalidateKeepsExternalWarm(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "comp")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "comp.gsx", "package comp\n\ncomponent Button(label string) {\n\t<button>{label}</button>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	if _, err := m.typesPackage(pkgDir); err != nil {
		t.Fatal(err)
	}
	// edit comp.gsx in-memory: Button now takes an int; Invalidate drops the
	// cached pkgTypes entry so typesPackage re-analyzes from the new override.
	m.SetOverride(filepath.Join(pkgDir, "comp.gsx"), []byte("package comp\n\ncomponent Button(n int) {\n\t<button>{ n }</button>\n}\n"))
	m.Invalidate(pkgDir)
	pkg, err := m.typesPackage(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	// The freshly rebuilt authored signature must reflect the int param.
	obj, ok := pkg.Scope().Lookup("Button").(*types.Func)
	if !ok {
		t.Fatal("Button function not in scope after invalidation")
	}
	sig := obj.Type().(*types.Signature)
	if sig.Params().Len() != 1 || !strings.Contains(sig.Params().At(0).Type().String(), "int") {
		t.Fatalf("Invalidate did not refresh comp signature: %s", sig)
	}
	// ext importer must still be non-nil (warm, not cleared by Invalidate)
	if m.ext == nil {
		t.Fatalf("Invalidate wrongly cleared the external importer")
	}
}

// TestStripGsxProbeWrappers pins the textual sanitization stripGsxunwrap's
// treatment is extended to: the skeleton's harvest-probe wrapper calls
// (_gsxuse/_gsxusen/_gsxuseq) must never appear verbatim in a message that
// reaches stripGsxProbeWrappers, mirroring how stripGsxunwrap already handles
// _gsxunwrap. "_gsxuse(" must not falsely match inside "_gsxusen("/
// "_gsxuseq(" text (they share the "_gsxuse" prefix but continue with a
// different character, never "(").
func TestStripGsxProbeWrappers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"_gsxuse(x) (no value) used as value", "x (no value) used as value"},
		// _gsxusen is variadic (a keep-alive over several identifiers at once);
		// stripping keeps every argument, not just one.
		{"cannot use _gsxusen(a, b) (value of type int)", "cannot use a, b (value of type int)"},
		{"invalid operation: _gsxuseq(a.b) is not addressable", "invalid operation: a.b is not addressable"},
		{"two sibling wrappers: _gsxuse(a) vs _gsxuse(b)", "two sibling wrappers: a vs b"},
		{"no wrapper here", "no wrapper here"},
	}
	for _, tc := range cases {
		if got := stripGsxProbeWrappers(tc.in); got != tc.want {
			t.Errorf("stripGsxProbeWrappers(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestModulePackageSanitizesGsxUseDiagLeak is the T11 fix from the LSP
// completion design's probe (docs/superpowers/specs/2026-07-21-lsp-completion-design.md,
// "Incidental (not fixed here, noted)"): `{{ x := }}` — a short var decl with
// no RHS — produces a raw go/types error whose message quotes the skeleton's
// internal _gsxuse(...) harvest-probe wrapper verbatim
// (`_gsxuse(x) (no value) used as value`), because the error's POSITION falls
// outside the probe span analyze's quietSpans suppression keys on, even though
// its MESSAGE still names the probe. Package's surfaced diagnostics must never
// expose that internal helper name to a user.
func TestModulePackageSanitizesGsxUseDiagLeak(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pkgDir, "page.gsx", "package page\n\ncomponent Home() {\n\t{{ x := }}\n\t<div>{ x }</div>\n}\n")

	m, _ := Open(Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{StdImportPath}})
	pr, err := m.Package(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) == 0 {
		t.Fatal("expected diagnostics for `{{ x := }}` (short var decl with no RHS); got none")
	}
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "_gsxuse") {
			t.Fatalf("diagnostic leaks the internal _gsxuse probe wrapper name: %q", d.Message)
		}
	}
}
