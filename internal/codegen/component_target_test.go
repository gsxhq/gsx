package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
)

func TestComponentTargetFactEffectiveSignature(t *testing.T) {
	raw := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewParam(token.NoPos, nil, "value", types.Typ[types.String])),
		types.NewTuple(types.NewParam(token.NoPos, nil, "", types.Typ[types.Int])),
		false,
	)
	instantiated := types.NewSignatureType(nil, nil, nil,
		types.NewTuple(types.NewParam(token.NoPos, nil, "value", types.Typ[types.Int])),
		types.NewTuple(types.NewParam(token.NoPos, nil, "", types.Typ[types.Int])),
		false,
	)

	fact := componentTargetFact{raw: raw}
	if got := fact.effectiveSignature(); got != raw {
		t.Fatalf("effectiveSignature without instance = %p, want raw %p", got, raw)
	}
	fact.explicitInstance = &types.Instance{Type: instantiated}
	if got := fact.effectiveSignature(); got != instantiated {
		t.Fatalf("effectiveSignature with instance = %p, want instantiated %p", got, instantiated)
	}

	// A target fact is immutable best-effort semantic data. If go/types leaves a
	// non-signature instance after a failed target check, discovery must fall back
	// to the origin signature instead of panicking while later diagnostics win.
	fact.explicitInstance = &types.Instance{Type: types.Typ[types.Int]}
	if got := fact.effectiveSignature(); got != raw {
		t.Fatalf("effectiveSignature with non-signature instance = %p, want raw %p", got, raw)
	}
}

func TestParseComponentTargetExpressionMapsSyntaxErrorToAuthoredCloseBracket(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "target.gsx", `package p
component Page() { <F[!]/> }
`)
	elements := targetTestElements(file, "F")
	if len(elements) != 1 {
		t.Fatalf("F elements = %d, want 1", len(elements))
	}

	parsed, err := parseComponentTargetExpression(elements[0], fset)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.expr != nil {
		t.Fatalf("invalid target expr = %#v, want nil", parsed.expr)
	}
	if parsed.diagnostic == nil {
		t.Fatal("invalid target has no deferred diagnostic")
	}
	if parsed.diagnostic.Code != "parse-error" || parsed.diagnostic.Source != "parser" {
		t.Fatalf("diagnostic = %+v, want parser parse-error", *parsed.diagnostic)
	}
	wantOffset := strings.Index(`package p
component Page() { <F[!]/> }
`, "]")
	if parsed.diagnostic.Start.Offset != wantOffset {
		t.Fatalf("diagnostic = %+v, want authored ] offset %d", *parsed.diagnostic, wantOffset)
	}
}

func TestBindComponentTargetMarkersUsesExactRawExpressionSpan(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "target.gsx", `package p
component Page() { <pkg.F[ map[string]int ]/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"target.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	elements := targetTestElements(file, "pkg.F")
	if len(elements) != 1 {
		t.Fatalf("pkg.F elements = %d, want 1", len(elements))
	}

	var source strings.Builder
	source.WriteString("package p\nfunc Package() {}\nfunc _probe() {\n")
	if err := markers.emitBinding(&source, elements[0], fset); err != nil {
		t.Fatal(err)
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "target.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	marker := markers.bySite[1]
	if marker.file != parsed || marker.valueSpec == nil || marker.expr == nil {
		t.Fatalf("marker not bound to parsed AST: %+v", marker)
	}
	raw := fset.PositionFor(marker.expr.Pos(), false).Offset
	if raw != marker.rawSpan.start {
		t.Fatalf("expr raw start = %d, recorded %d", raw, marker.rawSpan.start)
	}
	if got := fset.PositionFor(marker.expr.End(), false).Offset; got != marker.rawSpan.end {
		t.Fatalf("expr raw end = %d, recorded %d", got, marker.rawSpan.end)
	}
}

func TestBindComponentTargetMarkersIgnoresCompanionIdentifierSpelling(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "target.gsx", `package p
component Page() { <Package/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"target.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var source strings.Builder
	source.WriteString("package p\nfunc Package() {}\nfunc _probe() {\n")
	if err := markers.emitBinding(&source, preprocessed.registry.records[0].element, fset); err != nil {
		t.Fatal(err)
	}
	source.WriteString("}\n")
	discovery, err := goparser.ParseFile(fset, "target.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	companion, err := goparser.ParseFile(fset, "companion.go", "package p\nvar _gsxtarget1 = 1\n", goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	if err := bindComponentTargetMarkers(discovery, 0, fset, markers); err != nil {
		t.Fatalf("companion identifier spelling captured as generated marker: %v", err)
	}
	if marker := markers.bySite[1]; marker.file != discovery {
		t.Fatalf("marker file = %p, want exact discovery file %p", marker.file, discovery)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{discovery, companion}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{discovery, companion}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated errors = %+v", unrelated)
	}
	if got := facts[1]; got.provenance != targetPackageFunc || len(got.targetDiags) != 0 {
		t.Fatalf("target fact = %+v, want package function unaffected by companion marker spelling", got)
	}
}

func TestHarvestComponentTargetsPartitionsByTokenFileIdentity(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "target.gsx", `package p
component Page() { <Package/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"target.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var source strings.Builder
	source.WriteString("package p\nfunc Package() {}\nfunc _probe() {\n")
	if err := markers.emitBinding(&source, preprocessed.registry.records[0].element, fset); err != nil {
		t.Fatal(err)
	}
	source.WriteString("}\n")
	const sharedFilename = "same.target.x.go"
	discovery, err := goparser.ParseFile(fset, sharedFilename, source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(discovery, 0, fset, markers); err != nil {
		t.Fatal(err)
	}

	missingPrefix := "package p\nvar _ = "
	padding := markers.bySite[1].rawSpan.start - len(missingPrefix)
	if padding < 0 {
		t.Fatalf("marker offset %d is too short for companion probe", markers.bySite[1].rawSpan.start)
	}
	companionSource := "package p\n" + strings.Repeat(" ", padding) + "var _ = Missing\n"
	companion, err := goparser.ParseFile(fset, sharedFilename, companionSource, goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{discovery, companion}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{discovery, companion}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if got := facts[1]; got.provenance != targetPackageFunc || len(got.targetDiags) != 0 {
		t.Fatalf("target fact captured companion error: %+v", got)
	}
	if len(unrelated) != 1 || unrelated[0].Msg != "undefined: Missing" {
		t.Fatalf("unrelated errors = %+v, want companion undefined error", unrelated)
	}
}

func TestHarvestComponentTargetsClassifiesExactProvenance(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "targets.gsx", `package p
component Page() {
	<Package/><Plain/><Receiver.M/><Concrete.M/><Field.Fn/><Local/><Iface.M/>
}
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"targets.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var source strings.Builder
	source.WriteString(`package p
type Concrete struct{}
func (Concrete) M() int { return 0 }
type Contract interface{ M() int }
type Holder struct{ Fn func() int }
func Package() int { return 0 }
var Plain = Package
func _probe(Local func() int, Receiver Concrete, Iface Contract, Field Holder) {
`)
	for _, record := range preprocessed.registry.records {
		if err := markers.emitBinding(&source, record.element, fset); err != nil {
			t.Fatal(err)
		}
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "targets.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated target-skeleton errors: %+v", unrelated)
	}

	want := map[string]componentTargetProvenance{
		"Package":    targetPackageFunc,
		"Plain":      targetPackageVar,
		"Receiver.M": targetConcreteMethodValue,
	}
	for _, record := range preprocessed.registry.records {
		fact, ok := facts[record.id]
		if !ok {
			t.Errorf("site %d <%s> has no fact", record.id, record.element.Tag)
			continue
		}
		if provenance, accepted := want[record.element.Tag]; accepted {
			if fact.provenance != provenance || fact.raw == nil || fact.origin == nil || len(fact.targetDiags) != 0 {
				t.Errorf("accepted <%s> fact = %+v, want provenance %d with callable origin and no diagnostics", record.element.Tag, fact, provenance)
			}
			continue
		}
		if fact.object != nil || fact.origin != nil || fact.provenance != 0 || fact.raw != nil || len(fact.targetDiags) != 1 || fact.targetDiags[0].Code != "invalid-component-target" {
			t.Errorf("rejected <%s> fact = %+v, want one provenance diagnostic", record.element.Tag, fact)
		}
	}
}

func TestHarvestComponentTargetsAcceptsNamedAndAliasFunctionVariables(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "function-vars.gsx", `package p
component Page() { <NamedVar/><AliasVar/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"function-vars.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var source strings.Builder
	source.WriteString(`package p
type Named func(value int) string
type Alias = func(flag bool) string
var NamedVar Named
var AliasVar Alias
func _probe() {
`)
	for _, record := range preprocessed.registry.records {
		if err := markers.emitBinding(&source, record.element, fset); err != nil {
			t.Fatal(err)
		}
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "function-vars.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated target-skeleton errors: %+v", unrelated)
	}

	wantParam := map[string]string{"NamedVar": "value", "AliasVar": "flag"}
	for _, record := range preprocessed.registry.records {
		fact := facts[record.id]
		if fact.provenance != targetPackageVar || fact.raw == nil || fact.origin == nil || len(fact.targetDiags) != 0 {
			t.Errorf("<%s> fact = %+v, want accepted package function variable", record.element.Tag, fact)
			continue
		}
		if fact.raw.Params().Len() != 1 || fact.raw.Params().At(0).Name() != wantParam[record.element.Tag] {
			t.Errorf("<%s> raw params = %s, want authored name %q", record.element.Tag, fact.raw.Params(), wantParam[record.element.Tag])
		}
	}
}

func TestHarvestComponentTargetsUsesExactImportAndShadowIdentities(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "identities.gsx", `package p
component Page() { <dep.F/><target.F/><target.F/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"identities.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	defaultElement := targetTestElements(file, "dep.F")
	targetElements := targetTestElements(file, "target.F")
	if len(defaultElement) != 1 || len(targetElements) != 2 {
		t.Fatalf("target elements: dep.F=%d target.F=%d, want 1 and 2", len(defaultElement), len(targetElements))
	}

	var defaultSource strings.Builder
	defaultSource.WriteString("package p\nimport \"example.com/dep\"\nfunc _probeDefault() {\n")
	if err := markers.emitBinding(&defaultSource, defaultElement[0], fset); err != nil {
		t.Fatal(err)
	}
	defaultSource.WriteString("}\n")
	defaultFile, err := goparser.ParseFile(fset, "default.target.x.go", defaultSource.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(defaultFile, 0, fset, markers); err != nil {
		t.Fatal(err)
	}

	firstExplicit := len(markers.ordered)
	var explicitSource strings.Builder
	explicitSource.WriteString(`package p
import target "example.com/dep"
type Concrete struct{}
func (Concrete) F() int { return 0 }
func _probeImported() {
`)
	if err := markers.emitBinding(&explicitSource, targetElements[0], fset); err != nil {
		t.Fatal(err)
	}
	explicitSource.WriteString("}\nfunc _probeShadow(target Concrete) {\n")
	if err := markers.emitBinding(&explicitSource, targetElements[1], fset); err != nil {
		t.Fatal(err)
	}
	explicitSource.WriteString("}\n")
	explicitFile, err := goparser.ParseFile(fset, "explicit.target.x.go", explicitSource.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(explicitFile, firstExplicit, fset, markers); err != nil {
		t.Fatal(err)
	}

	dep := types.NewPackage("example.com/dep", "dep")
	depSignature := types.NewSignatureType(nil, nil, nil, nil, types.NewTuple(types.NewParam(token.NoPos, dep, "", types.Typ[types.Int])), false)
	dep.Scope().Insert(types.NewFunc(token.NoPos, dep, "F", depSignature))
	dep.MarkComplete()
	packageFiles := []*goast.File{defaultFile, explicitFile}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", packageFiles, fset, mapImporter{dep.Path(): dep}, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts(packageFiles, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated target-skeleton errors: %+v", unrelated)
	}

	defaultImport, ok := info.Implicits[defaultFile.Imports[0]].(*types.PkgName)
	if !ok {
		t.Fatalf("default import object = %T, want *types.PkgName", info.Implicits[defaultFile.Imports[0]])
	}
	explicitImport, ok := info.Defs[explicitFile.Imports[0].Name].(*types.PkgName)
	if !ok {
		t.Fatalf("explicit import object = %T, want *types.PkgName", info.Defs[explicitFile.Imports[0].Name])
	}
	if defaultImport == explicitImport {
		t.Fatal("default and explicit imports unexpectedly share one PkgName object")
	}

	defaultFact := facts[preprocessed.registry.byElement[defaultElement[0]]]
	importedFact := facts[preprocessed.registry.byElement[targetElements[0]]]
	shadowFact := facts[preprocessed.registry.byElement[targetElements[1]]]
	for label, fact := range map[string]componentTargetFact{"default": defaultFact, "explicit": importedFact} {
		if fact.provenance != targetPackageFunc || fact.raw == nil || len(fact.targetDiags) != 0 {
			t.Errorf("%s import fact = %+v, want package function", label, fact)
		}
	}
	defaultSelector := defaultFact.expr.(*goast.SelectorExpr)
	importedSelector := importedFact.expr.(*goast.SelectorExpr)
	shadowSelector := shadowFact.expr.(*goast.SelectorExpr)
	if got := info.Uses[defaultSelector.X.(*goast.Ident)]; got != defaultImport {
		t.Errorf("default qualifier object = %v, want exact implicit PkgName %v", got, defaultImport)
	}
	if got := info.Uses[importedSelector.X.(*goast.Ident)]; got != explicitImport {
		t.Errorf("explicit qualifier object = %v, want exact defined PkgName %v", got, explicitImport)
	}
	shadowQualifier, ok := info.Uses[shadowSelector.X.(*goast.Ident)].(*types.Var)
	if !ok {
		t.Fatalf("shadow qualifier object = %T, want *types.Var", info.Uses[shadowSelector.X.(*goast.Ident)])
	}
	selection := info.Selections[shadowSelector]
	if selection == nil || selection.Kind() != types.MethodVal {
		t.Fatalf("shadow selector selection = %v, want MethodVal", selection)
	}
	if shadowFact.provenance != targetConcreteMethodValue || shadowFact.raw == nil || len(shadowFact.targetDiags) != 0 {
		t.Fatalf("shadow fact = %+v, want accepted concrete MethodVal for %v", shadowFact, shadowQualifier)
	}
}

func TestHarvestComponentTargetsRejectsPromotedInterfaceMethod(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "promoted-interface.gsx", `package p
component Page() { <receiver.M/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"promoted-interface.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var source strings.Builder
	source.WriteString(`package p
type Contract interface{ M() int }
type Promoted struct{ Contract }
func _probe(receiver Promoted) {
`)
	if err := markers.emitBinding(&source, preprocessed.registry.records[0].element, fset); err != nil {
		t.Fatal(err)
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "promoted-interface.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated target-skeleton errors: %+v", unrelated)
	}
	fact := facts[preprocessed.registry.records[0].id]
	if !fact.hasSelection || fact.selectionKind != types.MethodVal {
		t.Fatalf("promoted interface fact selection = %+v, want MethodVal metadata", fact)
	}
	if fact.object != nil || fact.origin != nil || fact.raw != nil || fact.provenance != 0 || len(fact.targetDiags) != 1 || fact.targetDiags[0].Code != "invalid-component-target" || !strings.Contains(fact.targetDiags[0].Message, "interface") {
		t.Fatalf("promoted interface fact = %+v, want one interface-dispatch rejection", fact)
	}
}

func TestHarvestComponentTargetsKeepsValidSiblingWhenTargetSyntaxIsInvalid(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "syntax-sibling.gsx", `package p
component Page() { <F[!]/><Package/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"syntax-sibling.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var source strings.Builder
	source.WriteString("package p\nfunc Package() int { return 0 }\nfunc _probe() {\n")
	for _, record := range preprocessed.registry.records {
		if err := markers.emitBinding(&source, record.element, fset); err != nil {
			t.Fatal(err)
		}
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "syntax-sibling.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated target-skeleton errors: %+v", unrelated)
	}
	if len(facts) != len(preprocessed.registry.records) {
		t.Fatalf("facts = %d, want total %d", len(facts), len(preprocessed.registry.records))
	}
	for _, record := range preprocessed.registry.records {
		fact := facts[record.id]
		switch record.element.Tag {
		case "F":
			if fact.expr != nil || len(fact.targetDiags) != 1 || fact.targetDiags[0].Code != "parse-error" || fact.targetDiags[0].Source != "parser" {
				t.Errorf("syntax-invalid F fact = %+v, want one parser diagnostic", fact)
			}
		case "Package":
			if fact.provenance != targetPackageFunc || fact.raw == nil || len(fact.targetDiags) != 0 {
				t.Errorf("valid Package sibling fact = %+v, want accepted package function", fact)
			}
		}
	}
}

func TestHarvestComponentTargetsPartitionsMultipleMarkersAndUnrelatedError(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "partition.gsx", `package p
component Page() { <MissingOne/><MissingTwo/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"partition.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var source strings.Builder
	source.WriteString("package p\nvar _ = MissingOutside\nfunc _probe() {\n")
	for _, record := range preprocessed.registry.records {
		if err := markers.emitBinding(&source, record.element, fset); err != nil {
			t.Fatal(err)
		}
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "partition.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != len(preprocessed.registry.records) {
		t.Fatalf("facts = %d, want total %d", len(facts), len(preprocessed.registry.records))
	}
	for _, record := range preprocessed.registry.records {
		fact := facts[record.id]
		want := "undefined: " + record.element.Tag
		if len(fact.targetDiags) != 1 || fact.targetDiags[0].Source != "types" || fact.targetDiags[0].Message != want {
			t.Errorf("<%s> diagnostics = %+v, want only %q", record.element.Tag, fact.targetDiags, want)
		}
	}
	if len(unrelated) != 1 || unrelated[0].Msg != "undefined: MissingOutside" {
		t.Fatalf("unrelated errors = %+v, want only MissingOutside", unrelated)
	}
}

func TestHarvestComponentTargetsRetainsPartialGenericPrefixAndRequiresCleanInstance(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "generic.gsx", `package p
component Page() {
	<Pair/><Pair[int]/><Pair[int,string]/><IntOnly[string]/>
}
`)
	runGenericTargetHarvestTest(t, fset, file)
}

func TestHarvestComponentTargetsDoesNotDuplicateLookupFailure(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "missing.gsx", `package p
component Page() { <Missing/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"missing.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	source.WriteString("package p\nfunc _probe() {\n")
	if err := markers.emitBinding(&source, preprocessed.registry.records[0].element, fset); err != nil {
		t.Fatal(err)
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "missing.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated target-skeleton errors: %+v", unrelated)
	}
	fact := facts[1]
	if len(fact.targetDiags) != 1 || fact.targetDiags[0].Source != "types" || fact.targetDiags[0].Message != "undefined: Missing" {
		t.Fatalf("lookup diagnostics = %+v, want one native checker error", fact.targetDiags)
	}
}

func targetTestImporter() types.Importer {
	namedInterface := func(pkg *types.Package, name string) *types.Named {
		object := types.NewTypeName(token.NoPos, pkg, name, nil)
		named := types.NewNamed(object, types.NewInterfaceType(nil, nil).Complete(), nil)
		pkg.Scope().Insert(object)
		return named
	}
	runtimePkg := types.NewPackage("github.com/gsxhq/gsx", "gsx")
	namedInterface(runtimePkg, "Node")
	attrObject := types.NewTypeName(token.NoPos, runtimePkg, "Attr", nil)
	runtimePkg.Scope().Insert(attrObject)
	attrType := types.NewNamed(attrObject, types.NewStruct(nil, nil), nil)
	attrsObject := types.NewTypeName(token.NoPos, runtimePkg, "Attrs", nil)
	runtimePkg.Scope().Insert(attrsObject)
	types.NewNamed(attrsObject, types.NewSlice(attrType), nil)
	runtimePkg.MarkComplete()
	contextPkg := types.NewPackage("context", "context")
	namedInterface(contextPkg, "Context")
	contextPkg.MarkComplete()
	return mapImporter{
		runtimePkg.Path(): runtimePkg,
		contextPkg.Path(): contextPkg,
	}
}

func TestTargetDiscoverySkeletonUsesVerbatimSignatureAndPreservesNestedScopes(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "discovery.gsx", `package p
import "github.com/gsxhq/gsx"
type Concrete struct{}
func (Concrete) M() int { return 0 }
func Package() int { return 0 }
var Top = func(Inner Concrete) gsx.Node { return <Inner.M/> }(Concrete{})

component Page(Receiver Concrete, Local func() int) {
	<Package/><Receiver.M/><Local/>
	{ func(Inner Concrete) gsx.Node { return <Inner.M/> }(Receiver) }
}
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"discovery.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	skeleton, err := buildComponentTargetSkeleton(file, funcTables{}, fset, bag, markers, syntacticComponentTargetPlan(map[string]*gsxast.File{"discovery.gsx": file}), skeletonTargetDiscovery)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(skeleton.source, "func Page(Receiver Concrete, Local func() int) _gsxrt.Node") {
		t.Fatalf("target skeleton does not preserve authored signature:\n%s", skeleton.source)
	}
	if strings.Contains(skeleton.source, "PageProps") || strings.Contains(skeleton.source, "_gsxp") {
		t.Fatalf("target skeleton leaked shipping Props ABI:\n%s", skeleton.source)
	}
	if err := markers.validateComplete(); err != nil {
		t.Fatal(err)
	}
	parsed, err := goparser.ParseFile(fset, "discovery.target.x.go", skeleton.source, goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	prelude, err := goparser.ParseFile(fset, "_gsxtarget_shared.x.go", analysisPreludeSource("p"), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	packageFiles := []*goast.File{parsed, prelude}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", packageFiles, fset, targetTestImporter(), componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts(packageFiles, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		for _, typeErr := range unrelated {
			t.Logf("unrelated: %s", typeErr)
		}
		t.Fatalf("target discovery produced %d unrelated type errors", len(unrelated))
	}
	if len(facts) != 5 {
		t.Fatalf("facts = %d, want Top Inner.M + Package + Receiver.M + Local + nested Inner.M", len(facts))
	}
	for _, record := range preprocessed.registry.records {
		fact := facts[record.id]
		switch record.element.Tag {
		case "Package":
			if fact.provenance != targetPackageFunc {
				t.Errorf("Package provenance = %d, want package func", fact.provenance)
			}
		case "Receiver.M", "Inner.M":
			if fact.provenance != targetConcreteMethodValue {
				t.Errorf("%s provenance = %d, want concrete method value; diagnostics=%+v", record.element.Tag, fact.provenance, fact.targetDiags)
			}
		case "Local":
			if fact.object != nil || fact.origin != nil || fact.raw != nil || fact.provenance != 0 || len(fact.targetDiags) != 1 || fact.targetDiags[0].Code != "invalid-component-target" {
				t.Errorf("Local fact = %+v, want one rejected-parameter diagnostic", fact)
			}
		}
	}
}

func TestHarvestComponentTargetsRetainsNativeErrorForRejectedProvenance(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "field.gsx", `package p
component Page() { <Field.Fn[int]/> }
`)
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"field.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	source.WriteString("package p\ntype Holder struct{ Fn func() int }\nfunc _probe(Field Holder) {\n")
	if err := markers.emitBinding(&source, preprocessed.registry.records[0].element, fset); err != nil {
		t.Fatal(err)
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "field.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated errors = %+v", unrelated)
	}
	diagnostics := facts[preprocessed.registry.records[0].id].targetDiags
	if len(diagnostics) != 2 {
		t.Fatalf("target diagnostics = %+v, want provenance guidance and the native index error", diagnostics)
	}
	if diagnostics[0].Code != "invalid-component-target" || diagnostics[1].Source != "types" || !strings.Contains(diagnostics[1].Message, "cannot index") {
		t.Fatalf("target diagnostics = %+v, want GSX provenance then native cannot-index error", diagnostics)
	}
}

func TestTargetDiscoverySkeletonKeepsHelpersInOnePackagePrelude(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*gsxast.File{
		"a.gsx": parseTargetTestFile(t, fset, "a.gsx", "package p\ncomponent A() { <div/> }\n"),
		"b.gsx": parseTargetTestFile(t, fset, "b.gsx", "package p\ncomponent B() { <span/> }\n"),
	}
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(files, map[string]bool{"A": true, "B": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}

	var parsed []*goast.File
	plan := syntacticComponentTargetPlan(files)
	for _, path := range []string{"a.gsx", "b.gsx"} {
		first := len(markers.ordered)
		skeleton, err := buildComponentTargetSkeleton(files[path], funcTables{}, fset, bag, markers, plan, skeletonTargetDiscovery)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(skeleton.source, "func _gsxuse") {
			t.Fatalf("%s contains package helper declarations; want one shared prelude:\n%s", path, skeleton.source)
		}
		file, err := goparser.ParseFile(fset, strings.TrimSuffix(path, ".gsx")+".target.x.go", skeleton.source, goparser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if err := bindComponentTargetMarkers(file, first, fset, markers); err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, file)
	}
	prelude, err := goparser.ParseFile(fset, "_gsxtarget_shared.x.go", analysisPreludeSource("p"), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	parsed = append(parsed, prelude)
	_, _, typeErrs := checkComponentTargetPackage("example.com/p", "p", parsed, fset, targetTestImporter(), componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	if len(typeErrs) != 0 {
		t.Fatalf("two-file target package errors = %+v", typeErrs)
	}
}

func TestTargetComponentPlanEmitsOnePublicDeclarationAndEveryVariantBody(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*gsxast.File{
		"a.gsx": parseTargetTestFile(t, fset, "a.gsx", `//go:build variantA

package p
component First() { <span/> }
component Card[T any](value T) { <First/> }
`),
		"b.gsx": parseTargetTestFile(t, fset, "b.gsx", `//go:build variantB

package p
component Second() { <span/> }
component Card[T any](value T) { <Second/> }
`),
	}
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(files, map[string]bool{"First": true, "Second": true, "Card": true}, fset, attrclass.Builtin(), bag)
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
	bodyIndex := 0
	publicCard := true
	for _, path := range []string{"a.gsx", "b.gsx"} {
		for _, declaration := range files[path].Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok || component.Name != "Card" {
				continue
			}
			bodyIndex++
			plan.emissions[component] = componentTargetEmission{
				public:            publicCard,
				splitBody:         true,
				bodyName:          fmt.Sprintf("_gsxtargetbody%d", bodyIndex),
				analysisPropsName: fmt.Sprintf("_gsxtargetprops%d", bodyIndex),
			}
			publicCard = false
		}
	}
	var sources strings.Builder
	var parsed []*goast.File
	for _, path := range []string{"a.gsx", "b.gsx"} {
		first := len(markers.ordered)
		skeleton, err := buildComponentTargetSkeleton(files[path], funcTables{}, fset, bag, markers, plan, skeletonTargetDiscovery)
		if err != nil {
			t.Fatal(err)
		}
		sources.WriteString(skeleton.source)
		file, err := goparser.ParseFile(fset, strings.TrimSuffix(path, ".gsx")+".target.x.go", skeleton.source, goparser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		if err := bindComponentTargetMarkers(file, first, fset, markers); err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, file)
	}
	if got := strings.Count(sources.String(), "func Card["); got != 1 {
		t.Fatalf("public Card declarations = %d, want one\n%s", got, sources.String())
	}
	if got := strings.Count(sources.String(), "func _gsxtargetbody"); got != 2 {
		t.Fatalf("variant body declarations = %d, want two\n%s", got, sources.String())
	}
	prelude, err := goparser.ParseFile(fset, "_gsxtarget_shared.x.go", analysisPreludeSource("p"), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	parsed = append(parsed, prelude)
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", parsed, fset, targetTestImporter(), componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	if len(typeErrs) != 0 {
		t.Fatalf("variant target package errors = %+v", typeErrs)
	}
	facts, unrelated, err := harvestComponentTargetFacts(parsed, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 || len(facts) != 2 {
		t.Fatalf("variant facts=%d unrelated=%+v, want both body targets", len(facts), unrelated)
	}
	for _, fact := range facts {
		if fact.provenance != targetPackageFunc || len(fact.targetDiags) != 0 {
			t.Fatalf("variant body fact = %+v, want accepted package function", fact)
		}
	}
}

func runGenericTargetHarvestTest(t *testing.T, fset *token.FileSet, file *gsxast.File) {
	t.Helper()
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(map[string]*gsxast.File{"generic.gsx": file}, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	markers, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	source.WriteString(`package p
func Pair[A, B any](a A, b B) int { return 0 }
func IntOnly[T ~int](value T) int { return 0 }
func _probe() {
`)
	for _, record := range preprocessed.registry.records {
		if err := markers.emitBinding(&source, record.element, fset); err != nil {
			t.Fatal(err)
		}
	}
	source.WriteString("}\n")
	parsed, err := goparser.ParseFile(fset, "generic.target.x.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindComponentTargetMarkers(parsed, 0, fset, markers); err != nil {
		t.Fatal(err)
	}
	_, info, typeErrs := checkComponentTargetPackage("example.com/p", "p", []*goast.File{parsed}, fset, nil, componentTargetCheckConfig{typeEnvironment: testTypeCheckEnvironment()})
	facts, unrelated, err := harvestComponentTargetFacts([]*goast.File{parsed}, fset, info, typeErrs, markers)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated) != 0 {
		t.Fatalf("unrelated target-skeleton errors: %+v", unrelated)
	}

	byArgs := map[string]componentTargetFact{}
	for _, record := range preprocessed.registry.records {
		byArgs[record.element.Tag+"["+record.element.TypeArgs+"]"] = facts[record.id]
	}
	for key, wantArgs := range map[string]int{"Pair[]": 0, "Pair[int]": 1} {
		fact := byArgs[key]
		if fact.raw == nil || fact.provenance != targetPackageFunc || len(fact.authoredTypeArgs) != wantArgs || fact.explicitInstance != nil || len(fact.targetDiags) != 0 {
			t.Errorf("partial %s fact = %+v, want raw generic callable, %d authored args, no completed instance/diagnostic", key, fact, wantArgs)
		}
	}
	full := byArgs["Pair[int,string]"]
	if full.raw == nil || full.explicitInstance == nil || full.effectiveSignature() == full.raw || len(full.targetDiags) != 0 {
		t.Errorf("full Pair instance fact = %+v, want clean completed instance", full)
	}
	invalid := byArgs["IntOnly[string]"]
	if invalid.raw == nil || invalid.explicitInstance != nil || len(invalid.targetDiags) == 0 {
		t.Errorf("constraint-invalid fact = %+v, want raw callable, deferred error, and no completed instance", invalid)
	}
}

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

	if _, _, _, _, _, err := buildSkeleton(file, funcTables{}, fset, bag, nil, skeletonFull); err != nil {
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
	if _, _, _, _, _, err := buildSkeleton(file, funcTables{}, fset, diag.NewBag(fset), nil, skeletonFull); err != nil {
		t.Fatal(err)
	}
	if interp.Embedded != nil {
		t.Fatal("buildSkeleton mutated Interp.Embedded; preprocessing must be the only materializer")
	}
}

func TestTargetDiscoveryDeclarationFailureIsDiagnosticNotMarkerInvariant(t *testing.T) {
	fset := token.NewFileSet()
	file := parseTargetTestFile(t, fset, "views.gsx", `package views
component (broken) Page() { <Widget/> }
`)
	files := map[string]*gsxast.File{"views.gsx": file}
	bag := diag.NewBag(fset)
	preprocessed, err := preprocessComponentCallSites(files, map[string]bool{"Page": true}, fset, attrclass.Builtin(), bag)
	if err != nil {
		t.Fatal(err)
	}
	if !preprocessed.analysisReady() {
		t.Fatalf("preprocessing failed: %v", bag.Sorted())
	}
	targets, err := newComponentTargetMarkerRegistry(preprocessed.registry)
	if err != nil {
		t.Fatal(err)
	}
	targetBag := diag.NewBag(fset)
	if _, err := buildComponentTargetSkeleton(
		file, funcTables{}, fset, targetBag, targets, syntacticComponentTargetPlan(files), skeletonTargetDiscovery,
	); err != nil {
		t.Fatalf("target skeleton returned an infrastructure error: %v", err)
	}
	diagnostics := targetBag.Sorted()
	if len(diagnostics) != 1 || diagnostics[0].Code != "invalid-recv" {
		t.Fatalf("target diagnostics = %+v, want one invalid-recv error", diagnostics)
	}
	if err := targets.validateComplete(); err == nil {
		t.Fatal("skipped invalid declaration unexpectedly produced its nested target marker")
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
