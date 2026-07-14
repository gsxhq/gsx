package codegen

import (
	"go/token"
	"path/filepath"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func parseTargetTestFile(t *testing.T, fset *token.FileSet, path, src string) *gsxast.File {
	t.Helper()
	file, err := gsxparser.ParseFile(fset, path, []byte(src), 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

// preprocessComponentCallSites keeps semantic tests concise while production
// obtains its private owner only from parsePackageWithFset. Tests that exercise
// lifecycle reuse hold one parsedGSXPackage explicitly instead of constructing
// a fresh owner through this helper.
func preprocessComponentCallSites(files map[string]*gsxast.File, declNames map[string]bool, fset *token.FileSet, classifier *attrclass.Classifier, bag *diag.Bag) (callSitePreprocessResult, error) {
	name := ""
	for _, file := range files {
		if file != nil {
			name = file.Package
			break
		}
	}
	return newParsedGSXPackage(name, files).preprocessComponentCallSites(declNames, fset, classifier, bag)
}

func targetTestElements(file *gsxast.File, tag string) []*gsxast.Element {
	var out []*gsxast.Element
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if el, ok := node.(*gsxast.Element); ok && el.Tag == tag {
			out = append(out, el)
		}
		return true
	})
	return out
}

func targetTestEmbeddedElements(parts []gsxast.GoPart, out *[]*gsxast.Element) {
	var walk func([]gsxast.Markup)
	walk = func(nodes []gsxast.Markup) {
		for _, node := range nodes {
			switch node := node.(type) {
			case *gsxast.Element:
				*out = append(*out, node)
				walk(node.Children)
			case *gsxast.Fragment:
				walk(node.Children)
			case *gsxast.Interp:
				targetTestEmbeddedElements(node.Embedded, out)
			case *gsxast.EmbeddedInterp:
				walk(node.Segments)
			case *gsxast.ForMarkup:
				walk(node.Body)
			case *gsxast.IfMarkup:
				walk(node.Then)
				walk(node.Else)
			case *gsxast.SwitchMarkup:
				for _, clause := range node.Cases {
					walk(clause.Body)
				}
			}
		}
	}
	for _, part := range parts {
		if markup, ok := part.(gsxast.Markup); ok {
			walk([]gsxast.Markup{markup})
		}
	}
}

func registryRecordFor(t *testing.T, registry *callSiteRegistry, el *gsxast.Element) callSiteRecord {
	t.Helper()
	id, ok := registry.byElement[el]
	if !ok {
		t.Fatalf("element <%s> %p has no call-site ID", el.Tag, el)
	}
	if id == invalidCallSiteID || int(id) > len(registry.records) {
		t.Fatalf("element <%s> has invalid ID %d (records=%d)", el.Tag, id, len(registry.records))
	}
	record := registry.records[id-1]
	if record.id != id || record.element != el {
		t.Fatalf("record %d = %#v, want element %p", id, record, el)
	}
	return record
}

func TestAssignCallSitesDeterministicAndDistinct(t *testing.T) {
	fset := token.NewFileSet()
	a := parseTargetTestFile(t, fset, "a.gsx", `package views
component A() { <Same/><Same/> }
`)
	z := parseTargetTestFile(t, fset, "z.gsx", `package views
component Z() { <Last/> }
`)
	files := map[string]*gsxast.File{"z.gsx": z, "a.gsx": a}
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(files, map[string]bool{"A": true, "Z": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("unexpected preprocessing diagnostics: %+v", bag.Sorted())
	}
	registry := preprocessed.registry
	if len(registry.records) != 3 {
		t.Fatalf("records=%d, want 3", len(registry.records))
	}
	same := targetTestElements(a, "Same")
	last := targetTestElements(z, "Last")
	if len(same) != 2 || len(last) != 1 {
		t.Fatalf("elements Same=%d Last=%d", len(same), len(last))
	}
	first := registryRecordFor(t, registry, same[0])
	second := registryRecordFor(t, registry, same[1])
	third := registryRecordFor(t, registry, last[0])
	if first.id != 1 || second.id != 2 || third.id != 3 {
		t.Fatalf("IDs=%d,%d,%d, want 1,2,3", first.id, second.id, third.id)
	}
	if first.path != "a.gsx" || second.path != "a.gsx" || third.path != "z.gsx" {
		t.Fatalf("paths=%q,%q,%q", first.path, second.path, third.path)
	}
	if first.disposition != callSitePlanned || second.disposition != callSitePlanned || third.disposition != callSitePlanned {
		t.Fatalf("planned dispositions = %d,%d,%d", first.disposition, second.disposition, third.disposition)
	}
	if len(bag.Sorted()) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", bag.Sorted())
	}
}

func TestAssignCallSitesMaterializesOnceAndKeepsPointers(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
var Top = <TopCall/>
component Page() {
	{ wrap(<Nested/>) }
	<BodyCall/>
}
`)
	files := map[string]*gsxast.File{"views.gsx": file}
	bag := diag.NewBag(fset)
	declNames := map[string]bool{"Page": true}
	preprocessed, err := preprocessComponentCallSites(files, declNames, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("unexpected preprocessing diagnostics: %+v", bag.Sorted())
	}
	first := preprocessed.registry

	var interp *gsxast.Interp
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if got, ok := node.(*gsxast.Interp); ok && got.Expr == "wrap(<Nested/>)" {
			interp = got
		}
		return true
	})
	if interp == nil || len(interp.Embedded) == 0 {
		t.Fatalf("nested interpolation was not materialized: %#v", interp)
	}
	var embedded []*gsxast.Element
	targetTestEmbeddedElements(interp.Embedded, &embedded)
	if len(embedded) != 1 || embedded[0].Tag != "Nested" {
		t.Fatalf("embedded elements=%v", embedded)
	}
	nested := embedded[0]
	nestedRecord := registryRecordFor(t, first, nested)
	if nestedRecord.disposition != callSitePlanned {
		t.Fatalf("Nested disposition=%d, want planned", nestedRecord.disposition)
	}
	top := targetTestElements(file, "TopCall")
	body := targetTestElements(file, "BodyCall")
	if len(top) != 1 || len(body) != 1 {
		t.Fatalf("elements TopCall=%d BodyCall=%d, want one each", len(top), len(body))
	}
	if topID, nestedID, bodyID := registryRecordFor(t, first, top[0]).id, nestedRecord.id, registryRecordFor(t, first, body[0]).id; topID != 1 || nestedID != 2 || bodyID != 3 {
		t.Fatalf("IDs TopCall=%d Nested=%d BodyCall=%d, want 1,2,3", topID, nestedID, bodyID)
	}

	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(t.TempDir(), files)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, err := buildSkeleton(file, funcTables{}, propFields, nodeProps, attrsProps, nil, nil, byo, nil, fset, bag, nil, skeletonFull); err != nil {
		t.Fatal(err)
	}
	var embeddedAgain []*gsxast.Element
	targetTestEmbeddedElements(interp.Embedded, &embeddedAgain)
	if len(embeddedAgain) != 1 || embeddedAgain[0] != nested {
		t.Fatalf("skeleton build replaced embedded pointer: before=%p after=%p", nested, embeddedAgain[0])
	}
	if got := registryRecordFor(t, first, nested).id; got != nestedRecord.id {
		t.Fatalf("Nested ID changed from %d to %d", nestedRecord.id, got)
	}
}

func TestAssignCallSitesUsesAuthoredPreorderAcrossAttrsAndControlFlow(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component Page(on bool) {
	<Outer slot={<AttrCall/>}>
		{ if on { <ChildCall/> } }
	</Outer>
}
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("unexpected preprocessing diagnostics: %+v", bag.Sorted())
	}
	want := []string{"Outer", "AttrCall", "ChildCall"}
	if len(preprocessed.registry.records) != len(want) {
		t.Fatalf("records=%+v, want %d", preprocessed.registry.records, len(want))
	}
	for i, tag := range want {
		record := preprocessed.registry.records[i]
		if record.id != callSiteID(i+1) || record.element.Tag != tag {
			t.Fatalf("record[%d]=%+v, want ID %d <%s>", i, record, i+1, tag)
		}
	}
}

func TestAssignCallSitesPreservesUnsupportedGoBlock(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component Page() {
	{{ first := <Direct><Nested/></Direct>; second := <Second/> }}
	<Planned/>
}
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("unexpected preprocessing diagnostics: %+v", bag.Sorted())
	}
	registry := preprocessed.registry

	var block *gsxast.GoBlock
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if got, ok := node.(*gsxast.GoBlock); ok {
			block = got
		}
		return true
	})
	if block == nil || len(block.Embedded) == 0 {
		t.Fatalf("GoBlock was not materialized: %#v", block)
	}
	var blockElements []*gsxast.Element
	targetTestEmbeddedElements(block.Embedded, &blockElements)
	if len(blockElements) != 3 {
		t.Fatalf("GoBlock elements=%d, want Direct, Nested, Second", len(blockElements))
	}
	byTag := map[string]*gsxast.Element{}
	for _, el := range blockElements {
		byTag[el.Tag] = el
		if !el.IsComponent {
			t.Errorf("unsupported <%s> was not stamped", el.Tag)
		}
	}
	for _, tag := range []string{"Direct", "Second"} {
		record := registryRecordFor(t, registry, byTag[tag])
		if record.disposition != callSitePreserveUnsupportedGoBlock {
			t.Errorf("%s disposition=%d, want preserve", tag, record.disposition)
		}
	}
	if _, ok := registry.byElement[byTag["Nested"]]; ok {
		t.Fatal("nested call inside unsupported GoBlock entered registry")
	}
	planned := targetTestElements(file, "Planned")
	if len(planned) != 1 || registryRecordFor(t, registry, planned[0]).disposition != callSitePlanned {
		t.Fatal("supported sibling call was not planned")
	}
	diags := bag.Sorted()
	if len(diags) != 1 || diags[0].Code != "unsupported-node" {
		t.Fatalf("diagnostics=%+v, want one unsupported-node", diags)
	}
}

func TestAssignCallSitesPreservesUnsupportedGoBlockFragmentAsOneRegion(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component item(value string) {
	{{ _gsxhidden := <><script>let @{value} = 1</script><item/><div[int]/></>; attrs := <Second/> }}
	<Planned/>
}
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"item": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("unexpected preprocessing failure: %+v", bag.Sorted())
	}

	var block *gsxast.GoBlock
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if got, ok := node.(*gsxast.GoBlock); ok {
			block = got
		}
		return true
	})
	if block == nil {
		t.Fatal("GoBlock not found")
	}
	if _, ok := block.UnsupportedMarkup.(*gsxast.Fragment); !ok {
		t.Fatalf("UnsupportedMarkup=%T, want first direct fragment", block.UnsupportedMarkup)
	}
	if reserved := checkReservedDecls(file); len(reserved) != 0 {
		t.Fatalf("unsupported block leaked reserved-prefix facts: %+v", reserved)
	}
	var component *gsxast.Component
	for _, decl := range file.Decls {
		if got, ok := decl.(*gsxast.Component); ok {
			component = got
			break
		}
	}
	if component == nil {
		t.Fatal("component not found")
	}
	if reserved := checkReservedBodyBindings(component); len(reserved) != 0 {
		t.Fatalf("unsupported block leaked body-binding facts: %+v", reserved)
	}
	var clauseSrc []string
	collectClauseSrc(component.Body, func(src string) { clauseSrc = append(clauseSrc, src) })
	if len(clauseSrc) != 0 {
		t.Fatalf("unsupported block leaked clause facts: %q", clauseSrc)
	}
	var blockElements []*gsxast.Element
	targetTestEmbeddedElements(block.Embedded, &blockElements)
	byTag := make(map[string]*gsxast.Element)
	for _, element := range blockElements {
		byTag[element.Tag] = element
	}
	for _, nested := range []string{"script", "item", "div"} {
		if byTag[nested] == nil {
			t.Fatalf("nested <%s> not materialized", nested)
		}
		if _, exists := preprocessed.registry.byElement[byTag[nested]]; exists {
			t.Fatalf("nested <%s> inside unsupported fragment entered registry", nested)
		}
	}
	second := byTag["Second"]
	if second == nil || registryRecordFor(t, preprocessed.registry, second).disposition != callSitePreserveUnsupportedGoBlock {
		t.Fatal("direct <Second> was not preserved as part of the unsupported block")
	}
	planned := targetTestElements(file, "Planned")
	if len(planned) != 1 || registryRecordFor(t, preprocessed.registry, planned[0]).disposition != callSitePlanned {
		t.Fatal("supported sibling call was not planned")
	}
	diags := bag.Sorted()
	if len(diags) != 1 || diags[0].Code != "unsupported-node" {
		t.Fatalf("diagnostics=%+v, want one unsupported-node at the fragment", diags)
	}
}

func TestPreprocessMaterializesDoubleQuotedLiterals(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component Page(value string) {
	{ wrap(f"hello @{value}") }
	{{ value = string(js"save(@{value})") }}
}
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("JavaScript resolution failed: %+v", bag.Sorted())
	}

	var bodyInterp *gsxast.Interp
	var block *gsxast.GoBlock
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		switch node := node.(type) {
		case *gsxast.Interp:
			if node.Expr == `wrap(f"hello @{value}")` {
				bodyInterp = node
			}
		case *gsxast.GoBlock:
			block = node
		}
		return true
	})
	if bodyInterp == nil || len(bodyInterp.Embedded) == 0 {
		t.Fatalf("double-quoted f literal was not materialized: %#v", bodyInterp)
	}
	if block == nil || len(block.Embedded) == 0 {
		t.Fatalf("double-quoted js literal in GoBlock was not materialized: %#v", block)
	}
	assertDoubleQuoted := func(parts []gsxast.GoPart, lang gsxast.EmbeddedLang) {
		t.Helper()
		for _, part := range parts {
			if literal, ok := part.(*gsxast.EmbeddedInterp); ok && literal.Lang == lang {
				if !literal.DoubleQuoted {
					t.Fatalf("%v literal lost double-quoted delimiter", lang)
				}
				return
			}
		}
		t.Fatalf("no %v embedded literal in %#v", lang, parts)
	}
	assertDoubleQuoted(bodyInterp.Embedded, gsxast.EmbeddedText)
	assertDoubleQuoted(block.Embedded, gsxast.EmbeddedJS)
}

func TestPreprocessResolvesJavaScriptOnExpandedTree(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component Page(value string) {
	{ wrap(<script>const s = "@{value}"</script>) }
	{ wrap(<button x-data=js"{v:@{value}}"/>) }
	<Holder content={<script>const t = "@{value}"</script>}/>
	<script>const nested = @{wrap(js"save(@{value})")}</script>
}
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("JavaScript resolution failed: %+v", bag.Sorted())
	}

	var expandedElements []*gsxast.Element
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if interp, ok := node.(*gsxast.Interp); ok && len(interp.Embedded) > 0 {
			targetTestEmbeddedElements(interp.Embedded, &expandedElements)
		}
		return true
	})
	var expandedScript, expandedButton *gsxast.Element
	for _, element := range expandedElements {
		switch element.Tag {
		case "script":
			expandedScript = element
		case "button":
			expandedButton = element
		}
	}
	if expandedScript == nil || expandedButton == nil {
		t.Fatalf("expanded elements missing: %+v", expandedElements)
	}
	assertScriptStringHole := func(element *gsxast.Element) {
		t.Helper()
		for _, child := range element.Children {
			if interp, ok := child.(*gsxast.Interp); ok {
				if interp.JSCtx != gsxast.JSCtxString {
					t.Fatalf("script hole context=%v, want string", interp.JSCtx)
				}
				return
			}
		}
		t.Fatal("script has no interpolation hole")
	}
	assertScriptStringHole(expandedScript)

	var attrHole *gsxast.Interp
	for _, attr := range expandedButton.Attrs {
		if embedded, ok := attr.(*gsxast.EmbeddedAttr); ok {
			for _, segment := range embedded.Segments {
				if interp, ok := segment.(*gsxast.Interp); ok {
					attrHole = interp
				}
			}
		}
	}
	if attrHole == nil || attrHole.JSCtx != gsxast.JSCtxValue {
		t.Fatalf("materialized JS attr hole=%#v, want value context", attrHole)
	}

	var slottedScript *gsxast.Element
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if element, ok := node.(*gsxast.Element); ok && element.Tag == "script" && element != expandedScript {
			for _, child := range element.Children {
				if interp, ok := child.(*gsxast.Interp); ok && interp.Expr == "value" {
					slottedScript = element
				}
			}
		}
		return true
	})
	if slottedScript == nil {
		t.Fatal("script nested under MarkupAttr was not found")
	}
	assertScriptStringHole(slottedScript)

	var nestedOuter *gsxast.Interp
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if interp, ok := node.(*gsxast.Interp); ok && interp.Expr == `wrap(js"save(@{value})")` {
			nestedOuter = interp
		}
		return true
	})
	if nestedOuter == nil || nestedOuter.JSCtx != gsxast.JSCtxValue {
		t.Fatalf("outer script hole=%#v, want value context", nestedOuter)
	}
	var nestedLiteral *gsxast.EmbeddedInterp
	for _, part := range nestedOuter.Embedded {
		if literal, ok := part.(*gsxast.EmbeddedInterp); ok {
			nestedLiteral = literal
		}
	}
	if nestedLiteral == nil {
		t.Fatal("nested js literal was not materialized inside script hole")
	}
	var nestedHole *gsxast.Interp
	for _, segment := range nestedLiteral.Segments {
		if interp, ok := segment.(*gsxast.Interp); ok {
			nestedHole = interp
		}
	}
	if nestedHole == nil || nestedHole.JSCtx != gsxast.JSCtxValue {
		t.Fatalf("nested js literal hole=%#v, want value context", nestedHole)
	}
}

func TestPreprocessUnsupportedGoBlockHasSingleDiagnostic(t *testing.T) {
	cases := []struct {
		name      string
		component string
		block     string
	}{
		{name: "script", component: "Page", block: `{{ x := <script>let @{value} = 1</script> }}`},
		{name: "self reference", component: "item", block: `{{ x := <item/> }}`},
		{name: "leaf type args", component: "Page", block: `{{ x := <div[int]/> }}`},
		{name: "nested malformed expression", component: "Page", block: `{{ x := <div>{wrap(<Broken></Other>)}</div> }}`},
		{name: "later malformed direct element", component: "Page", block: `{{ first := <Direct/>; second := <Broken></Other> }}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			src := "package views\ncomponent " + tc.component + "(value string) {\n" + tc.block + "\n}\n"
			file := parseTargetTestFile(t, fset, "views.gsx", src)
			bag := diag.NewBag(fset)
			preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{tc.component: true}, fset, attrclass.Builtin(), bag)
			if err != nil {
				t.Fatal(err)
			}
			registry := preprocessed.registry
			diags := bag.Sorted()
			if len(diags) != 1 || diags[0].Code != "unsupported-node" {
				t.Fatalf("diagnostics=%+v, want exactly one unsupported-node", diags)
			}
			for _, record := range registry.records {
				if record.disposition != callSitePreserveUnsupportedGoBlock {
					t.Fatalf("record=%+v, unsupported block must not contain planned sites", record)
				}
			}
		})
	}
}

func TestPreprocessMalformedEmbeddedMarkupFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "interpolation", body: `{ wrap(<Broken></Other>) }`},
		{name: "GoBlock", body: `{{ value := <Broken></Other> }}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file := parseTargetTestFile(t, fset, "views.gsx", "package views\ncomponent Page() {\n"+tc.body+"\n}\n")
			bag := diag.NewBag(fset)
			preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
			if err != nil {
				t.Fatal(err)
			}
			if preprocessed.syntaxOK || preprocessed.analysisReady() || preprocessed.registry != nil {
				t.Fatalf("preprocess result=%+v, want syntax failure with no registry", preprocessed)
			}
			diags := bag.Sorted()
			if len(diags) == 0 || diags[0].Code != "parse-error" || diags[0].Source != "parser" {
				t.Fatalf("diagnostics=%+v, want positioned parser error", diags)
			}
			if len(diags) != 1 {
				t.Fatalf("diagnostics=%+v, want only the split parser error", diags)
			}
			gsxast.Inspect(file, func(node gsxast.Node) bool {
				switch node := node.(type) {
				case *gsxast.Interp:
					if node.Embedded != nil {
						t.Errorf("failed interpolation retained partial Embedded parts: %#v", node.Embedded)
					}
				case *gsxast.GoBlock:
					if node.Embedded != nil {
						t.Errorf("failed GoBlock retained partial Embedded parts: %#v", node.Embedded)
					}
				}
				return true
			})
		})
	}
}

func TestPreprocessMalformedGoWithElementsFailsClosed(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
var value = (
	<Widget/>
)
import "late"
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, nil, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if preprocessed.syntaxOK || preprocessed.analysisReady() || preprocessed.registry != nil {
		t.Fatalf("preprocess result=%+v, want Go syntax failure with no registry", preprocessed)
	}
	diags := bag.Sorted()
	if len(diags) != 1 || diags[0].Code != "parse-error" || diags[0].Source != "parser" {
		t.Fatalf("diagnostics=%+v, want one parser parse-error", diags)
	}
	if got := diags[0].Start; got.Filename != "views.gsx" || got.Line != 5 || got.Column != 1 {
		t.Fatalf("diagnostic=%+v, want position views.gsx:5:1 after canonical reconstruction", diags[0])
	}
	widgets := targetTestElements(file, "Widget")
	if len(widgets) != 1 || widgets[0].IsComponent {
		t.Fatalf("malformed GoWithElements entered semantic stamping: %+v", widgets)
	}
}

func TestPreprocessGoWithElementsUsesCanonicalExpressionLowering(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "decorative multiline parens",
			src: `package views
var value = (
	<Widget/>
)
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file := parseTargetTestFile(t, fset, "views.gsx", tt.src)
			bag := diag.NewBag(fset)
			preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, nil, fset, attrclass.Builtin(), bag)
			if err != nil {
				t.Fatal(err)
			}
			if !preprocessed.analysisReady() || preprocessed.registry == nil {
				t.Fatalf("preprocess result=%+v, diagnostics=%+v, want complete analysis", preprocessed, bag.Sorted())
			}
			if diags := bag.Sorted(); len(diags) != 0 {
				t.Fatalf("diagnostics=%+v, want valid GSX expression accepted", diags)
			}
			widgets := targetTestElements(file, "Widget")
			if len(widgets) != 1 {
				t.Fatalf("Widget elements=%+v, want one materialized call site", widgets)
			}
		})
	}
}

func TestPreprocessGoWithElementsUsesNonCallMarkersForValues(t *testing.T) {
	t.Run("ordinary expressions remain valid", func(t *testing.T) {
		fset := token.NewFileSet()
		file := parseTargetTestFile(t, fset, "views.gsx", "package views\nvar element = <Widget/>\nvar fragment = <><Widget/></>\nvar text = f`x`\nvar script = js`x`\nvar style = css`x`\n")
		bag := diag.NewBag(fset)
		preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, nil, fset, attrclass.Builtin(), bag)
		if err != nil {
			t.Fatal(err)
		}
		if !preprocessed.analysisReady() || preprocessed.registry == nil || len(bag.Sorted()) != 0 {
			t.Fatalf("preprocess result=%+v, diagnostics=%+v, want ordinary embedded literal expressions accepted", preprocessed, bag.Sorted())
		}
	})

	values := []struct {
		name, source string
	}{
		{name: "element", source: "<Widget/>"},
		{name: "fragment", source: "<><Widget/></>"},
		{name: "f", source: "f`x`"},
		{name: "js", source: "js`x`"},
		{name: "css", source: "css`x`"},
	}
	for _, statement := range []string{"go", "defer"} {
		for _, value := range values {
			t.Run(statement+" "+value.name, func(t *testing.T) {
				fset := token.NewFileSet()
				src := "package views\nfunc launch() {\n\t" + statement + " " + value.source + "\n}\n"
				file := parseTargetTestFile(t, fset, "views.gsx", src)
				bag := diag.NewBag(fset)
				preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, nil, fset, attrclass.Builtin(), bag)
				if err != nil {
					t.Fatal(err)
				}
				if preprocessed.analysisReady() || preprocessed.registry != nil {
					t.Fatalf("preprocess result=%+v, want non-call GSX value rejected from %s statement", preprocessed, statement)
				}
				diags := bag.Sorted()
				if len(diags) != 1 || diags[0].Code != "parse-error" || diags[0].Source != "parser" {
					t.Fatalf("diagnostics=%+v, want one parser parse-error", diags)
				}
				wantColumn := len("\t"+statement+" "+value.source) + 1
				if got := diags[0].Start; got.Filename != "views.gsx" || got.Line != 3 || got.Column != wantColumn {
					t.Fatalf("diagnostic position=%v, want parser point after GSX value at views.gsx:3:%d", got, wantColumn)
				}
			})
		}
	}
}

func TestPreprocessGoWithElementsRejectsValuesInTypeGrammar(t *testing.T) {
	tests := []struct {
		name, declaration string
	}{
		{name: "type alias", declaration: "type Alias = <Widget/>"},
		{name: "pointer type", declaration: "var pointer *<Widget/>"},
		{name: "channel type", declaration: "var channel chan <Widget/>"},
		{name: "map key type", declaration: "var mapping map[<Widget/>]int"},
		{name: "unnamed function parameter", declaration: "func consume(<Widget/>) {}"},
		{name: "function literal parameter", declaration: "var callback = func(<Widget/>) {}"},
		{name: "map type in value", declaration: "var mappingValue = map[<Widget/>]int{}"},
		{name: "channel type in make", declaration: "var channelValue = make(chan <Widget/>)"},
		{name: "embedded interface type", declaration: "type Contract interface { <Widget/> }"},
		{name: "type parameter", declaration: "type Generic[<Widget/> any] struct{}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file := parseTargetTestFile(t, fset, "views.gsx", "package views\n"+tt.declaration+"\n")
			bag := diag.NewBag(fset)
			preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, nil, fset, attrclass.Builtin(), bag)
			if err != nil {
				t.Fatal(err)
			}
			if preprocessed.analysisReady() || preprocessed.registry != nil {
				t.Fatalf("preprocess result=%+v, want GSX value rejected from type grammar", preprocessed)
			}
			diags := bag.Sorted()
			if len(diags) != 1 || diags[0].Code != "parse-error" || diags[0].Source != "parser" {
				t.Fatalf("diagnostics=%+v, want one parser parse-error", diags)
			}
			if got := diags[0].Start; got.Filename != "views.gsx" || got.Line != 2 {
				t.Fatalf("diagnostic position=%v, want authored type expression on views.gsx:2", got)
			}
			widgets := targetTestElements(file, "Widget")
			if len(widgets) != 1 || widgets[0].IsComponent {
				t.Fatalf("invalid type-position value entered semantic stamping: %+v", widgets)
			}
		})
	}
}

func TestPreprocessGoWithElementsLeavesAmbiguousTypeArgumentsToGoTypes(t *testing.T) {
	const source = `package views

type Generic[T any] struct{}
var genericValue = Generic[<div/>]{}
`

	// Go's parser represents bracket arguments as expressions before type
	// checking, so even a quoted basic-literal marker is syntactically valid in
	// this one position. Target preprocessing is a parser gate, not a duplicate
	// type checker.
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", source)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, nil, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() || preprocessed.registry == nil || len(bag.Sorted()) != 0 {
		t.Fatalf("preprocess result=%+v, diagnostics=%+v, want syntactically admissible bracket expression", preprocessed, bag.Sorted())
	}

	// The normal semantic phase must still reject the GSX value as a type
	// argument and suppress every generated file.
	dir, module := openTestModule(t, map[string]string{"views.gsx": source})
	out, diags, err := module.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("generated output escaped semantic type rejection: %v", out)
	}
	if len(diags) == 0 {
		t.Fatal("missing semantic type diagnostic")
	}
	var typeDiagnostic bool
	for _, d := range diags {
		if d.Source == "types" {
			typeDiagnostic = true
		}
		if d.Code == "parse-error" {
			t.Fatalf("ambiguous bracket expression was misclassified as a parser failure: %+v", d)
		}
	}
	if !typeDiagnostic {
		t.Fatalf("diagnostics=%+v, want go/types semantic rejection", diags)
	}
}

func TestPreprocessUnmappableGoWithElementsFailsClosed(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", "package views\nvar a, b = <a/>, <B/>, <C/>\n")
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"a": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if preprocessed.syntaxOK || preprocessed.analysisReady() || preprocessed.registry != nil {
		t.Fatalf("preprocess result=%+v, want unmappable Go declaration to fail closed", preprocessed)
	}
	diags := bag.Sorted()
	if len(diags) != 1 || diags[0].Code != "invalid-go-declaration" || diags[0].Source != "codegen" {
		t.Fatalf("diagnostics=%+v, want one exact invalid-Go-declaration diagnostic", diags)
	}
	for _, tag := range []string{"a", "B", "C"} {
		elements := targetTestElements(file, tag)
		if len(elements) != 1 || elements[0].IsComponent {
			t.Fatalf("unmappable declaration partially stamped <%s>: %+v", tag, elements)
		}
	}
}

func TestPreprocessRejectsRepeatedPassBeforeDiagnostics(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component item() {
	<item/>
	{{ value := <Bad/> }}
}
`)
	files := map[string]*gsxast.File{"views.gsx": file}
	parsed := newParsedGSXPackage("views", files)
	firstBag := diag.NewBag(fset)
	first, err := parsed.preprocessComponentCallSites(map[string]bool{"item": true}, fset, attrclass.Builtin(), firstBag)
	if err != nil {
		t.Fatal(err)
	}
	if !first.analysisReady() || first.registry == nil {
		t.Fatalf("first preprocess=%+v, diagnostics=%+v", first, firstBag.Sorted())
	}
	if got := len(firstBag.Sorted()); got != 2 {
		t.Fatalf("first diagnostics=%+v, want self-reference warning and unsupported-node", firstBag.Sorted())
	}

	secondBag := diag.NewBag(fset)
	if _, err := parsed.preprocessComponentCallSites(map[string]bool{"item": true}, fset, attrclass.Builtin(), secondBag); err == nil {
		t.Fatal("second preprocessing pass succeeded; want explicit single-pass invariant error")
	}
	if diags := secondBag.Sorted(); len(diags) != 0 {
		t.Fatalf("second pass emitted duplicate diagnostics before rejecting reuse: %+v", diags)
	}
}

func TestPreprocessPackageClaimIsConcurrentOneShot(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", "package views\ncomponent Page() { <Widget/> }\n")
	parsed := newParsedGSXPackage("views", map[string]*gsxast.File{"views.gsx": file})

	type outcome struct {
		result callSitePreprocessResult
		err    error
		diags  []diag.Diagnostic
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			bag := diag.NewBag(fset)
			<-start
			result, err := parsed.preprocessComponentCallSites(map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
			results <- outcome{result: result, err: err, diags: bag.Sorted()}
		}()
	}
	close(start)

	succeeded, rejected := 0, 0
	for range 2 {
		got := <-results
		switch got.err {
		case nil:
			succeeded++
			if !got.result.analysisReady() || got.result.registry == nil || len(got.diags) != 0 {
				t.Fatalf("winning preprocess=%+v diagnostics=%+v", got.result, got.diags)
			}
		default:
			rejected++
			if len(got.diags) != 0 {
				t.Fatalf("rejected claim emitted diagnostics before failing: %+v", got.diags)
			}
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent claims: succeeded=%d rejected=%d, want 1/1", succeeded, rejected)
	}
}

func TestPreprocessGoWithElementsExactExclusions(t *testing.T) {
	t.Run("parallel RHS", func(t *testing.T) {
		fset := token.NewFileSet()
		file := parseTargetTestFile(t, fset, "views.gsx", "package views\nvar a, b = <a/>, <b/>\n")
		bag := diag.NewBag(fset)
		if _, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"a": true, "b": true}, fset, attrclass.Builtin(), bag); err != nil {
			t.Fatal(err)
		}
		stamps := collectStamps(file)
		if stamps["a"] || stamps["b"] {
			t.Fatalf("parallel RHS self-exclusions=%v, want both leaves", stamps)
		}
	})

	t.Run("single tuple RHS", func(t *testing.T) {
		fset := token.NewFileSet()
		file := parseTargetTestFile(t, fset, "views.gsx", "package views\nvar a, b = <a><b/></a>\n")
		bag := diag.NewBag(fset)
		if _, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"a": true, "b": true}, fset, attrclass.Builtin(), bag); err != nil {
			t.Fatal(err)
		}
		stamps := collectStamps(file)
		if stamps["a"] || stamps["b"] {
			t.Fatalf("tuple RHS exclusions=%v, want both declared names excluded", stamps)
		}
	})

	t.Run("embedded literal RHS", func(t *testing.T) {
		fset := token.NewFileSet()
		file := parseTargetTestFile(t, fset, "views.gsx", "package views\nvar item = f`@{wrap(<item/>)}`\n")
		bag := diag.NewBag(fset)
		if _, err := preprocessComponentCallSites(map[string]*gsxast.File{"views.gsx": file}, map[string]bool{"item": true}, fset, attrclass.Builtin(), bag); err != nil {
			t.Fatal(err)
		}
		var elements []*gsxast.Element
		for _, decl := range file.Decls {
			if withElements, ok := decl.(*gsxast.GoWithElements); ok {
				targetTestEmbeddedElements(withElements.Parts, &elements)
			}
		}
		if len(elements) != 1 || elements[0].Tag != "item" || elements[0].IsComponent {
			t.Fatalf("embedded RHS elements=%+v, want self-excluded <item>", elements)
		}
	})
}

func TestBuildSkeletonDoesNotMaterializeCallSites(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component Page() { { wrap(<Nested/>) } }
`)
	var interp *gsxast.Interp
	gsxast.Inspect(file, func(node gsxast.Node) bool {
		if got, ok := node.(*gsxast.Interp); ok {
			interp = got
		}
		return true
	})
	if interp == nil || interp.Embedded != nil {
		t.Fatalf("unexpected precondition: %#v", interp)
	}
	files := map[string]*gsxast.File{"views.gsx": file}
	propFields, nodeProps, attrsProps, byo, err := componentPropFieldsFor(t.TempDir(), files)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, err := buildSkeleton(file, funcTables{}, propFields, nodeProps, attrsProps, nil, nil, byo, nil, fset, diag.NewBag(fset), nil, skeletonFull); err != nil {
		t.Fatal(err)
	}
	if interp.Embedded != nil {
		t.Fatal("buildSkeleton mutated Interp.Embedded; preprocessing must be the only materializer")
	}
}

func TestAnalyzeInvalidatesCallSitesWhenFileIsSkipped(t *testing.T) {
	dir, module := openTestModule(t, map[string]string{
		"bad.gsx": `package testmod

var _gsxbad = 1

component Bad() { <Widget/> }
`,
		"good.gsx": `package testmod

component Good() { <div/> }
`,
	})
	external, err := module.externalImporter()
	if err != nil {
		t.Fatal(err)
	}
	analyzed, err := module.analyze(dir, &moduleImporter{m: module, external: external, seen: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if analyzed.callSites != nil {
		t.Fatalf("callSites=%+v, want nil after a preprocessed file was removed", analyzed.callSites)
	}
	if len(analyzed.gsxFiles) != 1 {
		t.Fatalf("active gsx files=%v, want only good.gsx", analyzed.gsxFiles)
	}
	if _, ok := analyzed.gsxFiles[filepath.Join(dir, "good.gsx")]; !ok {
		t.Fatalf("good.gsx missing from active files: %v", analyzed.gsxFiles)
	}
}
