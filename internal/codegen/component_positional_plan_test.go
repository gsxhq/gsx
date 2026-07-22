package codegen

import (
	"go/constant"
	"go/token"
	"go/types"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
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
	materialization := planComponentMaterialization(site.call, positionalMaterializationFacts(site.call, site.expressionFacts, site.runtime))
	if len(materialization.values) != 1 || !materialization.values[0].inline {
		t.Fatalf("materialization = %+v", materialization)
	}
	if lookedUp, ok := got.siteForElement(el); !ok || lookedUp.call.site != 1 {
		t.Fatalf("element lookup = %+v, %v", lookedUp, ok)
	}
}

func TestValidateAssembledPositionalCallChecksCompletedShape(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	pkg := types.NewPackage("example.test/page", "page")
	definedAttrs := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "LocalAttrs", nil),
		types.NewSlice(fx.runtime.attr),
		nil,
	)
	sig := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, pkg, "label", types.Typ[types.String]),
		types.NewVar(token.NoPos, pkg, "count", types.Typ[types.Int]),
		types.NewVar(token.NoPos, pkg, "attrs", definedAttrs),
	), types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	model, err := analyzeComponentSignature(sig, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	plan := componentCallPlan{
		target: model,
		args: []componentArgSlot{
			{param: model.params[0], omitted: true},
			{param: model.params[1], valueIndexes: []int{0}},
			{param: model.params[2], valueIndexes: []int{1}},
		},
	}
	operands := []suppliedOperand{{paramIndex: 1, tv: types.TypeAndValue{Type: types.Typ[types.Int]}}}
	zeros := []componentZeroArgument{{paramIndex: 0, expr: `""`}}
	assembly, err := assemblePositionalCall(plan, operands, zeros)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAssembledPositionalCall(plan, assembly, fx.runtime); err != nil {
		t.Fatalf("valid completed call: %v", err)
	}

	if _, err := assemblePositionalCall(plan, operands, nil); err == nil || !strings.Contains(err.Error(), "omitted without a zero") {
		t.Fatalf("missing zero error = %v", err)
	}
	badOperands := []suppliedOperand{{paramIndex: 1, tv: types.TypeAndValue{Type: types.Typ[types.String]}}}
	badAssembly, err := assemblePositionalCall(plan, badOperands, zeros)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAssembledPositionalCall(plan, badAssembly, fx.runtime); err == nil {
		t.Fatal("final call accepted a string in the int position")
	}
}

func TestValidateAssembledPositionalCallPreservesUntypedRuneAssignmentContext(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	pkg := types.NewPackage("example.test/page", "page")
	letter := types.NewNamed(
		types.NewTypeName(token.NoPos, pkg, "Letter", nil),
		types.Typ[types.Rune],
		nil,
	)
	sig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, pkg, "letter", letter)),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	model, err := analyzeComponentSignature(sig, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	plan := componentCallPlan{
		target: model,
		args:   []componentArgSlot{{param: model.params[0], valueIndexes: []int{0}}},
	}
	operands := []suppliedOperand{{
		paramIndex: 0,
		tv: types.TypeAndValue{
			Type:  types.Typ[types.UntypedRune],
			Value: constant.MakeInt64('x'),
		},
	}}
	assembly, err := assemblePositionalCall(plan, operands, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAssembledPositionalCall(plan, assembly, fx.runtime); err != nil {
		t.Fatalf("valid untyped rune assignment to defined rune: %v", err)
	}
}

func TestValidateAssembledPositionalCallChecksVariadicChildrenArity(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil, types.NewTuple(
		types.NewVar(token.NoPos, pkg, "label", types.Typ[types.String]),
		types.NewVar(token.NoPos, pkg, "children", types.NewSlice(fx.runtime.node)),
	), types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), true)
	model, err := analyzeComponentSignature(sig, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	plan := componentCallPlan{
		target: model,
		args: []componentArgSlot{
			{param: model.params[0], valueIndexes: []int{0}},
			{param: model.params[1], valueIndexes: []int{1}},
		},
		values: []componentInputValue{
			{kind: componentInputProp, paramIndex: 0},
			{kind: componentInputBody, paramIndex: 1, children: []gsxast.Markup{nil, nil}},
		},
	}
	operands := []suppliedOperand{{paramIndex: 0, tv: types.TypeAndValue{Type: types.Typ[types.String]}}}
	assembly, err := assemblePositionalCall(plan, operands, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAssembledPositionalCall(plan, assembly, fx.runtime); err != nil {
		t.Fatalf("valid variadic children call: %v", err)
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
		fact, _ := site.expressionFacts.get(site.call.values[i].node)
		if gotType := fact.tv.Type; !types.Identical(gotType, want) {
			t.Errorf("value %d fact type = %v, want %v", i, gotType, want)
		}
	}
}

func TestPlanComponentPositionalCallsDerivesNestedClassAndStyleFactsForSiblings(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	elements, fset := plannerElements(t, `<div>
	<C class={ if on { first() } else { "off" } }/>
	<C style={ "display:block", css`+"`"+`color:@{tone()}`+"`"+` }/>
</div>`)
	if len(elements) != 2 {
		t.Fatalf("component elements = %d, want 2", len(elements))
	}
	classAttr := elements[0].Attrs[0].(*gsxast.ClassAttr)
	styleAttr := elements[1].Attrs[0].(*gsxast.ClassAttr)
	if len(styleAttr.Parts) != 2 || styleAttr.Parts[1].CSSSegments == nil {
		t.Fatalf("style parts = %+v, want plain part followed by CSS literal", styleAttr.Parts)
	}
	stylePlain := &styleAttr.Parts[0]
	classArms := valueFormArms(classAttr.Parts[0].CF)
	if len(classArms) != 2 {
		t.Fatalf("class arms = %d, want 2", len(classArms))
	}
	var cssValue *gsxast.Interp
	gsxast.Inspect(&styleAttr.Parts[1], func(node gsxast.Node) bool {
		if interp, ok := node.(*gsxast.Interp); ok {
			cssValue = interp
		}
		return true
	})
	if cssValue == nil {
		t.Fatal("CSS literal has no embedded value node")
	}

	pkg := types.NewPackage("example.test/page", "page")
	sig := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewVar(token.NoPos, pkg, "attrs", fx.runtime.attrs)),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	registry := &callSiteRegistry{
		byElement: map[*gsxast.Element]callSiteID{elements[0]: 1, elements[1]: 2},
		records: []callSiteRecord{
			{id: 1, path: "page.gsx", element: elements[0], disposition: componentSitePlanned},
			{id: 2, path: "page.gsx", element: elements[1], disposition: componentSitePlanned},
		},
	}
	target := componentTargetFact{raw: sig, provenance: targetPackageFunc}
	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites: registry,
		targets: map[callSiteID]componentTargetFact{
			1: target,
			2: target,
		},
		expressionFacts: map[gsxast.Node]expressionFact{
			classArms[0]: {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
			classArms[1]: {tv: types.TypeAndValue{Type: types.Typ[types.UntypedString], Value: constant.MakeString("off")}},
			stylePlain:   {tv: types.TypeAndValue{Type: types.Typ[types.UntypedString], Value: constant.MakeString("display:block")}},
			cssValue:     {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
		},
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	for id, attr := range map[callSiteID]*gsxast.ClassAttr{1: classAttr, 2: styleAttr} {
		site, ok := got.sites[id]
		if !ok {
			t.Errorf("site %d missing", id)
			continue
		}
		fact, ok := site.expressionFacts.get(attr)
		if !ok || !types.Identical(fact.tv.Type, types.Typ[types.String]) || !fact.hasOrderedOperation {
			t.Errorf("site %d class fact = %+v, want ordered string", id, fact)
		}
	}
}

func TestAggregateNestedComponentFactsUsesOnlyProbedValueNodes(t *testing.T) {
	t.Run("plain class part owns fact", func(t *testing.T) {
		part := &gsxast.ClassPart{Expr: "label"}
		root := &gsxast.ClassAttr{Parts: []gsxast.ClassPart{*part}}
		part = &root.Parts[0]
		if _, complete := aggregateNestedComponentFacts(root, newExpressionFactSet(nil)); complete {
			t.Fatal("plain part without its authoritative fact reported complete")
		}
		if _, complete := aggregateNestedComponentFacts(root, newExpressionFactSet(map[gsxast.Node]expressionFact{part: {}})); complete {
			t.Fatal("plain part with an incomplete authoritative fact reported complete")
		}
		ordered, complete := aggregateNestedComponentFacts(root, newExpressionFactSet(map[gsxast.Node]expressionFact{
			part: {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
		}))
		if !complete || !ordered {
			t.Fatalf("plain part aggregate = ordered %v complete %v", ordered, complete)
		}
	})

	t.Run("control flow delegates to value arms", func(t *testing.T) {
		thenArm := &gsxast.ValueArm{Expr: "first()"}
		elseArm := &gsxast.ValueArm{Expr: `"off"`}
		root := &gsxast.ClassAttr{Parts: []gsxast.ClassPart{{CF: &gsxast.ValueCF{If: &gsxast.ValueIf{Then: thenArm, Else: elseArm}}}}}
		facts := map[gsxast.Node]expressionFact{
			thenArm: {tv: types.TypeAndValue{Type: types.Typ[types.String]}},
			elseArm: {tv: types.TypeAndValue{Type: types.Typ[types.UntypedString]}},
		}
		ordered, complete := aggregateNestedComponentFacts(root, newExpressionFactSet(facts))
		if !complete || !ordered {
			t.Fatalf("control-flow aggregate = ordered %v complete %v", ordered, complete)
		}
		delete(facts, elseArm)
		if _, complete := aggregateNestedComponentFacts(root, newExpressionFactSet(facts)); complete {
			t.Fatal("control flow with a missing arm fact reported complete")
		}
	})

	t.Run("CSS literal delegates to embedded values", func(t *testing.T) {
		value := &gsxast.Interp{Expr: "tone()"}
		root := &gsxast.ClassAttr{Parts: []gsxast.ClassPart{{CSSSegments: []gsxast.Markup{&gsxast.Text{Value: "color:"}, value}}}}
		ordered, complete := aggregateNestedComponentFacts(root, newExpressionFactSet(map[gsxast.Node]expressionFact{
			value: {tv: types.TypeAndValue{Type: types.Typ[types.String]}, tuple: types.NewTuple(
				types.NewVar(token.NoPos, nil, "", types.Typ[types.String]),
				types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
			)},
		}))
		if !complete || !ordered {
			t.Fatalf("CSS aggregate = ordered %v complete %v", ordered, complete)
		}
		if _, complete := aggregateNestedComponentFacts(root, newExpressionFactSet(nil)); complete {
			t.Fatal("CSS literal with a missing embedded-value fact reported complete")
		}
	})
}

func TestPlanComponentPositionalCallsAdaptsExactNodeOperands(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, "<C static=\"x\" formatted=f`hi` label={label} count={n} flag node={node} markup={<i/>} labelTuple={makeLabel()} nodeTuple={makeNode()}/>")
	pkg := types.NewPackage("example.test/page", "page")
	labelType := testNamedType(t, pkg, "Label", types.NewStruct(nil, nil))
	labelType.AddMethod(types.NewFunc(token.NoPos, pkg, "String", types.NewSignatureType(
		types.NewVar(token.NoPos, pkg, "", labelType), nil, nil, types.NewTuple(),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.String])), false,
	)))
	labelTuple := types.NewTuple(
		types.NewVar(token.NoPos, pkg, "", labelType),
		types.NewVar(token.NoPos, pkg, "", types.Universe.Lookup("error").Type()),
	)
	nodeTuple := types.NewTuple(
		types.NewVar(token.NoPos, pkg, "", fx.runtime.node),
		types.NewVar(token.NoPos, pkg, "", types.Universe.Lookup("error").Type()),
	)
	params := make([]*types.Var, len(el.Attrs))
	for i, attr := range el.Attrs {
		name, ok := componentInputAttrName(attr)
		if !ok {
			t.Fatalf("attribute %d has no name", i)
		}
		params[i] = types.NewVar(token.NoPos, pkg, name, fx.runtime.node)
	}
	sig := types.NewSignatureType(nil, nil, nil, types.NewTuple(params...),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
	facts := map[gsxast.Node]expressionFact{
		el.Attrs[2]: {tv: types.TypeAndValue{Type: labelType}},
		el.Attrs[3]: {tv: types.TypeAndValue{Type: types.Typ[types.Int]}},
		el.Attrs[5]: {tv: types.TypeAndValue{Type: fx.runtime.node}},
		el.Attrs[7]: {tv: types.TypeAndValue{Type: labelTuple}, tuple: labelTuple},
		el.Attrs[8]: {tv: types.TypeAndValue{Type: nodeTuple}, tuple: nodeTuple},
	}

	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites:       positionalTestRegistry(el),
		targets:         map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: facts,
		runtime:         fx.runtime,
		analysisPackage: pkg,
		fset:            fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	site := got.sites[1]
	wantAdapters := []componentOperandAdapter{
		componentAdapterNodeText,
		componentAdapterNodeText,
		componentAdapterNodeVal,
		componentAdapterNodeVal,
		componentAdapterNodeVal,
		componentAdapterIdentity,
		componentAdapterIdentity,
		componentAdapterNodeVal,
		componentAdapterIdentity,
	}
	if len(site.operands) != len(wantAdapters) {
		t.Fatalf("operands = %d, want %d", len(site.operands), len(wantAdapters))
	}
	for i, want := range wantAdapters {
		op := site.operands[i]
		if op.valueIndex != i || op.adapter != want {
			t.Errorf("operand %d = %+v, want value index %d adapter %d", i, op, i, want)
		}
		if !types.Identical(op.tv.Type, fx.runtime.node) {
			t.Errorf("operand %d adapted type = %v, want %v", i, op.tv.Type, fx.runtime.node)
		}
	}
}

func TestPlanComponentPositionalCallsAdaptsNodeAliasButNotConcreteImplementation(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	pkg := types.NewPackage("example.test/page", "page")
	nodeAlias := testAliasType(t, pkg, "NodeAlias", fx.runtime.node)

	t.Run("canonical alias", func(t *testing.T) {
		el, fset := plannerElement(t, `<C value="x"/>`)
		sig := types.NewSignatureType(nil, nil, nil,
			types.NewTuple(types.NewVar(token.NoPos, pkg, "value", nodeAlias)),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
		got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
			callSites: positionalTestRegistry(el), targets: map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
			runtime: fx.runtime, analysisPackage: pkg, fset: fset,
		})
		if len(diagnostics) != 0 {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
		if op := got.sites[1].operands[0]; op.adapter != componentAdapterNodeText || !types.Identical(op.tv.Type, fx.runtime.node) {
			t.Fatalf("alias operand = %+v, want NodeText with canonical Node fact", op)
		}
	})

	t.Run("concrete implementation", func(t *testing.T) {
		el, fset := plannerElement(t, `<C value="x"/>`)
		concrete := testNamedType(t, pkg, "Concrete", types.NewStruct(nil, nil))
		concrete.AddMethod(types.NewFunc(token.NoPos, pkg, "Render", types.NewSignatureType(
			types.NewVar(token.NoPos, pkg, "", concrete), nil, nil, types.NewTuple(), types.NewTuple(), false,
		)))
		if !types.AssignableTo(concrete, fx.runtime.node) {
			t.Fatal("test concrete type must implement Node")
		}
		sig := types.NewSignatureType(nil, nil, nil,
			types.NewTuple(types.NewVar(token.NoPos, pkg, "value", concrete)),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)
		_, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
			callSites: positionalTestRegistry(el), targets: map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
			runtime: fx.runtime, analysisPackage: pkg, fset: fset,
		})
		if len(diagnostics) != 1 || diagnostics[0].Code != "component-prop-type" {
			t.Fatalf("diagnostics = %+v, want ordinary concrete assignment error", diagnostics)
		}
	})
}

func TestPlanComponentPositionalCallsUsesAdaptedNodeOperandDuringInference(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	el, fset := plannerElement(t, `<C item={n} label="x"/>`)
	pkg := types.NewPackage("example.test/page", "page")
	tp := types.NewTypeParam(types.NewTypeName(token.NoPos, pkg, "T", nil), types.NewInterfaceType(nil, nil).Complete())
	sig := types.NewSignatureType(nil, nil, []*types.TypeParam{tp}, types.NewTuple(
		types.NewVar(token.NoPos, pkg, "item", tp),
		types.NewVar(token.NoPos, pkg, "label", fx.runtime.node),
	), types.NewTuple(types.NewVar(token.NoPos, pkg, "", fx.runtime.node)), false)

	got, diagnostics := planComponentPositionalCalls(componentPositionalPlanningInput{
		callSites: positionalTestRegistry(el), targets: map[callSiteID]componentTargetFact{1: {site: 1, raw: sig, provenance: targetPackageFunc}},
		expressionFacts: map[gsxast.Node]expressionFact{el.Attrs[0]: {tv: types.TypeAndValue{Type: types.Typ[types.Int]}}},
		runtime:         fx.runtime, analysisPackage: pkg, fset: fset,
	})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	site := got.sites[1]
	if site.instance.TypeArgs == nil || site.instance.TypeArgs.Len() != 1 || !types.Identical(types.Unalias(site.instance.TypeArgs.At(0)), types.Typ[types.Int]) {
		t.Fatalf("inferred instance = %+v, want T=int", site.instance)
	}
	if op := site.operands[1]; op.adapter != componentAdapterNodeText || !types.Identical(op.tv.Type, fx.runtime.node) {
		t.Fatalf("label operand = %+v, want adapted NodeText fact", op)
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
	materialization := planComponentMaterialization(site.call, positionalMaterializationFacts(site.call, site.expressionFacts, site.runtime))
	if len(materialization.values) != 2 || materialization.values[0].temp == "" || materialization.values[1].temp == "" {
		t.Fatalf("crossing ordered values must both be pinned: %+v", materialization.values)
	}
	if fact, _ := site.expressionFacts.get(el.Attrs[1]); !fact.hasOrderedOperation {
		t.Fatalf("conditional attrs fact = %+v", fact)
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

	materializationFacts := positionalMaterializationFacts(plan, newExpressionFactSet(facts), fx.runtime)
	if got, _ := materializationFacts.get(embedded); got.tuple != nil || !types.Identical(got.tv.Type, types.Typ[types.String]) || !got.hasOrderedOperation {
		t.Fatalf("embedded lowering fact = %+v, want ordered string without outer tuple", got)
	}
	if got, _ := materializationFacts.get(pair); got.tuple != nil || !types.Identical(got.tv.Type, fx.runtime.attrs) || !got.hasOrderedOperation {
		t.Fatalf("attrs-pair lowering fact = %+v, want ordered attrs without outer tuple", got)
	}
	if got, _ := materializationFacts.get(plain); got.tuple == nil {
		t.Fatalf("plain expression tuple ownership moved away from outer materializer: %+v", got)
	}
}

func positionalTestRegistry(el *gsxast.Element) *callSiteRegistry {
	return &callSiteRegistry{
		byElement: map[*gsxast.Element]callSiteID{el: 1},
		records:   []callSiteRecord{{id: 1, path: "page.gsx", element: el, disposition: componentSitePlanned}},
	}
}

func plannerElements(t *testing.T, markup string) ([]*gsxast.Element, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	source := "package p\ncomponent Host() { " + markup + " }\n"
	file, err := gsxparser.ParseFile(fset, "planner.gsx", []byte(source), 0)
	if err != nil {
		t.Fatalf("parse planner markup: %v\n%s", err, source)
	}
	var elements []*gsxast.Element
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if element, ok := node.(*gsxast.Element); ok && element.Tag == "C" {
			elements = append(elements, element)
		}
		return true
	})
	return elements, fset
}
