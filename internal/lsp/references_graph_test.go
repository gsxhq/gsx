package lsp

import (
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// symbolGraphPage / symbolGraphHelper are one gsx package whose symbols are
// declared in the .gsx (a type, a method, a field, a component and its
// parameter) and referenced from both the .gsx (expression, <Tag/>, attr=) and
// a hand-written .go sibling.
const symbolGraphPage = "package page\n\ntype Model struct{ N int }\n\nfunc (m Model) inc() Model { m.N++; return m }\n\ncomponent Card(title string) {\n\t<p>{ title }</p>\n}\n\ncomponent Page(m Model) {\n\t<main><Card title=\"x\"/>{ m.inc().N }</main>\n}\n"

// Wrap embeds Model: the `Model` ident there is simultaneously a field
// definition and a use of the .gsx-declared type, which is the go-definition
// case symbolDefinitionAt (SymbolGraph.UseAt) exists for.
const symbolGraphHelper = "package page\n\ntype Wrap struct{ Model }\n\nfunc use() { _ = Model{}.inc(); _ = Card }\n"

// TestSymbolGraphHandlers covers both handlers the module symbol graph feeds —
// textDocument/references and textDocument/definition from a .go cursor — over
// ONE analyzed module (a codegen.Module open costs ~0.3s; see CLAUDE.md).
func TestSymbolGraphHandlers(t *testing.T) {
	pkg, path := analyzedLSPModule(t, map[string]string{
		"page/page.gsx":  symbolGraphPage,
		"page/helper.go": symbolGraphHelper,
	}, "page/page.gsx")
	graph := sourceintel.NewSymbolGraph()
	graph.AddIndex(pkg.SourceIndex, sourceintel.NewKeyer(pkg.Types))
	analyzer := &moduleRefsAnalyzer{moduleGraph: graph, pkg: pkg}
	uri := pathToURI(path)
	helperURI := pathToURI(filepath.Join(filepath.Dir(path), "helper.go"))
	opened := initFrame() + didOpenFrame(uri, symbolGraphPage) + didOpenFrame(helperURI, symbolGraphHelper)

	t.Run("references", func(t *testing.T) {
		cases := []struct {
			name          string
			fromURI, text string
			needle        string
			offset        int
			wantFiles     map[string]int // base filename → count, includeDeclaration=true
		}{
			// The Model type: declared in the .gsx, used by the method receiver
			// and result, the Page parameter type and the .go sibling (twice:
			// the Wrap embedding and the Model{} composite literal).
			{"type from gsx decl", uri, symbolGraphPage, "Model struct", 0, map[string]int{"page.gsx": 4, "helper.go": 2}},
			// A method declared in the .gsx, referenced from the .gsx body and
			// from the .go sibling.
			{"method from helper.go", helperURI, symbolGraphHelper, "inc()", 0, map[string]int{"page.gsx": 2, "helper.go": 1}},
			// The component: cursor on the <Card/> TAG (the deferral is gone) —
			// the declaration, the tag site and the .go value reference.
			{"component from tag cursor", uri, symbolGraphPage, "<Card", 1, map[string]int{"page.gsx": 2, "helper.go": 1}},
			// A component parameter: cursor on the `title=` attribute name — the
			// declaration, the attribute binding and the body interpolation.
			{"param from attr cursor", uri, symbolGraphPage, "title=\"x\"", 0, map[string]int{"page.gsx": 3}},
			// A struct field declared in the .gsx.
			{"field", uri, symbolGraphPage, "N int", 0, map[string]int{"page.gsx": 3}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				off := strings.Index(tc.text, tc.needle)
				if off < 0 {
					t.Fatalf("needle %q not in source", tc.needle)
				}
				off += tc.offset
				pos := positionForByteOffset(tc.text, off, encUTF16)
				out := drive(t, analyzer, opened+refsFrameDecl(7, tc.fromURI, pos.Line, pos.Character, true)+exitFrame())
				got := map[string]int{}
				for _, location := range referenceLocations(t, out, 7) {
					got[filepath.Base(uriToPath(location.URI))]++
				}
				if !maps.Equal(got, tc.wantFiles) {
					t.Fatalf("references = %v, want %v\n%s", got, tc.wantFiles, out)
				}
			})
		}
	})

	t.Run("references without declaration", func(t *testing.T) {
		off := strings.Index(symbolGraphPage, "Model struct")
		pos := positionForByteOffset(symbolGraphPage, off, encUTF16)
		out := drive(t, analyzer, opened+refsFrameDecl(8, uri, pos.Line, pos.Character, false)+exitFrame())
		got := map[string]int{}
		for _, location := range referenceLocations(t, out, 8) {
			got[filepath.Base(uriToPath(location.URI))]++
		}
		want := map[string]int{"page.gsx": 3, "helper.go": 2}
		if !maps.Equal(got, want) {
			t.Fatalf("references (no declaration) = %v, want %v\n%s", got, want, out)
		}
	})

	t.Run("go definition", func(t *testing.T) {
		for _, tc := range []struct{ needle, wantFile string }{
			{"Model{}", "page.gsx"}, // type declared in .gsx
			// Embedded field: the ident is a field DEFINITION in helper.go and a
			// USE of the .gsx type at the same span. Definition must travel to
			// the type, not sit still on the field it is written on.
			{"Model }", "page.gsx"},
			{"inc()", "page.gsx"},  // method declared in .gsx
			{"Card }", "page.gsx"}, // component declared in .gsx
			{"use()", "helper.go"}, // the graph answers .go declarations too
		} {
			off := strings.Index(symbolGraphHelper, tc.needle)
			if off < 0 {
				t.Fatalf("needle %q not in helper.go", tc.needle)
			}
			pos := positionForByteOffset(symbolGraphHelper, off, encUTF16)
			out := drive(t, analyzer, opened+definitionFrame(9, helperURI, pos)+exitFrame())
			got := definitionLocation(t, out, 9)
			if got == nil || filepath.Base(uriToPath(got.URI)) != tc.wantFile {
				t.Fatalf("%s: definition = %+v, want %s\n%s", tc.needle, got, tc.wantFile, out)
			}
		}
	})
}
