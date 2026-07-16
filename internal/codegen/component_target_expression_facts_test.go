package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"runtime"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func TestTargetDiscoveryHarvestsAuthoredOperandExpressionFacts(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetExpressionFactsFile(t, fset, "facts.gsx", `package p

func pair() (string, error) { return "", nil }
func predicate() bool { return true }

component Child(value any) {}

component Page(ch <-chan int, flag bool) {
	<Child value={7}/>
	<Child value={nil}/>
	<Child value={pair()}/>
	<Child value={<-ch}/>
	<Child value={flag && true}/>
	<Child value={flag || false}/>
	<Child value={func() bool { return predicate() }}/>
}

func Embedded(ch <-chan int) _gsxrt.Node {
	return <Child value={<-ch}/>
}
`)
	files := map[string]*gsxast.File{"facts.gsx": file}
	bag := diag.NewBag(fset)
	preprocessed, err := newParsedGSXPackage("p", files).preprocessComponentCallSites(map[string]bool{"Child": true, "Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("preprocessing failed: %+v", bag.Sorted())
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	plan := syntacticComponentTargetPlan(files)
	skeleton, err := buildComponentTargetSkeleton(file, funcTables{}, fset, bag, markers, plan, skeletonTargetDiscovery)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := goparser.ParseFile(fset, "facts.target.x.go", skeleton.source, goparser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse target skeleton: %v\n%s", err, skeleton.source)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	prelude, err := goparser.ParseFile(fset, "_gsxtarget_shared.x.go", analysisPreludeSource("p"), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	pkg, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed, prelude}, fset, targetExpressionFactsImporter(), componentTargetCheckConfig{typeEnvironment: targetExpressionFactsEnvironment()})
	if len(typeErrs) != 0 {
		t.Fatalf("target package errors = %+v\n%s", typeErrs, skeleton.source)
	}

	facts := harvestComponentTargetExpressionFacts(parsed, file, pkg, info, fset, skeleton.embeddedMarkups, plan, nil)
	if got, want := len(facts), 8; got != want {
		t.Fatalf("expression facts = %d, want %d", got, want)
	}
	byExpr := make(map[string]expressionFact)
	receives := 0
	for node, fact := range facts {
		ea, ok := node.(*gsxast.ExprAttr)
		if !ok {
			continue
		}
		byExpr[strings.TrimSpace(ea.Expr)] = fact
		if strings.TrimSpace(ea.Expr) == "<-ch" {
			receives++
		}
	}
	if got, want := len(byExpr), 7; got != want {
		t.Fatalf("expression facts = %d, want %d: %#v", got, want, byExpr)
	}
	if receives != 2 {
		t.Fatalf("receive facts = %d, want component-body and embedded-markup operands", receives)
	}

	constant := byExpr["7"]
	if constant.tv.Value == nil || constant.tv.Value.ExactString() != "7" || constant.isNil || constant.hasOrderedOperation || constant.tuple != nil {
		t.Errorf("constant fact = %+v, want preserved untyped constant", constant)
	}
	if basic, ok := constant.tv.Type.(*types.Basic); !ok || basic.Kind() != types.UntypedInt {
		t.Errorf("constant type = %v, want untyped int", constant.tv.Type)
	}

	nilFact := byExpr["nil"]
	if !nilFact.isNil || nilFact.tv.Value != nil || nilFact.hasOrderedOperation || nilFact.tuple != nil {
		t.Errorf("nil fact = %+v, want contextual untyped nil", nilFact)
	}
	if basic, ok := nilFact.tv.Type.(*types.Basic); !ok || basic.Kind() != types.UntypedNil {
		t.Errorf("nil type = %v, want untyped nil", nilFact.tv.Type)
	}

	tuple := byExpr["pair()"]
	if tuple.tuple == nil || tuple.tuple.Len() != 2 || !tuple.hasOrderedOperation {
		t.Errorf("tuple fact = %+v, want two-result ordered call", tuple)
	}
	if !byExpr["<-ch"].hasOrderedOperation {
		t.Errorf("receive fact = %+v, want ordered operation", byExpr["<-ch"])
	}
	if !byExpr["flag && true"].hasOrderedOperation {
		t.Errorf("logical-and fact = %+v, want ordered operation", byExpr["flag && true"])
	}
	if !byExpr["flag || false"].hasOrderedOperation {
		t.Errorf("logical-or fact = %+v, want ordered operation", byExpr["flag || false"])
	}
	if byExpr["func() bool { return predicate() }"].hasOrderedOperation {
		t.Errorf("function literal fact = %+v, body must not execute when literal is evaluated", byExpr["func() bool { return predicate() }"])
	}
}

func parseTargetExpressionFactsFile(t *testing.T, fset *token.FileSet, path, source string) *gsxast.File {
	t.Helper()
	file, err := gsxparser.ParseFile(fset, path, []byte(source), 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func targetExpressionFactsEnvironment() typeCheckEnvironment {
	return typeCheckEnvironment{
		sizes:     types.SizesFor("gc", runtime.GOARCH),
		goVersion: "go1.26",
	}
}

func targetExpressionFactsImporter() types.Importer {
	namedInterface := func(pkg *types.Package, name string) {
		object := types.NewTypeName(token.NoPos, pkg, name, nil)
		pkg.Scope().Insert(types.NewNamed(object, types.NewInterfaceType(nil, nil).Complete(), nil).Obj())
	}
	runtimePackage := types.NewPackage("github.com/gsxhq/gsx", "gsx")
	namedInterface(runtimePackage, "Node")
	runtimePackage.MarkComplete()
	contextPackage := types.NewPackage("context", "context")
	namedInterface(contextPackage, "Context")
	contextPackage.MarkComplete()
	return mapImporter{
		runtimePackage.Path(): runtimePackage,
		contextPackage.Path(): contextPackage,
	}
}
