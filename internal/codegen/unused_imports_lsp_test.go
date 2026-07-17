package codegen

import (
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// TestPackageUnusedImportsSurvivesOtherError is the regression test for the LSP
// unused-imports bug (see docs/superpowers/specs/2026-07-09-lsp-unused-imports-
// design.md): a real multi-file package whose skeleton carries an UNRELATED type
// error (a.gsx's undefined "Nope") alongside a genuinely unused import in a
// SIBLING file (b.gsx's "bytes") must still report that unused import via
// m.Package(dir).UnusedImports.
//
// This is the exact case the old detectUnusedImports (results.go, now deleted)
// returned nil for: it bailed to nil the moment it saw any type error that did
// not cleanly position-correlate to an "imported and not used" message, which
// is the norm for a real multi-file package. Going through Package() (not
// Module.UnusedImports directly) is deliberate: it's the entry point the
// self-deadlocking rejected patch (commit 42335ee) broke, and the only path the
// LSP's format/organizeImports handlers actually call.
func TestPackageUnusedImportsSurvivesOtherError(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"strings\"\n\ncomponent A() {\n\t<p>{strings.ToUpper(\"x\")}</p>\n\t<p>{Nope()}</p>\n}\n",
		"b.gsx": "package testmod\n\nimport \"bytes\"\n\ncomponent B() {\n\t<p>hi</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Sanity: confirm the unrelated error is actually present, else this test
	// isn't exercising the scenario it claims to.
	foundUnrelated := false
	for _, d := range pr.Diags {
		if d.Severity == diag.Error && strings.Contains(d.Message, "undefined: Nope") {
			foundUnrelated = true
		}
	}
	if !foundUnrelated {
		t.Fatalf("expected an unrelated type error (undefined: Nope) in diags, got %+v", pr.Diags)
	}
	bPath := filepath.Join(dir, "b.gsx")
	unused := pr.UnusedImports[bPath]
	if len(unused) != 1 || unused[0].Path != "bytes" {
		t.Errorf("want b.gsx's bytes reported unused despite a.gsx's unrelated type error; got %+v (all=%+v)", unused, pr.UnusedImports)
	}
	aPath := filepath.Join(dir, "a.gsx")
	if u := pr.UnusedImports[aPath]; len(u) != 0 {
		t.Errorf("a.gsx's strings IS used (strings.ToUpper); must not be reported unused, got %+v", u)
	}
}

// TestPackageParityWithModuleUnusedImports proves Package(dir).UnusedImports
// (the LSP surface, now computed from analyze's already-type-checked skeletons)
// agrees exactly with Module.UnusedImports(dir) (the CLI's independent
// packages.Load-based syntactic classifier) across every import-spec shape
// where the two are expected to agree: plain default, aliased, blank `_`, dot
// `.`, and a genuinely used import (must be absent from both).
func TestPackageParityWithModuleUnusedImports(t *testing.T) {
	cases := map[string]map[string]string{
		"default": {
			"a.gsx": "package testmod\n\nimport \"bytes\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
		},
		"aliased": {
			"a.gsx": "package testmod\n\nimport al \"bytes\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
		},
		"blank": {
			"a.gsx": "package testmod\n\nimport _ \"bytes\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
		},
		"dot": {
			"a.gsx": "package testmod\n\nimport . \"bytes\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
		},
		"used": {
			"a.gsx": "package testmod\n\nimport \"strings\"\n\ncomponent A() {\n\t<p>{strings.ToUpper(\"x\")}</p>\n}\n",
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			dir, m := openTestModule(t, files)
			pr, err := m.Package(dir)
			if err != nil {
				t.Fatal(err)
			}
			syn, _, err := m.UnusedImports(dir)
			if err != nil {
				t.Fatal(err)
			}
			assertSameRemovalSet(t, dir, pr.UnusedImports, syn)
		})
	}
}

// The authoritative cold source inventory loads imports authored only in GSX,
// so Package can resolve a real package name even when it differs from the path
// base. Both the types-only LSP path and the CLI path must remove the import.
func TestModuleAndPackageResolveGsxOnlyImportName(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"math/rand/v2\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := pr.UnusedImports[filepath.Join(dir, "a.gsx")]
	if len(u) != 1 || u[0].Path != "math/rand/v2" {
		t.Errorf("Package(): want math/rand/v2 reported unused from its authoritative type, got %+v", u)
	}
	syn, _, err := m.UnusedImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	su := syn[filepath.Join(dir, "a.gsx")]
	if len(su) != 1 || su[0].Path != "math/rand/v2" {
		t.Errorf(`Module.UnusedImports: want math/rand/v2 (resolved to "rand" via go list) reported unused; got %+v`, su)
	}
}

// TestPackageUnusedImportsDoesNotCallGoList is the deterministic (not timing-
// based) proof that Package() never shells out to `go list`: resolvePackageNames
// is the only place that calls packages.Load for name resolution, and
// resolvePackageNamesCalls counts its invocations. The package under test has
// an unused DEFAULT import ("bytes") — exactly the shape whose candidate
// resolution used to trigger a packages.Load in the rejected patch (measured
// 19.3ms vs 158us). Package() must resolve it from a.pkg.Imports() instead, so
// the counter must not move.
//
// The zero-assertion alone would be vacuous if resolvePackageNames were simply
// dead code, so this also drives Module.UnusedImports (the CLI path) over the
// SAME package afterwards and asserts the counter DOES move there — proving
// the instrumentation can actually observe a call.
func TestPackageUnusedImportsDoesNotCallGoList(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"bytes\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})

	before := resolvePackageNamesCalls.Load()
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolvePackageNamesCalls.Load(); got != before {
		t.Errorf("Package() called resolvePackageNames (go list) %d time(s); want 0 — LSP hot path must resolve names from types alone", got-before)
	}
	if u := pr.UnusedImports[filepath.Join(dir, "a.gsx")]; len(u) != 1 || u[0].Path != "bytes" {
		t.Errorf("want bytes reported unused via types alone, got %+v", u)
	}

	// The CLI path, over the same package, DOES resolve via packages.Load —
	// proving the counter is wired to something Package() could have hit.
	if _, _, err := m.UnusedImports(dir); err != nil {
		t.Fatal(err)
	}
	if got := resolvePackageNamesCalls.Load(); got == before {
		t.Errorf("Module.UnusedImports never called resolvePackageNames; counter is not observing real calls (test would be vacuous)")
	}
}

// GSX-authored imports are roots of the authoritative cold load. Package can
// therefore resolve math/rand/v2's real declared name without another go list.
func TestPackageUnusedImportsResolvesGsxOnlyCandidate(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"math/rand/v2\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})
	before := resolvePackageNamesCalls.Load()
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := pr.UnusedImports[filepath.Join(dir, "a.gsx")]
	if len(u) != 1 || u[0].Path != "math/rand/v2" {
		t.Errorf("want math/rand/v2 reported unused via its authoritative type, got %+v", u)
	}
	if got := resolvePackageNamesCalls.Load(); got != before {
		t.Errorf("go list (resolvePackageNames) was called %d time(s); want types-only resolution", got-before)
	}
}

func TestImportNamesFromTypesSkipsIncompletePackage(t *testing.T) {
	root := types.NewPackage("testmod", "testmod")
	incomplete := types.NewPackage("math/rand/v2", "v2")
	complete := types.NewPackage("bytes", "bytes")
	complete.MarkComplete()
	root.SetImports([]*types.Package{incomplete, complete})

	names := importNamesFromTypes(root)
	if _, ok := names["math/rand/v2"]; ok {
		t.Fatalf("incomplete package contributed a fabricated name: %v", names)
	}
	if got := names["bytes"]; got != "bytes" {
		t.Fatalf("complete package name = %q, want bytes", got)
	}
}

// TestPackageUnusedImportsDoesNotDeleteUsedRandV2 is the Critical regression
// test: an incomplete imported package must never contribute a fabricated path-
// base name, and an authoritative complete import must use its real declared
// name. In both cases a live rand.IntN reference keeps math/rand/v2.
//
// This must FAIL before the fix (verified) and PASS after it.
func TestPackageUnusedImportsDoesNotDeleteUsedRandV2(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"math/rand/v2\"\n\ncomponent A() {\n\t<p>{rand.IntN(3)}</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := pr.UnusedImports[filepath.Join(dir, "a.gsx")]
	for _, imp := range u {
		if imp.Path == "math/rand/v2" {
			t.Fatalf("math/rand/v2 is USED (rand.IntN(3)) but Package() reported it unused (would be deleted by the LSP): unused=%+v", u)
		}
	}
}

// Package also resolves an authoritative GSX-only import whose declared name
// equals its path base; this is not limited to name-different paths.
func TestPackageRemovesUnusedGsxOnlyImportNameEqualsBase(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"container/ring\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(dir, "a.gsx")
	u := pr.UnusedImports[aPath]
	if len(u) != 1 || u[0].Path != "container/ring" {
		t.Errorf("Package(): want container/ring reported unused from its authoritative type, got %+v", u)
	}
	syn, _, err := m.UnusedImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	su := syn[aPath]
	if len(su) != 1 || su[0].Path != "container/ring" {
		t.Errorf(`Module.UnusedImports: want container/ring reported unused (resolved via go list); got %+v`, su)
	}
}

// TestPackageRemovesUnusedSiblingGsxImportModuleKeeps pins Divergence B: the
// opposite direction from every other test in this file. main.gsx imports a
// sibling gsx-only package ("testmod/foo", no .go files) and never references
// it. Package() (LSP) type-checks foo from its skeleton via moduleImporter —
// unlike stdlib/third-party packages, sibling gsx packages route through the
// warm skeleton graph, not externalImporter — so it resolves foo's real name
// ("foo") from types and correctly reports the import unused. Module.UnusedImports
// (CLI) asks `go list -f NeedName` for the same path, which fails ("no Go
// files in .../foo": verified empirically) since go list cannot resolve a
// package with zero .go files; the candidate is therefore unresolvable and the
// CLI conservatively KEEPS it. This is the one shape where Package() is MORE
// aggressive than the CLI, not less — see TestPackageKeepsUsedSiblingGsxImport
// for the safety property that makes this acceptable (Package() never removes
// a sibling import that IS used).
func TestPackageRemovesUnusedSiblingGsxImportModuleKeeps(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"foo/box.gsx": "package foo\n\ncomponent Box() {\n\t<div>box</div>\n}\n",
		"main.gsx":    "package testmod\n\nimport \"testmod/foo\"\n\ncomponent Main() {\n\t<p>hi</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.gsx")
	u := pr.UnusedImports[mainPath]
	if len(u) != 1 || u[0].Path != "testmod/foo" {
		t.Errorf(`Package(): want testmod/foo (unused sibling gsx import, resolved via types) reported unused; got %+v`, u)
	}
	syn, _, err := m.UnusedImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	su := syn[mainPath]
	if len(su) != 0 {
		t.Errorf(`Module.UnusedImports: want testmod/foo conservatively KEPT (go list cannot resolve a .gsx-only sibling with no .go files); got reported unused: %+v`, su)
	}
}

// TestPackageKeepsUsedSiblingGsxImport is the safety property for
// Divergence B: when the same sibling gsx-only import IS used — here via a
// plain Go function call (foo.Helper()) inside an interpolation, not merely a
// component tag — Package() must NOT report it unused. Divergence B only ever
// makes Package() remove a GENUINELY unused sibling import (matching the
// adversarial review's finding that it was verified safe across tag, plain
// call, and other used-shapes); it must never misclassify a used one.
func TestPackageKeepsUsedSiblingGsxImport(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"foo/box.gsx": "package foo\n\nfunc Helper() string { return \"x\" }\n\ncomponent Box() {\n\t<div>box</div>\n}\n",
		"main.gsx":    "package testmod\n\nimport \"testmod/foo\"\n\ncomponent Main() {\n\t<p>{ foo.Helper() }</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.gsx")
	if u := pr.UnusedImports[mainPath]; len(u) != 0 {
		t.Errorf("Package(): testmod/foo IS used (foo.Helper()) but reported unused (would be deleted by the LSP): %+v", u)
	}
}

// TestPackageUnusedImportsHeadlineCaseStillRemoved guards against the
// Complete()-gating fix over-correcting into never removing anything: context
// and io are both genuinely unused here AND fully resolvable
// (types.Package.Complete()==true, since the gsx runtime itself imports both,
// so they are always in analyze's importer graph) — Package() must still
// report them unused via types alone, exactly as before the fix.
func TestPackageUnusedImportsHeadlineCaseStillRemoved(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport (\n\t\"context\"\n\t\"io\"\n)\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})
	before := resolvePackageNamesCalls.Load()
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := pr.UnusedImports[filepath.Join(dir, "a.gsx")]
	if len(u) != 2 {
		t.Fatalf("want both context and io reported unused via types alone; got %+v", u)
	}
	gotPaths := map[string]bool{}
	for _, imp := range u {
		gotPaths[imp.Path] = true
	}
	if !gotPaths["context"] || !gotPaths["io"] {
		t.Errorf("want context and io both reported unused; got %+v", u)
	}
	if got := resolvePackageNamesCalls.Load(); got != before {
		t.Errorf("go list (resolvePackageNames) was called %d time(s); want types-only resolution", got-before)
	}
}
