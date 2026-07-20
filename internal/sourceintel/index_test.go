package sourceintel

import (
	"crypto/sha256"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"strings"
	"testing"
)

func TestBuildIndexMapsDefinitionsUsesAndExpressions(t *testing.T) {
	const generated = `package p

func helper(x int) int {
	return (x + 1)
}

var glue = helper(2)
`
	const authored = `func helper(x int) int {
	return (x + 1)
}
<generated glue has no mapping>`

	generatedStart := strings.Index(generated, "func helper")
	generatedEnd := strings.Index(generated, "\n}\n") + len("\n}")
	authoredEnd := strings.Index(authored, "\n}\n") + len("\n}")
	info, mapped := parseAndCheckMappedFile(t, generated, authored, []Segment{{
		Source:         Span{Path: "view.gsx", Start: 0, End: authoredEnd},
		GeneratedStart: generatedStart,
		GeneratedEnd:   generatedEnd,
		Capabilities:   Definition | Hover,
	}}, nil)

	index := BuildIndex(info, []MappedFile{mapped})
	helperDefinition := findIdent(t, mapped.AST, "helper", 0)
	helperObject := info.Defs[helperDefinition]
	if helperObject == nil {
		t.Fatal("helper definition has no types.Object")
	}
	gotDefinition, ok := index.At("view.gsx", strings.Index(authored, "helper"))
	if !ok {
		t.Fatal("At(helper definition) returned no occurrence")
	}
	if gotDefinition.Kind != IdentifierDefinition || gotDefinition.Object != helperObject {
		t.Fatalf("At(helper definition) = %#v, want definition of %v", gotDefinition, helperObject)
	}

	xDefinition := findIdent(t, mapped.AST, "x", 0)
	xUse := findIdent(t, mapped.AST, "x", 1)
	if info.Defs[xDefinition] == nil || info.Uses[xUse] != info.Defs[xDefinition] {
		t.Fatal("type checker did not preserve x declaration/use identity")
	}
	gotUse, ok := index.At("view.gsx", strings.LastIndex(authored, "x"))
	if !ok {
		t.Fatal("At(x use) returned no occurrence")
	}
	if gotUse.Kind != IdentifierUse || gotUse.Object != info.Uses[xUse] {
		t.Fatalf("At(x use) = %#v, want use of %v", gotUse, info.Uses[xUse])
	}

	plusOffset := strings.Index(authored, "+")
	gotExpression, ok := index.At("view.gsx", plusOffset)
	if !ok {
		t.Fatal("At(+) returned no occurrence")
	}
	if gotExpression.Kind != Expression || !gotExpression.HasTypeValue {
		t.Fatalf("At(+) = %#v, want typed expression", gotExpression)
	}
	if got, want := gotExpression.Span, spanForSubstring(authored, "x + 1", 0); got != want {
		t.Fatalf("At(+) span = %+v, want smallest expression %+v", got, want)
	}
	if got := gotExpression.TypeAndValue.Type.String(); got != "int" {
		t.Fatalf("At(+) type = %q, want int", got)
	}

	glueObject := info.Defs[findIdent(t, mapped.AST, "glue", 0)]
	if _, ok := index.Definition(glueObject); ok {
		t.Fatal("Definition(generated glue) succeeded")
	}
	if _, ok := index.At("view.gsx", strings.Index(authored, "generated glue")); ok {
		t.Fatal("At(unmapped authored placeholder) returned an occurrence")
	}
}

func TestBuildIndexDefinitionsUseOriginIdentity(t *testing.T) {
	const source = `package p

type Box[T any] struct {
	Value T
}

func (Box[T]) Get() T { return *new(T) }
`
	info, mapped := parseAndCheckMappedFile(t, source, source, []Segment{{
		Source:         Span{Path: "view.gsx", Start: 0, End: len(source)},
		GeneratedStart: 0,
		GeneratedEnd:   len(source),
		Capabilities:   Definition | Hover,
	}}, nil)
	index := BuildIndex(info, []MappedFile{mapped})

	originMethod := info.Defs[findIdent(t, mapped.AST, "Get", 0)].(*types.Func)
	receiver := originMethod.Type().(*types.Signature).Recv().Type()
	receiverNamed, ok := receiver.(*types.Named)
	if !ok {
		t.Fatalf("method receiver type = %T, want *types.Named", receiver)
	}
	instantiatedType, err := types.Instantiate(nil, receiverNamed.Origin(), []types.Type{types.Typ[types.Int]}, true)
	if err != nil {
		t.Fatalf("instantiate Box[int]: %v", err)
	}
	instantiatedNamed := instantiatedType.(*types.Named)
	methodSelection := types.NewMethodSet(instantiatedNamed).Lookup(originMethod.Pkg(), originMethod.Name())
	if methodSelection == nil {
		t.Fatal("Box[int] method set has no Get")
	}
	instantiatedMethod := methodSelection.Obj().(*types.Func)
	if instantiatedMethod == originMethod {
		t.Fatal("instantiated method is the origin method")
	}

	originField := info.Defs[findIdent(t, mapped.AST, "Value", 0)].(*types.Var)
	instantiatedField, _, _ := types.LookupFieldOrMethod(instantiatedNamed, true, originField.Pkg(), originField.Name())
	if instantiatedField == nil {
		t.Fatal("Box[int] has no Value field")
	}
	concreteField := instantiatedField.(*types.Var)
	if concreteField == originField {
		t.Fatal("instantiated field is the origin field")
	}

	tests := []struct {
		name       string
		origin     types.Object
		concrete   types.Object
		wantSource string
	}{
		{"function", originMethod, instantiatedMethod, "Get"},
		{"variable", originField, concreteField, "Value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := spanForSubstring(source, test.wantSource, 0)
			for _, object := range []types.Object{test.origin, test.concrete, Origin(test.concrete)} {
				got, ok := index.Definition(object)
				if !ok || got != want {
					t.Fatalf("Definition(%v) = (%+v, %t), want (%+v, true)", object, got, ok, want)
				}
			}
			if got := Origin(test.concrete); got != test.origin {
				t.Fatalf("Origin(concrete) = %v, want %v", got, test.origin)
			}
		})
	}

	typeName := info.Defs[findIdent(t, mapped.AST, "Box", 0)]
	if got := Origin(typeName); got != typeName {
		t.Fatalf("Origin(type name) = %v, want unchanged %v", got, typeName)
	}
}

func TestBuildIndexDeclarations(t *testing.T) {
	const common = `package p

func Plain() {}

type Alias = int

type Record struct{}

type Contract interface{ Do() }

const (
	First = 1
	Second = 2
)

var (
	Count = 3
	Label string
)

func (Record) Method() {}

`
	const generatedMixed = `func Mixed() int { return 1 }`
	const authoredMixed = `func Mixed() int { return <strong/> }`
	generated := common + generatedMixed + "\n"
	authored := common + authoredMixed + "\n"
	mixedGeneratedStart := len(common)
	mixedSourceStart := len(common)
	mixedNameGeneratedStart := mixedGeneratedStart + strings.Index(generatedMixed, "Mixed")
	mixedNameSourceStart := mixedSourceStart + strings.Index(authoredMixed, "Mixed")
	info, mapped := parseAndCheckMappedFile(t, generated, authored, []Segment{
		{
			Source:         Span{Path: "view.gsx", Start: 0, End: len(common)},
			GeneratedStart: 0,
			GeneratedEnd:   len(common),
			Capabilities:   Symbol,
		},
		{
			Source:         Span{Path: "view.gsx", Start: mixedNameSourceStart, End: mixedNameSourceStart + len("Mixed")},
			GeneratedStart: mixedNameGeneratedStart,
			GeneratedEnd:   mixedNameGeneratedStart + len("Mixed"),
			Capabilities:   Symbol,
		},
	}, []DeclarationRegion{{
		Source:         Span{Path: "view.gsx", Start: mixedSourceStart, End: mixedSourceStart + len(authoredMixed)},
		GeneratedStart: mixedGeneratedStart,
		GeneratedEnd:   mixedGeneratedStart + len(generatedMixed),
	}})

	index := BuildIndex(info, []MappedFile{mapped})
	want := []struct {
		name        string
		kind        DeclarationKind
		container   string
		nameSpan    Span
		declaration string
	}{
		{"Plain", DeclarationFunction, "", spanForSubstring(authored, "Plain", 0), "func Plain() {}"},
		{"Alias", DeclarationType, "", spanForSubstring(authored, "Alias", 0), "type Alias = int"},
		{"Record", DeclarationStruct, "", spanForSubstring(authored, "Record", 0), "type Record struct{}"},
		{"Contract", DeclarationInterface, "", spanForSubstring(authored, "Contract", 0), "type Contract interface{ Do() }"},
		{"First", DeclarationConstant, "", spanForSubstring(authored, "First", 0), "const (\n\tFirst = 1\n\tSecond = 2\n)"},
		{"Second", DeclarationConstant, "", spanForSubstring(authored, "Second", 0), "const (\n\tFirst = 1\n\tSecond = 2\n)"},
		{"Count", DeclarationVariable, "", spanForSubstring(authored, "Count", 0), "var (\n\tCount = 3\n\tLabel string\n)"},
		{"Label", DeclarationVariable, "", spanForSubstring(authored, "Label", 0), "var (\n\tCount = 3\n\tLabel string\n)"},
		{"Method", DeclarationMethod, "Record", spanForSubstring(authored, "Method", 0), "func (Record) Method() {}"},
		{"Mixed", DeclarationFunction, "", spanForSubstring(authored, "Mixed", 0), authoredMixed},
	}
	got := index.Declarations("view.gsx")
	if len(got) != len(want) {
		t.Fatalf("Declarations length = %d, want %d\ngot: %#v", len(got), len(want), got)
	}
	for position, expected := range want {
		declaration := got[position]
		if declaration.Name != expected.name || declaration.Kind != expected.kind || declaration.Container != expected.container {
			t.Errorf("declaration %d identity = (%q, %d, %q), want (%q, %d, %q)", position, declaration.Name, declaration.Kind, declaration.Container, expected.name, expected.kind, expected.container)
		}
		if declaration.NameSpan != expected.nameSpan {
			t.Errorf("declaration %q name span = %+v, want %+v", expected.name, declaration.NameSpan, expected.nameSpan)
		}
		if expectedDeclSpan := spanForSubstring(authored, expected.declaration, 0); declaration.DeclSpan != expectedDeclSpan {
			t.Errorf("declaration %q full span = %+v, want %+v", expected.name, declaration.DeclSpan, expectedDeclSpan)
		}
		if wantObject := info.Defs[findIdent(t, mapped.AST, expected.name, 0)]; declaration.Object != wantObject {
			t.Errorf("declaration %q object = %v, want %v", expected.name, declaration.Object, wantObject)
		}
	}

	got[0].Name = "mutated"
	if declarations := index.Declarations("view.gsx"); declarations[0].Name != "Plain" {
		t.Fatalf("Declarations returned retained slice: first name = %q", declarations[0].Name)
	}
}

func TestIndexDoesNotRetainASTOrSourceBytes(t *testing.T) {
	const source = "package p\nvar Answer = 42\n"
	info, mapped := parseAndCheckMappedFile(t, source, source, []Segment{{
		Source:         Span{Path: "view.gsx", Start: 0, End: len(source)},
		GeneratedStart: 0,
		GeneratedEnd:   len(source),
		Capabilities:   Definition | Hover | Symbol,
	}}, nil)
	index := BuildIndex(info, []MappedFile{mapped})

	indexValue := reflect.ValueOf(index).Elem()
	allowedFields := map[string]reflect.Type{
		"occurrences":  reflect.TypeFor[map[string][]Occurrence](),
		"definitions":  reflect.TypeFor[map[types.Object]Span](),
		"declarations": reflect.TypeFor[map[string][]Declaration](),
		"sources":      reflect.TypeFor[map[string]SourceVersion](),
	}
	if got, want := indexValue.NumField(), len(allowedFields); got != want {
		t.Fatalf("Index has %d concrete fields, want %d", got, want)
	}
	for fieldIndex := range indexValue.NumField() {
		field := indexValue.Type().Field(fieldIndex)
		want, ok := allowedFields[field.Name]
		if !ok {
			t.Errorf("Index retains unexpected field %q of type %v", field.Name, field.Type)
			continue
		}
		if field.Type != want {
			t.Errorf("Index field %q type = %v, want %v", field.Name, field.Type, want)
		}
	}

	assertNoRetainedSourceInputs(t, reflect.ValueOf(index), make(map[reflectionVisit]bool), "Index")
}

func TestIndexMatchesExactSourceVersion(t *testing.T) {
	source := []byte("same-sized authored source")
	sourceMap, err := NewSourceMap(0, len(source), "view.gsx", nil, nil)
	if err != nil {
		t.Fatalf("NewSourceMap: %v", err)
	}
	index := BuildIndex(nil, []MappedFile{{
		SourceMap: sourceMap,
		SourceVersion: SourceVersion{
			Size:   len(source),
			SHA256: sha256.Sum256(source),
		},
	}})

	if !index.MatchesSource("view.gsx", source) {
		t.Fatal("MatchesSource rejected exact source bytes")
	}
	changed := append([]byte(nil), source...)
	changed[len(changed)-1] = 'X'
	if len(changed) != len(source) {
		t.Fatal("same-length test mutation changed source size")
	}
	if index.MatchesSource("view.gsx", changed) {
		t.Fatal("MatchesSource accepted same-length content change")
	}
	if index.MatchesSource("view.gsx", append(source, '!')) {
		t.Fatal("MatchesSource accepted different source size")
	}
	if index.MatchesSource("missing.gsx", source) {
		t.Fatal("MatchesSource accepted an unknown path")
	}
}

type reflectionVisit struct {
	typeOf  reflect.Type
	pointer uintptr
}

func assertNoRetainedSourceInputs(t *testing.T, value reflect.Value, visited map[reflectionVisit]bool, path string) {
	t.Helper()
	if !value.IsValid() {
		return
	}
	typeOf := value.Type()
	if typeOf == reflect.TypeFor[*token.File]() || typeOf == reflect.TypeFor[*SourceMap]() ||
		typeOf == reflect.TypeFor[[]byte]() || isASTType(typeOf) {
		t.Errorf("%s retains forbidden %v", path, typeOf)
		return
	}

	switch value.Kind() {
	case reflect.Interface:
		if !value.IsNil() {
			assertNoRetainedSourceInputs(t, value.Elem(), visited, path+".(interface)")
		}
	case reflect.Pointer:
		if value.IsNil() {
			return
		}
		visit := reflectionVisit{typeOf: typeOf, pointer: value.Pointer()}
		if visited[visit] {
			return
		}
		visited[visit] = true
		assertNoRetainedSourceInputs(t, value.Elem(), visited, path+".*")
	case reflect.Struct:
		for fieldIndex := range value.NumField() {
			field := value.Type().Field(fieldIndex)
			assertNoRetainedSourceInputs(t, value.Field(fieldIndex), visited, path+"."+field.Name)
		}
	case reflect.Map:
		if value.IsNil() {
			return
		}
		visit := reflectionVisit{typeOf: typeOf, pointer: value.Pointer()}
		if visited[visit] {
			return
		}
		visited[visit] = true
		iterator := value.MapRange()
		for iterator.Next() {
			assertNoRetainedSourceInputs(t, iterator.Key(), visited, path+"[key]")
			assertNoRetainedSourceInputs(t, iterator.Value(), visited, path+"[value]")
		}
	case reflect.Slice:
		if value.IsNil() {
			return
		}
		visit := reflectionVisit{typeOf: typeOf, pointer: value.Pointer()}
		if visited[visit] {
			return
		}
		visited[visit] = true
		for elementIndex := range value.Len() {
			assertNoRetainedSourceInputs(t, value.Index(elementIndex), visited, path+"[]")
		}
	case reflect.Array:
		for elementIndex := range value.Len() {
			assertNoRetainedSourceInputs(t, value.Index(elementIndex), visited, path+"[]")
		}
	}
}

func isASTType(typeOf reflect.Type) bool {
	for typeOf.Kind() == reflect.Pointer {
		typeOf = typeOf.Elem()
	}
	return typeOf.PkgPath() == "go/ast"
}

func parseAndCheckMappedFile(t *testing.T, generated, authored string, segments []Segment, regions []DeclarationRegion) (*types.Info, MappedFile) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "view.x.go", generated, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse generated Go: %v", err)
	}
	info := &types.Info{
		Defs:  make(map[*ast.Ident]types.Object),
		Uses:  make(map[*ast.Ident]types.Object),
		Types: make(map[ast.Expr]types.TypeAndValue),
	}
	if _, err := new(types.Config).Check("example.com/p", fset, []*ast.File{file}, info); err != nil {
		t.Fatalf("type-check generated Go: %v", err)
	}
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil {
		t.Fatal("parsed file has no token.File")
	}
	sourceMap, err := NewSourceMap(len(generated), len(authored), "view.gsx", segments, regions)
	if err != nil {
		t.Fatalf("NewSourceMap: %v", err)
	}
	return info, MappedFile{
		AST:       file,
		TokenFile: tokenFile,
		SourceMap: sourceMap,
		SourceVersion: SourceVersion{
			Size:   len(authored),
			SHA256: sha256.Sum256([]byte(authored)),
		},
	}
}

func findIdent(t *testing.T, file *ast.File, name string, ordinal int) *ast.Ident {
	t.Helper()
	var matches []*ast.Ident
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && ident.Name == name {
			matches = append(matches, ident)
		}
		return true
	})
	if ordinal < 0 || ordinal >= len(matches) {
		t.Fatalf("identifier %q ordinal %d not found; have %d", name, ordinal, len(matches))
	}
	return matches[ordinal]
}

func spanForSubstring(source, substring string, ordinal int) Span {
	start := -1
	remaining := source
	base := 0
	for range ordinal + 1 {
		offset := strings.Index(remaining, substring)
		if offset < 0 {
			return Span{}
		}
		start = base + offset
		base = start + len(substring)
		remaining = source[base:]
	}
	return Span{Path: "view.gsx", Start: start, End: start + len(substring)}
}
