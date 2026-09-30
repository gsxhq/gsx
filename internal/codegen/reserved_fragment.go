package codegen

import (
	goast "go/ast"
	"go/parser"
	"go/token"
)

const fragBodyPrefix = "package p\nfunc _f() {\n"

type fragKind uint8

const fragStmts fragKind = iota

type boundIdent struct {
	name string
	off  int
}

func fragmentBindings(src string, kind fragKind) []boundIdent {
	if kind != fragStmts {
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", fragBodyPrefix+src+"\n}", 0)
	if err != nil || len(file.Decls) == 0 {
		return nil
	}
	function, ok := file.Decls[0].(*goast.FuncDecl)
	if !ok || function.Body == nil {
		return nil
	}
	var out []boundIdent
	collect := func(id *goast.Ident) {
		if id == nil || id.Name != "ctx" {
			return
		}
		out = append(out, boundIdent{name: id.Name, off: fset.Position(id.Pos()).Offset - len(fragBodyPrefix)})
	}
	for _, statement := range function.Body.List {
		collectStmtBindings(statement, collect)
	}
	return out
}

func collectStmtBindings(statement goast.Stmt, collect func(*goast.Ident)) {
	switch statement := statement.(type) {
	case *goast.AssignStmt:
		if statement.Tok == token.DEFINE && !derivesCtx(statement) {
			for _, left := range statement.Lhs {
				if id, ok := left.(*goast.Ident); ok {
					collect(id)
				}
			}
		}
	case *goast.DeclStmt:
		declaration, ok := statement.Decl.(*goast.GenDecl)
		if !ok {
			return
		}
		for _, spec := range declaration.Specs {
			switch spec := spec.(type) {
			case *goast.ValueSpec:
				for _, id := range spec.Names {
					collect(id)
				}
			case *goast.TypeSpec:
				collect(spec.Name)
			}
		}
	case *goast.LabeledStmt:
		collectStmtBindings(statement.Stmt, collect)
	}
}

// derivesCtx reports whether a `:=` names some identifier other than ctx and
// the blank on its left. In the render closure's top scope ctx is already
// declared (the closure parameter), so such a statement is, to Go, an
// assignment to the ambient ctx plus declarations of the other names — the
// `ctx, cancel := context.WithCancel(ctx)` derivation idiom — and must not be
// reported. If none of the other names is actually new, Go's own "no new
// variables" error is the backstop. A `:=` whose only non-blank name is ctx
// can never compile at top scope, so it keeps the worded diagnostic.
func derivesCtx(statement *goast.AssignStmt) bool {
	for _, left := range statement.Lhs {
		if id, ok := left.(*goast.Ident); ok && id.Name != "ctx" && id.Name != "_" {
			return true
		}
	}
	return false
}
