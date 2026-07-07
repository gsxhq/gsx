package lsp

import (
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

// receiverTypeName extracts the type name from a component receiver source like
// "(f *Form)" or "(p UsersPage)" → "Form" / "UsersPage". Falls back to the raw
// trimmed text if it cannot parse the shape.
func receiverTypeName(recv string) string {
	s := strings.TrimSpace(recv)
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return strings.TrimSpace(recv)
	}
	typ := fields[len(fields)-1] // last field is the type (recv may be name+type or type-only)
	return strings.TrimPrefix(typ, "*")
}
