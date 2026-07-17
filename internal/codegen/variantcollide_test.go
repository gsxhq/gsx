package codegen

import (
	"go/token"
	"go/types"
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

func TestSyntacticComponentTargetPlanHasNoVariantAuthority(t *testing.T) {
	fset := token.NewFileSet()
	files := parseVariantFiles(t, fset, map[string]string{
		"icon_linux.gsx":   "package p\ncomponent Icon() { <span/> }\n",
		"icon_windows.gsx": "package p\ncomponent Icon() { <span/> }\n",
	})
	plan := syntacticComponentTargetPlan(files)
	if len(plan.families) != 0 || plan.invalidMembership {
		t.Fatalf("syntactic plan families = %+v invalid=%t, want no acceptance decisions", plan.families, plan.invalidMembership)
	}
	public := 0
	for component, emission := range plan.emissions {
		if component.Name != "Icon" || emission.splitBody || emission.bodyName != "" {
			t.Fatalf("emission for %s = %+v, want importer-free all-public skeleton", component.Name, emission)
		}
		if emission.public {
			public++
		}
	}
	if public != 2 {
		t.Fatalf("public emissions = %d, want every per-file declaration", public)
	}
}

func TestComponentKeyInvalidReceiverCannotEnterFunctionNamespace(t *testing.T) {
	first := &gsxast.Component{Name: "Card", Recv: "(r !)"}
	second := &gsxast.Component{Name: "Card", Recv: "(r ?)"}
	if got := componentKey(first); got == ".Card" {
		t.Fatalf("invalid receiver key = %q, collided with package function namespace", got)
	}
	if componentKey(first) == componentKey(second) {
		t.Fatalf("distinct invalid receivers share key %q", componentKey(first))
	}
}

func TestSemanticVariantPlanDoesNotFoldUnresolvedMethodIntoFunction(t *testing.T) {
	fset := token.NewFileSet()
	sourceText := map[string]string{
		"card_a.gsx": "//go:build variantA\n\npackage p\ncomponent (r Missing) Card() { <span/> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage p\ncomponent Card() { <span/> }\n",
	}
	files := parseVariantFiles(t, fset, sourceText)
	sources := make(map[string][]byte, len(sourceText))
	for path, source := range sourceText {
		sources[path] = []byte(source)
	}
	privatePlan := privateComponentTargetPlan(files, nil)
	plan := finalizedPlanFromSemanticReceivers(files, sources, privatePlan, nil, nil, diag.NewBag(fset))

	if len(plan.families) != 0 || plan.invalidMembership {
		t.Fatalf("semantic families = %+v invalid=%t, want unresolved method isolated from package function", plan.families, plan.invalidMembership)
	}
	if len(plan.emissions) != 2 {
		t.Fatalf("emissions = %d, want both declarations", len(plan.emissions))
	}
	for component, emission := range plan.emissions {
		if !emission.public || emission.splitBody {
			t.Fatalf("%s emission = %+v, want independent public declaration", component.Name, emission)
		}
	}
}

func TestSemanticVariantPlanDoesNotFoldDifferentInvalidReceivers(t *testing.T) {
	fset := token.NewFileSet()
	sourceText := map[string]string{
		"card_a.gsx": "//go:build variantA\n\npackage p\ncomponent (r MissingA) Card() { <span/> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage p\ncomponent (r MissingB) Card() { <span/> }\n",
	}
	files := parseVariantFiles(t, fset, sourceText)
	sources := make(map[string][]byte, len(sourceText))
	for path, source := range sourceText {
		sources[path] = []byte(source)
	}
	privatePlan := privateComponentTargetPlan(files, nil)
	objects := map[*gsxast.Component]*types.Func{}
	for component := range privatePlan.emissions {
		receiver := types.NewVar(token.NoPos, nil, "r", types.Typ[types.Invalid])
		signature := types.NewSignatureType(receiver, nil, nil, types.NewTuple(), types.NewTuple(), false)
		objects[component] = types.NewFunc(token.NoPos, nil, privatePlan.emissions[component].bodyName, signature)
	}
	plan := finalizedPlanFromSemanticReceivers(files, sources, privatePlan, objects, nil, diag.NewBag(fset))

	if len(plan.families) != 0 || plan.invalidMembership {
		t.Fatalf("semantic families = %+v invalid=%t, want different invalid receivers isolated", plan.families, plan.invalidMembership)
	}
	if len(plan.emissions) != 2 {
		t.Fatalf("emissions = %d, want both unresolved methods", len(plan.emissions))
	}
}

func TestSemanticVariantPlanGivesSameSpellingUnresolvedReceiversDistinctLogicalKeys(t *testing.T) {
	fset := token.NewFileSet()
	sourceText := map[string]string{
		"card_a.gsx": "//go:build variantA\n\npackage p\ncomponent (r Missing) Card(value string) { <span/> }\n",
		"card_b.gsx": "//go:build variantB\n\npackage p\ncomponent (r Missing) Card(value string) { <span/> }\n",
	}
	files := parseVariantFiles(t, fset, sourceText)
	sources := make(map[string][]byte, len(sourceText))
	for path, source := range sourceText {
		sources[path] = []byte(source)
	}
	privatePlan := privateComponentTargetPlan(files, nil)
	objects := map[*gsxast.Component]*types.Func{}
	for component := range privatePlan.emissions {
		receiver := types.NewVar(token.NoPos, nil, "r", types.Typ[types.Invalid])
		signature := types.NewSignatureType(receiver, nil, nil, types.NewTuple(), types.NewTuple(), false)
		objects[component] = types.NewFunc(token.NoPos, nil, privatePlan.emissions[component].bodyName, signature)
	}
	plan := finalizedPlanFromSemanticReceivers(files, sources, privatePlan, objects, nil, diag.NewBag(fset))
	keys := map[string]bool{}
	for component := range plan.emissions {
		key := plan.logicalKey(component)
		if !strings.HasPrefix(key, "!unresolved-receiver:") {
			t.Fatalf("logical key = %q, want opaque unresolved receiver identity", key)
		}
		keys[key] = true
	}
	if len(keys) != 2 {
		t.Fatalf("unresolved logical keys = %v, want one per declaration", keys)
	}
}
