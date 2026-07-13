package codegen

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/ast"
)

// freeuse.go — the syntactic free-use walker behind the manual-mode `attrs`
// trigger. usesAttrs (emit.go) answers "does the identifier `attrs` appear as a
// token anywhere in a body Go fragment?"; that token scan over-answers
// (`opt{attrs: 1}`, `x.attrs`, a loop var named `attrs`) and under-answers
// nothing. freeUseAttrs is the PRECISE predicate: it reports whether some Go
// fragment uses `attrs` FREE — not bound by a fragment-local scope (a func
// literal, a nested block, a `:=`/`range`/`for` binding) nor by a
// markup-inherited binding (a `for`/`if`/`switch` clause's names scope over its
// markup subtree; a GoBlock's top-level names extend the environment for
// subsequent sibling fragments). It mirrors usesAttrs + attrsRefAttrs's markup
// recursion EXACTLY — the same Go-fragment positions — but resolves each
// fragment with a hand-rolled scope stack over go/ast instead of the token scan.
//
// Exactness matters in BOTH directions: an over-answer synthesizes an unused
// `Attrs` field (`declared and not used: attrs`), an under-answer drops a needed
// one (`undefined: attrs`) — both are the false-rejection bug class this feature
// eliminates. The walk is therefore exact on PARSEABLE fragments. An unparseable
// fragment falls back to the token answer for that fragment (a component with an
// unparseable fragment does not compile anyway — fragments emit verbatim — so the
// fallback cannot reject a correct program; it only preserves today's behavior
// mid-edit).

// fragKind selects the parse wrapper for a Go fragment.
type fragKind uint8

const (
	fragStmts  fragKind = iota // GoBlock body: statement list
	fragClause                 // for/if/switch header (keyword-prefixed): a control statement
	fragExpr                   // interp/attr/spread/arg expression
)

// fragBodyPrefix wraps a statement list / clause into a parseable file. Its
// length is the offset correction applied to positions inside the fragment when
// mapping token offsets back to the fragment source (fragmentBindings).
const fragBodyPrefix = "package p\nfunc _f() {\n"

// boundIdent is a top-level binding identifier of a parsed fragment, with the
// byte offset of its name within the fragment source. Task 3 positions
// reserved-identifier diagnostics from these offsets.
type boundIdent struct {
	name string
	off  int
}

// isReservedBodyIdent reports whether name is one of the three reserved
// component-body identifiers. fragmentBindings returns only these bindings:
// they are the sole names that can shadow the reserved meaning (env threading)
// and the exact set Task 3 diagnoses.
func isReservedBodyIdent(name string) bool {
	return name == "ctx" || name == "children" || name == "attrs"
}

// parseFragment parses a Go fragment of the given kind and returns the node to
// walk (a *goast.BlockStmt for fragStmts, the single control *goast.Stmt for
// fragClause, or a goast.Expr for fragExpr), the FileSet it was parsed with (for
// offset mapping), and ok=false when the fragment does not parse.
//
// For fragClause, src is the FULL clause INCLUDING the keyword (e.g.
// "for _, attrs := range bags()"); a placeholder body is appended so the header
// forms a complete statement. For fragExpr, plain ParseExpr is tried first; a
// comma list (case labels, pipeline args) is then retried wrapped as call
// arguments so multi-expression fragments still parse.
func parseFragment(src string, kind fragKind) (goast.Node, *token.FileSet, bool) {
	fset := token.NewFileSet()
	switch kind {
	case fragExpr:
		if e, err := parser.ParseExpr(src); err == nil {
			return e, fset, true
		}
		if e, err := parser.ParseExpr("_gsxwrap(" + src + ")"); err == nil {
			return e, fset, true
		}
		return nil, fset, false
	case fragStmts:
		f, err := parser.ParseFile(fset, "", fragBodyPrefix+src+"\n}", 0)
		if err != nil {
			return nil, fset, false
		}
		body := funcBody(f)
		if body == nil {
			return nil, fset, false
		}
		return body, fset, true
	case fragClause:
		f, err := parser.ParseFile(fset, "", fragBodyPrefix+src+" {}\n}", 0)
		if err != nil {
			return nil, fset, false
		}
		body := funcBody(f)
		if body == nil || len(body.List) == 0 {
			return nil, fset, false
		}
		return body.List[0], fset, true
	}
	return nil, fset, false
}

// funcBody returns the body block of the synthesized `_f` function, or nil.
func funcBody(f *goast.File) *goast.BlockStmt {
	if len(f.Decls) == 0 {
		return nil
	}
	fn, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok {
		return nil
	}
	return fn.Body
}

// ---- markup recursion (mirrors usesAttrs + attrsRefAttrs) ----

// freeUseAttrs reports whether any Go fragment in a component body uses the
// identifier `attrs` free. It is the stage-2 predicate behind usesAttrs's token
// pre-filter; keep it adjacent to usesAttrs in review — the node cases must stay
// in lockstep.
func freeUseAttrs(body []ast.Markup) bool {
	return freeInBody(body, map[string]bool{})
}

// freeInBody walks a sibling markup list threading env — the set of names bound
// by enclosing clauses and by preceding GoBlock top-level declarations. env is
// COPIED when descending into a scoped subtree (a for/if/switch body, whose
// clause bindings shadow only there; a component element's children or a named
// markup slot's value, which lower into a nested gsx.Func slot closure) and
// EXTENDED in place across GoBlock siblings and through PLAIN-element children
// (a body-scope declaration is visible to later siblings, plain elements emit
// their children inline), matching what the emitter produces scope-for-scope.
func freeInBody(body []ast.Markup, env map[string]bool) bool {
	for _, n := range body {
		switch t := n.(type) {
		case *ast.Interp:
			if exprFragFree(t.Expr, env) || stagesFree(t.Stages, env) {
				return true
			}
		case *ast.EmbeddedInterp:
			if freeInBody(t.Segments, env) || stagesFree(t.Stages, env) {
				return true
			}
		case *ast.Element:
			if attrsFree(t.Attrs, env) {
				return true
			}
			// A PLAIN element's children emit inline in the enclosing closure
			// (emit.go genNode's Element case): a GoBlock binding there extends env
			// IN PLACE and stays visible to later siblings even after the element
			// closes, exactly like the emitted statements. A COMPONENT element's
			// children lower into a nested gsx.Func slot closure (emitSlotClosure):
			// bindings there are closure-scoped and must NOT leak outward — a later
			// SIBLING's free use of `attrs` is the implicit bag and must still
			// trigger. Descend with a COPY (extendEnv, no binds), like CF subtrees.
			childEnv := env
			if t.IsComponent {
				childEnv = extendEnv(env, nil)
			}
			if freeInBody(t.Children, childEnv) {
				return true
			}
		case *ast.Fragment:
			if freeInBody(t.Children, env) {
				return true
			}
		case *ast.ForMarkup:
			if clauseFree("for", t.Clause, env) {
				return true
			}
			if freeInBody(t.Body, extendEnv(env, clauseBindings("for", t.Clause))) {
				return true
			}
		case *ast.IfMarkup:
			if clauseFree("if", t.Cond, env) {
				return true
			}
			sub := extendEnv(env, clauseBindings("if", t.Cond))
			if freeInBody(t.Then, sub) || freeInBody(t.Else, sub) {
				return true
			}
		case *ast.SwitchMarkup:
			if t.Tag != "" && clauseFree("switch", t.Tag, env) {
				return true
			}
			sub := extendEnv(env, clauseBindings("switch", t.Tag))
			for _, cc := range t.Cases {
				if cc.List != "" && exprFragFree(cc.List, sub) {
					return true
				}
				if freeInBody(cc.Body, sub) {
					return true
				}
			}
		case *ast.GoBlock:
			if stmtsFree(t.Code, env) {
				return true
			}
			// A GoBlock's top-level declarations share the render closure's
			// scope, so they extend env for subsequent siblings (a body-scope
			// reserved-name binding is thereafter treated as bound — the single
			// Go collision error is the backstop, no double-report).
			for _, b := range fragmentBindings(t.Code, fragStmts) {
				env[b.name] = true
			}
		}
	}
	return false
}

// attrsFree mirrors attrsRefAttrs's position inventory: every verbatim-emitted
// Go fragment in an element's (or component tag's) attr list, resolved free
// against env instead of by token scan.
func attrsFree(attrs []ast.Attr, env map[string]bool) bool {
	for _, a := range attrs {
		switch at := a.(type) {
		case *ast.SpreadAttr:
			if exprFragFree(at.Expr, env) || stagesFree(at.Stages, env) {
				return true
			}
		case *ast.ClassAttr:
			for i := range at.Parts {
				p := &at.Parts[i]
				if exprFragFree(p.Expr, env) || (p.Cond != "" && exprFragFree(p.Cond, env)) || stagesFree(p.Stages, env) {
					return true
				}
				if p.CSSSegments != nil && freeInBody(p.CSSSegments, env) {
					return true
				}
				if p.CF != nil && valueCFFree(p.CF, env) {
					return true
				}
			}
		case *ast.ExprAttr:
			if exprFragFree(at.Expr, env) || stagesFree(at.Stages, env) {
				return true
			}
		case *ast.CondAttr:
			if clauseFree("if", at.Cond, env) {
				return true
			}
			sub := extendEnv(env, clauseBindings("if", at.Cond))
			if attrsFree(at.Then, sub) || attrsFree(at.Else, sub) {
				return true
			}
		case *ast.EmbeddedAttr:
			if freeInBody(at.Segments, env) || stagesFree(at.Stages, env) {
				return true
			}
		case *ast.MarkupAttr:
			// A named markup slot's value lowers into the SAME gsx.Func slot
			// closure shape as component children (emitSlotClosure is shared by
			// both) — bindings inside must not leak into the enclosing env.
			if freeInBody(at.Value, extendEnv(env, nil)) {
				return true
			}
		case *ast.OrderedAttrsAttr:
			for i := range at.Pairs {
				if exprFragFree(at.Pairs[i].Value, env) {
					return true
				}
			}
		}
	}
	return false
}

// valueCFFree resolves a value-form if/switch (inside a class/style list). Its
// arms and clauses are bare expressions that bind nothing.
func valueCFFree(cf *ast.ValueCF, env map[string]bool) bool {
	if cf == nil {
		return false
	}
	return valueIfFree(cf.If, env) || valueSwitchFree(cf.Switch, env)
}

func valueArmFree(a *ast.ValueArm, env map[string]bool) bool {
	return a != nil && (exprFragFree(a.Expr, env) || stagesFree(a.Stages, env))
}

func valueIfFree(vi *ast.ValueIf, env map[string]bool) bool {
	if vi == nil {
		return false
	}
	return exprFragFree(vi.Cond, env) || valueArmFree(vi.Then, env) ||
		valueIfFree(vi.ElseIf, env) || valueArmFree(vi.Else, env)
}

func valueSwitchFree(vs *ast.ValueSwitch, env map[string]bool) bool {
	if vs == nil {
		return false
	}
	if vs.Tag != "" && exprFragFree(vs.Tag, env) {
		return true
	}
	for _, c := range vs.Cases {
		if (c.List != "" && exprFragFree(c.List, env)) || valueArmFree(c.Value, env) {
			return true
		}
	}
	return false
}

// stagesFree resolves pipeline stage argument fragments.
func stagesFree(stages []ast.PipeStage, env map[string]bool) bool {
	for _, st := range stages {
		if st.Args != "" && exprFragFree(st.Args, env) {
			return true
		}
	}
	return false
}

// ---- fragment-level free resolution (parse → walk, token fallback) ----

func exprFragFree(src string, env map[string]bool) bool {
	if strings.TrimSpace(src) == "" {
		return false
	}
	node, _, ok := parseFragment(src, fragExpr)
	if !ok {
		return valueIdents(src)["attrs"]
	}
	return freeIn(node, "attrs", env)
}

func stmtsFree(code string, env map[string]bool) bool {
	node, _, ok := parseFragment(code, fragStmts)
	if !ok {
		return valueIdents(code)["attrs"]
	}
	return freeIn(node, "attrs", env)
}

// clauseFree resolves a for/if/switch header. clause is the raw clause WITHOUT
// the keyword (as stored on the markup node); the keyword is supplied so the
// header parses as its real statement kind.
func clauseFree(keyword, clause string, env map[string]bool) bool {
	if strings.TrimSpace(clause) == "" {
		return false
	}
	node, _, ok := parseFragment(keyword+" "+clause, fragClause)
	if !ok {
		return valueIdents(clause)["attrs"]
	}
	return freeIn(node, "attrs", env)
}

// clauseBindings returns the reserved-name bindings introduced by a for/if/switch
// clause (loop/range vars, if/switch init vars), for threading into the subtree
// environment.
func clauseBindings(keyword, clause string) []boundIdent {
	if strings.TrimSpace(clause) == "" {
		return nil
	}
	return fragmentBindings(keyword+" "+clause, fragClause)
}

// extendEnv returns a copy of env with binds added — used when descending into a
// scoped subtree so sibling scopes are not mutated.
func extendEnv(env map[string]bool, binds []boundIdent) map[string]bool {
	out := make(map[string]bool, len(env)+len(binds))
	for k, v := range env {
		if v {
			out[k] = true
		}
	}
	for _, b := range binds {
		out[b.name] = true
	}
	return out
}

// ---- the scope walk ----

// freeIn reports whether `name` occurs free (unbound) anywhere in node, given an
// initial set of bound names (the markup-inherited environment).
func freeIn(node goast.Node, name string, bound map[string]bool) bool {
	seed := make(map[string]bool, len(bound))
	for k, v := range bound {
		if v {
			seed[k] = true
		}
	}
	w := &freeWalker{name: name, scopes: []map[string]bool{seed}}
	w.walk(node)
	return w.found
}

// freeWalker is a hand-rolled scope stack over go/ast. goast.Inspect cannot
// express Go's walk order (a `:=` RHS evaluates before its LHS binds; a range X
// before its Key/Value bind) nor scope pop, so the walk is explicit.
type freeWalker struct {
	name   string
	scopes []map[string]bool
	found  bool
}

func (w *freeWalker) bound(n string) bool {
	for i := len(w.scopes) - 1; i >= 0; i-- {
		if w.scopes[i][n] {
			return true
		}
	}
	return false
}

func (w *freeWalker) push() { w.scopes = append(w.scopes, map[string]bool{}) }
func (w *freeWalker) pop()  { w.scopes = w.scopes[:len(w.scopes)-1] }

func (w *freeWalker) define(n string) {
	if n != "" && n != "_" {
		w.scopes[len(w.scopes)-1][n] = true
	}
}

// defineExpr defines a binding target that is a bare identifier (a `:=` LHS, a
// range Key/Value). Non-ident targets bind nothing.
func (w *freeWalker) defineExpr(e goast.Expr) {
	if id, ok := e.(*goast.Ident); ok {
		w.define(id.Name)
	}
}

func (w *freeWalker) defineFieldNames(fl *goast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		for _, id := range f.Names {
			w.define(id.Name)
		}
	}
}

func (w *freeWalker) walkFieldTypes(fl *goast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		w.walk(f.Type)
	}
}

// walk visits node in Go evaluation order, maintaining the scope stack. It
// handles every go/ast node that can appear in a function body, clause header,
// or expression fragment. Idents that are NOT value uses are skipped at their
// parent: a SelectorExpr's Sel, a CompositeLit key that is a bare Ident (a
// struct field name — and safe to skip for `attrs`, a non-comparable slice that
// can never be a valid map key/case value; documented in the design spec), and
// LabeledStmt / BranchStmt labels.
func (w *freeWalker) walk(n goast.Node) {
	if n == nil || w.found {
		return
	}
	switch node := n.(type) {

	// ---- expressions ----
	case *goast.Ident:
		if node.Name == w.name && !w.bound(node.Name) {
			w.found = true
		}
	case *goast.BasicLit:
		// no idents
	case *goast.ParenExpr:
		w.walk(node.X)
	case *goast.SelectorExpr:
		w.walk(node.X) // Sel is a field/method name, never a value ident
	case *goast.StarExpr:
		w.walk(node.X)
	case *goast.UnaryExpr:
		w.walk(node.X)
	case *goast.BinaryExpr:
		w.walk(node.X)
		w.walk(node.Y)
	case *goast.IndexExpr:
		w.walk(node.X)
		w.walk(node.Index)
	case *goast.IndexListExpr:
		w.walk(node.X)
		for _, e := range node.Indices {
			w.walk(e)
		}
	case *goast.SliceExpr:
		w.walk(node.X)
		w.walk(node.Low)
		w.walk(node.High)
		w.walk(node.Max)
	case *goast.TypeAssertExpr:
		w.walk(node.X)
		w.walk(node.Type)
	case *goast.CallExpr:
		w.walk(node.Fun)
		for _, a := range node.Args {
			w.walk(a)
		}
	case *goast.CompositeLit:
		w.walk(node.Type)
		for _, e := range node.Elts {
			if kv, ok := e.(*goast.KeyValueExpr); ok {
				// A bare-Ident key is a struct field name (skip). A non-ident key
				// (map/array index expression) is a value use — walk it.
				if _, isIdent := kv.Key.(*goast.Ident); !isIdent {
					w.walk(kv.Key)
				}
				w.walk(kv.Value)
			} else {
				w.walk(e)
			}
		}
	case *goast.KeyValueExpr:
		// Reached outside a CompositeLit (unusual); walk both sides.
		w.walk(node.Key)
		w.walk(node.Value)
	case *goast.FuncLit:
		w.push()
		w.defineFieldNames(node.Type.Params)
		w.defineFieldNames(node.Type.Results)
		w.walkFieldTypes(node.Type.Params)
		w.walkFieldTypes(node.Type.Results)
		w.walk(node.Body)
		w.pop()
	case *goast.Ellipsis:
		w.walk(node.Elt)
	case *goast.ArrayType:
		w.walk(node.Len)
		w.walk(node.Elt)
	case *goast.MapType:
		w.walk(node.Key)
		w.walk(node.Value)
	case *goast.ChanType:
		w.walk(node.Value)
	case *goast.StructType:
		w.walkFieldTypes(node.Fields)
	case *goast.InterfaceType:
		w.walkFieldTypes(node.Methods)
	case *goast.FuncType:
		w.walkFieldTypes(node.Params)
		w.walkFieldTypes(node.Results)

	// ---- statements ----
	case *goast.BlockStmt:
		w.push()
		for _, s := range node.List {
			w.walk(s)
		}
		w.pop()
	case *goast.ExprStmt:
		w.walk(node.X)
	case *goast.SendStmt:
		w.walk(node.Chan)
		w.walk(node.Value)
	case *goast.IncDecStmt:
		w.walk(node.X)
	case *goast.AssignStmt:
		// RHS evaluates in the outer scope before any `:=` LHS binds.
		for _, rhs := range node.Rhs {
			w.walk(rhs)
		}
		if node.Tok == token.DEFINE {
			for _, lhs := range node.Lhs {
				w.defineExpr(lhs)
			}
		} else {
			for _, lhs := range node.Lhs {
				w.walk(lhs) // assignment target of an existing var — a use
			}
		}
	case *goast.DeclStmt:
		if gd, ok := node.Decl.(*goast.GenDecl); ok {
			for _, spec := range gd.Specs {
				switch sp := spec.(type) {
				case *goast.ValueSpec:
					w.walk(sp.Type)
					for _, v := range sp.Values {
						w.walk(v)
					}
					for _, id := range sp.Names {
						w.define(id.Name)
					}
				case *goast.TypeSpec:
					w.define(sp.Name.Name)
					w.walk(sp.Type)
				}
			}
		}
	case *goast.ReturnStmt:
		for _, r := range node.Results {
			w.walk(r)
		}
	case *goast.GoStmt:
		w.walk(node.Call)
	case *goast.DeferStmt:
		w.walk(node.Call)
	case *goast.LabeledStmt:
		w.walk(node.Stmt) // label skipped
	case *goast.BranchStmt:
		// break/continue/goto label skipped
	case *goast.IfStmt:
		w.push()
		w.walk(node.Init) // init binding scopes over Cond, Body and Else
		w.walk(node.Cond)
		w.walk(node.Body)
		w.walk(node.Else)
		w.pop()
	case *goast.ForStmt:
		w.push()
		w.walk(node.Init)
		w.walk(node.Cond)
		w.walk(node.Post)
		w.walk(node.Body)
		w.pop()
	case *goast.RangeStmt:
		w.push()
		w.walk(node.X) // X evaluates before Key/Value bind
		if node.Tok == token.DEFINE {
			w.defineExpr(node.Key)
			w.defineExpr(node.Value)
		} else {
			w.walk(node.Key)
			w.walk(node.Value)
		}
		w.walk(node.Body)
		w.pop()
	case *goast.SwitchStmt:
		w.push()
		w.walk(node.Init) // init binding scopes over Tag and all cases
		w.walk(node.Tag)
		for _, c := range node.Body.List {
			w.walk(c)
		}
		w.pop()
	case *goast.CaseClause:
		w.push()
		for _, e := range node.List {
			w.walk(e)
		}
		for _, s := range node.Body {
			w.walk(s)
		}
		w.pop()
	case *goast.TypeSwitchStmt:
		w.push()
		w.walk(node.Init)
		// Assign is `v := x.(type)` (binds v per clause) or `x.(type)`.
		tsVar := ""
		switch a := node.Assign.(type) {
		case *goast.AssignStmt:
			if len(a.Lhs) == 1 {
				if id, ok := a.Lhs[0].(*goast.Ident); ok {
					tsVar = id.Name
				}
			}
			for _, rhs := range a.Rhs {
				if ta, ok := rhs.(*goast.TypeAssertExpr); ok {
					w.walk(ta.X) // Type is nil in `x.(type)`
				} else {
					w.walk(rhs)
				}
			}
		case *goast.ExprStmt:
			if ta, ok := a.X.(*goast.TypeAssertExpr); ok {
				w.walk(ta.X)
			} else {
				w.walk(a.X)
			}
		}
		for _, c := range node.Body.List {
			cc, ok := c.(*goast.CaseClause)
			if !ok {
				continue
			}
			w.push()
			if tsVar != "" {
				w.define(tsVar)
			}
			for _, e := range cc.List {
				w.walk(e)
			}
			for _, s := range cc.Body {
				w.walk(s)
			}
			w.pop()
		}
		w.pop()
	case *goast.SelectStmt:
		for _, c := range node.Body.List {
			cc, ok := c.(*goast.CommClause)
			if !ok {
				continue
			}
			w.push()
			w.walk(cc.Comm) // may define (`v := <-ch`)
			for _, s := range cc.Body {
				w.walk(s)
			}
			w.pop()
		}
	case *goast.EmptyStmt, *goast.BadStmt, *goast.BadExpr, *goast.BadDecl:
		// nothing
	}
}

// ---- top-level binding extraction (offsets for Task 3) ----

// fragmentBindings returns the TOP-LEVEL reserved-name bindings of a parsed
// fragment, with byte offsets into src. Only bindings that share the render
// closure's scope are returned (a nested block/func-literal binding is not
// top-level and does not extend the sibling environment). The reserved-name
// filter is exactly what env threading needs (only a reserved name can shadow
// the reserved meaning) and what Task 3 diagnoses.
func fragmentBindings(src string, kind fragKind) []boundIdent {
	node, fset, ok := parseFragment(src, kind)
	if !ok || node == nil {
		return nil
	}
	var out []boundIdent
	collect := func(id *goast.Ident) {
		if id == nil || !isReservedBodyIdent(id.Name) {
			return
		}
		out = append(out, boundIdent{name: id.Name, off: fset.Position(id.Pos()).Offset - len(fragBodyPrefix)})
	}
	switch kind {
	case fragStmts:
		if block, ok := node.(*goast.BlockStmt); ok {
			for _, s := range block.List {
				collectStmtBindings(s, collect)
			}
		}
	case fragClause:
		collectClauseBindings(node, collect)
	case fragExpr:
		// expressions bind nothing
	}
	return out
}

// collectStmtBindings reports the binding idents a single statement introduces
// in its OWN scope (not descending into nested blocks): a `:=` LHS, a var/const
// spec's names, a type spec's name. A LabeledStmt is transparent to its target.
func collectStmtBindings(s goast.Stmt, collect func(*goast.Ident)) {
	switch st := s.(type) {
	case *goast.AssignStmt:
		if st.Tok == token.DEFINE {
			for _, lhs := range st.Lhs {
				if id, ok := lhs.(*goast.Ident); ok {
					collect(id)
				}
			}
		}
	case *goast.DeclStmt:
		if gd, ok := st.Decl.(*goast.GenDecl); ok {
			for _, spec := range gd.Specs {
				switch sp := spec.(type) {
				case *goast.ValueSpec:
					for _, id := range sp.Names {
						collect(id)
					}
				case *goast.TypeSpec:
					collect(sp.Name)
				}
			}
		}
	case *goast.LabeledStmt:
		collectStmtBindings(st.Stmt, collect)
	}
}

// collectClauseBindings reports the binding idents a for/if/switch header
// introduces.
func collectClauseBindings(n goast.Node, collect func(*goast.Ident)) {
	switch s := n.(type) {
	case *goast.ForStmt:
		collectStmtBindings(s.Init, collect)
	case *goast.RangeStmt:
		if s.Tok == token.DEFINE {
			if id, ok := s.Key.(*goast.Ident); ok {
				collect(id)
			}
			if id, ok := s.Value.(*goast.Ident); ok {
				collect(id)
			}
		}
	case *goast.IfStmt:
		collectStmtBindings(s.Init, collect)
	case *goast.SwitchStmt:
		collectStmtBindings(s.Init, collect)
	case *goast.TypeSwitchStmt:
		collectStmtBindings(s.Assign, collect)
	}
}
