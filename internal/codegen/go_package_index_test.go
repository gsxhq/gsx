package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

const reverseDepMainSource = "package main\n\nimport (\n\t\"example.com/rd/app\"\n\t\"example.com/rd/components\"\n\t\"example.com/rd/util\"\n)\n\nfunc main() {\n\tvar h app.Home\n\t_ = h.Page\n\t_ = components.Input\n\tvar s components.Size\n\t_ = s\n\tutil.U()\n}\n"

const reverseDepInputSource = "package components\n\ntype Size int\n\ncomponent Input(name string, size Size) {\n\t<input name={ name }/>\n}\n"

// writeReverseDepModule: components/ (gsx) ← app/ (gsx, imports components)
// ← cmd/ (Go-only main, imports both, plus util) ; util/ (Go-only leaf that
// imports nothing, so it is a candidate the reachability walk must reject —
// and the sibling that must survive an unrelated import failure in cmd's walk).
func writeReverseDepModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "go.mod", "module example.com/rd\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, root, "components/input.gsx", reverseDepInputSource)
	writeFile(t, root, "app/page.gsx", "package app\n\nimport \"example.com/rd/components\"\n\ntype Home struct{}\n\ncomponent (h Home) Page() {\n\t<main><components.Input name=\"a\" size={ components.Size(1) }/></main>\n}\n")
	writeFile(t, root, "cmd/main.go", reverseDepMainSource)
	writeFile(t, root, "util/util.go", "package util\n\nfunc U() {}\n")
	return root
}

func TestReverseDependencyGoPackageIndex(t *testing.T) {
	root := writeReverseDepModule(t)
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/rd", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Package(filepath.Join(root, "app")); err != nil { // warms the inventory
		t.Fatal(err)
	}
	utilDir := filepath.Join(root, "util")
	m.mu.Lock()
	_, utilIsCandidate := m.sourcePackages[utilDir]
	m.mu.Unlock()
	if !utilIsCandidate {
		t.Fatal("util is not in the source inventory, so excluding it below proves nothing")
	}
	dirs, err := m.reverseDependencyGoDirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "cmd" {
		t.Fatalf("reverseDependencyGoDirs = %v, want [cmd] (util is Go-only but reaches no gsx package)", dirs)
	}
	index, pkg, err := m.GoPackageIndex(dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(root, "cmd", "main.go")
	src := reverseDepMainSource
	occ, ok := index.At(mainPath, strings.Index(src, "app.Home")+4)
	if !ok || occ.Object == nil || occ.Object.Name() != "Home" {
		t.Fatalf("main.go Home occurrence: %+v %v", occ, ok)
	}
	k := sourceintel.NewKeyer(pkg)
	key, _ := k.Key(occ.Object)
	if string(key) != "example.com/rd/app Home" {
		t.Fatalf("Home key from main.go = %q", key)
	}
	// second call is cached (no re-check)
	before := m.goPackageAnalysisCount()
	if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
		t.Fatal(err)
	}
	if m.goPackageAnalysisCount() != before {
		t.Fatal("GoPackageIndex must reuse the cached analysis")
	}
	// editing app/page.gsx invalidates cmd's analysis (reverse closure)
	m.Invalidate(filepath.Join(root, "app"))
	if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
		t.Fatal(err)
	}
	if m.goPackageAnalysisCount() != before+1 {
		t.Fatalf("expected re-analysis after dependency invalidation, count %d → %d", before, m.goPackageAnalysisCount())
	}

	t.Run("module graph", func(t *testing.T) {
		g, err := m.SymbolGraph([]string{filepath.Join(root, "components"), filepath.Join(root, "app")})
		if err != nil {
			t.Fatal(err)
		}
		inputGSX := filepath.Join(root, "components", "input.gsx")
		inputSrc := reverseDepInputSource
		key, _, ok := g.At(inputGSX, strings.Index(inputSrc, "Input"))
		if !ok || string(key) != "example.com/rd/components Input" {
			t.Fatalf("Input key = %q %v", key, ok)
		}
		byFile := map[string]int{}
		for _, s := range g.References(key) {
			byFile[filepath.Base(s.Path)]++
		}
		if byFile["page.gsx"] != 1 || byFile["main.go"] != 1 {
			t.Fatalf("Input refs = %v; want page.gsx tag + main.go value", byFile)
		}
		// main.go cursor → definitions in .gsx
		mainKey, _, ok := g.At(mainPath, strings.Index(src, "components.Size")+11)
		if !ok {
			t.Fatal("main.go Size cursor missed")
		}
		defs := g.Definitions(mainKey)
		if len(defs) != 1 || filepath.Base(defs[0].Path) != "input.gsx" {
			t.Fatalf("Size defs = %+v", defs)
		}
		// Home type: def in page.gsx, ref in main.go
		homeKey, _, _ := g.At(mainPath, strings.Index(src, "app.Home")+4)
		if d := g.Definitions(homeKey); len(d) != 1 || filepath.Base(d[0].Path) != "page.gsx" {
			t.Fatalf("Home defs = %+v", d)
		}
	})

	t.Run("gsx dir rejected", func(t *testing.T) {
		// A gsx dir has no companion-only source surface: reconstructing it here
		// would admit its own generated .x.go as authoritative syntax.
		// The message matters: without the invariant this fixture would still fail,
		// but only by accident (app/ has no .x.go on disk yet, so its companion set
		// is empty). A generated project would hand its own output to the checker.
		_, _, err := m.GoPackageIndex(filepath.Join(root, "app"))
		if err == nil || !strings.Contains(err.Error(), "is a gsx package") {
			t.Fatalf("GoPackageIndex on a gsx dir = %v, want the gsx-package invariant", err)
		}
	})

	t.Run("go edit invalidates its own analysis", func(t *testing.T) {
		before := m.goPackageAnalysisCount()
		m.mu.Lock()
		previous := m.goPkgAnalyses[dirs[0]]
		m.mu.Unlock()
		if previous == nil {
			t.Fatal("cmd has no cached analysis to invalidate")
		}
		m.SetOverride(mainPath, []byte(src+"\nfunc unused() {}\n"))
		if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		current := m.goPkgAnalyses[dirs[0]]
		m.mu.Unlock()
		// A .go override also reloads the cold source inventory, which resets every
		// analysis, so the counter can advance by more than one; what this pins is
		// that cmd's OWN analysis was redone rather than served from the cache.
		if current == nil || current == previous {
			t.Fatal("a .go edit in cmd/ reused the stale analysis")
		}
		if m.goPackageAnalysisCount() <= before {
			t.Fatalf("no re-analysis after a .go edit, count %d → %d", before, m.goPackageAnalysisCount())
		}
		if _, err := m.ClearOverride(mainPath); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unrelated import failure does not poison a sibling", func(t *testing.T) {
		// cmd's walk imports app first (which fails through the broken components)
		// and util last. The importer latches only the FIRST source error, so util
		// — which imports nothing — must not inherit it.
		m.Invalidate(utilDir) // drop util's analysis so this walk really checks it
		m.SetOverride(filepath.Join(root, "components", "input.gsx"), []byte("package components\n\ncomponent Input( {\n"))
		if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		utilAnalysis := m.goPkgAnalyses[utilDir]
		m.mu.Unlock()
		if utilAnalysis == nil {
			t.Fatal("util was not analyzed during cmd's walk")
		}
		if utilAnalysis.sourceErr != nil {
			t.Fatalf("util inherited an unrelated package's import failure: %v", utilAnalysis.sourceErr)
		}
		if utilAnalysis.info != nil || utilAnalysis.checkedWithInfo {
			t.Fatal("the importer path retained a types.Info nothing consumes")
		}
		if _, err := m.typesPackage(utilDir); err != nil {
			t.Fatalf("util is not importable after an unrelated failure: %v", err)
		}
		// …and the real failure clears once its source is repaired.
		if _, err := m.ClearOverride(filepath.Join(root, "components", "input.gsx")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := m.GoPackageIndex(dirs[0]); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		cmdAnalysis := m.goPkgAnalyses[dirs[0]]
		m.mu.Unlock()
		if cmdAnalysis == nil || cmdAnalysis.sourceErr != nil || len(cmdAnalysis.typeErrs) != 0 {
			t.Fatalf("cmd did not recover: %+v", cmdAnalysis)
		}
		// The graph path checked WITH info; the maps themselves are released as
		// soon as the index that consumes them exists, so what must survive is the
		// record that this analysis is graph-ready (info != nil || index != nil).
		if !cmdAnalysis.checkedWithInfo {
			t.Fatal("the symbol-graph path did not check with a types.Info")
		}
		if cmdAnalysis.info != nil && cmdAnalysis.index != nil {
			t.Fatal("the types.Info was retained after its index was built")
		}
		if cmdAnalysis.info == nil && cmdAnalysis.index == nil {
			t.Fatal("a graph-ready analysis has neither a types.Info nor an index")
		}
	})

	t.Run("info-less analysis is rechecked for the graph", func(t *testing.T) {
		// util was last checked through the importer path, so its cached analysis
		// has no Info; asking for its index must re-check it rather than index nil.
		m.mu.Lock()
		cached := m.goPkgAnalyses[utilDir]
		m.mu.Unlock()
		if cached == nil || cached.checkedWithInfo {
			t.Fatalf("precondition: util should hold an info-less analysis, got %+v", cached)
		}
		before := m.goPackageAnalysisCount()
		index, pkg, err := m.GoPackageIndex(utilDir)
		if err != nil {
			t.Fatal(err)
		}
		if m.goPackageAnalysisCount() != before+1 {
			t.Fatalf("info-less analysis was reused, count %d → %d", before, m.goPackageAnalysisCount())
		}
		utilSrc := "package util\n\nfunc U() {}\n"
		occurrence, ok := index.At(filepath.Join(utilDir, "util.go"), strings.Index(utilSrc, "U()"))
		if !ok || occurrence.Object == nil || occurrence.Object.Name() != "U" {
			t.Fatalf("util.U occurrence: %+v %v", occurrence, ok)
		}
		key, _ := sourceintel.NewKeyer(pkg).Key(occurrence.Object)
		if string(key) != "example.com/rd/util U" {
			t.Fatalf("U key = %q", key)
		}
		// …and the analysis whose info was released once the index existed is a
		// cache HIT, not a re-check: a dropped types.Info must not read as "never
		// had one".
		after := m.goPackageAnalysisCount()
		again, _, err := m.GoPackageIndex(utilDir)
		if err != nil {
			t.Fatal(err)
		}
		if m.goPackageAnalysisCount() != after || again != index {
			t.Fatalf("index-backed analysis was re-checked, count %d → %d", after, m.goPackageAnalysisCount())
		}
	})
}
