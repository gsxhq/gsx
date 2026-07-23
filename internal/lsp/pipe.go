package lsp

import (
	"go/ast"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
)

// ctxIdent is the reserved ambient render-context identifier the codegen lowering
// injects as the first argument of a ctx-taking filter. It MUST match codegen's
// pipeCtxIdent ("ctx"). The value is stable; a real ctx-injected end-to-end guard
// lands with the filter-resolution wiring (std has no ctx filter today).
const ctxIdent = "ctx"

// walkPipe peels the N seed-first filter layers of a lowered pipeline expression.
// The lowering shape is `Func([ctx,] subject, args…)` nested via the subject, so
// for stage i it returns the filter's Sel ident and its user stage args, and at
// the bottom the (unwrapped) seed expression. ok=false on any unexpected shape.
func walkPipe(skel ast.Expr, n int) (selSel []*ast.Ident, selArgs [][]ast.Expr, seed ast.Expr, ok bool) {
	if n <= 0 {
		return nil, nil, nil, false
	}
	selSel = make([]*ast.Ident, n)
	selArgs = make([][]ast.Expr, n)
	cur := skel
	for i := n - 1; i >= 0; i-- {
		call, isCall := cur.(*ast.CallExpr)
		if !isCall {
			return nil, nil, nil, false
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel || len(call.Args) == 0 {
			return nil, nil, nil, false
		}
		selSel[i] = sel.Sel
		subjIdx := 0
		if id, isID := call.Args[0].(*ast.Ident); isID && id.Name == ctxIdent {
			subjIdx = 1 // ctx injected at args[0]
		}
		if subjIdx >= len(call.Args) {
			return nil, nil, nil, false
		}
		selArgs[i] = call.Args[subjIdx+1:]
		cur = call.Args[subjIdx]
	}
	return selSel, selArgs, unwrapParens(cur), true
}

func unwrapParens(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// bridgePipeNodeBySeed locates, in pkg.Files[path], the pipeline-carrying node
// whose seed span (the primary nodeNavSpans span) starts at byte offset seedOff.
// It bridges a completion cursor — classified against the repair parse (distinct
// node pointers) — to the semantically equivalent node in the ephemeral analysis
// package, whose ExprMap/Info carry the type facts. The seed sits BEFORE the
// repair insertion point, so its offset is identical in both parses (bytes
// before the insertion never move). nil when no pipeline node's seed matches.
func bridgePipeNodeBySeed(pkg *Package, path string, seedOff int) gsxast.Node {
	f := pkg.Files[path]
	if f == nil || pkg.GSXFset == nil {
		return nil
	}
	var found gsxast.Node
	inspectWithEmbedded(f, func(n gsxast.Node) bool {
		if found != nil {
			return false
		}
		if len(pipeStages(n)) == 0 {
			return true
		}
		spans, _ := nodeNavSpans(n)
		if len(spans) == 0 || !spans[0].pos.IsValid() {
			return true
		}
		if pkg.GSXFset.Position(spans[0].pos).Offset == seedOff {
			found = n
			return false
		}
		return true
	})
	return found
}

// cursorStageIndex returns the 0-based index of the pipeline stage the cursor
// (authored byte offset off) sits on: the LAST stage whose filter-name start is
// at or before off. Stages are left-to-right, and the cursor stage is the
// rightmost one begun at or before the cursor — an at-boundary end-of-token
// cursor (`|> up▮`) and a repair-healed empty stage (`|> ▮` → `_`) both resolve
// to that stage. ok=false when no stage begins at or before off (a cursor before
// the first stage — not a real pipe-stage completion position).
func cursorStageIndex(pkg *Package, node gsxast.Node, off int) (int, bool) {
	if pkg.GSXFset == nil {
		return 0, false
	}
	idx := -1
	for i, st := range pipeStages(node) {
		if !st.NamePos.IsValid() {
			continue
		}
		if pkg.GSXFset.Position(st.NamePos).Offset <= off {
			idx = i
		}
	}
	if idx < 0 {
		return 0, false
	}
	return idx, true
}

// pipeIncomingType returns the Go type flowing INTO the pipeline stage at
// stageIdx, resolved in the ephemeral skeleton's type universe (the same
// universe pkg.Info/pkg.Types live in, so a later types.AssignableTo against a
// filter's subject parameter is sound). Stage 0's incoming type is the seed
// expression's type; a later stage's is the RESULT type of the immediately
// preceding filter (its first result — an (R, error) filter chains its R).
// ok=false — the fail-open signal — when the type cannot be resolved: an
// untyped/invalid seed, a preceding filter whose package is not imported into
// the skeleton universe, or a generic (type-parameter) result that cannot be
// soundly narrowed against.
func pipeIncomingType(pkg *Package, node gsxast.Node, stageIdx int, filters []FilterCandidate) (types.Type, bool) {
	if stageIdx == 0 {
		return pipeSeedType(pkg, node)
	}
	stages := pipeStages(node)
	if stageIdx-1 >= len(stages) {
		return nil, false
	}
	sig := pipeFilterSignature(pkg, stages[stageIdx-1].Name, filters)
	if sig == nil || sig.Results().Len() == 0 {
		return nil, false
	}
	t := sig.Results().At(0).Type()
	if !resolvableIncomingType(t) {
		return nil, false
	}
	return t, true
}

// pipeSeedType returns the type of the pipeline's seed expression. When the
// lowered skeleton is a nested filter-call chain (every stage resolved) the seed
// is walkPipe's innermost expression; when the pipeline failed to lower — the
// common completion case, where the cursor stage is an unknown/partial filter
// and codegen falls the whole pipeline back to the bare seed (see analyze.go's
// probeExpr) — ExprMap[node] IS the seed expression. Both are looked up in
// Info.Types. ok=false when untyped or invalid.
func pipeSeedType(pkg *Package, node gsxast.Node) (types.Type, bool) {
	skel := pkg.ExprMap[node]
	if skel == nil || pkg.Info == nil {
		return nil, false
	}
	seed := skel
	if _, _, s, ok := walkPipe(skel, len(pipeStages(node))); ok && s != nil {
		seed = s
	}
	tv, ok := pkg.Info.Types[seed]
	if !ok || !resolvableIncomingType(tv.Type) {
		return nil, false
	}
	return tv.Type, true
}

// resolvableIncomingType reports whether t is a concrete type a subject-parameter
// compatibility check can soundly narrow against: not nil, not the invalid type,
// and not an un-instantiated type parameter (a generic result flows into the next
// stage as a type parameter, which cannot be soundly matched — fail open).
func resolvableIncomingType(t types.Type) bool {
	if t == nil {
		return false
	}
	if b, ok := t.(*types.Basic); ok && b.Kind() == types.Invalid {
		return false
	}
	if _, ok := types.Unalias(t).(*types.TypeParam); ok {
		return false
	}
	return true
}

// pipeFilterSignature resolves the *types.Signature of the filter registered
// under the template-level name, by matching it to its FilterCandidate (which
// carries the winning package path and Go func name) and looking that func up in
// the ephemeral skeleton universe. nil when the name is unknown or its package
// is not imported into the skeleton (see filterFuncSignature).
func pipeFilterSignature(pkg *Package, name string, filters []FilterCandidate) *types.Signature {
	for _, f := range filters {
		if f.Name == name {
			return filterFuncSignature(pkg, f.Pkg, f.Func)
		}
	}
	return nil
}

// filterFuncSignature looks up the exported func funcName in the package pkgPath
// as it is imported into the ephemeral skeleton's type universe (pkg.Types'
// direct imports), returning its signature. The skeleton imports a filter
// package ONLY when some pipeline successfully lowered against it, so a filter
// whose package is absent from this universe resolves to nil — the caller treats
// that as a per-candidate fail-open (offer it), never as an exclusion. Resolving
// here (rather than in the separate filter-table load universe) keeps the
// signature's parameter types identity-comparable with pipeIncomingType, which
// is drawn from this same universe.
func filterFuncSignature(pkg *Package, pkgPath, funcName string) *types.Signature {
	target := importedPackageAt(pkg, pkgPath)
	if target == nil {
		return nil
	}
	fn, ok := target.Scope().Lookup(funcName).(*types.Func)
	if !ok {
		return nil
	}
	sig, _ := fn.Type().(*types.Signature)
	return sig
}

// filterSubjectType returns the type of a filter's SUBJECT parameter — the one
// gsx binds the piped value to. That is parameter 0, or parameter 1 when the
// filter takes the ambient context.Context first (wantsCtx). When the subject is
// itself the trailing variadic parameter (`func(vs ...string) R`), the piped
// value binds to one element, so the element type is returned. ok=false when the
// signature has no subject parameter at the expected index.
func filterSubjectType(sig *types.Signature, wantsCtx bool) (types.Type, bool) {
	idx := 0
	if wantsCtx {
		idx = 1
	}
	n := sig.Params().Len()
	if idx >= n {
		return nil, false
	}
	t := sig.Params().At(idx).Type()
	if sig.Variadic() && idx == n-1 {
		if slice, ok := t.(*types.Slice); ok {
			t = slice.Elem()
		}
	}
	return t, true
}

// subjectAccepts reports whether a value of type incoming may flow into a filter
// whose subject parameter is subject. A type-parameter subject (a generic filter
// like `default[T comparable]`) can instantiate to the incoming type, so it is
// always accepted; otherwise standard Go assignability decides — which already
// admits an `any`/interface subject (printf) and an interface the incoming type
// implements.
func subjectAccepts(subject, incoming types.Type) bool {
	if _, ok := types.Unalias(subject).(*types.TypeParam); ok {
		return true
	}
	return types.AssignableTo(incoming, subject)
}

// pipeFilterAccepts reports whether candidate f should be offered for a stage
// whose incoming type is incoming. It resolves f's signature in the skeleton
// universe and checks its subject parameter against incoming. A filter whose
// package is not imported into the universe, or whose signature lacks a subject
// parameter, is offered unconditionally — the per-candidate fail-open that keeps
// narrowing a refinement, never a gate that can hide a candidate on uncertainty.
func pipeFilterAccepts(pkg *Package, f FilterCandidate, incoming types.Type) bool {
	sig := filterFuncSignature(pkg, f.Pkg, f.Func)
	if sig == nil {
		return true
	}
	subject, ok := filterSubjectType(sig, f.WantsCtx)
	if !ok {
		return true
	}
	return subjectAccepts(subject, incoming)
}

// compatiblePipeFilters narrows filters to those whose subject parameter accepts
// the type flowing into the pipeline stage under the cursor. It returns
// (narrowed, true) only when the whole chain up to the cursor stage was
// type-resolved; otherwise (nil, false) — the FAIL-OPEN signal telling the
// caller to offer the full, unnarrowed list. Fail-open covers a missing/shell
// analysis, a cursor that does not bridge to an analyzed pipeline node, an
// unresolvable stage index, and an undeterminable incoming type (broken seed,
// unimported preceding filter, generic previous result). Individual candidates
// whose own package is not imported still pass through (pipeFilterAccepts's
// per-candidate fail-open), so narrowing never hides a filter merely because its
// signature could not be resolved.
func compatiblePipeFilters(pkg *Package, path string, seedOff, off int, filters []FilterCandidate) ([]FilterCandidate, bool) {
	if pkg == nil || pkg.Info == nil || pkg.Types == nil || pkg.GSXFset == nil {
		return nil, false
	}
	node := bridgePipeNodeBySeed(pkg, path, seedOff)
	if node == nil {
		return nil, false
	}
	stageIdx, ok := cursorStageIndex(pkg, node, off)
	if !ok {
		return nil, false
	}
	incoming, ok := pipeIncomingType(pkg, node, stageIdx, filters)
	if !ok {
		return nil, false
	}
	out := make([]FilterCandidate, 0, len(filters))
	for _, f := range filters {
		if pipeFilterAccepts(pkg, f, incoming) {
			out = append(out, f)
		}
	}
	return out, true
}

func pipeStages(node gsxast.Node) []gsxast.PipeStage {
	switch e := node.(type) {
	case *gsxast.Interp:
		return e.Stages
	case *gsxast.ExprAttr:
		return e.Stages
	case *gsxast.SpreadAttr:
		return e.Stages
	case *gsxast.ClassPart:
		return e.Stages
	case *gsxast.ValueArm:
		return e.Stages
	}
	return nil
}

func useObj(pkg *Package, id *ast.Ident) types.Object {
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	return obj
}

func identInArgs(args []ast.Expr, pos token.Pos) *ast.Ident {
	for _, a := range args {
		if a.Pos() <= pos && pos < a.End() {
			return innermostIdent(a, pos)
		}
	}
	return nil
}

// pipedTarget resolves the go/types object under the cursor inside a piped node,
// plus the .gsx byte span of the hovered region (for a Range). ok=false (→ null)
// when the cursor is on no resolvable region or the lowered shape is unexpected.
// It never panics — every assertion is guarded.
func pipedTarget(pkg *Package, node gsxast.Node, exprPos token.Pos, off int) (types.Object, [2]int, bool) {
	stages := pipeStages(node)
	skel := pkg.ExprMap[node]
	if skel == nil || len(stages) == 0 || pkg.Info == nil || pkg.GSXFset == nil {
		return nil, [2]int{}, false
	}
	selSel, selArgs, seedExpr, ok := walkPipe(skel, len(stages))
	if !ok {
		return nil, [2]int{}, false
	}

	// seed region: [seedStart, seedStart+len(seedText)); byte-identical to seedExpr.
	seedStart := pkg.GSXFset.Position(exprPos).Offset
	seedText := exprText(node)
	if seedExpr != nil && off >= seedStart && off < seedStart+len(seedText) {
		if id := innermostIdent(seedExpr, seedExpr.Pos()+token.Pos(off-seedStart)); id != nil {
			if obj := useObj(pkg, id); obj != nil {
				start := seedStart + int(id.Pos()-seedExpr.Pos())
				return obj, [2]int{start, start + len(id.Name)}, true
			}
		}
		return nil, [2]int{}, false
	}

	for i, st := range stages {
		// filter name region.
		if st.NamePos.IsValid() {
			nameStart := pkg.GSXFset.Position(st.NamePos).Offset
			if off >= nameStart && off < nameStart+len(st.Name) {
				if selSel[i] != nil {
					if obj := useObj(pkg, selSel[i]); obj != nil {
						return obj, [2]int{nameStart, nameStart + len(st.Name)}, true
					}
				}
				return nil, [2]int{}, false
			}
		}
		// filter args region: byte-identical to the skeleton args.
		if st.HasArgs && st.ArgsPos.IsValid() && len(selArgs[i]) > 0 {
			argsStart := pkg.GSXFset.Position(st.ArgsPos).Offset
			if off >= argsStart && off < argsStart+len(st.Args) {
				base := selArgs[i][0].Pos()
				if id := identInArgs(selArgs[i], base+token.Pos(off-argsStart)); id != nil {
					if obj := useObj(pkg, id); obj != nil {
						start := argsStart + int(id.Pos()-base)
						return obj, [2]int{start, start + len(id.Name)}, true
					}
				}
				return nil, [2]int{}, false
			}
		}
	}
	return nil, [2]int{}, false
}
