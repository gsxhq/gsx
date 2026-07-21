package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
)

// scopedObject is one visible go/types object at a cursor, tagged with the sort
// tier its declaring scope earns (see completion_items.go).
type scopedObject struct {
	obj  types.Object
	tier int
}

// goKeywords are the Go keywords offered in statement contexts (GoBlock `{{ }}`
// and top-level GoChunk positions), where a statement or declaration may begin.
// Expression-only contexts (an Interp's `{ }`, an attribute value) never see
// these.
var goKeywords = []string{
	"if", "for", "switch", "select", "return", "var", "const", "type", "func",
	"defer", "go", "break", "continue", "fallthrough", "range", "struct",
	"interface", "map", "chan",
}

// fileScopeSet collects the analyzed package's file scopes. In go/types the file
// scopes are exactly the scopes recorded in Info.Scopes under an *ast.File key;
// imported package names (*types.PkgName) are declared there, so they earn the
// tierImported sort tier.
func fileScopeSet(pkg *Package) map[*types.Scope]bool {
	set := map[*types.Scope]bool{}
	if pkg == nil || pkg.Info == nil {
		return set
	}
	for node, scope := range pkg.Info.Scopes {
		if _, ok := node.(*ast.File); ok {
			set[scope] = true
		}
	}
	return set
}

// isFileScope reports whether s is a file scope of the analyzed package.
func isFileScope(pkg *Package, s *types.Scope) bool {
	return fileScopeSet(pkg)[s]
}

// innermostScopeAt returns the smallest go/types scope containing pos, then
// descends to the innermost scope at pos within it (Scope.Innermost). It reads
// only Info.Scopes, whose positions live in skeleton (pkg.Fset) coordinates —
// so pos must be a skeleton position. Falls back to the package scope when no
// recorded scope contains pos (e.g. a top-level position outside any block).
func innermostScopeAt(pkg *Package, pos token.Pos) *types.Scope {
	if pkg == nil || pkg.Info == nil {
		if pkg != nil && pkg.Types != nil {
			return pkg.Types.Scope()
		}
		return nil
	}
	var best *types.Scope
	for _, s := range pkg.Info.Scopes {
		if !s.Contains(pos) {
			continue
		}
		if best == nil || (s.Pos() >= best.Pos() && s.End() <= best.End()) {
			best = s
		}
	}
	if best == nil {
		return pkg.Types.Scope()
	}
	return best.Innermost(pos)
}

// innermostScopeAtAuthored resolves the innermost scope for a cursor expressed
// in AUTHORED .gsx coordinates (path, byte offset off) rather than skeleton
// coordinates. It is the GoChunk bridge: a top-level verbatim Go span has no
// ExprMap/CtrlMap entry, so there is no skeleton expr node to add a relative
// offset to. Instead it matches each recorded scope's start position — mapped
// back to the .gsx through the skeleton's //line directives (pkg.Fset.Position)
// — against the authored path and offset, and keeps the smallest span that
// contains off. Falls back to the package scope when nothing maps back (a
// top-level position between declarations, whose enclosing scope is the file
// scope, is not //line-mapped to the .gsx and so is not matched here — a known
// v1 limitation: imported names do not complete inside a bare GoChunk position).
func innermostScopeAtAuthored(pkg *Package, path string, off int) *types.Scope {
	if pkg == nil || pkg.Info == nil || pkg.Fset == nil {
		if pkg != nil && pkg.Types != nil {
			return pkg.Types.Scope()
		}
		return nil
	}
	var best *types.Scope
	bestSpan := 1 << 30
	for _, s := range pkg.Info.Scopes {
		if !s.Pos().IsValid() || !s.End().IsValid() {
			continue
		}
		lo := pkg.Fset.Position(s.Pos())
		hi := pkg.Fset.Position(s.End())
		if lo.Filename == "" || !samePath(lo.Filename, path) || lo.Filename != hi.Filename {
			continue
		}
		if off < lo.Offset || off > hi.Offset {
			continue
		}
		if span := hi.Offset - lo.Offset; span < bestSpan {
			best = s
			bestSpan = span
		}
	}
	if best == nil {
		return pkg.Types.Scope()
	}
	return best
}

// samePath compares two filesystem paths for scope-authored matching. The
// skeleton's //line directives carry the exact authored path the analyzer was
// given, so a plain equality is correct; a base-name fallback tolerates the rare
// relative/absolute mismatch without widening to unrelated files.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	return baseName(a) != "" && baseName(a) == baseName(b)
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

// scopeCandidates walks from scope outward to the universe, collecting every
// visible name once (inner declarations shadow same-named outer ones). A
// function-local object declared after pos is excluded — Go's declaration-order
// rule — while package scope and above are order-independent, so their objects
// are always visible. Each object is tagged with its scope's sort tier. When
// pos is invalid (the GoChunk bridge, whose scope is order-independent anyway)
// the declared-after filter is skipped.
func scopeCandidates(pkg *Package, scope *types.Scope, pos token.Pos) []scopedObject {
	if pkg == nil || pkg.Types == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []scopedObject
	pkgScope := pkg.Types.Scope()
	fileScopes := fileScopeSet(pkg)
	for s := scope; s != nil; s = s.Parent() {
		fileScope := fileScopes[s]
		local := s != types.Universe && s != pkgScope && !fileScope
		for _, name := range s.Names() {
			if seen[name] || name == "_" {
				continue
			}
			obj := s.Lookup(name)
			if obj == nil {
				continue
			}
			if local && obj.Pos().IsValid() && pos.IsValid() && obj.Pos() > pos {
				continue // declared after the cursor
			}
			seen[name] = true
			tier := tierLocal
			switch {
			case s == types.Universe:
				tier = tierUniverse
			case s == pkgScope:
				tier = tierPackage
			case fileScope:
				tier = tierImported
			}
			out = append(out, scopedObject{obj, tier})
		}
	}
	return out
}

// goCompletionItems builds the completion list for a Go identifier-position
// cursor whose enclosing lexical scope is scope and whose skeleton position is
// pos (invalid for the GoChunk bridge). It enumerates every visible object via
// scopeCandidates and, when statementCtx is set (GoBlock/GoChunk positions),
// appends the Go statement keywords. Items replace the [start, end) token span
// in text. skel/skelPos are unused by the identifier path but are the anchor a
// later member-completion path (after a `.`) reads.
func goCompletionItems(pkg *Package, scope *types.Scope, pos token.Pos, statementCtx bool, text string, start, end int, enc encoding) []CompletionItem {
	qf := qualifierFor(pkg)
	var items []CompletionItem
	for _, cand := range scopeCandidates(pkg, scope, pos) {
		kind, detail := goObjectPresentation(cand.obj, qf)
		name := cand.obj.Name()
		items = append(items, newCompletionItem(text, start, end, enc, name, name, kind, cand.tier, detail, nil))
	}
	if statementCtx {
		for _, kw := range goKeywords {
			items = append(items, newCompletionItem(text, start, end, enc, kw, kw, ciKindKeyword, tierKeyword, "keyword", nil))
		}
	}
	return items
}

// goObjectPresentation maps a go/types object to its LSP completion kind and a
// human-readable detail string. Vars and fields show their type; funcs show the
// full signature; a package name shows its import path; other objects show the
// go/types object string.
func goObjectPresentation(obj types.Object, qf types.Qualifier) (kind int, detail string) {
	switch o := obj.(type) {
	case *types.Var:
		if o.IsField() {
			return ciKindField, types.TypeString(o.Type(), qf)
		}
		return ciKindVariable, types.TypeString(o.Type(), qf)
	case *types.Func:
		kind = ciKindFunction
		if sig, ok := o.Type().(*types.Signature); ok && sig.Recv() != nil {
			kind = ciKindMethod
		}
		return kind, types.ObjectString(o, qf)
	case *types.Const:
		return ciKindConstant, types.ObjectString(o, qf)
	case *types.TypeName:
		return typeNameKind(o), types.ObjectString(o, qf)
	case *types.PkgName:
		return ciKindModule, o.Imported().Path()
	case *types.Builtin:
		return ciKindFunction, types.ObjectString(o, qf)
	case *types.Nil:
		return ciKindConstant, types.ObjectString(o, qf)
	default:
		return ciKindVariable, types.ObjectString(obj, qf)
	}
}

// typeNameKind classifies a type name by its underlying type: a struct, an
// interface, or (default) a class-like named type / alias / basic type.
func typeNameKind(tn *types.TypeName) int {
	underlying := types.Unalias(tn.Type()).Underlying()
	switch underlying.(type) {
	case *types.Struct:
		return ciKindStruct
	case *types.Interface:
		return ciKindInterface
	default:
		return ciKindClass
	}
}

// goCompletionBridge resolves a ctxGoExpr / ctxSigType cursor into the enclosing
// go/types scope and skeleton position for identifier enumeration, working on
// the EPHEMERAL package eph (which re-parses the repaired buffer, so its node
// identities differ from the classifier's r.parsed — the cursor is re-located by
// byte offset here rather than reused). Routing is by the classifier's node type
// (cc, resolved over r.parsed) but the actual bridge reads eph's ExprMap /
// CtrlMap / SigTypes over the ephemerally re-located node. exprStartOff is the
// byte offset of the classifier's matched fragment start (cc.exprPos in original
// buffer coordinates, which equal the ephemeral buffer's since repairs insert
// only at off); off is the cursor's byte offset. Returns statementCtx true for
// GoBlock/GoChunk positions.
func goCompletionBridge(eph *Package, cc completionContext, exprStartOff, off int, path string) (scope *types.Scope, skel ast.Expr, skelPos token.Pos, statementCtx bool, ok bool) {
	if eph == nil || eph.Info == nil {
		return nil, nil, token.NoPos, false, false
	}

	// GoChunk: a top-level verbatim Go span with no ExprMap/CtrlMap entry. Bridge
	// through authored coordinates.
	if _, isChunk := cc.node.(*gsxast.GoChunk); isChunk {
		scope = innermostScopeAtAuthored(eph, path, off)
		if scope == nil {
			return nil, nil, token.NoPos, false, false
		}
		return scope, nil, token.NoPos, true, true
	}

	// Signature type (ctxSigType): mirror signatureTypeIdentAt's bridge to reach
	// the type-checked skeleton position, then enumerate the scope there (a
	// component signature type resolves in the package/file scope).
	if cc.kind == ctxSigType {
		return sigTypeCompletionBridge(eph, path, off)
	}

	// ExprMap / CtrlMap: re-locate the ephemeral node by its fragment START
	// offset (matching the classifier's fragment), not by cursor containment — an
	// empty `{{ }}` GoBlock or empty `{ }` Interp has a zero-length nav span that
	// no containment probe can hit. Then bridge by relative offset like
	// hover/definition do. The cursor's in-fragment offset is off-exprStartOff.
	node := ephemeralNodeByStart(eph, path, exprStartOff)
	if node == nil {
		return nil, nil, token.NoPos, false, false
	}

	if isCtrlSpan(node, matchedSpanPos(node, exprStartOff, eph)) {
		cr, found := eph.CtrlMap[node]
		if !found || cr.Node == nil {
			return nil, nil, token.NoPos, false, false
		}
		skelPos = cr.ClauseStart + token.Pos(off-exprStartOff)
		scope = innermostScopeAt(eph, skelPos)
		if scope == nil {
			return nil, nil, token.NoPos, false, false
		}
		_, isBlock := node.(*gsxast.GoBlock)
		return scope, nil, skelPos, isBlock, true
	}

	skel = eph.ExprMap[node]
	if skel == nil {
		return nil, nil, token.NoPos, false, false
	}
	skelPos = skel.Pos() + token.Pos(off-exprStartOff)
	scope = innermostScopeAt(eph, skelPos)
	if scope == nil {
		return nil, nil, token.NoPos, false, false
	}
	return scope, skel, skelPos, false, true
}

// matchedSpanPos returns the nav-span position of node whose byte-offset start
// equals startOff, so isCtrlSpan can discriminate a multi-span node (a
// ClassPart's expr span vs its `: cond` ctrl span). Falls back to the node's
// first span position when none matches exactly.
func matchedSpanPos(node gsxast.Node, startOff int, eph *Package) token.Pos {
	spans, _ := nodeNavSpans(node)
	for _, s := range spans {
		if s.pos.IsValid() && eph.GSXFset.Position(s.pos).Offset == startOff {
			return s.pos
		}
	}
	if len(spans) > 0 {
		return spans[0].pos
	}
	return token.NoPos
}

// sigTypeCompletionBridge mirrors signatureTypeIdentAt (definition.go) to bridge
// a cursor inside a component-signature type span into the type-checked skeleton
// type expression, and returns the scope for identifier enumeration there.
func sigTypeCompletionBridge(eph *Package, path string, off int) (scope *types.Scope, skel ast.Expr, skelPos token.Pos, statementCtx bool, ok bool) {
	f := eph.Files[path]
	if f == nil || eph.GSXFset == nil || eph.SigTypes == nil {
		return nil, nil, token.NoPos, false, false
	}
	for _, d := range f.Decls {
		c, isComp := d.(*gsxast.Component)
		if !isComp {
			continue
		}
		for _, r := range eph.SigTypes[c] {
			start := eph.GSXFset.Position(r.GSXPos).Offset
			if off < start || off > start+r.Len {
				continue
			}
			skelPos = r.SkelTyp.Pos() + token.Pos(off-start)
			scope = innermostScopeAt(eph, skelPos)
			if scope == nil {
				return nil, nil, token.NoPos, false, false
			}
			return scope, r.SkelTyp, skelPos, false, true
		}
	}
	return nil, nil, token.NoPos, false, false
}

// ephemeralNodeByStart locates the ExprMap/CtrlMap-bridged gsx node in the
// ephemeral package whose nav-fragment START byte offset equals startOff — the
// classifier's matched fragment start. Matching by start rather than cursor
// containment is what lets a zero-length span (an empty `{{ }}` block, an empty
// `{ }` interp) be found: a completion cursor sits at or past the fragment's
// bytes, which no half-open containment interval of length zero can cover.
// Fragment starts are unique across nodes (spans never overlap), so the first
// match is unambiguous.
func ephemeralNodeByStart(eph *Package, path string, startOff int) gsxast.Node {
	f := eph.Files[path]
	if f == nil || eph.GSXFset == nil {
		return nil
	}
	var found gsxast.Node
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if found != nil || n == nil {
			return found == nil
		}
		spans, _ := nodeNavSpans(n)
		for _, s := range spans {
			if s.pos.IsValid() && eph.GSXFset.Position(s.pos).Offset == startOff {
				found = n
				return false
			}
		}
		return true
	})
	return found
}
