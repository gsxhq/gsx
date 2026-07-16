package codegen

import (
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

func TestPlanComponentPositionalCallsAcceptsExplicitOperandsAndZerosOmissions(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C count={n}/>`)
	count := el.Attrs[0]
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, pkg, "title", types.Typ[types.String]),
		types.NewVar(token.NoPos, pkg, "count", types.Typ[types.Int]),
	), types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	registry := positionalTestRegistry(el)

	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites:       registry,
		targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{count: {tv: types.TypeAndValue{Type: types.Typ[types.Int]}}},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	site, ok := got.sites[1]
	if !ok {
		t.Fatal("planned site missing")
	}
	if len(site.operands) != 1 || site.operands[0].paramIndex != 1 {
		t.Fatalf("authored operands = %+v", site.operands)
	}
	if len(site.zeros) != 1 || site.zeros[0].paramIndex != 0 || site.zeros[0].expr != `""` {
		t.Fatalf("zeros = %+v, want title=\"\"", site.zeros)
	}
	if len(site.materialization.values) != 1 || !site.materialization.values[0].inline {
		t.Fatalf("materialization = %+v", site.materialization)
	}
	if lookedUp, ok := got.siteForElement(el); !ok || lookedUp.call.site != 1 {
		t.Fatalf("element lookup = %+v, %v", lookedUp, ok)
	}
}

func TestPlanComponentPositionalCallsOpaqueCrossPackageZeroFailsButExplicitOperandWorks(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	dep := types.NewPackage("example.test/ui", "ui")
	id := types.NewField(token.NoPos, dep, "id", types.Typ[types.Uint64], false)
	theme := types.NewNamed(types.NewTypeName(token.NoPos, dep, "theme", nil), types.NewStruct([]*types.Var{id}, nil), nil)
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, dep, "theme", theme)),
		types.NewTuple(types.NewVar(token.NoPos, dep, "", fx.runtime.node)), false)

	t.Run("omitted", func(t *testing.T) {
		el, fset := plannerElement(t, `<C/>`)
		_, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
			callSites:       positionalTestRegistry(el),
			targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
			expressionFacts: map[gsxast.Node]expressionFact{},
			runtime:         fx.runtime,
			analysisPackage: pkg,
			fset:            fset,
		})
		if len(diagnostics) != 1 || diagnostics[0].Code != "component-required-attribute" || !strings.Contains(diagnostics[0].Message, "theme") {
			t.Fatalf("diagnostics = %+v, want positioned required theme", diagnostics)
		}
	})

	t.Run("explicit", func(t *testing.T) {
		el, fset := plannerElement(t, `<C theme={current}/>`)
		value := el.Attrs[0]
		got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
			callSites:       positionalTestRegistry(el),
			targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
			expressionFacts: map[gsxast.Node]expressionFact{value: {tv: types.TypeAndValue{Type: theme}}},
			runtime:         fx.runtime,
			analysisPackage: pkg,
			fset:            fset,
		})
		if len(diagnostics) != 0 || got.sites[1].call.args[0].omitted {
			t.Fatalf("explicit opaque operand: plan=%+v diagnostics=%+v", got, diagnostics)
		}
	})
}

func TestPlanComponentPositionalCallsValidatesRecursiveAttrsContributorLeaves(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C { if ok { {bad...} } }/>`)
	conditional := el.Attrs[0].(*gsxast.CondAttr)
	bad := conditional.Then[0]
	if _, ok := bad.(*gsxast.SpreadAttr); !ok {
		t.Fatalf("conditional leaf = %T, want spread", bad)
	}
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, pkg, "attrs", fx.runtime.attrs)),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	_, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites: positionalTestRegistry(el),
		targets:   map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{
			bad: {tv: types.TypeAndValue{Type: types.NewStruct(nil, nil)}},
		},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 1 || diagnostics[0].Code != "component-attrs-spread-type" {
		t.Fatalf("recursive attrs diagnostics = %+v", diagnostics)
	}
}

func TestPlanComponentPositionalCallsOperandErrorsPrecedeDeferredTargetErrors(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C title={pair()}/>`)
	value := el.Attrs[0]
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, pkg, "title", types.Typ[types.String])),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	tuple := types.NewTuple(
		types.NewVar(token.NoPos, pkg, "", types.Typ[types.String]),
		types.NewVar(token.NoPos, pkg, "", types.Typ[types.Int]),
	)
	_, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites: positionalTestRegistry(el),
		targets: map[callSiteID]componentTargetFact{1: {
			site: 1, raw: sig, provenance: targetPackageFunc,
			targetDiags: []diag.Diagnostic{{Severity: diag.Error, Code: "target-error", Message: "deferred", Source: "types"}},
		}},
		expressionFacts: map[gsxast.Node]expressionFact{value: {tv: types.TypeAndValue{Type: tuple}, tuple: tuple}},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 1 || diagnostics[0].Code != "invalid-tuple" {
		t.Fatalf("diagnostics = %+v, want operand diagnostic only", diagnostics)
	}
}

func TestPlanComponentPositionalCallsRetainsFullInferredTypeArgumentsBeforeZeroFill(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C value={n}/>`)
	value := el.Attrs[0]
	pkg := types.NewPackage("example.test/page", "page")
	tp := newTypeParam(pkg, "T", anyConstraint())
	sig := types.NewSignatureType(nil, nil, []*types.TypeParam{tp}, types.NewTuple(
		types.NewVar(token.NoPos, pkg, "value", tp),
		types.NewVar(token.NoPos, pkg, "fallback", tp),
	), types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites:       positionalTestRegistry(el),
		targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{value: {tv: types.TypeAndValue{Type: types.Typ[types.Int]}}},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	site := got.sites[1]
	if len(site.typeArgs) != 1 || !types.Identical(site.typeArgs[0], types.Typ[types.Int]) {
		t.Fatalf("full inferred type args = %v, want [int]", site.typeArgs)
	}
	if len(site.typeArgExprs) != 1 || site.typeArgExprs[0] != "int" {
		t.Fatalf("emission type args = %v, want [int]", site.typeArgExprs)
	}
	if site.signature.goSig.TypeParams().Len() != 0 || !types.Identical(site.signature.params[1].typ, types.Typ[types.Int]) {
		t.Fatalf("instantiated signature = %v", site.signature.goSig)
	}
	if len(site.zeros) != 1 || site.zeros[0].paramIndex != 1 || site.zeros[0].expr != "0" {
		t.Fatalf("post-inference zeros = %+v", site.zeros)
	}
}

func TestPlanComponentPositionalCallsValidatesAndCommitsForeignZeroImports(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C/>`)
	dep := types.NewPackage("example.test/ui", "ui")
	widget := types.NewNamed(types.NewTypeName(token.NoPos, dep, "Widget", nil), types.NewStruct(nil, nil), nil)
	dep.Scope().Insert(widget.Obj())
	dep.MarkComplete()
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, dep, "widget", widget)),
		types.NewTuple(types.NewVar(token.NoPos, dep, "", fx.runtime.node)), false)
	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites:       positionalTestRegistry(el),
		targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if zeros := got.sites[1].zeros; len(zeros) != 1 || zeros[0].expr != "*new(_gsxty1.Widget)" {
		t.Fatalf("foreign zero = %+v", zeros)
	}
	if specs := got.imports["page.gsx"].specs(); len(specs) != 1 || specs[0].path != dep.Path() || specs[0].name != "_gsxty1" {
		t.Fatalf("committed imports = %+v", specs)
	}
}

func TestPlanComponentPositionalCallsRejectsExplicitOperandTypeMismatch(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C count={name}/>`)
	value := el.Attrs[0]
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, pkg, "count", types.Typ[types.Int])),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	_, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites:       positionalTestRegistry(el),
		targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{value: {tv: types.TypeAndValue{Type: types.Typ[types.String]}}},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 1 || diagnostics[0].Code != "component-prop-type" {
		t.Fatalf("diagnostics = %+v, want component-prop-type", diagnostics)
	}
}

func TestPlanComponentPositionalCallsDerivesSyntaxDefinedOrdinaryPropFacts(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C label="x" disabled icon={<i/>} bag={{"role": "button"}} text=f"hello"/>`)
	ordered := el.Attrs[3].(*gsxast.OrderedAttrsAttr)
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, pkg, "label", types.Typ[types.String]),
		types.NewVar(token.NoPos, pkg, "disabled", types.Typ[types.Bool]),
		types.NewVar(token.NoPos, pkg, "icon", fx.runtime.node),
		types.NewVar(token.NoPos, pkg, "bag", fx.runtime.attrs),
		types.NewVar(token.NoPos, pkg, "text", types.Typ[types.String]),
	), types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites: positionalTestRegistry(el),
		targets:   map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{
			&ordered.Pairs[0]: {tv: types.TypeAndValue{Type: types.Typ[types.UntypedString], Value: constant.MakeString("button")}},
		},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	site := got.sites[1]
	if len(site.call.values) != 5 || len(site.operands) != 5 {
		t.Fatalf("authored values=%d operands=%d", len(site.call.values), len(site.operands))
	}
	wantTypes := []types.Type{types.Typ[types.UntypedString], types.Typ[types.UntypedBool], fx.runtime.node, fx.runtime.attrs, types.Typ[types.String]}
	for i, want := range wantTypes {
		if gotType := site.expressionFacts[site.call.values[i].node].tv.Type; !types.Identical(gotType, want) {
			t.Errorf("value %d fact type = %v, want %v", i, gotType, want)
		}
	}
}

func TestPlanComponentPositionalCallsPinsConditionalAttrsAtAuthoredOrder(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C late={first()} { if ok { id="x" } }/>`)
	late := el.Attrs[0]
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, pkg, "attrs", fx.runtime.attrs),
		types.NewVar(token.NoPos, pkg, "late", types.Typ[types.String]),
	), types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites: positionalTestRegistry(el),
		targets:   map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{
			late: {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
		},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	site := got.sites[1]
	if len(site.materialization.values) != 2 || site.materialization.values[0].temp == "" || site.materialization.values[1].temp == "" {
		t.Fatalf("crossing ordered values must both be pinned: %+v", site.materialization.values)
	}
	if !site.expressionFacts[el.Attrs[1]].hasOrderedOperation {
		t.Fatalf("conditional attrs fact = %+v", site.expressionFacts[el.Attrs[1]])
	}
}

func TestPositionalMaterializationFactsLeaveTupleOwnershipWithCompoundLowering(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	errType := types.Universe.Lookup("error").Type()
	tuple := types.NewTuple(
		types.NewVar(token.NoPos, nil, "", types.Typ[types.String]),
		types.NewVar(token.NoPos, nil, "", errType),
	)

	embedded := &gsxast.EmbeddedAttr{Name: "label", Lang: gsxast.EmbeddedText}
	plain := &gsxast.ExprAttr{Name: "title", Expr: "load()"}
	pair := &gsxast.ExprAttr{Name: "href", Expr: "url()"}
	pairNode := componentAttrsStreamNode{kind: componentAttrsStreamPair, attr: pair}
	plan := componentCallPlan{values: []componentInputValue{
		{kind: componentInputProp, paramIndex: 0, contributorIndex: -1, node: embedded},
		{kind: componentInputProp, paramIndex: 1, contributorIndex: -1, node: plain},
		{kind: componentInputAttrsPair, paramIndex: 2, contributorIndex: 0, node: pair, attrsNode: &pairNode},
	}}
	facts := map[gsxast.Node]expressionFact{
		embedded: {tv: types.TypeAndValue{Type: tuple}, tuple: tuple},
		plain:    {tv: types.TypeAndValue{Type: tuple}, tuple: tuple},
		pair:     {tv: types.TypeAndValue{Type: tuple}, tuple: tuple},
	}

	materializationFacts := positionalMaterializationFacts(plan, facts, fx.runtime, funcTables{})
	if got := materializationFacts[embedded]; got.tuple != nil || !types.Identical(got.tv.Type, types.Typ[types.String]) || !got.hasOrderedOperation {
		t.Fatalf("embedded lowering fact = %+v, want ordered string without outer tuple", got)
	}
	if got := materializationFacts[pair]; got.tuple != nil || !types.Identical(got.tv.Type, fx.runtime.attrs) || !got.hasOrderedOperation {
		t.Fatalf("attrs-pair lowering fact = %+v, want ordered attrs without outer tuple", got)
	}
	if got := materializationFacts[plain]; got.tuple == nil {
		t.Fatalf("plain expression tuple ownership moved away from outer materializer: %+v", got)
	}
}

func positionalTestRegistry(el *gsxast.Element) *callSiteRegistry {
	return &callSiteRegistry{
		byElement: map[*gsxast.Element]callSiteID{el: 1},
		records:   []callSiteRecord{{id: 1, path: "page.gsx", element: el, disposition: callSitePlanned}},
	}
}
