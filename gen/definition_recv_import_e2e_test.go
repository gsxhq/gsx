package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSigModule writes a temp module (go.mod + a store pkg + a .gsx) and
// returns dir. Shared by the receiver/import navigation tests.
func writeSigModule(t *testing.T, files map[string]string) (dir string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir = t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	files["go.mod"] = "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => " + repoRoot + "\n"
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// gd on a method-component RECEIVER type resolves to that type's declaration.
// (Receiver types are always same-package — Go forbids methods on non-local
// types — so there is no package qualifier to resolve here.)
func TestDefinitionReceiverType(t *testing.T) {
	t.Parallel()
	src := "package x\n\ncomponent (p Page) Render() {\n\t<div>{ p.Title }</div>\n}\n"
	dir := writeSigModule(t, map[string]string{
		"types.go": "package x\n\ntype Page struct{ Title string }\n",
		"comp.gsx": src,
	})
	lines := strings.Split(src, "\n")
	var rl int
	for i, l := range lines {
		if strings.Contains(l, "(p Page)") {
			rl = i
			break
		}
	}
	col := strings.Index(lines[rl], "Page)")
	loc := sigTypeDefAt(t, dir, "comp.gsx", src, rl, col)
	if loc == nil || !strings.HasSuffix(loc.URI, "types.go") {
		t.Fatalf("gd on receiver type `Page` resolved to %v, want types.go", loc)
	}
}

// A POINTER receiver type (`(p *Page)`) navigates to the type too, and a method
// component carrying BOTH a receiver and a parameter resolves each independently
// (the receiver to a same-package type, the param qualifier across packages).
func TestDefinitionReceiverAndParam(t *testing.T) {
	t.Parallel()
	src := "package blog\n\nimport \"example.com/x/store\"\n\ncomponent (p *Page) Show(c store.Comment) {\n\t<div>{ c.Body }</div>\n}\n"
	dir := writeSigModule(t, map[string]string{
		"store/store.go": "package store\n\ntype Comment struct{ Body string }\n",
		"blog/types.go":  "package blog\n\ntype Page struct{ Title string }\n",
		"blog/comp.gsx":  src,
	})
	lines := strings.Split(src, "\n")
	var sl int
	for i, l := range lines {
		if strings.Contains(l, "*Page") {
			sl = i
			break
		}
	}
	// Pointer receiver type `Page` → blog/types.go.
	loc := sigTypeDefAt(t, dir, "blog/comp.gsx", src, sl, strings.Index(lines[sl], "Page)"))
	if loc == nil || !strings.HasSuffix(loc.URI, "types.go") {
		t.Fatalf("gd on `*Page` receiver resolved to %v, want types.go", loc)
	}
	// Param type `Comment` → store/store.go.
	loc = sigTypeDefAt(t, dir, "blog/comp.gsx", src, sl, strings.Index(lines[sl], "Comment)"))
	if loc == nil || !strings.HasSuffix(loc.URI, filepath.Join("store", "store.go")) {
		t.Fatalf("gd on `store.Comment` param resolved to %v, want store/store.go", loc)
	}
}

// Hovering a method-component RECEIVER type shows the resolved type.
func TestHoverReceiverType(t *testing.T) {
	t.Parallel()
	src := "package x\n\ncomponent (p Page) Render() {\n\t<div>{ p.Title }</div>\n}\n"
	dir := writeSigModule(t, map[string]string{
		"types.go": "package x\n\ntype Page struct{ Title string }\n",
		"comp.gsx": src,
	})
	h := hoverAt(t, dir, "comp.gsx", src, "Page)", 0) // on the receiver type `Page`
	if h == nil || !strings.Contains(h.Contents.Value, "Page") || !strings.Contains(h.Contents.Value, "struct") {
		t.Fatalf("hover on receiver type, want the Page struct, got %+v", h)
	}
}

// gd on the package qualifier of a .gsx IMPORT statement gives the same package
// file-picker (a list of the package's `package` clauses) as gd on the qualifier
// of a parameter type.
func TestDefinitionImportStatement(t *testing.T) {
	t.Parallel()
	src := "package blog\n\nimport \"example.com/x/store\"\n\ncomponent C(c store.Comment) {\n\t<ul></ul>\n}\n"
	dir := writeSigModule(t, map[string]string{
		"store/store.go": "package store\n\ntype Comment struct{ Body string }\n",
		"blog/comp.gsx":  src,
	})
	lines := strings.Split(src, "\n")
	var il int
	for i, l := range lines {
		if strings.Contains(l, "import ") {
			il = i
			break
		}
	}
	// Cursor on the last path segment `store` inside the import string literal.
	col := strings.LastIndex(lines[il], "store")
	locs := sigTypeDefLocations(t, dir, "blog/comp.gsx", src, il, col)
	if len(locs) == 0 {
		t.Fatalf("gd on import path returned no locations")
	}
	found := false
	for _, l := range locs {
		if strings.HasSuffix(l.URI, filepath.Join("store", "store.go")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("gd on import path resolved to %v, want store/store.go package clause", locs)
	}
}

// gd on the ALIAS of an aliased .gsx import (`st "…/store"`) also opens the
// package picker.
func TestDefinitionImportAlias(t *testing.T) {
	t.Parallel()
	src := "package blog\n\nimport st \"example.com/x/store\"\n\ncomponent C(c st.Comment) {\n\t<ul></ul>\n}\n"
	dir := writeSigModule(t, map[string]string{
		"store/store.go": "package store\n\ntype Comment struct{ Body string }\n",
		"blog/comp.gsx":  src,
	})
	lines := strings.Split(src, "\n")
	var il int
	for i, l := range lines {
		if strings.Contains(l, "import st") {
			il = i
			break
		}
	}
	// Cursor on the alias `st`.
	locs := sigTypeDefLocations(t, dir, "blog/comp.gsx", src, il, strings.Index(lines[il], "st "))
	found := false
	for _, l := range locs {
		if strings.HasSuffix(l.URI, filepath.Join("store", "store.go")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("gd on import alias resolved to %v, want store/store.go", locs)
	}
}
