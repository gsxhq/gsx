package codegen

import (
	"os"
	"path/filepath"
	"testing"
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
	repoRoot, _ := filepath.Abs("..")
	repoRoot = filepath.Dir(repoRoot) // internal/codegen -> repo root
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	must("page/other.gsx", "package page\n\ncomponent Other() {\n\t<div>ok</div>\n}\n")

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
