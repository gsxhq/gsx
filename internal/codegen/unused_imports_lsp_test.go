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
// packages.Load-based syntactic classifier) across every import-spec shape:
// plain default, aliased, blank `_`, dot `.`, a name that differs from its
// path's base (math/rand/v2 declares package "rand"), and a genuinely used
// import (must be absent from both).
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
		"name_ne_base": {
			"a.gsx": "package testmod\n\nimport \"math/rand/v2\"\n\ncomponent A() {\n\t<p>hi</p>\n}\n",
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

// TestPackageUnusedImportsNameNeBaseViaTypes covers the case
// resolvePackageNames exists to handle for the CLI: a default import whose real
// package name differs from its path's last segment (math/rand/v2 declares
// package "rand", not "v2"). Package() must resolve this correctly from
// a.pkg.Imports() (populated by the skeleton type-check) without calling
// resolvePackageNames at all.
func TestPackageUnusedImportsNameNeBaseViaTypes(t *testing.T) {
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
		t.Errorf(`want math/rand/v2 (declared package "rand", base "v2") reported unused via types alone; got %+v`, u)
	}
	if got := resolvePackageNamesCalls.Load(); got != before {
		t.Errorf("go list (resolvePackageNames) was called %d time(s); want types-only resolution", got-before)
	}
}
