package codegen

import (
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func TestPackagePublishesExactComponentCallFacts(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"page.gsx": `package views

import "github.com/gsxhq/gsx"

component Card(title string, someAttrs gsx.Attrs, attrs gsx.Attrs) {
	<div/>
}

component Page() {
	<Card title="ok" someAttrs={{"id": "ordinary"}} attrs={{"class": "reserved"}}/>
}
`,
	})

	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("unexpected diagnostics: %v", result.Diags)
	}
	if len(result.ComponentCalls) != 1 {
		t.Fatalf("component call facts = %d, want 1", len(result.ComponentCalls))
	}

	var call ComponentCallFact
	for _, candidate := range result.ComponentCalls {
		call = candidate
	}
	if call.Target == nil || call.Target.Name() != "Card" {
		t.Fatalf("target = %v, want Card object", call.Target)
	}
	if call.TargetPackage != "testmod" || call.TargetKey != ".Card" {
		t.Fatalf("target identity = (%q, %q), want (testmod, .Card)", call.TargetPackage, call.TargetKey)
	}
	if call.Signature == nil || call.Signature.Params().Len() != 3 {
		t.Fatalf("signature = %v, want three params", call.Signature)
	}
	if len(call.Params) != 3 {
		t.Fatalf("bound param facts = %d, want 3", len(call.Params))
	}

	want := map[string]struct {
		param string
		role  ComponentParamRole
	}{
		"title":     {param: "title", role: ComponentParamOrdinary},
		"someAttrs": {param: "someAttrs", role: ComponentParamOrdinary},
		"attrs":     {param: "attrs", role: ComponentParamAttrs},
	}
	for attr, param := range call.Params {
		name, ok := componentInputAttrName(attr)
		if !ok {
			t.Fatalf("published bound param for unnamed attr %T", attr)
		}
		expect, ok := want[name]
		if !ok {
			t.Fatalf("unexpected bound attr %q", name)
		}
		if param.Name != expect.param || param.Role != expect.role {
			t.Errorf("%s fact = {Name:%q Role:%v}, want {%q %v}", name, param.Name, param.Role, expect.param, expect.role)
		}
		if param.Var == nil || param.Origin == nil || param.Ordinal < 0 {
			t.Errorf("%s fact lacks semantic identity: %+v", name, param)
		}
		pos := result.Fset.Position(param.Origin.Pos())
		if filepath.Base(pos.Filename) != "page.gsx" {
			t.Errorf("%s origin position = %v, want page.gsx", name, pos)
		}
	}
}

func TestPackagePublishesExactComponentParameterFamilies(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"icon_a.gsx": `//go:build !never

package views

component Icon[T ~string](value T) { <span>{value}</span> }
`,
		"icon_b.gsx": `//go:build never

package views

component Icon[U ~string](value U) { <strong>{value}</strong> }
`,
		"page.gsx": `package views

component Page() { <Icon value="ok"/> }
`,
	})

	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("unexpected diagnostics: %v", result.Diags)
	}
	if len(result.ComponentParamDecls) != 1 {
		t.Fatalf("component parameter declarations = %+v, want one logical parameter", result.ComponentParamDecls)
	}
	decl := result.ComponentParamDecls[0]
	if decl.PackagePath != "testmod" || decl.ComponentKey != ".Icon" || decl.Ordinal != 0 {
		t.Fatalf("declaration key = (%q, %q, %d), want (testmod, .Icon, 0)", decl.PackagePath, decl.ComponentKey, decl.Ordinal)
	}
	if decl.Name != "value" || decl.Role != ComponentParamOrdinary || decl.Origin == nil {
		t.Fatalf("declaration identity = %+v, want ordinary value with origin", decl)
	}
	if len(decl.Decls) != 2 {
		t.Fatalf("variant declaration positions = %+v, want both variants", decl.Decls)
	}
	for _, pos := range decl.Decls {
		if filepath.Base(pos.Filename) != "icon_a.gsx" && filepath.Base(pos.Filename) != "icon_b.gsx" {
			t.Fatalf("unexpected declaration position: %+v", pos)
		}
	}

	if len(result.ComponentParamRefs) != 3 {
		t.Fatalf("component parameter refs = %+v, want both variant body uses and the invocation attr", result.ComponentParamRefs)
	}
	refFiles := map[string]int{}
	for _, ref := range result.ComponentParamRefs {
		if ref.PackagePath != decl.PackagePath || ref.ComponentKey != decl.ComponentKey || ref.Ordinal != decl.Ordinal || ref.Name != decl.Name {
			t.Fatalf("reference key = %+v, want declaration key %+v", ref, decl)
		}
		if ref.Origin == nil || ref.Origin != decl.Origin {
			t.Fatalf("reference origin = %p, declaration origin = %p; want generic origin normalization", ref.Origin, decl.Origin)
		}
		refFiles[filepath.Base(ref.Ref.Filename)]++
	}
	for _, filename := range []string{"icon_a.gsx", "icon_b.gsx", "page.gsx"} {
		if refFiles[filename] != 1 {
			t.Fatalf("reference files = %v, want one exact ref in %s", refFiles, filename)
		}
	}
}

func TestPackagePublishesSemanticComponentParameterBodyRefs(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"card.gsx": `package views

component Card(title string, items []string, limit int) {
	<div data-title={title}>
		{{ copied := title }}
		{ if title != "" { <p>{copied}</p> } }
		<ul>{ for _, title := range items { <li>{title}</li> } }</ul>
		<p>{ title |> truncate(limit) }</p>
	</div>
}
`,
	})
	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("unexpected diagnostics: %v", result.Diags)
	}
	counts := map[string]int{}
	for _, ref := range result.ComponentParamRefs {
		counts[ref.Name]++
		if filepath.Base(ref.Ref.Filename) != "card.gsx" {
			t.Fatalf("body ref position = %+v, want card.gsx", ref.Ref)
		}
	}
	if counts["title"] != 4 || counts["items"] != 1 || counts["limit"] != 1 {
		t.Fatalf("semantic body refs = %v, want title=4, items=1, limit=1; loop-local title must be excluded", counts)
	}
}

func TestFactoryComponentFactsUseStaticSignatureNamesAndPositions(t *testing.T) {
	const factorySource = `package views

import "github.com/gsxhq/gsx"

type NamedFactory func(name, label string) gsx.Node
type AliasFactory = func(name, label string) gsx.Node

func anonymousFactory() func(name, label string) gsx.Node {
	return func(first, second string) gsx.Node { return nil }
}
func namedFactory() NamedFactory {
	return func(first, second string) gsx.Node { return nil }
}
func aliasFactory() AliasFactory {
	return func(first, second string) gsx.Node { return nil }
}

var Anonymous = anonymousFactory()
var Named = namedFactory()
var Alias = aliasFactory()
`
	dir, module := openTestModule(t, map[string]string{
		"factory.go": factorySource,
		"page.gsx": `package views

component Page() {
	<Anonymous name="anonymous" label="value"/>
	<Named name="named" label="value"/>
	<Alias name="alias" label="value"/>
}
`,
	})

	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasDiagErrors(result.Diags) {
		t.Fatalf("unexpected diagnostics: %+v", result.Diags)
	}
	if len(result.ComponentCalls) != 3 {
		t.Fatalf("component calls = %d, want three factory values", len(result.ComponentCalls))
	}
	if len(result.ComponentParamDecls) != 0 {
		t.Fatalf("plain-Go factory parameters entered GSX rename declaration families: %+v", result.ComponentParamDecls)
	}

	staticOffsets := map[string]map[string]int{
		"Anonymous": {
			"name":  strings.Index(factorySource, "func anonymousFactory() func(name") + len("func anonymousFactory() func("),
			"label": strings.Index(factorySource, "func anonymousFactory() func(name, label") + len("func anonymousFactory() func(name, "),
		},
		"Named": {
			"name":  strings.Index(factorySource, "type NamedFactory func(name") + len("type NamedFactory func("),
			"label": strings.Index(factorySource, "type NamedFactory func(name, label") + len("type NamedFactory func(name, "),
		},
		"Alias": {
			"name":  strings.Index(factorySource, "type AliasFactory = func(name") + len("type AliasFactory = func("),
			"label": strings.Index(factorySource, "type AliasFactory = func(name, label") + len("type AliasFactory = func(name, "),
		},
	}
	for _, call := range result.ComponentCalls {
		if call.Target == nil {
			t.Fatal("factory call has no static target")
		}
		wantByName := staticOffsets[call.Target.Name()]
		if wantByName == nil {
			t.Fatalf("unexpected factory target %v", call.Target)
		}
		if len(call.Params) != 2 {
			t.Fatalf("%s params = %+v, want name and label", call.Target.Name(), call.Params)
		}
		for attr, param := range call.Params {
			name, ok := componentInputAttrName(attr)
			if !ok {
				t.Fatalf("%s has unnamed published attr %T", call.Target.Name(), attr)
			}
			if param.Name != name || param.Var == nil || param.Origin == nil {
				t.Fatalf("%s %s fact = %+v", call.Target.Name(), name, param)
			}
			position := result.Fset.Position(param.Origin.Pos())
			if filepath.Base(position.Filename) != "factory.go" || position.Offset != wantByName[name] {
				t.Errorf("%s %s origin = %+v, want factory.go offset %d", call.Target.Name(), name, position, wantByName[name])
			}
		}
	}
}

func TestFactoryComponentSignaturesRequireStaticParameterNames(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"factory.go": `package views

import "github.com/gsxhq/gsx"

type UnnamedFactory func(string, string) gsx.Node
type UnnamedAlias = func(string, string) gsx.Node

func anonymousUnnamedFactory() func(string, string) gsx.Node { return nil }
func namedUnnamedFactory() UnnamedFactory { return nil }
func aliasUnnamedFactory() UnnamedAlias { return nil }
func unnamedVariadicFactory() func(...gsx.Attr) gsx.Node { return nil }
func blankVariadicFactory() func(_ ...gsx.Attr) gsx.Node { return nil }

var AnonymousUnnamed = anonymousUnnamedFactory()
var NamedUnnamed = namedUnnamedFactory()
var AliasUnnamed = aliasUnnamedFactory()
var UnnamedVariadic = unnamedVariadicFactory()
var BlankVariadic = blankVariadicFactory()
`,
		"page.gsx": `package views

component Page() {
	<AnonymousUnnamed/>
	<NamedUnnamed/>
	<AliasUnnamed/>
	<UnnamedVariadic/>
	<BlankVariadic/>
}
`,
	})

	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	wantMessages := map[string]int{
		"function parameters must be named to be used as a component; parameter 0 is unnamed": 4,
		"function parameters must be named to be used as a component; parameter 0 is blank":   1,
	}
	var nameDiagnostics int
	for _, diagnostic := range result.Diags {
		if diagnostic.Code != "component-parameter-name" {
			continue
		}
		nameDiagnostics++
		matched := false
		for message := range wantMessages {
			if strings.Contains(diagnostic.Message, message) {
				wantMessages[message]--
				matched = true
			}
		}
		if !matched {
			t.Errorf("unexpected parameter-name diagnostic: %+v", diagnostic)
		}
	}
	if nameDiagnostics != 5 {
		t.Errorf("parameter-name diagnostics = %d, want 5; all diagnostics: %+v", nameDiagnostics, result.Diags)
	}
	for message, remaining := range wantMessages {
		if remaining != 0 {
			t.Errorf("diagnostic %q remaining count = %d; all diagnostics: %+v", message, remaining, result.Diags)
		}
	}

	for _, tag := range []string{"AnonymousUnnamed", "NamedUnnamed", "AliasUnnamed", "UnnamedVariadic", "BlankVariadic"} {
		var elements []*gsxast.Element
		for _, file := range result.GSXFiles {
			elements = append(elements, targetTestElements(file, tag)...)
		}
		if len(elements) != 1 {
			t.Errorf("<%s> elements = %d, want one", tag, len(elements))
			continue
		}
		if !elements[0].IsComponent {
			t.Errorf("<%s> lost semantic component identity before positioned signature validation", tag)
		}
	}
}

func TestComponentCallFactsRetainNamedParameterWithoutSourcePosition(t *testing.T) {
	param := types.NewVar(token.NoPos, nil, "name", types.Typ[types.String])
	model := componentSignatureModel{
		goSig: types.NewSignatureType(nil, nil, nil, types.NewTuple(param), types.NewTuple(), false),
		params: []componentParam{{
			variable: param,
			origin:   param.Origin(),
			name:     param.Name(),
			typ:      param.Type(),
			role:     roleProp,
		}},
	}
	element, fset := plannerElement(t, `<C name="value"/>`)
	call, diagnostics := planComponentInputs(1, element, model, fset)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	plan := componentPositionalPackagePlan{
		sites:     map[callSiteID]componentPositionalSitePlan{1: {call: call, signature: model}},
		byElement: map[*gsxast.Element]callSiteID{element: 1},
	}
	fact := componentCallFacts(plan)[element]
	if len(fact.Params) != 1 {
		t.Fatalf("params = %+v, want retained export-only name", fact.Params)
	}
	for _, got := range fact.Params {
		if got.Name != "name" || got.Var != param || got.Origin != param || got.Origin.Pos().IsValid() {
			t.Fatalf("no-position param fact = %+v, want named identity without a source position", got)
		}
	}
}
