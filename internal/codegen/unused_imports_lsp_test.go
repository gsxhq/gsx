package codegen

import (
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
// where the two ARE expected to agree: plain default, aliased, blank `_`, dot
// `.`, and a genuinely used import (must be absent from both).
//
// A default import whose real name differs from its path base
// (math/rand/v2 declares package "rand", base "v2") is deliberately NOT a case
// here: see TestModuleAndPackageDivergeOnUnresolvableNameNeBase below, which
// documents and asserts that the two surfaces now legitimately DISAGREE on it.
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

// TestModuleAndPackageDivergeOnUnresolvableNameNeBase documents and asserts
// the one input shape where Package() (LSP) and Module.UnusedImports (CLI) now
// legitimately disagree, as a direct consequence of this file's Complete()
// fix: a default import whose real package name differs from its path base
// AND is outside the type-checker's own importer graph (math/rand/v2, unused,
// declares package "rand", path base "v2").
//
//   - Module.UnusedImports resolves the candidate's real name via
//     resolvePackageNames — a fresh, targeted `go list` (NeedName-only) that
//     always finds the real name regardless of what analyze's importer graph
//     happens to already contain. It correctly resolves "rand" and reports the
//     (unused) import removed.
//   - Package() resolves candidate names from the already-type-checked
//     *types.Package (importNamesFromTypes) with NO extra go-list call, for
//     LSP hot-path performance. Here go/types never loaded math/rand/v2 (nothing
//     else in the importer graph — the gsx runtime, the std filter package, the
//     module's other Go files — reaches it), so it fabricates an incomplete
//     placeholder named after the path base ("v2"), which importNamesFromTypes
//     now deliberately skips (Complete()==false) rather than trust as a guess.
//     The candidate is therefore unresolvable and conservatively KEPT.
//
// This is the documented, accepted trade-off: under-removal (LSP keeps a
// genuinely unused import it can't safely name-resolve) is safe; the
// alternative — trusting the fabricated name — is what let the LSP delete a
// USED import (see TestPackageUnusedImportsDoesNotDeleteUsedRandV2).
func TestModuleAndPackageDivergeOnUnresolvableNameNeBase(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"math/rand/v2\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u := pr.UnusedImports[filepath.Join(dir, "a.gsx")]; len(u) != 0 {
		t.Errorf("Package(): want math/rand/v2 conservatively KEPT (unresolvable via types), got reported unused: %+v", u)
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

// TestPackageUnusedImportsKeepsUnresolvableCandidate covers the false-positive
// this file's Critical fix removes. math/rand/v2 is outside the type-checker's
// own importer graph (moduleImporter/externalImporter never load it here: it's
// reached only from this .gsx file, not from the gsx runtime, the std filter
// package, or any other Go file in the module), so go/types cannot resolve it
// and fabricates an incomplete placeholder package NAMED AFTER THE PATH'S LAST
// SEGMENT ("v2") — not its real declared name ("rand"). That guessed name used
// to be trusted outright (importNamesFromTypes had no Complete() gate), which
// happened to give the RIGHT answer here only because the import is genuinely
// unused: "v2" is absent from the used-set for the same reason "rand" would
// have been. TestPackageUnusedImportsDoesNotDeleteUsedRandV2 (in the same
// file) proves that agreement was a coincidence, not a correct mechanism — the
// moment the import IS used (`rand.IntN(3)`), the guessed name still isn't in
// the used-set and the old code deleted a working import.
//
// The corrected contract: an import whose package is !Complete() has no
// resolvable real name from types alone, so Package() conservatively KEEPS it
// (matching resolvePackageNames' own "absent path ⇒ keep" contract for the CLI
// path) — even though it is, in fact, unused. This is the documented
// under-removal trade-off; see importNamesFromTypes' doc comment and
// docs/superpowers/specs/2026-07-09-lsp-unused-imports-design.md.
func TestPackageUnusedImportsKeepsUnresolvableCandidate(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"math/rand/v2\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})
	before := resolvePackageNamesCalls.Load()
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := pr.UnusedImports[filepath.Join(dir, "a.gsx")]
	if len(u) != 0 {
		t.Errorf(`want math/rand/v2 conservatively KEPT (unresolvable via types alone, real name "rand" != fabricated "v2"); got reported unused: %+v`, u)
	}
	if got := resolvePackageNamesCalls.Load(); got != before {
		t.Errorf("go list (resolvePackageNames) was called %d time(s); want types-only resolution", got-before)
	}
}

// TestPackageUnusedImportsDoesNotDeleteUsedRandV2 is the Critical regression
// test: before the Complete()-gating fix, importNamesFromTypes trusted
// go/types' fabricated placeholder name for an import outside the importer
// graph — the path's last segment ("v2" for "math/rand/v2"), not its real
// declared name ("rand"). classifyUnusedImports makes math/rand/v2 a removal
// CANDIDATE because its path base "v2" is never referenced (the source
// references it as "rand"), and the old code then resolved that candidate's
// name to the same wrong "v2", which is ALSO absent from the used-set — so a
// live `rand.IntN(3)` reference was invisible and Package() reported the
// import unused. The LSP's format/organizeImports handlers act on exactly that
// signal and would delete a working import, leaving `rand.IntN(3)` undefined.
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

// TestPackageKeepsUnusedImportOutsideImporterGraph pins the REAL shape of
// Divergence A (see the design doc's "Behavior when the type-check fails, or
// an import is unresolved" section): ANY unused default import whose package
// is outside analyze's importer graph (externalImporter's one-shot preload of
// the gsx runtime + std filter package + FilterPkgs/LoadPkgs + "./..." — see
// externalImporter's doc in module.go) is kept by Package(), not just the
// name != path-base shape TestModuleAndPackageDivergeOnUnresolvableNameNeBase
// covers. container/ring is unused, outside the importer graph (reachable only
// from this one .gsx file), AND its declared package name ("ring") equals its
// path base ("ring") — so importNamesFromTypes' Complete() gate, not a
// name-mismatch, is what makes it unresolvable here. Module.UnusedImports
// (CLI, go list) has no such gate and correctly removes it.
func TestPackageKeepsUnusedImportOutsideImporterGraph(t *testing.T) {
	dir, m := openTestModule(t, map[string]string{
		"a.gsx": "package testmod\n\nimport \"container/ring\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
	})
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	aPath := filepath.Join(dir, "a.gsx")
	if u := pr.UnusedImports[aPath]; len(u) != 0 {
		t.Errorf("Package(): want container/ring conservatively KEPT (outside importer graph, name==base), got reported unused: %+v", u)
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
