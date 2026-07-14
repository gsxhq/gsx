package codegen

import (
	"go/scanner"
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// reservedPrefix is the generator's reserved identifier namespace. Generated
// code reaches every import and every internal binding through a `_gsx`-prefixed
// name (`_gsxrt`, `_gsxctx`, `_gsxio`, `_gsxsc`, `_gsxgw`, `_gsxf<i>`, the
// per-call-site `_gsxinfer<N>` probes, …). That is precisely what lets a .gsx
// file bind `gsx`, `context`, `io` or `strconv` to whatever it likes. In exchange
// the prefix is off-limits to user Go wherever gsx can see it — see
// checkReservedDecls.
const reservedPrefix = "_gsx"

// reservedDecl is one user-authored identifier that intrudes on reservedPrefix,
// with the .gsx position of the offending name (in the shared gsx FileSet).
type reservedDecl struct {
	name string
	pos  token.Pos
}

// checkReservedDecls reports every identifier a .gsx file writes in Go position
// whose name begins reservedPrefix — a name that could shadow, or collide with,
// one the generator emits into the same scope. Component params and
// method-component receiver vars are checked separately (checkReservedParams /
// checkReservedRecvVar); this covers everything else: top-level declarations,
// function-body locals, `{{ }}` GoBlock statements, and every Go fragment
// embedded in the markup tree (interpolations, `{ if/for/switch }` clauses,
// attribute and class/style expressions, pipe stages).
//
// It walks the gsx AST and runs go/scanner — a real Go lexer — over each Go
// fragment, reporting only token.IDENT tokens. This is why it does not parse:
// main's formatter can wrap a multi-line element literal as `return ( <>…</> )`,
// and `return (\n x \n)` is not a valid Go *statement* under automatic semicolon
// insertion — but it lexes fine. A prefix check must survive whatever shape the
// formatter produces, so it works at the token level, below the grammar.
// Tokenizing (not text-scanning) is also what keeps `"_gsxfoo"` inside a string
// literal and `// _gsxfoo` inside a comment from being mistaken for identifiers:
// go/scanner classifies STRING/CHAR/COMMENT distinctly, and comments are not
// emitted at all in the default mode.
//
// A name is reported at most once per file, at its first occurrence — a
// declaration and its later uses are one mistake, not several.
//
// Coverage is by design a superset of what can actually collide: a user has no
// legitimate reason to write a `_gsx` identifier anywhere in Go position, so a
// reference (`{ _gsxfoo }`) is reported as readily as a declaration
// (`{{ _gsxfoo := 1 }}`), and a method name (`func (T) _gsxFoo()`) as readily as
// a package-level one — even though a method, living in its receiver's namespace,
// could never collide with an import alias. The rule is blanket, not
// collision-derived: no `_gsx` identifier anywhere gsx lexes Go. Carving out the
// safe cases would mean parsing to tell a method name from a func name, which is
// exactly the grammar-level work the paren-wrap problem forbids. Missing a
// fragment kind is a false negative (safe — the name surfaces later as undefined
// or as a build error); a false positive is not, which is why only lexed
// identifiers, never raw text, are reported.
//
// Hand-written sibling .go files in the same package are NOT scanned here: gsx
// never reads their bodies (only their struct field names, via BYO). A `_gsx`
// name declared there is still caught, loudly, by `go build`. See
// docs/ROADMAP.md.
func checkReservedDecls(file *gsxast.File) []reservedDecl {
	var out []reservedDecl
	seen := map[string]bool{}
	emit := func(name string, pos token.Pos) {
		if seen[name] || !pos.IsValid() {
			return
		}
		seen[name] = true
		out = append(out, reservedDecl{name: name, pos: pos})
	}
	scan := func(src string, base token.Pos) {
		scanReservedIdents(src, base, emit)
	}
	stages := func(ss []gsxast.PipeStage) {
		for _, st := range ss {
			scan(st.Name, st.NamePos)
			if st.HasArgs {
				scan(st.Args, st.ArgsPos)
			}
		}
	}

	gsxast.Inspect(file, func(n gsxast.Node) bool {
		switch x := n.(type) {
		case *gsxast.GoChunk:
			scan(x.Src, x.Pos())
		case gsxast.GoText:
			scan(x.Src, x.Pos())
		case *gsxast.GoBlock:
			if x.UnsupportedMarkup == nil {
				scan(x.Code, x.CodePos)
			}
		case *gsxast.Interp:
			scan(x.Expr, x.ExprPos)
			stages(x.Stages)
		case *gsxast.ExprAttr:
			scan(x.Expr, x.ExprPos)
			stages(x.Stages)
		case *gsxast.SpreadAttr:
			scan(x.Expr, x.ExprPos)
			stages(x.Stages)
		case *gsxast.IfMarkup:
			scan(x.Cond, x.CondPos)
		case *gsxast.ForMarkup:
			scan(x.Clause, x.ClausePos)
		case *gsxast.SwitchMarkup:
			scan(x.Tag, x.TagPos)
		case *gsxast.CaseClause:
			scan(x.List, x.ListPos)
		case *gsxast.CondAttr:
			scan(x.Cond, x.CondPos)
		case *gsxast.ClassPart:
			scan(x.Expr, x.ExprPos)
			scan(x.Cond, x.CondPos)
			stages(x.Stages)
		case *gsxast.ValueArm:
			scan(x.Expr, x.ExprPos)
			stages(x.Stages)
		case *gsxast.ValueIf:
			scan(x.Cond, x.CondPos)
		case *gsxast.ValueSwitch:
			scan(x.Tag, x.TagPos)
		case *gsxast.ValueSwitchCase:
			scan(x.List, x.ListPos)
		case *gsxast.OrderedPair:
			scan(x.Value, x.Pos())
		case *gsxast.EmbeddedAttr:
			stages(x.Stages)
		case *gsxast.EmbeddedInterp:
			stages(x.Stages)
		}
		return true
	})
	return out
}

// scanReservedIdents lexes one fragment of user Go with go/scanner and calls emit
// for each identifier whose name begins reservedPrefix. base is the .gsx position
// of src[0]; the fragment's bytes align 1:1 with the source there (the AST's
// *Pos fields point at the first char before trimming — see parser/navpos_test),
// so the .gsx position of a token is base + its byte offset in the fragment.
//
// The scanner is given a no-op error handler: a fragment that is not, on its own,
// a complete Go expression or statement (a bare `for` clause, an element-bearing
// interp) still lexes token by token, and any lexical error — an unterminated
// rune from an apostrophe in embedded prose, say — is swallowed rather than
// reported here (that is the parser's job, elsewhere). At worst such an error
// ends the scan early, which can only miss a later `_gsx` name (a safe false
// negative), never invent one.
func scanReservedIdents(src string, base token.Pos, emit func(name string, pos token.Pos)) {
	if src == "" || !base.IsValid() {
		return
	}
	var s scanner.Scanner
	fset := token.NewFileSet()
	f := fset.AddFile("", fset.Base(), len(src))
	s.Init(f, []byte(src), func(token.Position, string) {}, 0)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.IDENT && strings.HasPrefix(lit, reservedPrefix) {
			emit(lit, base+token.Pos(fset.Position(pos).Offset))
		}
	}
}
