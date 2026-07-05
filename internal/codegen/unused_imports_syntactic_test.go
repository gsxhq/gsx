package codegen

import (
	"go/parser"
	"go/token"
	"testing"
)

func TestSkeletonUsedNames(t *testing.T) {
	const src = `package p
import "strings"
func f() { _ = strings.TrimSpace("x"); _ = a.b.c }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "s.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	used := skeletonUsedNames(f)
	if !used["strings"] {
		t.Errorf("want strings used")
	}
	if !used["a"] { // inner selector a.b of a.b.c
		t.Errorf("want a used")
	}
}

func TestImportBaseName(t *testing.T) {
	for path, want := range map[string]string{
		"strings":            "strings",
		"gopkg.in/yaml.v3":   "yaml.v3", // base is NOT the package name → forces candidate resolution
		"github.com/x/go-fo": "go-fo",
	} {
		if got := importBaseName(path); got != want {
			t.Errorf("importBaseName(%q)=%q want %q", path, got, want)
		}
	}
}

func TestClassifyUnusedImports(t *testing.T) {
	fset := token.NewFileSet()
	used := map[string]bool{"strings": true, "sx": true}
	imps := []importSpec{
		{name: "", path: "strings"},        // default, base used → kept
		{name: "", path: "bytes"},          // default, base unused → candidate
		{name: "sx", path: "text/scanner"}, // aliased, alias used → kept
		{name: "al", path: "os"},           // aliased, alias unused → unused
		{name: "_", path: "embed"},         // blank → never removed
		{name: ".", path: "math"},          // dot → never removed
	}
	unused, candidates := classifyUnusedImports(used, imps, nil, fset)
	if len(unused) != 1 || unused[0].Path != "os" || unused[0].Name != "al" {
		t.Errorf("unused=%+v, want only {al os}", unused)
	}
	if len(candidates) != 1 || candidates[0].path != "bytes" {
		t.Errorf("candidates=%+v, want only bytes", candidates)
	}
}
