package gen

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

var coldWorkspaceSymbolsBenchmarkResult []lsp.Symbol

func BenchmarkColdModuleWorkspaceSymbols(b *testing.B) {
	root := b.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		b.Fatal(err)
	}
	write := func(relative, source string) {
		b.Helper()
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	write("go.mod", "module example.com/workspacebench\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	for index := range 3 {
		write(fmt.Sprintf("pkg%d/page.gsx", index), fmt.Sprintf("package pkg%d\n\ntype Model struct{}\nfunc helper() int { return %d }\ncomponent Page() { <p>{helper()}</p> }\n", index, index))
	}
	prime, err := newLSPAnalyzer(config{}, io.Discard).ModuleSymbols(root, nil)
	if err != nil || len(prime) == 0 {
		b.Fatalf("prime ModuleSymbols = (%d symbols, %v)", len(prime), err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		analyzer := newLSPAnalyzer(config{}, io.Discard)
		symbols, err := analyzer.ModuleSymbols(root, nil)
		if err != nil {
			b.Fatal(err)
		}
		if len(symbols) == 0 {
			b.Fatal("cold ModuleSymbols produced no symbols")
		}
		coldWorkspaceSymbolsBenchmarkResult = symbols
	}
}
