package lsp

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
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
// top-level Go declaration inside a GoChunk. Positions are resolved against the
// file's FileSet (byte columns, 1-based line/column).
type Symbol struct {
	Name      string
	Kind      int            // LSP SymbolKind
	Container string         // package name, or receiver type name for methods
	NamePos   token.Position // start of the name (selectionRange / workspace Location)
	DeclStart token.Position // start of the whole declaration (documentSymbol range)
	DeclEnd   token.Position // end of the whole declaration
}

// FileSymbols extracts the symbols declared in one parsed .gsx file. fset
// resolves gsx node positions (the package's GSXFset or the module-shared fset).
// A nil file yields no symbols.
func FileSymbols(path string, file *gsxast.File, fset *token.FileSet) []Symbol {
	if file == nil {
		return nil
	}
	var out []Symbol
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *gsxast.Component:
			out = append(out, componentSymbol(file, fset, decl))
		case *gsxast.GoChunk:
			out = append(out, goChunkSymbols(file, fset, decl)...)
		}
	}
	return out
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

// goChunkSymbols parses a GoChunk's verbatim Go source and returns a Symbol for
// each top-level func/type/const/var declaration, with positions mapped back
// into the .gsx file. A chunk whose Src does not parse yields no symbols.
func goChunkSymbols(file *gsxast.File, fset *token.FileSet, gc *gsxast.GoChunk) []Symbol {
	gfset := token.NewFileSet()
	gf, err := parser.ParseFile(gfset, "chunk.go", goWrapPrefix+gc.Src, 0)
	if err != nil {
		return nil // incomplete fragment; skip (tolerant)
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
						NamePos: mapPos(ts.Name.Pos()), DeclStart: mapPos(ts.Pos()), DeclEnd: mapPos(ts.End()),
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
							NamePos: mapPos(n.Pos()), DeclStart: mapPos(vs.Pos()), DeclEnd: mapPos(vs.End()),
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
