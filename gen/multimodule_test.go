package gen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// writeModule writes a go.mod (replace → repo root) into dir, making it an
// independent module rooted there.
func writeModule(t *testing.T, dir, mod string) {
	t.Helper()
	writeFile(t, dir, "go.mod", "module "+mod+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
}

// TestGenerateMultiModule proves that a single Generate spanning a parent dir
// that itself is NOT a module but contains two independent sub-modules resolves
// each sub-module against its OWN module root. Before the multi-module grouping
// fix, every interpolation in both modules failed to type-check because all dirs
// were loaded against the first module's root.
func TestGenerateMultiModule(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	parent := t.TempDir() // NOT a module: no go.mod here.

	aDir := filepath.Join(parent, "alpha")
	writeModule(t, aDir, "alphamod")
	writeFile(t, aDir, "hi.gsx", "package alpha\n\ncomponent Hi(name string) {\n\t<p>{name}</p>\n}\n")

	bDir := filepath.Join(parent, "beta")
	writeModule(t, bDir, "betamod")
	writeFile(t, bDir, "bye.gsx", "package beta\n\ncomponent Bye(name string) {\n\t<p>bye {name}</p>\n}\n")

	res, err := Generate([]string{parent})
	if err != nil {
		t.Fatalf("Generate: %v\ndiags: %v", err, res.Diags)
	}
	if len(res.Diags) != 0 {
		t.Fatalf("expected no diagnostics, got: %v", res.Diags)
	}
	for _, p := range []string{
		filepath.Join(aDir, "hi.x.go"),
		filepath.Join(bDir, "bye.x.go"),
	} {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Fatalf("expected %s on disk: %v", p, statErr)
		}
	}
	if len(res.Written) != 2 {
		t.Fatalf("expected 2 written, got %d: %v", len(res.Written), res.Written)
	}
	goBuild(t, aDir)
	goBuild(t, bDir)
}

// TestWatchSessionMultiModule proves that a --watch session spanning two
// independent sub-modules warm-regenerates each against its OWN module resolver.
// Before the fix the session built a single resolver rooted at the first module,
// so regenerating a dir in the second module resolved nothing.
func TestWatchSessionMultiModule(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	parent := t.TempDir()

	// alpha sorts first, so a single-resolver watch session anchors at alpha's
	// module. beta is a SECOND module containing a cross-package gsx ref:
	// beta/views imports beta/comp. The warm resolver must be rooted at beta, or
	// beta/comp is "not loaded" in alpha's importer and regenerating beta/views
	// fails — the multi-module watch bug.
	aDir := filepath.Join(parent, "alpha")
	writeModule(t, aDir, "alphamod")
	writeFile(t, aDir, "hi.gsx", "package alpha\n\ncomponent Hi(name string) {\n\t<p>{name}</p>\n}\n")

	bRoot := filepath.Join(parent, "beta")
	writeModule(t, bRoot, "betamod")
	compDir := filepath.Join(bRoot, "comp")
	viewsDir := filepath.Join(bRoot, "views")
	writeFile(t, compDir, "card.gsx", "package comp\n\ncomponent Card(title string) {\n\t<div class=\"card\">{title}</div>\n}\n")
	writeFile(t, viewsDir, "page.gsx", "package views\n\nimport \"betamod/comp\"\n\ncomponent Page() {\n\t<comp.Card title=\"hi\"/>\n}\n")

	cfg := watchConfig{paths: []string{parent}, cls: attrclass.Builtin()}
	sess, _, err := startWatchSessionForTest(cfg)
	if err != nil {
		t.Fatalf("startWatchSessionForTest: %v", err)
	}

	// Warm-regen the cross-package consumer in the SECOND module; its Module
	// must be anchored at beta so the sibling package resolves.
	r := sess.regenDir(viewsDir)
	if !r.OK {
		t.Fatalf("regenDir(beta/views) not OK: err=%v diags=%v", r.Err, r.Diags)
	}
}
