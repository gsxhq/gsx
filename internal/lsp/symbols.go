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
