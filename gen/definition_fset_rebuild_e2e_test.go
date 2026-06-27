package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefinitionSurvivesFsetRebuild proves that the full LSP→Module→rebuild
// path resolves cross-package go-to-def positions correctly after one or more
// FileSet rebuilds. With GSX_FSET_REBUILD_BYTES=1, growth exceeds the threshold
// after the very first Analyze call, so every subsequent Analyze call triggers a
// rebuildFset. The test asserts that widgets.Badge still resolves to the correct
// file and line after multiple rebuild cycles.
//
// Failure mode: if rebuildFset orphaned positions — e.g. it cleared ext but kept
// pkgTypes (or vice versa), leaving live types.Package objects with Pos() values
// in the discarded fset — then Fset.Position would return an invalid position
// (line 0 or wrong file). badgeDeclLine would then call t.Fatal because no valid
// Info.Uses entry maps to a positive line. The test would fail before the
// wantBadgeLine assertion even fires.
//
// Note: the Task-3 codegen-level tests (TestCrossPkgResolutionSurvivesRebuild,
// TestGraphSurvivesRebuild, TestRebuildFsetPreservesGraph) assert rebuilds()>0
// directly via the Module counter. This e2e does not have Module access — it goes
// through the lspAnalyzer façade — so it relies on GSX_FSET_REBUILD_BYTES=1
// making rebuilds virtually certain after the first Analyze, and confirms that the
// full LSP path continues to serve correct positions regardless.
func TestDefinitionSurvivesFsetRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	// Set a tiny threshold so the warm Module triggers a FileSet rebuild on almost
	// every Analyze call after the first. Open reads this env at construction;
	// module() calls Open lazily on the first Analyze, so t.Setenv here covers it.
	t.Setenv("GSX_FSET_REBUILD_BYTES", "1")

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

	// widgets/badge.gsx: Badge declaration is on line 3 (1-indexed).
	//   line 1: package widgets
	//   line 2: (blank)
	//   line 3: component Badge(label string) { …
	const widgetsSrc = "package widgets\n\ncomponent Badge(label string) {\n\t<span class=\"badge\">{label}</span>\n}\n"
	const wantBadgeLine = 3

	widgetsFile := filepath.Join(dir, "widgets", "badge.gsx")
	mk("widgets/badge.gsx", widgetsSrc)

	const homeSrc = "package home\n\nimport \"example.com/x/widgets\"\n\ncomponent Home() {\n\t<widgets.Badge label=\"test\"/>\n}\n"
	homeFile := filepath.Join(dir, "home", "home.gsx")
	homeDir := filepath.Join(dir, "home")
	mk("home/home.gsx", homeSrc)

	a := newLSPAnalyzer(config{}, nil)
	overrides := map[string][]byte{
		homeFile:    []byte(homeSrc),
		widgetsFile: []byte(widgetsSrc),
	}

	// Phase 1: warm both packages. The Module is lazily created here (module() →
	// Open reads GSX_FSET_REBUILD_BYTES=1). The first Analyze has no prior fset
	// growth to measure against fsetBaseline, so no rebuild fires yet.
	pkg1, err := a.Analyze(homeDir, overrides)
	if err != nil {
		t.Fatalf("phase-1 Analyze: %v", err)
	}
	if pkg1 == nil || pkg1.Info == nil {
		t.Fatalf("phase-1: nil package")
	}
	line1 := badgeDeclLine(t, pkg1, "Badge", "widgets")
	if line1 != wantBadgeLine {
		t.Errorf("phase-1: Badge decl line = %d, want %d", line1, wantBadgeLine)
	}

	// Phases 2–5: with threshold=1, the fset grows beyond 1 byte during any real
	// analysis, so every Analyze cycle from here on triggers a FileSet rebuild.
	// Each cycle must still resolve Badge to the same file and line — if rebuildFset
	// orphaned positions the resolution would return line 0 or the wrong file.
	for cycle := 2; cycle <= 5; cycle++ {
		pkg, err := a.Analyze(homeDir, overrides)
		if err != nil {
			t.Fatalf("cycle %d Analyze: %v", cycle, err)
		}
		if pkg == nil || pkg.Info == nil {
			t.Fatalf("cycle %d: nil package", cycle)
		}
		line := badgeDeclLine(t, pkg, "Badge", "widgets")
		if line != wantBadgeLine {
			t.Errorf("cycle %d: Badge decl line = %d, want %d\n"+
				"  If this is 0 or points to the wrong file, rebuildFset orphaned\n"+
				"  positions: ext and pkgTypes were not reset atomically together.",
				cycle, line, wantBadgeLine)
		}
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
