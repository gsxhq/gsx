package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeModuleCrossPkg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip()
	}
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const inputSrc = "package components\n\ncomponent Input(name string) {\n\t<input name={ name }/>\n}\n"
	const useSrc = "package x\n\nimport \"example.com/x/components\"\n\nfunc use() { _ = components.Input }\n"
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("components/input.gsx", inputSrc)
	must("post.gsx", "package x\n\nimport \"example.com/x/components\"\n\ncomponent Post() {\n\t<main><components.Input name=\"a\"/></main>\n}\n")
	must("use.go", useSrc)

	componentsDir := filepath.Join(root, "components")
	g, err := newLSPAnalyzer(config{}, nil).AnalyzeModule(componentsDir, nil)
	if err != nil {
		t.Fatal(err)
	}

	inputPath := filepath.Join(root, "components", "input.gsx")
	key, _, ok := g.At(inputPath, strings.Index(inputSrc, "Input"))
	if !ok {
		t.Fatalf("SymbolGraph.At found no key for the Input declaration in %s", inputPath)
	}

	counts := map[string]int{}
	for _, span := range g.References(key) {
		counts[filepath.Base(span.Path)]++
	}
	if counts["post.gsx"] != 1 || counts["use.go"] != 1 {
		t.Errorf("SymbolGraph.References(%s) = %v, want one exact markup and one Go reference", key, counts)
	}

	usePath := filepath.Join(root, "use.go")
	useOffset := strings.Index(useSrc, "components.Input") + len("components.")
	useKey, _, ok := g.At(usePath, useOffset)
	if !ok {
		t.Fatalf("SymbolGraph.At found no key for the components.Input use in %s", usePath)
	}
	if useKey != key {
		t.Errorf("SymbolGraph.At(use.go) key = %v, want the same key as the Input declaration %v", useKey, key)
	}
}
