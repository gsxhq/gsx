package lsp

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

const sourceIndexBenchmarkSource = `package page

type Label string

func helper(value Label) Label {
	local := value
	return local
}

component Card(value Label) {
	<strong>{helper(value)}</strong>
}
`

var (
	semanticDefinitionBenchmarkTarget semanticDefinitionTarget
	semanticHoverBenchmarkResult      Hover
	documentSymbolsBenchmarkResult    []Symbol
)

func BenchmarkSemanticDefinition(b *testing.B) {
	pkg, path := benchmarkSourceIndexPackage(b)
	offset := strings.Index(sourceIndexBenchmarkSource, "return local") + len("return ")
	if _, ok := semanticDefinition(pkg, path, []byte(sourceIndexBenchmarkSource), offset); !ok {
		b.Fatal("fixture has no semantic definition")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		target, ok := semanticDefinition(pkg, path, []byte(sourceIndexBenchmarkSource), offset)
		if !ok {
			b.Fatal("semantic definition became empty")
		}
		semanticDefinitionBenchmarkTarget = target
	}
}

func BenchmarkSemanticHover(b *testing.B) {
	pkg, path := benchmarkSourceIndexPackage(b)
	offset := strings.Index(sourceIndexBenchmarkSource, "helper(value)")
	if _, ok := semanticHover(pkg, path, []byte(sourceIndexBenchmarkSource), offset); !ok {
		b.Fatal("fixture has no semantic hover")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		hover, ok := semanticHover(pkg, path, []byte(sourceIndexBenchmarkSource), offset)
		if !ok {
			b.Fatal("semantic hover became empty")
		}
		semanticHoverBenchmarkResult = hover
	}
}

func BenchmarkDocumentSymbols(b *testing.B) {
	pkg, path := benchmarkSourceIndexPackage(b)
	file := pkg.Files[path]
	if file == nil {
		b.Fatal("fixture has no parsed file")
	}
	source := []byte(sourceIndexBenchmarkSource)
	if got := FileSymbols(path, source, file, pkg.GSXFset, pkg.SourceIndex); len(got) == 0 {
		b.Fatal("fixture has no document symbols")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		documentSymbolsBenchmarkResult = FileSymbols(path, source, file, pkg.GSXFset, pkg.SourceIndex)
	}
}

func BenchmarkCachedWorkspaceSymbols(b *testing.B) {
	module := b.TempDir()
	path := filepath.Join(module, "page.gsx")
	const source = "package page\n\nvar Target = 1\n"
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		b.Fatal(err)
	}
	analyzer := &wsSymAnalyzer{syms: []Symbol{{
		Name: "Target", Kind: symKindVariable, Container: "page",
		NamePos: authoredTokenPosition(path, source, strings.Index(source, "Target")),
	}}}
	server := NewServer(nil, io.Discard, analyzer)
	server.workspaceViewValid = true
	server.workspaceRoots = []string{module}
	server.workspaceModules = []string{module}
	server.workspaceModuleOwners[module] = module
	server.workspaceModulePaths[module] = "example.com/workspace"
	request := frame{ID: json.RawMessage("1"), Params: json.RawMessage(`{"query":"Target"}`)}
	if err := server.handleWorkspaceSymbol(request); err != nil {
		b.Fatal(err)
	}
	if analyzer.calls != 1 {
		b.Fatalf("prime ModuleSymbols calls = %d, want 1", analyzer.calls)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := server.handleWorkspaceSymbol(request); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if analyzer.calls != 1 {
		b.Fatalf("cached workspace benchmark reloaded inventory %d times", analyzer.calls)
	}
}

func benchmarkSourceIndexPackage(b *testing.B) (*Package, string) {
	b.Helper()
	root := b.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/lspbench\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	pageDir := filepath.Join(root, "page")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(pageDir, "page.gsx")
	if err := os.WriteFile(path, []byte(sourceIndexBenchmarkSource), 0o644); err != nil {
		b.Fatal(err)
	}
	module, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/lspbench", FilterPkgs: []string{codegen.StdImportPath}})
	if err != nil {
		b.Fatal(err)
	}
	result, err := module.Package(pageDir)
	if err != nil {
		b.Fatal(err)
	}
	if result.SourceIndex == nil {
		b.Fatal("Package produced no source index")
	}
	return &Package{
		GSXFset:     result.GSXFset,
		Fset:        result.Fset,
		Info:        result.Info,
		SourceIndex: result.SourceIndex,
		Types:       result.Types,
		Files:       result.GSXFiles,
	}, path
}
