package lsp

import (
	"encoding/json"
	"go/types"
	"strings"
	"testing"
)

func TestHoverSourceIndex(t *testing.T) {
	const page = `package page

import (
	str "strings"
	"time"
	widgets "example.com/app/widgets"
)

type Page struct{}
type Alias string

func helper(input widgets.Label) Alias {
	cleaned := str.TrimSpace(string(input))
	return Alias(cleaned)
}

func duration(d time.Duration) float64 {
	return d.Hours()
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
	receiverDecl := nth(page, "p *Page", 0)
	typeParamDecl := nth(page, "T widgets.Labelish", 0)
	wholeCall := nth(page, "d.Hours()", 0) + len("d.Hours")

	tests := []struct {
		name          string
		cursor        int
		wantFragments []string
	}{
		{name: "component declaration", cursor: nth(page, "Card", 0), wantFragments: []string{"func Card", "[T widgets.Labelish]", "value T"}},
		{name: "receiver variable declaration", cursor: receiverDecl, wantFragments: []string{"var p *Page"}},
		{name: "receiver variable use", cursor: nth(page, "{p != nil}", 0) + 1, wantFragments: []string{"var p *Page"}},
		{name: "receiver type", cursor: nth(page, "*Page", 0) + 1, wantFragments: []string{"type Page struct{}"}},
		{name: "type parameter declaration", cursor: typeParamDecl, wantFragments: []string{"type parameter T widgets.Labelish"}},
		{name: "type parameter constraint qualifier", cursor: nth(page, "widgets.Labelish", 0), wantFragments: []string{"package widgets", `"example.com/app/widgets"`}},
		{name: "type parameter constraint", cursor: nth(page, "widgets.Labelish", 0) + len("widgets."), wantFragments: []string{"type widgets.Labelish interface{~string}"}},
		{name: "parameter declaration", cursor: nth(page, "value T", 0), wantFragments: []string{"var value T"}},
		{name: "parameter use", cursor: nth(page, "widgets.Label(value)", 0) + len("widgets.Label("), wantFragments: []string{"var value T"}},
		{name: "parameter type", cursor: nth(page, "value T", 0) + len("value "), wantFragments: []string{"type parameter T widgets.Labelish"}},
		{name: "top-level helper declaration", cursor: nth(page, "helper", 0), wantFragments: []string{"func helper(input widgets.Label) Alias"}},
		{name: "top-level helper call", cursor: nth(page, "helper", 1), wantFragments: []string{"func helper(input widgets.Label) Alias"}},
		{name: "top-level helper signature parameter type qualifier", cursor: nth(page, "widgets.Label", 0), wantFragments: []string{"package widgets", `"example.com/app/widgets"`}},
		{name: "top-level helper signature parameter type", cursor: nth(page, "widgets.Label", 0) + len("widgets."), wantFragments: []string{"type widgets.Label string"}},
		{name: "top-level helper return type", cursor: nth(page, "Alias", 1), wantFragments: []string{"type Alias string"}},
		{name: "top-level helper body local declaration", cursor: nth(page, "cleaned", 0), wantFragments: []string{"var cleaned string"}},
		{name: "top-level helper body local use", cursor: nth(page, "cleaned", 1), wantFragments: []string{"var cleaned string"}},
		{name: "top-level helper selector", cursor: nth(page, "TrimSpace", 0), wantFragments: []string{"func strings.TrimSpace(s string) string"}},
		{name: "top-level helper import qualifier", cursor: nth(page, "str.TrimSpace", 0), wantFragments: []string{"package str (\"strings\")"}},
		{name: "same-package explicit component type argument", cursor: nth(page, "Card[Alias]", 0) + len("Card["), wantFragments: []string{"type Alias string"}},
		{name: "cross-package explicit component type argument qualifier", cursor: nth(page, "Box[widgets.Label]", 0) + len("Box["), wantFragments: []string{"package widgets", `"example.com/app/widgets"`}},
		{name: "cross-package explicit component type argument", cursor: nth(page, "Box[widgets.Label]", 0) + len("Box[widgets."), wantFragments: []string{"type widgets.Label string"}},
		{name: "GoWithElements signature", cursor: nth(page, "around", 0), wantFragments: []string{"func around(input Alias) Alias"}},
		{name: "Go text before nested markup", cursor: nth(page, "local :=", 0), wantFragments: []string{"var local Alias"}},
		{name: "Go text inside nested markup", cursor: nth(page, "Label(local)", 0) + len("Label("), wantFragments: []string{"var local Alias"}},
		{name: "Go text after nested markup", cursor: nth(page, "return local", 0) + len("return "), wantFragments: []string{"var local Alias"}},
		{name: "method selection", cursor: nth(page, "d.Hours", 0) + len("d."), wantFragments: []string{"func (time.Duration).Hours() float64"}},
		{name: "whole top-level call expression", cursor: wholeCall, wantFragments: []string{"float64"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			occurrence, ok := pkg.SourceIndex.At(path, test.cursor)
			if !ok {
				t.Fatal("SourceIndex.At returned no occurrence")
			}
			var wantContents MarkupContent
			switch {
			case occurrence.Object != nil:
				wantContents = markdownGo(types.ObjectString(occurrence.Object, qualifierFor(pkg)))
			case occurrence.HasTypeValue && occurrence.TypeAndValue.Type != nil:
				wantContents = markdownGo(types.TypeString(occurrence.TypeAndValue.Type, qualifierFor(pkg)))
			default:
				t.Fatalf("indexed occurrence has no hover semantic: %+v", occurrence)
			}

			got, ok := semanticHover(pkg, path, []byte(page), test.cursor)
			if !ok {
				t.Fatal("semanticHover returned no result")
			}
			if got.Contents != wantContents {
				t.Fatalf("contents = %+v, want exact indexed semantic %+v", got.Contents, wantContents)
			}
			for _, fragment := range test.wantFragments {
				if !strings.Contains(got.Contents.Value, fragment) {
					t.Errorf("contents %q do not contain %q", got.Contents.Value, fragment)
				}
			}
		})
	}

	t.Run("stale source", func(t *testing.T) {
		if got, ok := semanticHover(pkg, path, []byte(page+"\n"), nth(page, "helper", 1)); ok {
			t.Fatalf("semanticHover(stale source) = %+v, want no result", got)
		}
	})
	t.Run("unindexed markup is null", func(t *testing.T) {
		if got, ok := semanticHover(pkg, path, []byte(page), nth(page, "<strong>", 0)+1); ok {
			t.Fatalf("semanticHover(markup) = %+v, want no result", got)
		}
	})
	t.Run("generated glue object is null", func(t *testing.T) {
		cardOffset := nth(page, "Card", 0)
		index, fset := generatedGlueIndex(t, path, page, cardOffset)
		gluePackage := *pkg
		gluePackage.SourceIndex = index
		gluePackage.Fset = fset
		if got, ok := semanticHover(&gluePackage, path, []byte(page), cardOffset); ok {
			t.Fatalf("semanticHover(generated glue) = %+v, want no result", got)
		}
	})
}

func TestHoverSourceIndexHandler(t *testing.T) {
	const source = `package page

func helper() int { _ = "😀"; result := 1; return result }
`
	pkg, path := analyzedLSPPackage(t, source)
	uri := pathToURI(path)
	resultUse := strings.Index(source, "return result") + len("return ")
	cursor := positionForByteOffset(source, resultUse, encUTF16)
	out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+hoverFrame(2, uri, cursor)+exitFrame())
	got := hoverResult(t, out, 2)
	if got == nil || !strings.Contains(got.Contents.Value, "var result int") {
		t.Fatalf("hover = %+v, want top-level local; output:\n%s", got, out)
	}
	wantRange := rangeForSpan(source, resultUse, resultUse+len("result"), encUTF16)
	if got.Range == nil || *got.Range != wantRange {
		t.Fatalf("hover range = %+v, want UTF-16 range %+v", got.Range, wantRange)
	}
}

func TestHoverComponentDeclarationUsesAuthoredSignature(t *testing.T) {
	const source = `package page

type Page struct{}

component (頁 *Page) Card(title string) {
	<p>{title}</p>
}
`
	pkg, path := analyzedLSPPackage(t, source)
	uri := pathToURI(path)
	nameStart := strings.Index(source, "Card")
	cursor := positionForByteOffset(source, nameStart, encUTF16)
	out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+hoverFrame(2, uri, cursor)+exitFrame())
	got := hoverResult(t, out, 2)
	wantContents := markdownGo("component (頁 *Page) Card(title string)")
	if got == nil || got.Contents != wantContents {
		t.Fatalf("hover = %+v, want authored component signature %+v; output:\n%s", got, wantContents, out)
	}
	wantRange := rangeForSpan(source, nameStart, nameStart+len("Card"), encUTF16)
	if got.Range == nil || *got.Range != wantRange {
		t.Fatalf("hover range = %+v, want exact NamePos UTF-16 range %+v", got.Range, wantRange)
	}
}

func TestHoverSpecializedFactsPrecedeSourceIndex(t *testing.T) {
	const source = `package page

type First string
type Other string

component Card(value First) { <p>{value}</p> }

component Page() {
	<Card value="hello"/>
}
`
	pkg, path := analyzedLSPPackage(t, source)
	uri := pathToURI(path)

	t.Run("component invocation", func(t *testing.T) {
		callOffset := strings.Index(source, "<Card") + 1
		pkg.SourceIndex = conflictingDefinitionIndex(t, path, source, "Card", callOffset, strings.Index(source, "type Other")+len("type "))
		semantic, ok := semanticHover(pkg, path, []byte(source), callOffset)
		if !ok || !strings.Contains(semantic.Contents.Value, "type fake.Card string") {
			t.Fatalf("semantic fallback = %+v, want conflicting indexed type", semantic)
		}

		cursor := positionForByteOffset(source, callOffset, encUTF16)
		out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+hoverFrame(2, uri, cursor)+exitFrame())
		got := hoverResult(t, out, 2)
		if got == nil || !strings.Contains(got.Contents.Value, "component Card(value First)") {
			t.Fatalf("hover = %+v, want specialized component signature; output:\n%s", got, out)
		}
	})

	t.Run("bound attribute", func(t *testing.T) {
		attrOffset := strings.Index(source, "value=\"hello\"")
		pkg.SourceIndex = conflictingDefinitionIndex(t, path, source, "value", attrOffset, strings.Index(source, "First string"))
		semantic, ok := semanticHover(pkg, path, []byte(source), attrOffset)
		if !ok || !strings.Contains(semantic.Contents.Value, "type fake.value string") {
			t.Fatalf("semantic fallback = %+v, want conflicting indexed type", semantic)
		}

		cursor := positionForByteOffset(source, attrOffset, encUTF16)
		out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+hoverFrame(2, uri, cursor)+exitFrame())
		got := hoverResult(t, out, 2)
		if got == nil || !strings.Contains(got.Contents.Value, "value First") {
			t.Fatalf("hover = %+v, want specialized bound parameter; output:\n%s", got, out)
		}
	})
}

func TestHoverSpecializedTerminalNullPrecedesSourceIndex(t *testing.T) {
	t.Run("failed control resolution", func(t *testing.T) {
		const source = `package page

component Page(disabled bool) {
	{ if !disabled { <p/> } }
}
`
		pkg, path := analyzedLSPPackage(t, source)
		offset := strings.Index(source, "!disabled")
		node, exprPos := exprNodeAtOffset(pkg, path, offset)
		if node == nil || !isCtrlSpan(node, exprPos) {
			t.Fatalf("cursor is not a control span: node=%T pos=%v", node, exprPos)
		}
		if _, _, _, ok := ctrlObjectAt(pkg, node, exprPos, offset); ok {
			t.Fatal("control resolver unexpectedly found an object at the operator")
		}
		pkg.SourceIndex = conflictingDefinitionIndex(t, path, source, "T", offset, 0)
		if semantic, ok := semanticHover(pkg, path, []byte(source), offset); !ok || !strings.Contains(semantic.Contents.Value, "type fake.T string") {
			t.Fatalf("semantic fallback = %+v, want conflicting indexed type", semantic)
		}

		uri := pathToURI(path)
		cursor := positionForByteOffset(source, offset, encUTF16)
		out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+hoverFrame(2, uri, cursor)+exitFrame())
		if got := hoverResult(t, out, 2); got != nil {
			t.Fatalf("hover = %+v, want terminal null for unresolved control span; output:\n%s", got, out)
		}
	})

	t.Run("failed pipeline resolution", func(t *testing.T) {
		const source = `package page

component Page(value string) {
	<p>{value |> truncate(5)}</p>
}
`
		pkg, path := analyzedLSPPackage(t, source)
		offset := strings.Index(source, "truncate(5)") + len("truncate(")
		node, exprPos := exprNodeAtOffset(pkg, path, offset)
		if node == nil || !hasPipeStages(node) {
			t.Fatalf("cursor is not a piped expression: node=%T pos=%v", node, exprPos)
		}
		if _, _, ok := pipedTarget(pkg, node, exprPos, offset); ok {
			t.Fatal("pipeline resolver unexpectedly found a target at the literal argument")
		}
		pkg.SourceIndex = conflictingDefinitionIndex(t, path, source, "T", offset, 0)
		if semantic, ok := semanticHover(pkg, path, []byte(source), offset); !ok || !strings.Contains(semantic.Contents.Value, "type fake.T string") {
			t.Fatalf("semantic fallback = %+v, want conflicting indexed type", semantic)
		}

		uri := pathToURI(path)
		cursor := positionForByteOffset(source, offset, encUTF16)
		out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, source)+hoverFrame(2, uri, cursor)+exitFrame())
		if got := hoverResult(t, out, 2); got != nil {
			t.Fatalf("hover = %+v, want terminal null for unresolved pipeline; output:\n%s", got, out)
		}
	})
}

func hoverFrame(id int, uri string, position Position) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     position,
		},
	})
}

func hoverResult(t *testing.T, output string, id int) *Hover {
	t.Helper()
	for _, message := range readFrames(t, output) {
		var gotID int
		if err := json.Unmarshal(message["id"], &gotID); err != nil || gotID != id {
			continue
		}
		if string(message["result"]) == "null" {
			return nil
		}
		var hover Hover
		if err := json.Unmarshal(message["result"], &hover); err != nil {
			t.Fatalf("decode hover result: %v", err)
		}
		return &hover
	}
	t.Fatalf("no hover response for id %d in:\n%s", id, output)
	return nil
}
