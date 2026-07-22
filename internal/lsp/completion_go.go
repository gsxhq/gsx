package lsp

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// isReservedGsxInternal reports whether name is a gsx-generated internal that
// must never be offered as a completion candidate. The `_gsx` prefix is
// reserved repo-wide for generated code: the skeleton package scope declares
// _gsxuse/_gsxuseq/_gsxusen/_gsxcompsig/_gsxunwrap/_gsxstr/_gsxelem, file
// scopes bind the _gsxrt/_gsxctx runtime imports as PkgNames, and body
// closures declare _gsxbody. Accepting any of them inserts a reserved
// identifier that poisons the file's own analysis, so every enumeration path
// (scope walk, import qualifiers, type members) filters on this prefix.
func isReservedGsxInternal(name string) bool {
	return strings.HasPrefix(name, "_gsx")
}

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
// contains off.
//
// A cursor sitting BETWEEN top-level declarations (whitespace inside a GoChunk,
// not inside any func body) is contained by no func/block scope: only the file
// scope encloses it, and the file scope's Pos/End span the package clause and
// EOF, which are NOT //line-mapped to the .gsx (they lie in the skeleton's
// unmapped prologue), so the span match above misses it. When that happens the
// fallback finds the skeleton file scope whose GoChunk-derived decls //line-map
// to the authored path (fileScopeForAuthoredPath) — the file scope parents to
// the package scope and contributes the file's imported package names, so a
// bare GoChunk position completes imports (tierImported), package decls, and
// (via statementCtx) the Go keywords. Package-scope fallback only when no file
// maps back at all.
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
		if fs := fileScopeForAuthoredPath(pkg, path); fs != nil {
			return fs
		}
		return pkg.Types.Scope()
	}
	return best
}

// fileScopeForAuthoredPath returns the go/types file scope of the skeleton
// *ast.File that lowers the authored .gsx at path, or nil when none is found.
// The mapping uses the //line directives the skeleton already carries: each
// top-level GoChunk body (a user helper func/type/var) is emitted under a
// //line directive to its .gsx position (codegen's splitFileGoSource +
// emitSkeletonLine), so the decls parsed from it report the authored path via
// pkg.Fset.Position. A file scope is keyed in Info.Scopes by its *ast.File; the
// file whose first .gsx-mapped decl matches path owns that authored source.
// (Component-generated funcs sit on unmapped skeleton signature lines and so do
// not match — only genuine GoChunk decls do, which is exactly the population a
// bare GoChunk position lives among.)
func fileScopeForAuthoredPath(pkg *Package, path string) *types.Scope {
	if pkg == nil || pkg.Info == nil || pkg.Fset == nil {
		return nil
	}
	for node, scope := range pkg.Info.Scopes {
		f, ok := node.(*ast.File)
		if !ok {
			continue
		}
		for _, d := range f.Decls {
			if !d.Pos().IsValid() {
				continue
			}
			if samePath(pkg.Fset.Position(d.Pos()).Filename, path) {
				return scope
			}
		}
	}
	return nil
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
			if seen[name] || name == "_" || isReservedGsxInternal(name) {
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

// goCompletionItems builds the completion list for a Go cursor. skel is the
// bridged skeleton expression (nil for the GoBlock/GoChunk bridges — see the
// member gap note below); pos is the cursor's skeleton position (invalid for the
// GoChunk bridge). DISPATCH: when the cursor at pos sits on the Sel of a selector
// `X.Sel` in skel, the member path enumerates X's members (fields/methods, or an
// imported package's exported names); otherwise the scope path enumerates every
// visible object via scopeCandidates and, when statementCtx is set
// (GoBlock/GoChunk positions), appends the Go statement keywords. Items replace
// the [start, end) token span in text.
//
// KNOWN GAP (v1): skel is non-nil only on the ExprMap and sigType bridges; the
// GoBlock/CtrlMap and GoChunk bridges return nil skel, so a member cursor like
// `{{ x.▮ }}` or a GoChunk `x.▮` cannot find its selector and falls through to
// the scope path. Recovering the selector from Info.Types alone (by byte range)
// was rejected as too loose to be sound with the facts these bridges retain.
func goCompletionItems(pkg *Package, scope *types.Scope, skel ast.Expr, pos token.Pos, statementCtx bool, text string, start, end int, enc encoding) []CompletionItem {
	if items, ok := memberCompletionItems(pkg, skel, pos, text, start, end, enc); ok {
		return items // member path: committed even when empty (no scope fallback)
	}
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

// memberObject is one selectable member of a receiver type: the object plus the
// embedding depth at which it was found (0 = direct field/method, larger = a
// deeper promotion). The depth feeds the member sort tier so shallower members
// sort ahead of deeply-promoted ones.
type memberObject struct {
	obj   types.Object
	depth int // embedding depth, 0 = direct
}

// memberCandidates enumerates the selectable members of T: methods of T and *T
// via types.NewMethodSet, plus fields found by a breadth-first embedded-field
// walk with promotion (a shallower depth shadows a deeper same-name member).
// Unexported members are offered only when their declaring package == samePkg.
// Info.Selections is NOT consulted (the warm core never allocates it — probe
// 2026-07-21); the type is walked directly instead.
func memberCandidates(T types.Type, samePkg *types.Package) []memberObject {
	if T == nil {
		return nil
	}
	var out []memberObject
	seen := map[string]bool{}
	include := func(obj types.Object, depth int) {
		if obj == nil || seen[obj.Name()] || isReservedGsxInternal(obj.Name()) {
			return
		}
		if !obj.Exported() && (obj.Pkg() == nil || samePkg == nil || obj.Pkg() != samePkg) {
			return
		}
		seen[obj.Name()] = true
		out = append(out, memberObject{obj, depth})
	}
	// Methods: NewMethodSet on *T sees both pointer and value methods. For a
	// non-addressable T this over-offers pointer methods; acceptable for
	// completion (the type checker flags real misuse; matches gopls behavior).
	mset := types.NewMethodSet(types.NewPointer(T))
	for sel := range mset.Methods() {
		include(sel.Obj(), len(sel.Index())-1)
	}
	// Fields: BFS over embedded structs, depth-tracked, promotion shadowing.
	// Methods are included first, so a field/method name collision keeps the
	// method — Go forbids selecting either ambiguously, so offering the method is
	// an acceptable resolution.
	//
	// visited guards against embedding CYCLES (`type Rec struct{ *Rec; Label
	// string }`, or mutual A-embeds-B / B-embeds-A). The `include` name-dedup
	// alone does NOT stop the walk: a self-embedded field re-enqueues its own
	// type forever while `include` silently drops the duplicate name — an
	// infinite loop. The key is the identity of the pointer-DEREFERENCED type:
	// go/types interns named types, so the same *types.Named backs every
	// `*Rec` embedded field regardless of how many distinct *types.Pointer
	// wrappers present it, and dereferencing consistently before keying makes
	// the pointer-identity comparison sound. BFS visits the shallowest
	// occurrence first, so skipping a later (deeper) revisit preserves
	// promotion shadowing.
	visited := map[types.Type]bool{}
	type queued struct {
		t     types.Type
		depth int
	}
	q := []queued{{T, 0}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		// Defensive depth cap. memberCompletionItems clamps every depth at or
		// above tierPackage-tierMember into one sort tier (depths past ~19 are
		// already indistinguishable in the UI), so 20 is a hard stop no
		// legitimate embedding chain reaches; it guarantees termination even if
		// the identity dedup below ever failed to catch a cycle.
		if cur.depth > 20 {
			continue
		}
		t := cur.t
		if p, ok := t.Underlying().(*types.Pointer); ok {
			t = p.Elem()
		}
		if visited[t] {
			continue
		}
		visited[t] = true
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for f := range st.Fields() {
			include(f, cur.depth)
			if f.Embedded() {
				q = append(q, queued{f.Type(), cur.depth + 1})
			}
		}
	}
	return out
}

// enclosingSelector returns the *ast.SelectorExpr in root whose Sel is exactly
// id, or nil when id is not a selector's Sel. That distinction is the member vs
// scope dispatch: only a cursor ON the member half of `X.Sel` completes members;
// a cursor on X (or a bare identifier) completes scope names.
func enclosingSelector(root ast.Node, id *ast.Ident) *ast.SelectorExpr {
	if root == nil || id == nil {
		return nil
	}
	var found *ast.SelectorExpr
	ast.Inspect(root, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if se, ok := n.(*ast.SelectorExpr); ok && se.Sel == id {
			found = se
			return false
		}
		return true
	})
	return found
}

// memberCompletionItems produces the member-position candidates when the bridged
// cursor at pos sits on the Sel of a selector `X.Sel` in skel. It returns
// ok=false only when there is NO enclosing selector (the identifier/scope path
// applies); once a selector is found the member path is committed even if the
// item list is empty (offering scope locals after a `.` would be wrong). When X
// resolves to an imported package name the exported package members are offered
// (tierImported); otherwise the type of X drives memberCandidates, tiered
// tierMember+depth clamped below tierPackage.
func memberCompletionItems(pkg *Package, skel ast.Expr, pos token.Pos, text string, start, end int, enc encoding) ([]CompletionItem, bool) {
	if skel == nil || pkg == nil || pkg.Info == nil {
		return nil, false
	}
	// innermostIdent uses a half-open [Pos, End) span, but a completion cursor
	// sits AFTER the token it completes: for a typed prefix `user.N▮` the cursor
	// is at `N`'s End and lands on no ident. Probe one byte back to land on the
	// prefix's last char. (The empty-prefix trailing-dot case never needs this —
	// the phantom `_` sits AT the cursor, so the first probe already lands on it;
	// a back-probe there would hit the `.` and find nothing.)
	id := innermostIdent(skel, pos)
	if id == nil && pos > skel.Pos() {
		id = innermostIdent(skel, pos-1)
	}
	if id == nil {
		return nil, false
	}
	sel := enclosingSelector(skel, id)
	if sel == nil {
		return nil, false
	}
	qf := qualifierFor(pkg)

	// Package member: X is an imported package name. Uses records the PkgName;
	// Info.Types does not (a package name is not a value), so check Uses first.
	if xid, ok := sel.X.(*ast.Ident); ok {
		if pn, ok := pkg.Info.Uses[xid].(*types.PkgName); ok {
			scope := pn.Imported().Scope()
			var items []CompletionItem
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if obj == nil || !obj.Exported() || isReservedGsxInternal(name) {
					continue
				}
				kind, detail := goObjectPresentation(obj, qf)
				items = append(items, newCompletionItem(text, start, end, enc, name, name, kind, tierImported, detail, nil))
			}
			return items, true
		}
	}

	// Value member: the type of X drives the method set + field BFS.
	tv, ok := pkg.Info.Types[sel.X]
	if !ok || tv.Type == nil {
		return nil, true // selector found but no type info: member path, empty
	}
	var items []CompletionItem
	for _, m := range memberCandidates(tv.Type, pkg.Types) {
		tier := tierMember + m.depth
		if tier >= tierPackage {
			tier = tierPackage - 1 // clamp to 29: member items never reach tierPackage
		}
		kind, detail := goObjectPresentation(m.obj, qf)
		name := m.obj.Name()
		items = append(items, newCompletionItem(text, start, end, enc, name, name, kind, tier, detail, nil))
	}
	return items, true
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
