package lsp

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// LSP SymbolKind numeric constants (subset gsx emits).
const (
	symKindClass     = 5
	symKindMethod    = 6
	symKindInterface = 11
	symKindFunction  = 12
	symKindVariable  = 13
	symKindConstant  = 14
	symKindStruct    = 23
)

// Symbol is one navigable declaration in a .gsx file: a component or a
// top-level authored Go declaration. Positions are resolved against the file's
// FileSet (byte columns, 1-based line/column).
type Symbol struct {
	Name      string
	Kind      int            // LSP SymbolKind
	Container string         // package name, or receiver type name for methods
	NamePos   token.Position // start of the name (selectionRange / workspace Location)
	DeclStart token.Position // start of the whole declaration (documentSymbol range)
	DeclEnd   token.Position // end of the whole declaration
}

// FileSymbols extracts the symbols declared in one parsed .gsx file. Components
// are owned by the GSX AST. Top-level Go declarations come from the retained
// semantic index when it describes these exact source bytes; otherwise a
// focused partial-parser fallback recovers declarations from GoChunks.
func FileSymbols(path string, source []byte, file *gsxast.File, fset *token.FileSet, index *sourceintel.Index) []Symbol {
	semantic := file != nil && fset != nil && index != nil && index.MatchesSource(path, source)
	if !semantic {
		fset = token.NewFileSet()
		file, _ = gsxparser.ParseFileWithClassifier(fset, path, source, 0, nil)
		if file == nil {
			return nil
		}
	}
	var out []Symbol
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *gsxast.Component:
			out = append(out, componentSymbol(file, fset, decl))
		}
	}
	if semantic {
		for _, declaration := range index.Declarations(path) {
			if symbol, ok := semanticDeclarationSymbol(file, fset, declaration); ok {
				out = append(out, symbol)
			}
		}
	} else {
		for _, d := range file.Decls {
			if chunk, ok := d.(*gsxast.GoChunk); ok {
				out = append(out, partialGoChunkSymbols(file, fset, chunk)...)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.DeclStart.Offset != right.DeclStart.Offset {
			return left.DeclStart.Offset < right.DeclStart.Offset
		}
		leftLength := left.DeclEnd.Offset - left.DeclStart.Offset
		rightLength := right.DeclEnd.Offset - right.DeclStart.Offset
		if leftLength != rightLength {
			return leftLength < rightLength
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.Name < right.Name
	})
	return out
}

func semanticDeclarationSymbol(file *gsxast.File, fset *token.FileSet, declaration sourceintel.Declaration) (Symbol, bool) {
	kind, ok := semanticSymbolKind(declaration.Kind)
	if !ok || declaration.NameSpan.Path != declaration.DeclSpan.Path {
		return Symbol{}, false
	}
	namePos, ok := sourceOffsetPosition(file, fset, declaration.NameSpan.Start)
	if !ok {
		return Symbol{}, false
	}
	declStart, ok := sourceOffsetPosition(file, fset, declaration.DeclSpan.Start)
	if !ok {
		return Symbol{}, false
	}
	declEnd, ok := sourceOffsetPosition(file, fset, declaration.DeclSpan.End)
	if !ok {
		return Symbol{}, false
	}
	container := declaration.Container
	if container == "" {
		container = file.Package
	}
	return Symbol{
		Name:      declaration.Name,
		Kind:      kind,
		Container: container,
		NamePos:   namePos,
		DeclStart: declStart,
		DeclEnd:   declEnd,
	}, true
}

func semanticSymbolKind(kind sourceintel.DeclarationKind) (int, bool) {
	switch kind {
	case sourceintel.DeclarationFunction:
		return symKindFunction, true
	case sourceintel.DeclarationMethod:
		return symKindMethod, true
	case sourceintel.DeclarationType:
		return symKindClass, true
	case sourceintel.DeclarationStruct:
		return symKindStruct, true
	case sourceintel.DeclarationInterface:
		return symKindInterface, true
	case sourceintel.DeclarationConstant:
		return symKindConstant, true
	case sourceintel.DeclarationVariable:
		return symKindVariable, true
	default:
		return 0, false
	}
}

func sourceOffsetPosition(file *gsxast.File, fset *token.FileSet, offset int) (token.Position, bool) {
	if file == nil || fset == nil {
		return token.Position{}, false
	}
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil || offset < 0 || offset > tokenFile.Size() {
		return token.Position{}, false
	}
	return fset.Position(tokenFile.Pos(offset)), true
}

func componentSymbol(file *gsxast.File, fset *token.FileSet, c *gsxast.Component) Symbol {
	kind := symKindFunction
	container := file.Package
	if c.Recv != "" {
		kind = symKindMethod
		container = receiverTypeName(c.Recv)
	}
	return Symbol{
		Name:      c.Name,
		Kind:      kind,
		Container: container,
		NamePos:   fset.Position(c.NamePos),
		DeclStart: fset.Position(c.Pos()),
		DeclEnd:   fset.Position(c.End()),
	}
}

// goWrapPrefix wraps a GoChunk's verbatim source so go/parser accepts it as a
// file. Its byte length is subtracted when mapping parsed offsets back into the
// .gsx (the chunk's Src is the source verbatim, so offsets align 1:1).
const goWrapPrefix = "package p\n"

// partialGoChunkSymbols parses a GoChunk's verbatim source in recovery mode and
// emits only declarations represented by the returned partial Go AST.
func partialGoChunkSymbols(file *gsxast.File, fset *token.FileSet, gc *gsxast.GoChunk) []Symbol {
	gfset := token.NewFileSet()
	gf, _ := parser.ParseFile(gfset, "chunk.go", goWrapPrefix+gc.Src, parser.AllErrors|parser.SkipObjectResolution)
	if gf == nil {
		return nil
	}
	tf := fset.File(gc.Pos())
	if tf == nil {
		return nil
	}
	chunkOff := tf.Offset(gc.Pos())

	// mapPos converts a token.Pos in the wrapped parse to a resolved position in
	// the .gsx file via exact byte arithmetic.
	mapPos := func(p token.Pos) token.Position {
		w := gfset.Position(p).Offset
		gsxOff := chunkOff + (w - len(goWrapPrefix))
		return fset.Position(tf.Pos(gsxOff))
	}

	var out []Symbol
	for _, d := range gf.Decls {
		switch decl := d.(type) {
		case *goast.FuncDecl:
			kind := symKindFunction
			container := file.Package
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				kind = symKindMethod
				container = exprTypeName(decl.Recv.List[0].Type)
			}
			out = append(out, Symbol{
				Name: decl.Name.Name, Kind: kind, Container: container,
				NamePos: mapPos(decl.Name.Pos()), DeclStart: mapPos(decl.Pos()), DeclEnd: mapPos(decl.End()),
			})
		case *goast.GenDecl:
			switch decl.Tok {
			case token.TYPE:
				for _, sp := range decl.Specs {
					ts := sp.(*goast.TypeSpec)
					out = append(out, Symbol{
						Name: ts.Name.Name, Kind: typeSpecKind(ts), Container: file.Package,
						NamePos: mapPos(ts.Name.Pos()), DeclStart: mapPos(decl.Pos()), DeclEnd: mapPos(decl.End()),
					})
				}
			case token.CONST, token.VAR:
				kind := symKindVariable
				if decl.Tok == token.CONST {
					kind = symKindConstant
				}
				for _, sp := range decl.Specs {
					vs := sp.(*goast.ValueSpec)
					for _, n := range vs.Names {
						if n.Name == "_" {
							continue
						}
						out = append(out, Symbol{
							Name: n.Name, Kind: kind, Container: file.Package,
							NamePos: mapPos(n.Pos()), DeclStart: mapPos(decl.Pos()), DeclEnd: mapPos(decl.End()),
						})
					}
				}
			}
		}
	}
	return out
}

func typeSpecKind(ts *goast.TypeSpec) int {
	switch ts.Type.(type) {
	case *goast.StructType:
		return symKindStruct
	case *goast.InterfaceType:
		return symKindInterface
	default:
		return symKindClass
	}
}

// receiverTypeName extracts the base type name from a component receiver
// source like "(f *Form)" or "(p UsersPage)" → "Form" / "UsersPage". It
// parses the receiver as real Go source (mirroring codegen.parseRecv) rather
// than string-splitting, so it handles pointer receivers, generic type args
// (e.g. "(f *Form[T, U])" → "Form"), and irregular spacing correctly. Falls
// back to the raw trimmed text if it cannot parse the shape.
func receiverTypeName(recv string) string {
	trimmed := strings.TrimSpace(recv)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", "package _\nfunc "+trimmed+" _m() {}", 0)
	if err != nil {
		return trimmed
	}
	fn, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return trimmed
	}
	if name := exprTypeName(fn.Recv.List[0].Type); name != "" {
		return name
	}
	return trimmed
}

// exprTypeName returns the base type name of a (possibly pointer or
// generic-instantiated) type expression, e.g. "*Form" → "Form",
// "Form[T]" → "Form", "*Form[T, U]" → "Form". Returns "" for shapes it
// doesn't recognize. Shared by receiver-type extraction for gsx component
// receivers and (future) Go method receivers.
func exprTypeName(e goast.Expr) string {
	switch t := e.(type) {
	case *goast.StarExpr:
		return exprTypeName(t.X)
	case *goast.IndexExpr:
		return exprTypeName(t.X)
	case *goast.IndexListExpr:
		return exprTypeName(t.X)
	case *goast.Ident:
		return t.Name
	default:
		return ""
	}
}
