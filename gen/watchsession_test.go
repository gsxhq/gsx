package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A package whose .gsx references a symbol defined in a sibling hand-written
// .go file (the structpages/blog pattern: `commentHandler`/`resultsCount` live
// in comment.go, used from post.gsx/search.gsx). The warm resolver must include
// the package's own .go files in its type-check, or the symbol reads as
// "undefined" on every warm regenerate even though the cold generate resolved it.
func TestNewModuleResolver_SiblingGoSymbol(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	// helper() lives in a hand-written .go file, NOT a .gsx.
	write("blog/helper.go", "package blog\n\nfunc helper(n int) string { return \"r\" }\n")
	write("blog/page.gsx", "package blog\n\ncomponent Page(items []int) {\n\t<p>{helper(len(items))}</p>\n}\n")

	blogDir := filepath.Join(root, "blog")
	// Initial cold generate (creates blog/.x.go); the cold path includes helper.go.
	if _, err := generateCached([]string{blogDir}, nil, nil, nil, nil, false, nil, nil, true, true); err != nil {
		t.Fatalf("initial cold generate: %v", err)
	}

	r, err := newModuleResolver(root, nil, nil)
	if err != nil {
		t.Fatalf("newModuleResolver: %v", err)
	}
	res, err := r.Generate(blogDir, nil)
	if err != nil {
		t.Fatalf("warm Generate must resolve the sibling-.go symbol, got: %v (diags=%v)", err, res.Diags)
	}
	for _, d := range res.Diags {
		if strings.Contains(d.Message, "helper") {
			t.Fatalf("warm regen reported a false undefined for a sibling-.go symbol: %s", d.Message)
		}
	}
}

// A two-package module: pkg `comp` defines a component; pkg `views` references
// it. The whole-module resolver must let `views` regenerate with the cross-
// package ref resolved (no "cached importer: not loaded" error).
func TestNewModuleResolver_CrossPackage(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Use the current go toolchain version so packages.Load does not refuse the module.
	// No go mod tidy: with a local replace directive the require+replace is sufficient
	// for packages.Load; tidy would strip "require github.com/gsxhq/gsx" because
	// there are no .go files importing it before the cold generate runs.
	write("go.mod", "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
	write("comp/card.gsx", "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card\">{title}</div>\n}\n")
	write("views/page.gsx", "package views\n\nimport \"example.com/m/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\"hi\"/>\n}\n")

	// Initial cold generate so comp's .x.go exists for the resolver to load.
	if _, err := generateCached([]string{filepath.Join(root, "comp"), filepath.Join(root, "views")}, nil, nil, nil, nil, false, nil, nil, true, true); err != nil {
		t.Fatalf("initial generate: %v", err)
	}

	r, err := newModuleResolver(root, nil, nil)
	if err != nil {
		t.Fatalf("newModuleResolver: %v", err)
	}
	res, err := r.Generate(filepath.Join(root, "views"), nil)
	if err != nil {
		t.Fatalf("warm Generate(views): %v (diags=%v)", err, res.Diags)
	}
	// The regenerated views/.x.go must call comp.Card.
	var got string
	for path, b := range res.Files {
		if strings.HasSuffix(path, "page.x.go") {
			got = string(b)
		}
	}
	if !strings.Contains(got, "comp.Card") {
		t.Fatalf("regenerated page.x.go does not reference comp.Card:\n%s", got)
	}
}

func writeFileT(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeMod(t *testing.T, root string) {
	t.Helper()
	writeFileT(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+gsxModuleDir(t)+"\n")
}

// TestWatchSession_WarmRegen proves that a pure .gsx edit regenerates via the
// warm resolver and updates the .x.go on disk.
func TestWatchSession_WarmRegen(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>one</h1>\n}\n")

	s, _, err := newWatchSession(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
	}
	// Edit the source, then warm-regen.
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>two</h1>\n}\n")
	r := s.regen(filepath.Join(root, "views"))
	if !r.OK {
		t.Fatalf("regen not OK: err=%v diags=%v", r.Err, r.Diags)
	}
	xgo, _ := os.ReadFile(filepath.Join(root, "views", "page.x.go"))
	// Coalesced static writes emit `S("<h1>two</h1>")`, so assert on the content.
	if !strings.Contains(string(xgo), `two</h1>`) {
		t.Fatalf("page.x.go not updated to \"two\":\n%s", xgo)
	}
}

// TestWatchSession_RegenError proves that a broken .gsx yields OK=false with
// diagnostics.
func TestWatchSession_RegenError(t *testing.T) {
	root := t.TempDir()
	writeMod(t, root)
	gsxPath := filepath.Join(root, "views", "page.gsx")
	writeFileT(t, gsxPath, "package views\n\ncomponent Page() {\n\t<h1>{undefinedSym}</h1>\n}\n")

	s, _, err := newWatchSession(watchConfig{paths: []string{filepath.Join(root, "views")}})
	if err != nil {
		t.Fatalf("newWatchSession: %v", err)
	}
	r := s.regen(filepath.Join(root, "views"))
	if r.OK || len(r.Diags) == 0 {
		t.Fatalf("expected OK=false with diagnostics, got OK=%v diags=%v", r.OK, r.Diags)
	}
}

// gsxModuleDir returns the absolute path of this gsx module checkout, for the
// test fixture's replace directive.
func gsxModuleDir(t *testing.T) string {
	t.Helper()
	// gen/ is one level under the module root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}
