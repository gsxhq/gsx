package lsp

import (
	"crypto/sha256"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

func TestDefinitionSourceIndex(t *testing.T) {
	const page = `package page

import (
	str "strings"
	widgets "example.com/app/widgets"
)

type Page struct{}
type Alias string

func helper(input widgets.Label) Alias {
	cleaned := str.TrimSpace(string(input))
	return Alias(cleaned)
}

func around(input Alias) Alias {
	local := input
	node := <span>{helper(widgets.Label(local))}</span>
	_ = node
	return local
}

component Card[T widgets.Labelish](value T) {
	<strong>{helper(widgets.Label(value))}</strong>
}

component (p *Page) Render(label Alias) {
	<Card[Alias] value={label}/>
	<widgets.Box[widgets.Label] value={widgets.Label(label)}/>
	<p>{around(label)} {p != nil}</p>
}
`
	const widgets = `package widgets

type Label string
type Labelish interface{ ~string }
`
	const box = `package widgets

component Box[T Labelish](value T) {
	<span>{value}</span>
}
`
	pkg, path := analyzedLSPModule(t, map[string]string{
		"page/page.gsx":       page,
		"widgets/types.go":    widgets,
		"widgets/widgets.gsx": box,
	}, "page/page.gsx")
	if pkg.SourceIndex == nil {
		t.Fatal("analyzed package has no SourceIndex")
	}

	nth := func(text, needle string, occurrence int) int {
		t.Helper()
		start := 0
		for current := 0; current <= occurrence; current++ {
			relative := strings.Index(text[start:], needle)
			if relative < 0 {
				t.Fatalf("occurrence %d of %q not found", occurrence, needle)
			}
			start += relative
			if current == occurrence {
				return start
			}
			start += len(needle)
		}
		panic("unreachable")
	}
	span := func(needle string, occurrence int) sourceintel.Span {
		start := nth(page, needle, occurrence)
		return sourceintel.Span{Path: path, Start: start, End: start + len(needle)}
	}
	spanAt := func(start, length int) sourceintel.Span {
		return sourceintel.Span{Path: path, Start: start, End: start + length}
	}
	receiverDecl := nth(page, "p *Page", 0)
	typeParamDecl := nth(page, "T widgets.Labelish", 0)

	tests := []struct {
		name       string
		cursor     int
		wantSpan   sourceintel.Span
		wantGoBase string
	}{
		{name: "component declaration to self", cursor: nth(page, "Card", 0), wantSpan: span("Card", 0)},
		{name: "receiver variable declaration to self", cursor: receiverDecl, wantSpan: spanAt(receiverDecl, len("p"))},
		{name: "receiver variable use", cursor: nth(page, "{p != nil}", 0) + 1, wantSpan: spanAt(receiverDecl, len("p"))},
		{name: "receiver type", cursor: nth(page, "*Page", 0) + 1, wantSpan: span("Page", 0)},
		{name: "type parameter declaration to self", cursor: typeParamDecl, wantSpan: spanAt(typeParamDecl, len("T"))},
		{name: "type parameter constraint qualifier", cursor: nth(page, "widgets.Labelish", 0), wantSpan: span("widgets", 0)},
		{name: "type parameter constraint", cursor: nth(page, "widgets.Labelish", 0) + len("widgets."), wantGoBase: "types.go"},
		{name: "parameter declaration to self", cursor: nth(page, "value T", 0), wantSpan: span("value", 0)},
		{name: "parameter use", cursor: nth(page, "widgets.Label(value)", 0) + len("widgets.Label("), wantSpan: span("value", 0)},
		{name: "parameter type", cursor: nth(page, "value T", 0) + len("value "), wantSpan: spanAt(typeParamDecl, len("T"))},
		{name: "top-level helper declaration to self", cursor: nth(page, "helper", 0), wantSpan: span("helper", 0)},
		{name: "top-level helper call", cursor: nth(page, "helper", 1), wantSpan: span("helper", 0)},
		{name: "top-level helper signature parameter type qualifier", cursor: nth(page, "widgets.Label", 0), wantSpan: span("widgets", 0)},
		{name: "top-level helper signature parameter type", cursor: nth(page, "widgets.Label", 0) + len("widgets."), wantGoBase: "types.go"},
		{name: "top-level helper return type", cursor: nth(page, "Alias", 1), wantSpan: span("Alias", 0)},
		{name: "top-level helper body local declaration to self", cursor: nth(page, "cleaned", 0), wantSpan: span("cleaned", 0)},
		{name: "top-level helper body local use", cursor: nth(page, "cleaned", 1), wantSpan: span("cleaned", 0)},
		{name: "top-level helper selector", cursor: nth(page, "TrimSpace", 0), wantGoBase: "strings.go"},
		{name: "top-level helper import qualifier", cursor: nth(page, "str.TrimSpace", 0), wantSpan: span("str", 0)},
		{name: "same-package explicit component type argument", cursor: nth(page, "Card[Alias]", 0) + len("Card["), wantSpan: span("Alias", 0)},
		{name: "cross-package explicit component type argument qualifier", cursor: nth(page, "Box[widgets.Label]", 0) + len("Box["), wantSpan: span("widgets", 0)},
		{name: "cross-package explicit component type argument", cursor: nth(page, "Box[widgets.Label]", 0) + len("Box[widgets."), wantGoBase: "types.go"},
		{name: "GoWithElements signature", cursor: nth(page, "around", 0), wantSpan: span("around", 0)},
		{name: "Go text before nested markup", cursor: nth(page, "local :=", 0), wantSpan: span("local", 0)},
		{name: "Go text inside nested markup", cursor: nth(page, "Label(local)", 0) + len("Label("), wantSpan: span("local", 0)},
		{name: "Go text after nested markup", cursor: nth(page, "return local", 0) + len("return "), wantSpan: span("local", 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := semanticDefinition(pkg, path, []byte(page), test.cursor)
			if !ok {
				t.Fatal("semanticDefinition returned no target")
			}
			if test.wantGoBase == "" {
				if got.Authored != test.wantSpan || got.Go.Filename != "" {
					t.Fatalf("target = %+v, want authored %+v", got, test.wantSpan)
				}
				return
			}
			if got.Authored != (sourceintel.Span{}) || filepath.Base(got.Go.Filename) != test.wantGoBase {
				t.Fatalf("target = %+v, want real Go file %q", got, test.wantGoBase)
			}
		})
	}

	t.Run("stale source", func(t *testing.T) {
		if got, ok := semanticDefinition(pkg, path, []byte(page+"\n"), nth(page, "helper", 1)); ok {
			t.Fatalf("semanticDefinition(stale source) = %+v, want no target", got)
		}
	})
	t.Run("authored target does not require Go file set", func(t *testing.T) {
		withoutFset := *pkg
		withoutFset.Fset = nil
		got, ok := semanticDefinition(&withoutFset, path, []byte(page), nth(page, "helper", 1))
		if !ok || got.Authored != span("helper", 0) {
			t.Fatalf("semanticDefinition(without Fset) = (%+v, %t), want authored helper", got, ok)
		}
	})
	t.Run("unindexed markup is null", func(t *testing.T) {
		if got, ok := semanticDefinition(pkg, path, []byte(page), nth(page, "<strong>", 0)+1); ok {
			t.Fatalf("semanticDefinition(markup) = %+v, want no target", got)
		}
	})
	t.Run("generated glue object is null", func(t *testing.T) {
		cardOffset := nth(page, "Card", 0)
		index, fset := generatedGlueIndex(t, path, page, cardOffset)
		gluePackage := *pkg
		gluePackage.SourceIndex = index
		gluePackage.Fset = fset
		if got, ok := semanticDefinition(&gluePackage, path, []byte(page), cardOffset); ok {
			t.Fatalf("semanticDefinition(generated glue) = %+v, want no target", got)
		}
	})
}

func TestDefinitionSourceIndexHandler(t *testing.T) {
	const source = `package page

component Card() {
	<p>card</p>
}
`
	pkg, path := analyzedLSPPackage(t, source)
	uri := pathToURI(path)
	cursor := positionForByteOffset(source, strings.Index(source, "Card"), encUTF16)
	out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+definitionFrame(2, uri, cursor)+exitFrame())
	got := definitionLocation(t, out, 2)
	if got == nil {
		t.Fatalf("definition returned null; output:\n%s", out)
	}
	wantRange := rangeForSpan(source, strings.Index(source, "Card"), strings.Index(source, "Card")+len("Card"), encUTF16)
	if got.URI != uri || got.Range != wantRange {
		t.Fatalf("definition = %+v, want %s at %+v", got, uri, wantRange)
	}
}

func TestDefinitionSpecializedFactsPrecedeSourceIndex(t *testing.T) {
	const source = `package page

type First string
type Other string
type Else string

component Card() { <p>card</p> }

component Page(value First) {
	<Card/>
	<p>{value}</p>
}

component OtherPage(value Other) {
	<p>{value}</p>
}
`
	pkg, path := analyzedLSPPackage(t, source)
	uri := pathToURI(path)

	t.Run("component call", func(t *testing.T) {
		callOffset := strings.Index(source, "<Card") + 1
		pkg.SourceIndex = conflictingDefinitionIndex(t, path, source, "Card", callOffset, strings.Index(source, "type Else")+len("type "))
		semantic, ok := semanticDefinition(pkg, path, []byte(source), callOffset)
		if !ok || semantic.Authored.Start != strings.Index(source, "type Else")+len("type ") {
			t.Fatalf("semantic fallback = %+v, want conflicting Else declaration", semantic)
		}

		cursor := positionForByteOffset(source, callOffset, encUTF16)
		out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+definitionFrame(2, uri, cursor)+exitFrame())
		got := definitionLocation(t, out, 2)
		want := positionForByteOffset(source, strings.Index(source, "component Card")+len("component "), encUTF16)
		if got == nil || got.URI != uri || got.Range.Start != want {
			t.Fatalf("definition = %+v, want specialized Card target at %+v; output:\n%s", got, want, out)
		}
	})

	t.Run("signature type", func(t *testing.T) {
		firstOffset := strings.Index(source, "value First") + len("value ")
		firstObject, _, _, ok := signatureTypeIdentAt(pkg, path, firstOffset)
		if !ok {
			t.Fatal("signatureTypeIdentAt returned no original First object")
		}
		firstDefinition := strings.Index(source, "type First") + len("type ")
		otherDefinition := strings.Index(source, "type Other") + len("type ")
		pkg.SourceIndex = conflictingDefinitionIndexPreservingObject(
			t, path, source, "First", firstOffset, "Other", otherDefinition, firstObject, firstDefinition,
		)
		semantic, ok := semanticDefinition(pkg, path, []byte(source), firstOffset)
		if !ok || semantic.Authored.Start != otherDefinition {
			t.Fatalf("semantic fallback = %+v, want conflicting Other declaration", semantic)
		}
		if preserved, ok := pkg.SourceIndex.Definition(firstObject); !ok || preserved.Start != firstDefinition {
			t.Fatalf("preserved specialized definition = (%+v, %t), want First at %d", preserved, ok, firstDefinition)
		}

		cursor := positionForByteOffset(source, firstOffset, encUTF16)
		out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+definitionFrame(2, uri, cursor)+exitFrame())
		got := definitionLocation(t, out, 2)
		want := positionForByteOffset(source, strings.Index(source, "type First")+len("type "), encUTF16)
		if got == nil || got.URI != uri || got.Range.Start != want {
			t.Fatalf("definition = %+v, want specialized First target at %+v; output:\n%s", got, want, out)
		}
	})
}

// conflictingDefinitionIndexPreservingObject keeps Package Info and
// SourceIndex snapshot-consistent for the specialized object while assigning a
// different object to the indexed use. The handler must therefore prefer the
// specialized First object non-vacuously, yet can still convert that object's
// exact authored span through the one authoritative index.
func conflictingDefinitionIndexPreservingObject(
	t *testing.T,
	path, source, useName string,
	useStart int,
	conflictingName string,
	conflictingDefinitionStart int,
	preservedObject types.Object,
	preservedDefinitionStart int,
) *sourceintel.Index {
	t.Helper()
	generated := "package fake\n\ntype " + useName + " string\ntype " + conflictingName + " string\nvar _ " + useName + "\n"
	preservedGenerated := strings.Index(generated, useName)
	conflictingGenerated := strings.Index(generated, conflictingName)
	useGenerated := strings.LastIndex(generated, useName)
	segments := []sourceintel.Segment{
		{
			Source:         sourceintel.Span{Path: path, Start: preservedDefinitionStart, End: preservedDefinitionStart + len(useName)},
			GeneratedStart: preservedGenerated,
			GeneratedEnd:   preservedGenerated + len(useName),
			Capabilities:   sourceintel.Definition | sourceintel.Hover,
		},
		{
			Source:         sourceintel.Span{Path: path, Start: conflictingDefinitionStart, End: conflictingDefinitionStart + len(conflictingName)},
			GeneratedStart: conflictingGenerated,
			GeneratedEnd:   conflictingGenerated + len(conflictingName),
			Capabilities:   sourceintel.Definition | sourceintel.Hover,
		},
		{
			Source:         sourceintel.Span{Path: path, Start: useStart, End: useStart + len(useName)},
			GeneratedStart: useGenerated,
			GeneratedEnd:   useGenerated + len(useName),
			Capabilities:   sourceintel.Definition | sourceintel.Hover,
		},
	}
	sourceMap, err := sourceintel.NewSourceMap(len(generated), len(source), path, segments, nil)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.go", generated, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
	if _, err := new(types.Config).Check("fake", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	var preservedIdent, conflictingIdent, useIdent *ast.Ident
	ast.Inspect(file, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		offset := fset.Position(ident.Pos()).Offset
		switch offset {
		case preservedGenerated:
			preservedIdent = ident
		case conflictingGenerated:
			conflictingIdent = ident
		case useGenerated:
			useIdent = ident
		}
		return true
	})
	if preservedIdent == nil || conflictingIdent == nil || useIdent == nil {
		t.Fatalf("generated identifiers missing: preserved=%v conflicting=%v use=%v", preservedIdent, conflictingIdent, useIdent)
	}
	conflictingObject := info.Defs[conflictingIdent]
	if conflictingObject == nil {
		t.Fatal("conflicting declaration has no object")
	}
	info.Defs[preservedIdent] = preservedObject
	info.Uses[useIdent] = conflictingObject
	return sourceintel.BuildIndex(info, []sourceintel.MappedFile{{
		AST:           file,
		TokenFile:     fset.File(file.Pos()),
		SourceMap:     sourceMap,
		SourceVersion: sourceintel.SourceVersion{Size: len(source), SHA256: sha256.Sum256([]byte(source))},
	}})
}

func conflictingDefinitionIndex(t *testing.T, path, source, name string, useStart, definitionStart int) *sourceintel.Index {
	t.Helper()
	generated := "package fake\n\ntype " + name + " string\nvar _ " + name + "\n"
	definitionGenerated := strings.Index(generated, name)
	useGenerated := strings.LastIndex(generated, name)
	segments := []sourceintel.Segment{
		{
			Source:         sourceintel.Span{Path: path, Start: definitionStart, End: definitionStart + len(name)},
			GeneratedStart: definitionGenerated,
			GeneratedEnd:   definitionGenerated + len(name),
			Capabilities:   sourceintel.Definition | sourceintel.Hover,
		},
		{
			Source:         sourceintel.Span{Path: path, Start: useStart, End: useStart + len(name)},
			GeneratedStart: useGenerated,
			GeneratedEnd:   useGenerated + len(name),
			Capabilities:   sourceintel.Definition | sourceintel.Hover,
		},
	}
	sourceMap, err := sourceintel.NewSourceMap(len(generated), len(source), path, segments, nil)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.go", generated, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
	if _, err := new(types.Config).Check("fake", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	return sourceintel.BuildIndex(info, []sourceintel.MappedFile{{
		AST:           file,
		TokenFile:     fset.File(file.Pos()),
		SourceMap:     sourceMap,
		SourceVersion: sourceintel.SourceVersion{Size: len(source), SHA256: sha256.Sum256([]byte(source))},
	}})
}

func generatedGlueIndex(t *testing.T, path, source string, useStart int) (*sourceintel.Index, *token.FileSet) {
	t.Helper()
	const generated = "package fake\n\nvar glue int\nvar _ = glue\n"
	useGenerated := strings.LastIndex(generated, "glue")
	sourceMap, err := sourceintel.NewSourceMap(len(generated), len(source), path, []sourceintel.Segment{{
		Source:         sourceintel.Span{Path: path, Start: useStart, End: useStart + len("glue")},
		GeneratedStart: useGenerated,
		GeneratedEnd:   useGenerated + len("glue"),
		Capabilities:   sourceintel.Definition | sourceintel.Hover,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.x.go", generated, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
	if _, err := new(types.Config).Check("fake", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	index := sourceintel.BuildIndex(info, []sourceintel.MappedFile{{
		AST:           file,
		TokenFile:     fset.File(file.Pos()),
		SourceMap:     sourceMap,
		SourceVersion: sourceintel.SourceVersion{Size: len(source), SHA256: sha256.Sum256([]byte(source))},
	}})
	return index, fset
}

func definitionFrame(id int, uri string, position Position) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "textDocument/definition",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     position,
		},
	})
}

func definitionLocation(t *testing.T, output string, id int) *Location {
	t.Helper()
	for _, message := range readFrames(t, output) {
		var gotID int
		if err := json.Unmarshal(message["id"], &gotID); err != nil || gotID != id {
			continue
		}
		if string(message["result"]) == "null" {
			return nil
		}
		var location Location
		if err := json.Unmarshal(message["result"], &location); err != nil {
			t.Fatalf("decode definition result: %v", err)
		}
		return &location
	}
	t.Fatalf("no definition response for id %d in:\n%s", id, output)
	return nil
}
