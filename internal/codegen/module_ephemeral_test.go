package codegen

import (
	goast "go/ast"
	"path/filepath"
	"testing"
	"time"
)

// newEphemeralTestModule creates a temporary module with a single "page"
// package: page/types.go (a plain Go file defining User), page/other.gsx (a
// second, always-valid component "Other"), and an empty page/page.gsx on
// disk whose content callers set via m.SetOverride(pagePath, ...) — the
// in-memory buffer authority path exercised by the LSP-completion work.
//
// Returns the opened Module, the page package directory, and the absolute
// path to page.gsx. Reused and grown by later ephemeral-module tests
// (Tasks 1-3 of the LSP-completion plan).
func newEphemeralTestModule(t *testing.T) (m *Module, pkgDir string, pageGsxAbsPath string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, root, "page/types.go", "package page\n\ntype User struct{ Name string }\n")
	writeFile(t, root, "page/other.gsx", "package page\n\ncomponent Other() {\n\t<div>ok</div>\n}\n")

	mod, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pkgDir = filepath.Join(root, "page")
	pageGsxAbsPath = filepath.Join(pkgDir, "page.gsx")
	return mod, pkgDir, pageGsxAbsPath
}

// componentDeclsSurviveTypeErrors: a type error in one file must not empty the
// package's syntactic component-declaration facts (spec: tag completion works
// mid-edit; probe 2026-07-21 showed 2 -> 0 before the fix).
func TestComponentDeclsSurviveTypeErrors(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t) // helper: see step notes below
	// Valid baseline: two components (Home in page.gsx, Other in other.gsx).
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n"))
	res, err := m.Package(dir)
	if err != nil {
		t.Fatalf("baseline Package: %v", err)
	}
	if len(res.ComponentDecls) != 2 {
		t.Fatalf("baseline ComponentDecls = %d, want 2", len(res.ComponentDecls))
	}
	// Introduce a type error (User has no field Nam).
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Nam }</div>\n}\n"))
	res, err = m.Package(dir)
	if err != nil {
		t.Fatalf("type-error Package: %v", err)
	}
	if len(res.ComponentDecls) != 2 {
		t.Fatalf("type-error ComponentDecls = %d, want 2 (syntactic facts must survive type errors)", len(res.ComponentDecls))
	}
}

func TestPackageResultFilters(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name |> upper }</div>\n}\n"))
	res, err := m.Package(dir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(res.Filters) == 0 {
		t.Fatal("Filters empty; want std filters (upper, lower, trim, ...)")
	}
	names := map[string]FilterCandidate{}
	for i, f := range res.Filters {
		names[f.Name] = f
		if i > 0 && res.Filters[i-1].Name >= f.Name {
			t.Fatalf("Filters not sorted by Name at %d: %q >= %q", i, res.Filters[i-1].Name, f.Name)
		}
	}
	up, ok := names["upper"]
	if !ok {
		t.Fatalf("std filter upper missing; got %v", res.Filters)
	}
	if up.Func != "Upper" || up.Pkg != "github.com/gsxhq/gsx/std" {
		t.Fatalf("upper = %+v, want Func=Upper Pkg=github.com/gsxhq/gsx/std", up)
	}
}

func TestAnalyzeEphemeralBasics(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	live := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	m.SetOverride(pagePath, live)
	base, err := m.Package(dir)
	if err != nil || base.Info == nil {
		t.Fatalf("baseline: %v info=%v", err, base.Info)
	}

	// Ephemeral: the phantom-repaired trailing-dot buffer. user._ typechecks
	// with a type error but full Info (probe 2026-07-21).
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")
	eph, err := m.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}
	if eph.Info == nil || eph.Types == nil {
		t.Fatal("ephemeral result missing Info/Types")
	}
	if len(eph.ExprMap) == 0 {
		t.Fatal("ephemeral ExprMap empty; want the user._ interp bridged")
	}
	if len(eph.Filters) == 0 {
		t.Fatal("ephemeral result missing Filters")
	}

	// The persistent view is untouched: Package(dir) returns the SAME cached
	// result pointer as before, and its facts reflect the live buffer.
	after, err := m.Package(dir)
	if err != nil {
		t.Fatalf("Package after ephemeral: %v", err)
	}
	if after != base {
		t.Fatal("AnalyzeEphemeral evicted the cached PackageResult; must not touch pkgResults")
	}
}

// TestTryAnalyzeEphemeralUncontended proves the fast path: on a free Module,
// TryAnalyzeEphemeral acquires (acquired=true) and returns a result with the
// same facts AnalyzeEphemeral produces over the identical buffer. This is the
// justification for the LSP handlers switching to the non-blocking variant
// unconditionally — when uncontended it is indistinguishable from the blocking
// call.
func TestTryAnalyzeEphemeralUncontended(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	live := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	m.SetOverride(pagePath, live)
	if _, err := m.Package(dir); err != nil {
		t.Fatalf("baseline Package: %v", err)
	}
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")

	blocking, err := m.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}
	tryRes, acquired, err := m.TryAnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("TryAnalyzeEphemeral: %v", err)
	}
	if !acquired {
		t.Fatal("uncontended TryAnalyzeEphemeral did not acquire the lock")
	}
	if tryRes == nil || tryRes.Info == nil || tryRes.Types == nil {
		t.Fatalf("TryAnalyzeEphemeral result missing Info/Types: %+v", tryRes)
	}
	if len(tryRes.ExprMap) == 0 {
		t.Fatal("TryAnalyzeEphemeral result ExprMap empty; want the user._ interp bridged")
	}
	// Same facts as the blocking variant over the identical buffer.
	if len(tryRes.Filters) != len(blocking.Filters) {
		t.Fatalf("Filters len = %d, want %d (identical to AnalyzeEphemeral)", len(tryRes.Filters), len(blocking.Filters))
	}
	if len(tryRes.ComponentDecls) != len(blocking.ComponentDecls) {
		t.Fatalf("ComponentDecls len = %d, want %d (identical to AnalyzeEphemeral)", len(tryRes.ComponentDecls), len(blocking.ComponentDecls))
	}
	if len(tryRes.ExprMap) != len(blocking.ExprMap) {
		t.Fatalf("ExprMap len = %d, want %d (identical to AnalyzeEphemeral)", len(tryRes.ExprMap), len(blocking.ExprMap))
	}
}

// TestTryAnalyzeEphemeralContended proves the insurance: while analysisMu is
// held (modeling an in-flight background Package/Generate), TryAnalyzeEphemeral
// declines rather than blocks — acquired=false, nil result, no error, and it
// returns promptly. The test holds the real analysisMu directly (same-package
// access, no export needed and no sleep-based synchronization).
func TestTryAnalyzeEphemeralContended(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	src := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	m.SetOverride(pagePath, src)
	if _, err := m.Package(dir); err != nil {
		t.Fatalf("baseline Package: %v", err)
	}

	// Hold the same lock every top-level entry point takes.
	m.analysisMu.Lock()
	start := time.Now()
	res, acquired, err := m.TryAnalyzeEphemeral(dir, pagePath, src)
	elapsed := time.Since(start)
	m.analysisMu.Unlock()

	if acquired {
		t.Fatal("TryAnalyzeEphemeral acquired the lock while it was held; want acquired=false")
	}
	if res != nil {
		t.Fatalf("not-acquired result must be nil, got %+v", res)
	}
	if err != nil {
		t.Fatalf("not-acquired must not error, got %v", err)
	}
	if elapsed > 10*time.Millisecond {
		t.Fatalf("TryAnalyzeEphemeral blocked %v under contention; must return promptly", elapsed)
	}
}

func TestAnalyzeEphemeralShellOnBrokenElsewhere(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	// other.gsx is valid; break page.gsx structurally somewhere the repair
	// didn't fix (an unclosed tag on a DIFFERENT line than the "cursor").
	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div\n\t<span>{ user._ }</span>\n}\n")
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n"))
	eph, err := m.AnalyzeEphemeral(dir, pagePath, patched)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral must not hard-error on parse diags: %v", err)
	}
	if eph.Info != nil {
		t.Fatal("want diagnostics-only shell for unrepaired parse error")
	}
	if len(eph.Diags) == 0 {
		t.Fatal("shell result must carry the parse diagnostics")
	}
}

// TestAnalyzeEphemeralColdReanalyzeReflectsLiveBuffer proves cache restore: an
// AnalyzeEphemeral over the phantom-repaired `user._` buffer must not poison a
// later COLD re-analysis. After dropping the cached PackageResult (and the
// snapshot-restored pkgTypes) for the dir, Package(dir) re-type-checks from the
// retained LIVE buffer, and its facts must reflect `user.Name` (a resolved
// string selector) — never the ephemeral `user._`.
func TestAnalyzeEphemeralColdReanalyzeReflectsLiveBuffer(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	live := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	m.SetOverride(pagePath, live)
	base, err := m.Package(dir)
	if err != nil || base.Info == nil {
		t.Fatalf("baseline: %v info=%v", err, base.Info)
	}

	patched := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")
	if _, err := m.AnalyzeEphemeral(dir, pagePath, patched); err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}

	// Force a cold re-analysis: Invalidate drops pkgResults[dir] and pkgTypes[dir]
	// so Package re-runs analysis from retained source instead of a cache hit.
	m.Invalidate(dir)
	res, err := m.Package(dir)
	if err != nil || res.Info == nil {
		t.Fatalf("cold re-analyze: %v info=%v", err, res.Info)
	}
	if res == base {
		t.Fatal("Invalidate did not drop the cached PackageResult; cold re-analysis did not run")
	}

	// Every bridged interp expression must reflect the live buffer: a `._` blank
	// selector would mean the ephemeral buffer leaked into the retained facts; a
	// `.Name` selector resolving to string confirms the live buffer.
	var foundLive bool
	for _, expr := range res.ExprMap {
		goast.Inspect(expr, func(nd goast.Node) bool {
			se, ok := nd.(*goast.SelectorExpr)
			if !ok {
				return true
			}
			if se.Sel.Name == "_" {
				t.Fatalf("cold re-analysis bridged a blank `._` selector; ephemeral buffer leaked")
			}
			if se.Sel.Name == "Name" {
				if tv, ok := res.Info.Types[se]; ok && tv.Type != nil && tv.Type.String() == "string" {
					foundLive = true
				}
			}
			return true
		})
	}
	if !foundLive {
		t.Fatal("cold re-analysis missing a resolved live `user.Name` selector; facts do not reflect the live buffer")
	}
}

func TestAnalyzeEphemeralDoesNotDirty(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	m.SetOverride(pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n"))
	if _, err := m.Package(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AnalyzeEphemeral(dir, pagePath, []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user._ }</div>\n}\n")); err != nil {
		t.Fatal(err)
	}
	if got := m.dirtyDirs(); len(got) != 0 {
		t.Fatalf("ephemeral analysis dirtied %v; must leave dirty tracking untouched", got)
	}
}

// TestPackageRetainsFileScopesOnly pins the P3 retention policy (perf-hunt
// #2): a cached Package() result's Info.Scopes must contain ONLY *ast.File
// keys (retainFileScopesOnly, called before m.pkgResults[dir] is populated) —
// exactly the subset internal/lsp/completion_gsx.go's importQualifierCandidates
// needs via fileScopeSet, and the only retained-package reader of Scopes. An
// AnalyzeEphemeral result, never cached and consumed by the Go-completion
// scope walk (innermostScopeAt/innermostScopeAtAuthored), must keep every
// scope go/types recorded (func/block scopes included).
func TestPackageRetainsFileScopesOnly(t *testing.T) {
	m, dir, pagePath := newEphemeralTestModule(t)
	src := []byte("package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name }</div>\n}\n")
	m.SetOverride(pagePath, src)

	res, err := m.Package(dir)
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if res.Info == nil || len(res.Info.Scopes) == 0 {
		t.Fatalf("Package result Info.Scopes empty; want file scopes retained (Info=%v)", res.Info)
	}
	for node := range res.Info.Scopes {
		if _, ok := node.(*goast.File); !ok {
			t.Fatalf("Package result Info.Scopes has a non-file entry %T; want only *ast.File keys after retainFileScopesOnly", node)
		}
	}

	eph, err := m.AnalyzeEphemeral(dir, pagePath, src)
	if err != nil {
		t.Fatalf("AnalyzeEphemeral: %v", err)
	}
	if eph.Info == nil {
		t.Fatal("AnalyzeEphemeral result Info is nil")
	}
	var sawNonFileScope bool
	for node := range eph.Info.Scopes {
		if _, ok := node.(*goast.File); !ok {
			sawNonFileScope = true
			break
		}
	}
	if !sawNonFileScope {
		t.Fatal("AnalyzeEphemeral result Info.Scopes has no func/block scopes; the Go-completion scope walk needs the full chain")
	}
}
