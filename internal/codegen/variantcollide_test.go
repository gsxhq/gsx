package codegen

import (
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/parser"
)

func mustParseComponent(t *testing.T, source string) *gsxast.Component {
	t.Helper()
	fset := token.NewFileSet()
	files := parseVariantFiles(t, fset, map[string]string{"input.gsx": source})
	for _, declaration := range files["input.gsx"].Decls {
		if component, ok := declaration.(*gsxast.Component); ok {
			return component
		}
	}
	t.Fatal("no component declaration")
	return nil
}

func parseVariantFiles(t *testing.T, fset *token.FileSet, sources map[string]string) map[string]*gsxast.File {
	t.Helper()
	files := make(map[string]*gsxast.File, len(sources))
	for path, source := range sources {
		file, diagnostics := parser.ParseFileWithClassifier(fset, path, []byte(source), 0, nil)
		if len(diagnostics) != 0 {
			t.Fatalf("parse %s: %v", path, diagnostics)
		}
		files[path] = file
	}
	return files
}

func TestComponentFileHasEffectiveConstraint(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   string
		source string
		want   bool
	}{
		{name: "go build", path: "icon_a.gsx", source: "//go:build variantA\n\npackage p\ncomponent Icon() { <span/> }\n", want: true},
		{name: "legacy build", path: "icon_a.gsx", source: "// +build variantA\n\npackage p\ncomponent Icon() { <span/> }\n", want: true},
		{name: "invalid legacy ignored before valid", path: "icon_a.gsx", source: "// +build " + strings.Repeat("a ", 102) + "\n// +build linux\n\npackage p\ncomponent Icon() { <span/> }\n", want: true},
		{name: "goos filename", path: "icon_linux.gsx", source: "package p\ncomponent Icon() { <span/> }\n", want: true},
		{name: "goarch filename", path: "icon_amd64.gsx", source: "package p\ncomponent Icon() { <span/> }\n", want: true},
		{name: "ordinary filename", path: "icon_variantA.gsx", source: "package p\ncomponent Icon() { <span/> }\n", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fset := token.NewFileSet()
			files := parseVariantFiles(t, fset, map[string]string{test.path: test.source})
			got, err := componentFileHasEffectiveConstraint(test.path, files[test.path], []byte(test.source))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("componentFileHasEffectiveConstraint = %v, want %v", got, test.want)
			}
		})
	}
}

func TestComponentTargetPlanUsesOnlyConstrainedCrossFileFamilies(t *testing.T) {
	fset := token.NewFileSet()
	files := parseVariantFiles(t, fset, map[string]string{
		"icon_linux.gsx":   "package p\ncomponent Icon() { <span/> }\n",
		"icon_windows.gsx": "package p\ncomponent Icon() { <span/> }\n",
	})
	bag := diag.NewBag(fset)
	plan := newComponentTargetPlan(files, map[string][]byte{
		"icon_linux.gsx":   []byte("package p\ncomponent Icon() { <span/> }\n"),
		"icon_windows.gsx": []byte("package p\ncomponent Icon() { <span/> }\n"),
	}, bag)
	if bag.HasErrors() {
		t.Fatalf("plan diagnostics: %v", bag.Sorted())
	}
	if len(plan.families) != 1 || len(plan.families[0].members) != 2 {
		t.Fatalf("families = %+v, want one two-member family", plan.families)
	}
	public := 0
	for component, emission := range plan.emissions {
		if component.Name != "Icon" || !emission.splitBody || emission.bodyName == "" {
			t.Fatalf("emission for %s = %+v", component.Name, emission)
		}
		if emission.public {
			public++
		}
	}
	if public != 1 {
		t.Fatalf("public emissions = %d, want one", public)
	}
}
