package lsp

import (
	"encoding/json"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

func semanticHover(pkg *Package, path string, source []byte, offset int) (Hover, bool) {
	if pkg == nil || pkg.SourceIndex == nil || !pkg.SourceIndex.MatchesSource(path, source) {
		return Hover{}, false
	}
	occurrence, ok := pkg.SourceIndex.At(path, offset)
	if !ok {
		return Hover{}, false
	}
	if occurrence.Object != nil {
		object := sourceintel.Origin(occurrence.Object)
		if _, authored := pkg.SourceIndex.Definition(object); !authored {
			if object.Pkg() != nil {
				if pkg.Fset == nil || !object.Pos().IsValid() {
					return Hover{}, false
				}
				position := pkg.Fset.Position(object.Pos())
				if position.Filename == "" || !strings.HasSuffix(position.Filename, ".go") || strings.HasSuffix(position.Filename, ".x.go") {
					return Hover{}, false
				}
			}
		}
		return Hover{Contents: markdownGo(types.ObjectString(occurrence.Object, qualifierFor(pkg)))}, true
	}
	if occurrence.HasTypeValue && occurrence.TypeAndValue.Type != nil {
		return Hover{Contents: markdownGo(types.TypeString(occurrence.TypeAndValue.Type, qualifierFor(pkg)))}, true
	}
	return Hover{}, false
}

// handleHover answers textDocument/hover for a .gsx file: it shows the Go
// type/signature of the symbol or expression under the cursor. .go files are
// gopls's to hover (null).
func (s *Server) handleHover(f frame) error {
	var p textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	if !s.diskViewValid {
		return s.reply(f.ID, nil)
	}
	path := uriToPath(p.TextDocument.URI)
	if strings.HasSuffix(path, ".go") {
		return s.reply(f.ID, nil) // gopls owns .go hover
	}
	text, ok := s.docs.text(p.TextDocument.URI)
	if !ok {
		return s.reply(f.ID, nil)
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil {
		return s.reply(f.ID, nil)
	}
	off := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc)

	// A successfully planned component tag hovers codegen's exact callable
	// target. GSX declarations keep their source-language presentation; plain Go
	// callable values use the ordinary go/types object string.
	if cursor, ok := componentTargetAtOffset(pkg, path, off); ok {
		rng := rangeForSpan(text, cursor.start, cursor.start+cursor.length, s.enc)
		if comp := componentDeclForTarget(pkg, cursor.fact); comp != nil {
			return s.reply(f.ID, Hover{Contents: markdownGo(renderComponentSig(comp)), Range: &rng})
		}
		// Imported GSX declarations are not retained in pkg.Files. Resolve only
		// their presentation source here; the callable identity and range above
		// still come from the exact call fact.
		if comp, _, _, found := componentAtTag(pkg, path, off); found {
			return s.reply(f.ID, Hover{Contents: markdownGo(renderComponentSig(comp)), Range: &rng})
		}
		if obj := componentTargetObject(cursor.fact); obj != nil {
			return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qualifierFor(pkg))), Range: &rng})
		}
	}

	// A component tag (`<Card/>` or `<pkg.Comp/>`) → the component's signature.
	// This path needs only the AST (pkg.GSXFset + pkg.Files), not type info,
	// so it can answer even when type-checking failed (mid-edit state).
	if c, nameStart, nameLen, ok := componentAtTag(pkg, path, off); ok {
		rng := rangeForSpan(text, nameStart, nameStart+nameLen, s.enc)
		return s.reply(f.ID, Hover{Contents: markdownGo(renderComponentSig(c)), Range: &rng})
	}

	// H1: a component-invocation attribute name → codegen's exact bound
	// parameter. This consumes the same semantic fact as emission; it does not
	// reparse the declaration or case-fold authored names.
	if cursor, ok := componentAttrAtOffset(pkg, path, off); ok {
		param := cursor.param.Var
		if param == nil {
			param = cursor.param.Origin
		}
		if param != nil {
			decl := cursor.param.Name + " " + types.TypeString(param.Type(), qualifierFor(pkg))
			rng := rangeForSpan(text, cursor.start, cursor.start+len(cursor.name), s.enc)
			return s.reply(f.ID, Hover{Contents: markdownGo(decl), Range: &rng})
		}
	}

	if pkg.Info != nil {
		// H2: an identifier inside a component-signature parameter TYPE (e.g.
		// `store.Comment` in `component C(c []store.Comment)`) → the resolved object's
		// signature, like hovering the same identifier in Go.
		if obj, idStart, idLen, ok := signatureTypeIdentAt(pkg, path, off); ok {
			rng := rangeForSpan(text, idStart, idStart+idLen, s.enc)
			return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qualifierFor(pkg))), Range: &rng})
		}

		node, exprPos := exprNodeAtOffset(pkg, path, off)
		if node != nil {
			// H3: an identifier inside a CtrlMap-bridged span — a for/if/{{ }} clause,
			// switch tag or case list, in-tag conditional-attribute cond, class guard
			// cond, or value-form control expression — hovers like the same identifier
			// in Go. Checked before the pipeline path: a ClassPart's `: cond` guard is
			// a ctrl span even when the part's expr carries a pipeline.
			if isCtrlSpan(node, exprPos) {
				if obj, idStart, idLen, ok := ctrlObjectAt(pkg, node, exprPos, off); ok {
					rng := rangeForSpan(text, idStart, idStart+idLen, s.enc)
					return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qualifierFor(pkg))), Range: &rng})
				}
			} else if hasPipeStages(node) {
				if obj, span, ok := pipedTarget(pkg, node, exprPos, off); ok {
					rng := rangeForSpan(text, span[0], span[1], s.enc)
					return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qualifierFor(pkg))), Range: &rng})
				}
			} else if skel := pkg.ExprMap[node]; skel != nil {
				exprStart := pkg.GSXFset.Position(exprPos).Offset
				skelPos := skel.Pos() + token.Pos(off-exprStart)
				qf := qualifierFor(pkg)

				// On an identifier → show the resolved object's signature.
				if id := innermostIdent(skel, skelPos); id != nil {
					obj := pkg.Info.Uses[id]
					if obj == nil {
						obj = pkg.Info.Defs[id]
					}
					if obj != nil {
						identStart := exprStart + int(id.Pos()-skel.Pos())
						rng := rangeForSpan(text, identStart, identStart+len(id.Name), s.enc)
						return s.reply(f.ID, Hover{Contents: markdownGo(types.ObjectString(obj, qf)), Range: &rng})
					}
				}
				// Otherwise → the whole expression's type.
				if tv, ok := pkg.Info.Types[skel]; ok && tv.Type != nil {
					rng := rangeForSpan(text, exprStart, exprStart+len(exprText(node)), s.enc)
					return s.reply(f.ID, Hover{Contents: markdownGo(types.TypeString(tv.Type, qf)), Range: &rng})
				}
			}
		}
	}
	if hover, ok := semanticHover(pkg, path, []byte(text), off); ok {
		return s.reply(f.ID, hover)
	}
	return s.reply(f.ID, nil)
}

// qualifierFor renders the analyzed package's own types unqualified and imported
// types by package name (gopls-style: `User`, `store.User`).
func qualifierFor(pkg *Package) types.Qualifier {
	return func(p *types.Package) string {
		if pkg.Types != nil && (p == pkg.Types || p.Path() == pkg.Types.Path()) {
			return ""
		}
		return p.Name()
	}
}

// markdownGo wraps a Go signature/type string in a fenced go code block.
func markdownGo(s string) MarkupContent {
	return MarkupContent{Kind: "markdown", Value: "```go\n" + s + "\n```"}
}

// exprText returns the Go-expression source of an ExprMap-bridged node — the
// text whose source bytes start at the node's expr span (see nodeNavSpans).
func exprText(n gsxast.Node) string {
	switch e := n.(type) {
	case *gsxast.Interp:
		return e.Expr
	case *gsxast.ExprAttr:
		return e.Expr
	case *gsxast.SpreadAttr:
		return e.Expr
	case *gsxast.ClassPart:
		return e.Expr
	case *gsxast.ValueArm:
		return e.Expr
	case *gsxast.OrderedPair:
		return e.Value
	}
	return ""
}

// rangeForSpan converts a [startOff, endOff) byte span in text to an LSP Range
// (characters counted in the negotiated encoding).
func rangeForSpan(text string, startOff, endOff int, enc encoding) Range {
	return Range{
		Start: positionForByteOffset(text, startOff, enc),
		End:   positionForByteOffset(text, endOff, enc),
	}
}

// positionForByteOffset is the inverse of byteOffsetForPosition: a byte offset in
// text → a 0-based LSP position (character counted in enc).
func positionForByteOffset(text string, off int, enc encoding) Position {
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	line := strings.Count(text[:off], "\n")
	lineStart := strings.LastIndexByte(text[:off], '\n') + 1 // 0 when no newline precedes
	char := charForByteCol(text[lineStart:off], (off-lineStart)+1, enc)
	return Position{Line: line, Character: char}
}

// componentAtTag reports whether off sits on the name of a component tag —
// simple <Card/>, lowercase <card/> resolving to a package-level declaration,
// or dotted <pkg.Comp/> — per el.IsComponent, the codegen-stamped answer, and
// returns the resolved component declaration, the byte offset of the tag
// name, and the tag length. Cross-package tags resolve via the imported
// package's .gsx. Method-receiver tags (<p.Content/>) resolve false.
func componentAtTag(pkg *Package, path string, off int) (comp *gsxast.Component, nameStart, nameLen int, ok bool) {
	if pkg == nil || pkg.GSXFset == nil || pkg.Files == nil {
		return nil, 0, 0, false
	}
	f := pkg.Files[path]
	if f == nil {
		return nil, 0, 0, false
	}
	tag := ""
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if tag != "" {
			return false
		}
		el, isEl := n.(*gsxast.Element)
		if !isEl || !el.IsComponent {
			return true
		}
		start := pkg.GSXFset.Position(el.Pos()).Offset + 1 // skip '<'
		if off >= start && off < start+len(el.Tag) {
			tag, nameStart, nameLen = el.Tag, start, len(el.Tag)
		}
		return true
	})
	if tag == "" {
		return nil, 0, 0, false
	}
	c, _, found := resolveTagComponent(pkg, tag)
	if !found {
		return nil, 0, 0, false
	}
	return c, nameStart, nameLen, true
}

// renderComponentSig renders a component declaration's signature, e.g.
// "component Card(title string)" or "component (p UsersPage) Row(u User)".
func renderComponentSig(c *gsxast.Component) string {
	var b strings.Builder
	b.WriteString("component ")
	if c.Recv != "" {
		b.WriteString(c.Recv)
		b.WriteByte(' ')
	}
	b.WriteString(c.Name)
	if c.TypeParams != "" {
		b.WriteByte('[')
		b.WriteString(c.TypeParams)
		b.WriteByte(']')
	}
	b.WriteByte('(')
	b.WriteString(c.Params)
	b.WriteByte(')')
	return b.String()
}
