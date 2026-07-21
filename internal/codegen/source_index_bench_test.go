package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

var (
	sourceIndexBenchmarkIndex      *sourceintel.Index
	sourceIndexBenchmarkOccurrence sourceintel.Occurrence
)

func BenchmarkModuleAnalyzeSourceIndex(b *testing.B) {
	root := b.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	writeSourceIndexBenchmarkFile(b, root, "go.mod", "module example.com/sourcebench\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pageDir := filepath.Join(root, "page")
	path := filepath.Join(pageDir, "page.gsx")
	variants := [2][]byte{
		[]byte("package page\n\ntype Label string\nfunc helper(v Label) Label { return v }\ncomponent Page(label Label) { <p>{helper(label)}</p> }\n"),
		[]byte("package page\n\ntype Label string\nfunc helper(v Label) Label { return v }\ncomponent Page(label Label) { <section>{helper(label)}</section> }\n"),
	}
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path, variants[0], 0o644); err != nil {
		b.Fatal(err)
	}
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/sourcebench", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		b.Fatal(err)
	}
	prime, err := module.Package(pageDir)
	if err != nil || prime.SourceIndex == nil {
		b.Fatalf("prime Package = (%v, %v)", prime, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		b.StopTimer()
		module.SetOverride(path, variants[(i+1)%2])
		b.StartTimer()
		result, err := module.Package(pageDir)
		if err != nil {
			b.Fatal(err)
		}
		if result.SourceIndex == nil {
			b.Fatal("Package produced no source index")
		}
		sourceIndexBenchmarkIndex = result.SourceIndex
	}
}

func BenchmarkSourceIndexLookup(b *testing.B) {
	root := b.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	writeSourceIndexBenchmarkFile(b, root, "go.mod", "module example.com/lookupbench\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pageDir := filepath.Join(root, "page")
	const source = "package page\n\ntype Label string\nfunc helper(v Label) Label { return v }\ncomponent Page(label Label) { <p>{helper(label)}</p> }\n"
	writeSourceIndexBenchmarkFile(b, pageDir, "page.gsx", source)
	path := filepath.Join(pageDir, "page.gsx")
	module, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/lookupbench", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		b.Fatal(err)
	}
	result, err := module.Package(pageDir)
	if err != nil || result.SourceIndex == nil {
		b.Fatalf("Package = (%v, %v)", result, err)
	}
	offset := strings.LastIndex(source, "label)")
	if occurrence, ok := result.SourceIndex.At(path, offset); !ok || occurrence.Object == nil {
		b.Fatalf("lookup fixture has no semantic occurrence: %+v, %t", occurrence, ok)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		occurrence, ok := result.SourceIndex.At(path, offset)
		if !ok {
			b.Fatal("source index lookup became empty")
		}
		sourceIndexBenchmarkOccurrence = occurrence
	}
}

func writeSourceIndexBenchmarkFile(b *testing.B, dir, name, contents string) {
	b.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		b.Fatal(err)
	}
}
