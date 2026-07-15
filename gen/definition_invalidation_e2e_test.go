package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

// TestDefinitionInvalidationCrossPkg verifies that after a dependency package
// (widgets) is changed in-memory, an importer package (home) re-analyzed without
// an explicit re-analysis of widgets sees the updated declaration position.
//
// Non-tautology: the test transitions widgetsV2 into the analyzer but calls
// Analyze(homeDir) WITHOUT first calling Analyze(widgetsDir). This causes the
// warm Module to mark widgetsDir dirty. If applyDirty is
// wired in Package (as it is after Task 4), Package(homeDir) drops
// pkgTypes[widgetsDir] before analyzing home, which forces typesPackageWith to
// re-analyze widgets from V2 → Badge appears at the new (higher) line.
//
// WITHOUT applyDirty wired: pkgTypes[widgetsDir] retains the stale V1
// types.Package from the Phase-1 analysis, so typesPackageWith returns the V1
// Badge object with its old Pos() → same line as Phase 1 → assertion failure.
//
// Test seam note: transitioning widgetsV2 without a separate Analyze(widgetsDir)
// call (as the real server does on a widgets edit) is
// deliberate — it keeps widgets a CACHE-GATED DEPENDENCY (never a direct Package
// target, so never re-analyzed directly), the only shape under which applyDirty's
// drop is the sole refresh path and the test is non-tautological. Editing the dep
// via its own Analyze(widgetsDir) would re-cache it fresh regardless of applyDirty
// (analyze caches unconditionally), making such a test pass vacuously. In
// production the same applyDirty mechanism is triggered instead by the widgets edit
// marking widgetsDir dirty. The reverse-dependency CLOSURE itself (importers drop
// transitively) is covered white-box by TestEditInvalidatesReverseClosureOnly.
func TestDefinitionInvalidationCrossPkg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	mk := func(p, c string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	// V1: Badge component declaration starts on line 3 (1-indexed).
	//   line 1: package widgets
	//   line 2: (blank)
	//   line 3: component Badge(label string) { …
	widgetsV1 := "package widgets\n\ncomponent Badge(label string) {\n\t<span class=\"badge\">{label}</span>\n}\n"
	// V2: two blank lines inserted above the Badge declaration push it to line 5.
	//   line 1: package widgets
	//   line 2: (blank)
	//   line 3: (blank)
	//   line 4: (blank)
	//   line 5: component Badge(label string) { …
	widgetsV2 := "package widgets\n\n\n\ncomponent Badge(label string) {\n\t<span class=\"badge\">{label}</span>\n}\n"

	widgetsFile := filepath.Join(dir, "widgets", "badge.gsx")
	mk("widgets/badge.gsx", widgetsV1)

	homeSrc := "package home\n\nimport \"example.com/x/widgets\"\n\ncomponent Home() {\n\t<widgets.Badge label=\"test\"/>\n}\n"
	homeFile := filepath.Join(dir, "home", "home.gsx")
	homeDir := filepath.Join(dir, "home")
	mk("home/home.gsx", homeSrc)

	a := newLSPAnalyzer(config{}, nil)

	// Phase 1: analyze home with V1 widgets in the override.
	// SetOverride(widgetsFile, V1) is a no-op change (V1 matches disk content),
	// so widgetsDir is NOT marked dirty. Package(homeDir) analyzes home, which
	// calls typesPackageWith(widgetsDir) → analyze(widgets) from V1 → caches
	// pkgTypes[widgetsDir] = V1 types (Badge at line 3).
	if _, err := a.SetOverride(homeFile, []byte(homeSrc)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetOverride(widgetsFile, []byte(widgetsV1)); err != nil {
		t.Fatal(err)
	}
	pkg1, err := a.Analyze(homeDir, nil)
	if err != nil {
		t.Fatalf("phase-1 Analyze: %v", err)
	}
	if pkg1 == nil || pkg1.Info == nil {
		t.Fatalf("phase-1 Analyze returned nil or empty package")
	}

	// Phase 2: transition widgets to V2, then analyze home again.
	// SetOverride(widgetsFile, V2) detects V2 ≠ V1 and marks widgetsDir dirty.
	// Package(homeDir) calls applyDirty, which drops pkgTypes[widgetsDir] (and
	// pkgTypes[homeDir] via the reverse closure). analyze(homeDir) then calls
	// typesPackageWith(widgetsDir) → not cached → analyze(widgets) from V2
	// override → Badge at line 5. If applyDirty is NOT wired, pkgTypes[widgetsDir]
	// = stale V1 → typesPackageWith returns V1 types → Badge stays at line 3.
	if _, err := a.SetOverride(widgetsFile, []byte(widgetsV2)); err != nil {
		t.Fatal(err)
	}
	pkg2, err := a.Analyze(homeDir, nil)
	if err != nil {
		t.Fatalf("phase-2 Analyze: %v", err)
	}
	if pkg2 == nil || pkg2.Info == nil {
		t.Fatalf("phase-2 Analyze returned nil or empty package")
	}

	const wantV1Line = 3 // Badge in V1 (line 3)
	const wantV2Line = 5 // Badge in V2 (two blank lines push it to line 5)

	line1 := badgeDeclLine(t, pkg1, "Badge", "widgets")
	line2 := badgeDeclLine(t, pkg2, "Badge", "widgets")

	if line1 != wantV1Line {
		t.Errorf("phase-1 Badge decl line = %d, want %d (V1)", line1, wantV1Line)
	}
	if line2 != wantV2Line {
		t.Errorf("phase-2 Badge decl line = %d, want %d (V2 after invalidation re-analyzed widgets)\n"+
			"  If this equals %d (V1 line), applyDirty is not wired: home re-used stale pkgTypes[widgetsDir]=V1",
			line2, wantV2Line, wantV1Line)
	}

	// The warm Module must never write .x.go to disk: assert the entire temp
	// module is free of generated files, matching the .x.go-independence contract.
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".x.go") {
			t.Errorf("LSP analysis wrote .x.go to disk (must not): %s", path)
		}
		return nil
	}); err != nil {
		t.Errorf("walk temp module: %v", err)
	}
}

// badgeDeclLine finds the 1-indexed line of the declaration of an exported
// function named funcName from a package whose import path ends with pkgSuffix,
// as recorded in the analyzed home package's Info.Uses map. It uses pkg.Fset
// (the Module-wide shared FileSet) to resolve the declaration position.
func badgeDeclLine(t *testing.T, pkg *lsp.Package, funcName, pkgSuffix string) int {
	t.Helper()
	seen := map[string]bool{}
	for _, obj := range pkg.Info.Uses {
		if obj == nil || obj.Name() != funcName {
			continue
		}
		p := obj.Pkg()
		if p == nil || !strings.HasSuffix(p.Path(), pkgSuffix) {
			continue
		}
		pos := pkg.Fset.Position(obj.Pos())
		if pos.Line <= 0 || seen[pos.Filename] {
			continue
		}
		seen[pos.Filename] = true
		return pos.Line
	}
	t.Fatalf("no Info.Uses entry for function %q from package ending in %q", funcName, pkgSuffix)
	return -1
}
