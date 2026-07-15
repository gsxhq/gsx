package codegen

import (
	"go/token"
	"go/types"
	"slices"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
)

type plannerParam struct {
	name string
	role paramRole
}

func plannerTarget(params ...plannerParam) componentSignatureModel {
	vars := make([]*types.Var, len(params))
	model := componentSignatureModel{params: make([]componentParam, len(params))}
	for i, param := range params {
		typ := types.Type(types.Typ[types.String])
		if param.role == roleAttrs || param.role == roleGoOnlyVariadic {
			typ = types.NewSlice(typ)
		}
		variable := types.NewParam(token.NoPos, nil, param.name, typ)
		vars[i] = variable
		model.params[i] = componentParam{
			variable: variable,
			origin:   variable.Origin(),
			name:     param.name,
			typ:      typ,
			index:    i,
			role:     param.role,
		}
	}
	model.goSig = types.NewSignatureType(nil, nil, nil, types.NewTuple(vars...), types.NewTuple(), len(params) > 0 && params[len(params)-1].role == roleGoOnlyVariadic)
	return model
}

func plannerElement(t *testing.T, markup string) (*gsxast.Element, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	source := "package p\ncomponent Host() { " + markup + " }\n"
	file, err := gsxparser.ParseFile(fset, "planner.gsx", []byte(source), 0)
	if err != nil {
		t.Fatalf("parse planner markup: %v\n%s", err, source)
	}
	var found *gsxast.Element
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		el, ok := node.(*gsxast.Element)
		if ok && el.Tag == "C" && found == nil {
			found = el
		}
		return true
	})
	if found == nil {
		t.Fatal("planner markup has no <C> element")
	}
	return found, fset
}

func TestPlanComponentInputsExactNamesAndOrdinaryCapitalizedParams(t *testing.T) {
	el, fset := plannerElement(t, `<C title="hello" _foo={value} Attrs={capitalAttrs} Children={capitalChildren}/>`)
	target := plannerTarget(
		plannerParam{name: "title", role: roleProp},
		plannerParam{name: "_foo", role: roleProp},
		plannerParam{name: "Attrs", role: roleProp},
		plannerParam{name: "Children", role: roleProp},
	)
	plan, diagnostics := planComponentInputs(7, el, target, fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if plan.site != 7 || len(plan.args) != 4 || len(plan.values) != 4 {
		t.Fatalf("plan shape = site %d args %d values %d", plan.site, len(plan.args), len(plan.values))
	}
	for i, value := range plan.values {
		if value.kind != componentInputProp || value.sourceIndex != i || value.paramIndex != i || value.contributorIndex != -1 {
			t.Errorf("value %d = %+v, want exact prop route", i, value)
		}
		if plan.args[i].omitted || !slices.Equal(plan.args[i].valueIndexes, []int{i}) {
			t.Errorf("arg %d = %+v, want supplied value %d", i, plan.args[i], i)
		}
	}
}

func TestPlanComponentInputsRetainsAuthoredCallNode(t *testing.T) {
	el, fset := plannerElement(t, `<C/>`)
	plan, diagnostics := planComponentInputs(17, el, plannerTarget(plannerParam{name: "title", role: roleProp}), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if plan.call != el {
		t.Fatalf("call = %p, want authored element %p", plan.call, el)
	}
	if plan.callStart != fset.Position(el.Pos()) || plan.callEnd != fset.Position(el.End()) {
		t.Fatalf("resolved call range = %v..%v, want %v..%v", plan.callStart, plan.callEnd, fset.Position(el.Pos()), fset.Position(el.End()))
	}
	if len(plan.args) != 1 || !plan.args[0].omitted {
		t.Fatalf("omitted prop plan = %+v", plan.args)
	}
	if plan.call.Pos() != el.Pos() || plan.call.End() != el.End() || plan.call.TagPos != el.TagPos {
		t.Fatalf("retained call range = %v..%v tag=%v, want %v..%v tag=%v", plan.call.Pos(), plan.call.End(), plan.call.TagPos, el.Pos(), el.End(), el.TagPos)
	}
}

func TestPlanComponentInputsCapitalizedNamesHaveNoLegacyBranch(t *testing.T) {
	t.Run("ordinary fallthrough", func(t *testing.T) {
		el, fset := plannerElement(t, `<C Attrs={a} Children={n}/>`)
		plan, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "attrs", role: roleAttrs}), fset)
		if len(diagnostics) != 0 {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
		if len(plan.values) != 2 || plan.values[0].kind != componentInputAttrsPair || plan.values[1].kind != componentInputAttrsPair {
			t.Fatalf("values = %+v, want ordinary fallthrough pairs", plan.values)
		}
	})

	t.Run("ordinary missing attrs", func(t *testing.T) {
		el, fset := plannerElement(t, `<C Attrs={a}/>`)
		_, diagnostics := planComponentInputs(1, el, plannerTarget(), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"component-missing-attrs"}) {
			t.Fatalf("diagnostic codes = %v, want ordinary missing attrs", got)
		}
	})
}

func TestPlanComponentInputsDuplicatePropIsPositioned(t *testing.T) {
	el, fset := plannerElement(t, `<C title="first" title="second"/>`)
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "title", role: roleProp}), fset)
	if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"duplicate-prop"}) {
		t.Fatalf("diagnostic codes = %v, want duplicate-prop", got)
	}
	if diagnostics[0].Start != fset.Position(el.Attrs[1].Pos()) || diagnostics[0].End != fset.Position(el.Attrs[1].End()) {
		t.Fatalf("diagnostic range = %v..%v, want second attr %v..%v", diagnostics[0].Start, diagnostics[0].End, fset.Position(el.Attrs[1].Pos()), fset.Position(el.Attrs[1].End()))
	}
	if len(plan.values) != 1 || plan.args[0].omitted || !slices.Equal(plan.args[0].valueIndexes, []int{0}) {
		t.Fatalf("duplicate changed first binding: %+v", plan)
	}
}

func TestPlanComponentInputsStrictReservedRoles(t *testing.T) {
	t.Run("non identifier needs attrs", func(t *testing.T) {
		el, fset := plannerElement(t, `<C data-x="value"/>`)
		_, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "data", role: roleProp}), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"component-missing-attrs"}) {
			t.Fatalf("diagnostic codes = %v", got)
		}
	})

	t.Run("body needs children", func(t *testing.T) {
		el, fset := plannerElement(t, `<C><i/></C>`)
		_, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "attrs", role: roleAttrs}), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"component-missing-children"}) {
			t.Fatalf("diagnostic codes = %v", got)
		}
	})

	t.Run("children is body only", func(t *testing.T) {
		el, fset := plannerElement(t, `<C children={<i/>}/>`)
		_, diagnostics := planComponentInputs(1, el, plannerTarget(
			plannerParam{name: "children", role: roleChildren},
			plannerParam{name: "attrs", role: roleAttrs},
		), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"reserved-input-form"}) {
			t.Fatalf("diagnostic codes = %v", got)
		}
	})

	for _, markup := range []string{`<C attrs="x"/>`, `<C attrs/>`, `<C attrs={<i/>}/>`} {
		t.Run(markup, func(t *testing.T) {
			el, fset := plannerElement(t, markup)
			_, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "attrs", role: roleAttrs}), fset)
			if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"reserved-input-form"}) {
				t.Fatalf("diagnostic codes = %v", got)
			}
		})
	}

	t.Run("braced embedded literal is an expression contributor", func(t *testing.T) {
		el, fset := plannerElement(t, "<C attrs={f`not-a-bag`}/>")
		plan, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "attrs", role: roleAttrs}), fset)
		if len(diagnostics) != 0 {
			t.Fatalf("diagnostics = %+v; semantic bag typing belongs to Task 5", diagnostics)
		}
		if len(plan.values) != 1 || plan.values[0].kind != componentInputAttrsContributor {
			t.Fatalf("plan values = %+v, want braced expression contributor", plan.values)
		}
	})

	t.Run("bare embedded literal is not attrs expression syntax", func(t *testing.T) {
		el, fset := plannerElement(t, "<C attrs=f`not-a-bag`/>")
		_, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "attrs", role: roleAttrs}), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"reserved-input-form"}) {
			t.Fatalf("diagnostic codes = %v", got)
		}
	})
}

func TestPlanComponentInputsReportsEveryMissingAttrsContributor(t *testing.T) {
	el, fset := plannerElement(t, `<C id="x" class="y" {bag...} attrs={more}/>`)
	_, diagnostics := planComponentInputs(1, el, plannerTarget(), fset)
	if len(diagnostics) != len(el.Attrs) {
		t.Fatalf("diagnostics = %+v, want one for each of %d attrs contributors", diagnostics, len(el.Attrs))
	}
	for i, diagnostic := range diagnostics {
		if diagnostic.Code != "component-missing-attrs" {
			t.Errorf("diagnostic %d code = %q, want component-missing-attrs", i, diagnostic.Code)
		}
		if diagnostic.Start != fset.Position(el.Attrs[i].Pos()) || diagnostic.End != fset.Position(el.Attrs[i].End()) {
			t.Errorf("diagnostic %d range = %v..%v, want %v..%v", i, diagnostic.Start, diagnostic.End, fset.Position(el.Attrs[i].Pos()), fset.Position(el.Attrs[i].End()))
		}
	}
}

func TestPlanComponentInputsAttrsStreamKeepsAuthoredOrder(t *testing.T) {
	el, fset := plannerElement(t, `<C title="x" attrs={base} id="first" attrs={{ "id": second }} {spread...} { if ok { title="bag" } } attrs={{ "role": third }}/>`)
	plan, diagnostics := planComponentInputs(9, el, plannerTarget(
		plannerParam{name: "title", role: roleProp},
		plannerParam{name: "attrs", role: roleAttrs},
	), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	wantKinds := []componentInputKind{
		componentInputProp,
		componentInputAttrsContributor,
		componentInputAttrsPair,
		componentInputAttrsContributor,
		componentInputAttrsSegment,
		componentInputAttrsSegment,
		componentInputAttrsContributor,
	}
	wantContributors := []int{-1, 0, 1, 2, 3, 4, 5}
	wantStreamKinds := []componentAttrsStreamKind{
		componentAttrsStreamContributor,
		componentAttrsStreamPair,
		componentAttrsStreamContributor,
		componentAttrsStreamSpread,
		componentAttrsStreamConditional,
		componentAttrsStreamContributor,
	}
	if len(plan.values) != len(wantKinds) {
		t.Fatalf("values = %+v", plan.values)
	}
	for i, value := range plan.values {
		if value.kind != wantKinds[i] || value.sourceIndex != i || value.contributorIndex != wantContributors[i] {
			t.Errorf("value %d = %+v, want kind=%v source=%d contributor=%d", i, value, wantKinds[i], i, wantContributors[i])
		}
		if i == 0 {
			if value.attrsNode != nil {
				t.Errorf("ordinary prop has attrs stream node: %+v", value.attrsNode)
			}
			continue
		}
		if value.attrsNode == nil || value.attrsNode.kind != wantStreamKinds[i-1] || value.attrsNode.attr != value.node {
			t.Errorf("value %d attrs node = %+v, want kind %v and exact authored attr", i, value.attrsNode, wantStreamKinds[i-1])
		}
	}
	if !slices.Equal(plan.args[0].valueIndexes, []int{0}) || !slices.Equal(plan.args[1].valueIndexes, []int{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("arg routing = %+v", plan.args)
	}
	if plan.args[0].omitted || plan.args[1].omitted {
		t.Fatalf("supplied args marked omitted: %+v", plan.args)
	}
}

func TestPlanComponentInputsConditionalAttrsBuildsRecursiveStream(t *testing.T) {
	el, fset := plannerElement(t, `<C id="before" { if ok { title="inside" attrs={base} data-x="x" {inside...} { if nested { attrs={{ "k": value }} } else { role="button" } } } else { attrs={{ "e": other }} } } id="after"/>`)
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(
		plannerParam{name: "title", role: roleProp},
		plannerParam{name: "attrs", role: roleAttrs},
	), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(plan.values) != 3 {
		t.Fatalf("values = %+v, want pair, one conditional segment, pair", plan.values)
	}
	if !plan.args[0].omitted {
		t.Fatalf("conditional title filled ordinary prop: %+v", plan.args[0])
	}
	middle := plan.values[1]
	if middle.kind != componentInputAttrsSegment || middle.sourceIndex != 1 || middle.contributorIndex != 1 || middle.attrsNode == nil {
		t.Fatalf("conditional top-level record = %+v", middle)
	}
	root := middle.attrsNode
	if root.kind != componentAttrsStreamConditional || root.sourceIndex != 1 || root.attr != el.Attrs[1] {
		t.Fatalf("conditional stream root = %+v", root)
	}
	cond := el.Attrs[1].(*gsxast.CondAttr)
	for i, node := range root.then {
		if node.attr != cond.Then[i] {
			t.Errorf("then node %d attr = %p, want authored attr %p", i, node.attr, cond.Then[i])
		}
	}
	for i, node := range root.otherwise {
		if node.attr != cond.Else[i] {
			t.Errorf("else node %d attr = %p, want authored attr %p", i, node.attr, cond.Else[i])
		}
	}
	if got := attrsStreamKinds(root.then); !slices.Equal(got, []componentAttrsStreamKind{
		componentAttrsStreamPair,
		componentAttrsStreamContributor,
		componentAttrsStreamPair,
		componentAttrsStreamSpread,
		componentAttrsStreamConditional,
	}) {
		t.Fatalf("then kinds = %v", got)
	}
	if got := attrsStreamSourceIndexes(root.then); !slices.Equal(got, []int{0, 1, 2, 3, 4}) {
		t.Fatalf("then source indexes = %v", got)
	}
	if got := attrsStreamKinds(root.otherwise); !slices.Equal(got, []componentAttrsStreamKind{componentAttrsStreamContributor}) {
		t.Fatalf("else kinds = %v", got)
	}
	nested := root.then[4]
	if got := attrsStreamKinds(nested.then); !slices.Equal(got, []componentAttrsStreamKind{componentAttrsStreamContributor}) {
		t.Fatalf("nested then kinds = %v", got)
	}
	if got := attrsStreamKinds(nested.otherwise); !slices.Equal(got, []componentAttrsStreamKind{componentAttrsStreamPair}) {
		t.Fatalf("nested else kinds = %v", got)
	}
}

func TestPlanComponentInputsConditionalReservedFormsAreValidated(t *testing.T) {
	for _, markup := range []string{
		`<C { if ok { children={node} } }/>`,
		`<C { if ok { attrs="bad" } }/>`,
		`<C { if ok { attrs } }/>`,
		`<C { if ok { attrs={<i/>} } }/>`,
	} {
		t.Run(markup, func(t *testing.T) {
			el, fset := plannerElement(t, markup)
			_, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "attrs", role: roleAttrs}), fset)
			if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"reserved-input-form"}) {
				t.Fatalf("diagnostic codes = %v", got)
			}
		})
	}
}

func TestPlanComponentInputsConditionalMissingAttrsReportsEveryLeaf(t *testing.T) {
	el, fset := plannerElement(t, `<C { if ok { id="x" attrs={bag} } else { {spread...} } }/>`)
	cond := el.Attrs[0].(*gsxast.CondAttr)
	wantNodes := []gsxast.Node{cond.Then[0], cond.Then[1], cond.Else[0]}
	_, diagnostics := planComponentInputs(1, el, plannerTarget(), fset)
	if len(diagnostics) != len(wantNodes) {
		t.Fatalf("diagnostics = %+v, want one missing-attrs error per branch leaf", diagnostics)
	}
	for i, node := range wantNodes {
		if diagnostics[i].Code != "component-missing-attrs" || diagnostics[i].Start != fset.Position(node.Pos()) || diagnostics[i].End != fset.Position(node.End()) {
			t.Errorf("diagnostic %d = %+v, want %v..%v", i, diagnostics[i], fset.Position(node.Pos()), fset.Position(node.End()))
		}
	}
}

func TestPlanComponentInputsLeaflessConditionalIsAnAttrsContributor(t *testing.T) {
	t.Run("retained with attrs role", func(t *testing.T) {
		el, fset := plannerElement(t, `<C { if outer { { if inner { /* source only */ } } } }/>`)
		plan, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "attrs", role: roleAttrs}), fset)
		if len(diagnostics) != 0 {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
		if len(plan.values) != 1 || plan.values[0].attrsNode == nil {
			t.Fatalf("values = %+v, want one retained conditional contributor", plan.values)
		}
		outer := plan.values[0].attrsNode
		if outer.kind != componentAttrsStreamConditional || len(outer.then) != 1 || outer.then[0].kind != componentAttrsStreamConditional {
			t.Fatalf("conditional tree = %+v", outer)
		}
		if len(outer.then[0].then) != 0 || len(outer.then[0].otherwise) != 0 {
			t.Fatalf("comment-only inner conditional retained value leaves: %+v", outer.then[0])
		}
	})

	t.Run("strict without attrs role", func(t *testing.T) {
		el, fset := plannerElement(t, `<C { if ok { /* source only */ } }/>`)
		conditional := el.Attrs[0]
		_, diagnostics := planComponentInputs(1, el, plannerTarget(), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"component-missing-attrs"}) {
			t.Fatalf("diagnostic codes = %v", got)
		}
		if diagnostics[0].Start != fset.Position(conditional.Pos()) || diagnostics[0].End != fset.Position(conditional.End()) {
			t.Fatalf("diagnostic range = %v..%v, want leafless conditional %v..%v", diagnostics[0].Start, diagnostics[0].End, fset.Position(conditional.Pos()), fset.Position(conditional.End()))
		}
	})

	t.Run("nested leafless reports only the inner conditional", func(t *testing.T) {
		el, fset := plannerElement(t, `<C { if outer { { if inner { } } } }/>`)
		outer := el.Attrs[0].(*gsxast.CondAttr)
		inner := outer.Then[0]
		_, diagnostics := planComponentInputs(1, el, plannerTarget(), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"component-missing-attrs"}) {
			t.Fatalf("diagnostic codes = %v", got)
		}
		if diagnostics[0].Start != fset.Position(inner.Pos()) || diagnostics[0].End != fset.Position(inner.End()) {
			t.Fatalf("diagnostic range = %v..%v, want inner conditional %v..%v", diagnostics[0].Start, diagnostics[0].End, fset.Position(inner.Pos()), fset.Position(inner.End()))
		}
	})

	t.Run("invalid reserved leaf does not cascade", func(t *testing.T) {
		el, fset := plannerElement(t, `<C { if ok { attrs="bad" } }/>`)
		_, diagnostics := planComponentInputs(1, el, plannerTarget(), fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"reserved-input-form"}) {
			t.Fatalf("diagnostic codes = %v, want only the precise reserved-form error", got)
		}
	})
}

func attrsStreamKinds(nodes []componentAttrsStreamNode) []componentAttrsStreamKind {
	kinds := make([]componentAttrsStreamKind, len(nodes))
	for i, node := range nodes {
		kinds[i] = node.kind
	}
	return kinds
}

func attrsStreamSourceIndexes(nodes []componentAttrsStreamNode) []int {
	indexes := make([]int, len(nodes))
	for i, node := range nodes {
		indexes[i] = node.sourceIndex
	}
	return indexes
}

func TestPlanComponentInputsOrdinaryAttrsValuesRemainProps(t *testing.T) {
	el, fset := plannerElement(t, `<C someAttrs={{ "aria-label": label }} otherAttrs={computed} header={<h1/>} class={ "a" } style={ "color:red" }/>`)
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(
		plannerParam{name: "someAttrs", role: roleProp},
		plannerParam{name: "otherAttrs", role: roleProp},
		plannerParam{name: "header", role: roleProp},
		plannerParam{name: "class", role: roleProp},
		plannerParam{name: "style", role: roleProp},
	), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(plan.values) != 5 {
		t.Fatalf("values = %+v", plan.values)
	}
	if plan.values[0].kind != componentInputProp || plan.values[0].paramIndex != 0 {
		t.Fatalf("ordinary ordered bag = %+v, want one prop-owned literal", plan.values[0])
	}
	for i := 1; i < len(plan.values); i++ {
		if plan.values[i].kind != componentInputProp || plan.values[i].paramIndex != i {
			t.Errorf("ordinary value %d = %+v", i, plan.values[i])
		}
	}
}

func TestPlanComponentInputsKeepsEmptyAndMultiPairOrderedLiterals(t *testing.T) {
	el, fset := plannerElement(t, `<C someAttrs={{ }} attrs={{ }} attrs={{ "a": first, "b": second }}/>`)
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(
		plannerParam{name: "someAttrs", role: roleProp},
		plannerParam{name: "attrs", role: roleAttrs},
	), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(plan.values) != 3 {
		t.Fatalf("values = %+v, want one record per authored ordered literal", plan.values)
	}
	wantKinds := []componentInputKind{componentInputProp, componentInputAttrsContributor, componentInputAttrsContributor}
	for i, value := range plan.values {
		literal, ok := value.node.(*gsxast.OrderedAttrsAttr)
		if !ok || value.kind != wantKinds[i] || value.sourceIndex != i {
			t.Errorf("value %d = %+v, want ordered literal kind %v", i, value, wantKinds[i])
			continue
		}
		wantPairs := 0
		if i == 2 {
			wantPairs = 2
		}
		if len(literal.Pairs) != wantPairs {
			t.Errorf("value %d literal pairs = %d, want %d", i, len(literal.Pairs), wantPairs)
		}
	}
	if !slices.Equal(plan.args[0].valueIndexes, []int{0}) || !slices.Equal(plan.args[1].valueIndexes, []int{1, 2}) {
		t.Fatalf("arg routing = %+v", plan.args)
	}
}

func TestPlanComponentInputsSpreadIsSyntaxOnlyAndConditionalNamesDoNotBind(t *testing.T) {
	el, fset := plannerElement(t, `<C {looksLikeStruct...} { if ok { title="inside" } }/>`)
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(
		plannerParam{name: "title", role: roleProp},
		plannerParam{name: "attrs", role: roleAttrs},
	), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if !plan.args[0].omitted || len(plan.args[0].valueIndexes) != 0 {
		t.Fatalf("conditional branch filled ordinary title: %+v", plan.args[0])
	}
	if len(plan.values) != 2 || plan.values[0].kind != componentInputAttrsSegment || plan.values[1].kind != componentInputAttrsSegment {
		t.Fatalf("values = %+v, want two attrs segments", plan.values)
	}
}

func TestPlanComponentInputsOrdinaryVariadicIsGoOnly(t *testing.T) {
	target := plannerTarget(plannerParam{name: "xs", role: roleGoOnlyVariadic})
	t.Run("omitted", func(t *testing.T) {
		el, fset := plannerElement(t, `<C/>`)
		plan, diagnostics := planComponentInputs(1, el, target, fset)
		if len(diagnostics) != 0 || len(plan.args) != 1 || !plan.args[0].omitted || len(plan.values) != 0 {
			t.Fatalf("plan=%+v diagnostics=%+v", plan, diagnostics)
		}
	})
	t.Run("named fill rejected", func(t *testing.T) {
		el, fset := plannerElement(t, `<C xs={items}/>`)
		_, diagnostics := planComponentInputs(1, el, target, fset)
		if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"ordinary-variadic-prop"}) {
			t.Fatalf("diagnostic codes = %v", got)
		}
	})
	t.Run("blank name is not bindable", func(t *testing.T) {
		el, fset := plannerElement(t, `<C _={items}/>`)
		plan, diagnostics := planComponentInputs(1, el, plannerTarget(
			plannerParam{name: "attrs", role: roleAttrs},
			plannerParam{name: "_", role: roleGoOnlyVariadic},
		), fset)
		if len(diagnostics) != 0 {
			t.Fatalf("diagnostics = %+v", diagnostics)
		}
		if !plan.args[1].omitted || len(plan.args[1].valueIndexes) != 0 || len(plan.args[0].valueIndexes) != 1 || plan.values[0].kind != componentInputAttrsPair {
			t.Fatalf("blank variadic unexpectedly bound: %+v", plan)
		}
	})
}

func TestPlanComponentInputsBodyFillsDeclaredChildren(t *testing.T) {
	el, fset := plannerElement(t, `<C title="x"><i/><b/></C>`)
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(
		plannerParam{name: "title", role: roleProp},
		plannerParam{name: "children", role: roleChildren},
	), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(plan.values) != 2 || plan.values[1].kind != componentInputBody || plan.values[1].sourceIndex != len(el.Attrs) || plan.values[1].node != el {
		t.Fatalf("body plan = %+v", plan)
	}
	if plan.args[1].omitted || !slices.Equal(plan.args[1].valueIndexes, []int{1}) {
		t.Fatalf("children arg = %+v", plan.args[1])
	}
}

func TestPlanComponentInputsCommentOnlyBodyIsAbsent(t *testing.T) {
	el, fset := plannerElement(t, `<C>{/* source only */}</C>`)
	if len(el.Children) != 1 {
		t.Fatalf("parser children = %d, want one source-only comment", len(el.Children))
	}
	if _, ok := el.Children[0].(*gsxast.Comment); !ok {
		t.Fatalf("child = %T, want *ast.Comment", el.Children[0])
	}
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("comment-only body diagnostics = %+v", diagnostics)
	}
	if len(plan.values) != 0 {
		t.Fatalf("comment-only body values = %+v", plan.values)
	}
}

func TestPlanComponentInputsBodyRetainsOnlySemanticChildren(t *testing.T) {
	el, fset := plannerElement(t, `<C><i/>{/* source only */}<b/></C>`)
	if len(el.Children) != 3 {
		t.Fatalf("parser children = %d, want element/comment/element", len(el.Children))
	}
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "children", role: roleChildren}), fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(plan.values) != 1 || plan.values[0].kind != componentInputBody {
		t.Fatalf("body plan = %+v", plan.values)
	}
	want := []gsxast.Markup{el.Children[0], el.Children[2]}
	if !slices.Equal(plan.values[0].children, want) {
		t.Fatalf("semantic children = %v, want %v", plan.values[0].children, want)
	}
	if got := componentSemanticChildren(el.Children); !slices.Equal(got, want) {
		t.Fatalf("shared semantic-child filter = %v, want %v", got, want)
	}
}

func TestPlanComponentInputsRequiresFileSet(t *testing.T) {
	el, _ := plannerElement(t, `<C title="x"/>`)
	plan, diagnostics := planComponentInputs(1, el, plannerTarget(plannerParam{name: "title", role: roleProp}), nil)
	if got := diagnosticCodes(diagnostics); !slices.Equal(got, []string{"component-call-plan"}) {
		t.Fatalf("diagnostic codes = %v, want internal fail-closed diagnostic", got)
	}
	if len(plan.values) != 0 {
		t.Fatalf("planner continued without a FileSet: %+v", plan)
	}
}

func diagnosticCodes(diagnostics []diag.Diagnostic) []string {
	codes := make([]string, len(diagnostics))
	for i, diagnostic := range diagnostics {
		codes[i] = diagnostic.Code
	}
	return codes
}

func TestValidateComponentOperands(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)

	newPlan := func(values ...componentInputValue) componentCallPlan {
		return componentCallPlan{values: values}
	}

	t.Run("gsx.Attrs spread is a valid contributor", func(t *testing.T) {
		spread := &gsxast.SpreadAttr{Expr: "bag"}
		plan := newPlan(componentInputValue{
			kind:      componentInputAttrsSegment,
			node:      spread,
			attrsNode: &componentAttrsStreamNode{kind: componentAttrsStreamSpread},
		})
		facts := map[gsxast.Node]expressionFact{spread: {tv: types.TypeAndValue{Type: fx.runtime.attrs}}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 0 {
			t.Fatalf("gsx.Attrs spread must validate, got %+v", diags)
		}
	})

	t.Run("[]gsx.Attr spread is a valid contributor", func(t *testing.T) {
		spread := &gsxast.SpreadAttr{Expr: "bag"}
		plan := newPlan(componentInputValue{
			kind:      componentInputAttrsSegment,
			node:      spread,
			attrsNode: &componentAttrsStreamNode{kind: componentAttrsStreamSpread},
		})
		facts := map[gsxast.Node]expressionFact{spread: {tv: types.TypeAndValue{Type: types.NewSlice(fx.runtime.attr)}}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 0 {
			t.Fatalf("[]gsx.Attr spread must validate, got %+v", diags)
		}
	})

	t.Run("struct spread is rejected as a non-bag type", func(t *testing.T) {
		user := types.NewPackage("example.test/user", "user")
		props := types.NewNamed(types.NewTypeName(token.NoPos, user, "Props", nil), types.NewStruct(nil, nil), nil)
		spread := &gsxast.SpreadAttr{Expr: "props"}
		plan := newPlan(componentInputValue{
			kind:      componentInputAttrsSegment,
			node:      spread,
			attrsNode: &componentAttrsStreamNode{kind: componentAttrsStreamSpread},
		})
		facts := map[gsxast.Node]expressionFact{spread: {tv: types.TypeAndValue{Type: props}}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 1 || diags[0].Code != "component-attrs-spread-type" {
			t.Fatalf("struct splat must be rejected, got %+v", diags)
		}
	})

	t.Run("attrs={expr} contributor must be a bag", func(t *testing.T) {
		contributor := &gsxast.ExprAttr{Name: "attrs", Expr: "notABag"}
		plan := newPlan(componentInputValue{
			kind:      componentInputAttrsContributor,
			node:      contributor,
			attrsNode: &componentAttrsStreamNode{kind: componentAttrsStreamContributor},
		})
		facts := map[gsxast.Node]expressionFact{contributor: {tv: types.TypeAndValue{Type: types.Typ[types.String]}}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 1 || diags[0].Code != "component-attrs-spread-type" {
			t.Fatalf("non-bag attrs contributor must be rejected, got %+v", diags)
		}
	})

	t.Run("(int, error) attrs contributor is rejected as a non-bag", func(t *testing.T) {
		contributor := &gsxast.ExprAttr{Name: "attrs", Expr: "load()"}
		tuple := types.NewTuple(
			types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]),
			types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
		)
		plan := newPlan(componentInputValue{
			kind:      componentInputAttrsContributor,
			node:      contributor,
			attrsNode: &componentAttrsStreamNode{kind: componentAttrsStreamContributor},
		})
		facts := map[gsxast.Node]expressionFact{contributor: {tv: types.TypeAndValue{Type: tuple}, tuple: tuple}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 1 || diags[0].Code != "component-attrs-spread-type" {
			t.Fatalf("(int, error) attrs contributor must be rejected as a non-bag, got %+v", diags)
		}
	})

	t.Run("(gsx.Attrs, error) attrs contributor is accepted", func(t *testing.T) {
		contributor := &gsxast.ExprAttr{Name: "attrs", Expr: "load()"}
		tuple := types.NewTuple(
			types.NewVar(token.NoPos, nil, "", fx.runtime.attrs),
			types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
		)
		plan := newPlan(componentInputValue{
			kind:      componentInputAttrsContributor,
			node:      contributor,
			attrsNode: &componentAttrsStreamNode{kind: componentAttrsStreamContributor},
		})
		facts := map[gsxast.Node]expressionFact{contributor: {tv: types.TypeAndValue{Type: tuple}, tuple: tuple}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 0 {
			t.Fatalf("(gsx.Attrs, error) attrs contributor must validate, got %+v", diags)
		}
	})

	t.Run("(T, error) prop is consumed as one value", func(t *testing.T) {
		prop := &gsxast.ExprAttr{Name: "title", Expr: "load()"}
		tuple := types.NewTuple(
			types.NewVar(token.NoPos, nil, "", types.Typ[types.String]),
			types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
		)
		plan := newPlan(componentInputValue{kind: componentInputProp, node: prop})
		facts := map[gsxast.Node]expressionFact{prop: {tv: types.TypeAndValue{Type: tuple}, tuple: tuple}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 0 {
			t.Fatalf("(T, error) prop must validate, got %+v", diags)
		}
	})

	t.Run("non (T, error) tuple prop is rejected", func(t *testing.T) {
		prop := &gsxast.ExprAttr{Name: "title", Expr: "pair()"}
		tuple := types.NewTuple(
			types.NewVar(token.NoPos, nil, "", types.Typ[types.String]),
			types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]),
		)
		plan := newPlan(componentInputValue{kind: componentInputProp, node: prop})
		facts := map[gsxast.Node]expressionFact{prop: {tv: types.TypeAndValue{Type: tuple}, tuple: tuple}}
		_, diags := validateComponentOperands(plan, facts, fx.runtime)
		if len(diags) != 1 || diags[0].Code != "invalid-tuple" {
			t.Fatalf("non-(T,error) tuple must be rejected, got %+v", diags)
		}
	})
}
