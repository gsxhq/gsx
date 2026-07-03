package codegen

import (
	"errors"
	"fmt"
	"go/types"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/ast"
)

// errFailingStageUnsupported backs lowerPipe's defensive nil-wrap guard: a
// mid-pipeline (R, error) stage can only hoist if the caller supplies a wrap
// hook, so a nil wrap fails closed with this sentinel instead of emitting an
// uncompilable tuple assignment. Every real lowering context now passes a
// non-nil wrap (emitPipeWrap, probePipeWrap, or thunkPipeWrap inside a
// cond-attr branch thunk), so as of this writing no caller triggers it — it
// remains as a guard against a future position that forgets to wire one up.
var errFailingStageUnsupported = errors.New("a failing stage is not supported in this position")

// pipeCtxIdent is the literal ambient render-context identifier that emitted
// component bodies bind (the `ctx` of `gsx.Func(func(ctx context.Context, …))`).
// A ctx-taking filter receives THIS identifier as its first argument, so the
// lowered call references the same `ctx` user interpolation already uses.
const pipeCtxIdent = "ctx"

// lowerPipe lowers a pipeline (a seed expression plus left-to-right stages) to a
// nested Go expression string of qualified seed-first filter calls. Each stage
// `subject |> name(args…)` lowers to `<alias>.<Func>( [ctx, ] (subject) [, args…] )`:
//
//   - `ctx` (the ambient render context, pipeCtxIdent) is prepended IFF the
//     filter's first parameter type is exactly context.Context;
//   - the accumulated subject expression goes next, parenthesized;
//   - the stage's explicit args follow (st.Args, comma-joined), when present.
//
// Chaining accumulates left-to-right, so `x |> a |> b(n)` with `a` ctx-less and
// `b` ctx-taking becomes `B(ctx, A((x)), n)`. <alias> is each filter's OWNING
// package alias (e.g. _gsxstd for std, _gsxf0 for the first non-std package).
//
// The SAME string is used for the type probe (analyze.go) and the emitted render
// (emit.go), so type resolution and emission stay aligned (the order invariant).
//
// usedPkgs reports WHICH filter packages the lowered expression references, as a
// map alias→pkgPath, so the caller imports exactly those packages under exactly
// those aliases — the probe (skeleton) and the emit drive their import blocks
// from this SAME set, keeping resolution and emission in lockstep.
//
// Arity/type mismatches are NOT hand-checked here: a ctx-less zero-arg filter is
// fine, and wrong/extra args surface as positioned go/types errors via the probe
// against the resolved signature. Only an unknown filter name is rejected here.
//
// A non-final stage whose filter hasErr (returns (R, error)) is passed through
// wrap after lowering, so the caller can hoist the tuple (emit: a temp + early
// return) or tolerate it (probe: _gsxunwrap) while keeping the SAME lowered
// string shape across both contexts. The final stage is never wrapped — its
// tuple flows through the existing per-context machinery unchanged. wrap == nil
// means the caller does not yet support a failing stage in this position, so a
// mid-pipeline hasErr stage is rejected with a friendly, caller-positioned error.
func lowerPipe(seed string, stages []ast.PipeStage, table filterTable, wrap func(call string) string) (expr string, usedPkgs map[string]string, err error) {
	acc := "(" + strings.TrimSpace(seed) + ")"
	usedPkgs = map[string]string{}
	for i, st := range stages {
		e, ok := table.lookup(st.Name)
		if !ok {
			return "", nil, fmt.Errorf("codegen: unknown filter %q", st.Name)
		}
		usedPkgs[e.alias] = e.pkgPath
		args := make([]string, 0, 3)
		if e.wantsCtx {
			args = append(args, pipeCtxIdent)
		}
		args = append(args, acc)
		if st.HasArgs && strings.TrimSpace(st.Args) != "" {
			args = append(args, st.Args)
		}
		acc = e.alias + "." + e.funcName + "(" + strings.Join(args, ", ") + ")"
		if e.hasErr && i < len(stages)-1 {
			if wrap == nil {
				return "", nil, fmt.Errorf("codegen: filter %q returns (R, error); %w", st.Name, errFailingStageUnsupported)
			}
			acc = wrap(acc)
		}
	}
	return acc, usedPkgs, nil
}

// filterEntry is one harvested filter. funcName is the exported Go name in its
// owning package (e.g. "Upper"); wantsCtx is true when the filter's first
// parameter is context.Context (gsx injects the ambient ctx as that argument);
// hasErr is true when the filter returns (R, error) and needs stage-hoisting for
// non-final pipes; alias is that package's reserved import alias (the caller
// qualifies the call as <alias>.<funcName>); pkgPath is the package's import path
// (so the caller can emit `<alias> "<pkgPath>"`).
type filterEntry struct {
	funcName string
	wantsCtx bool
	hasErr   bool
	alias    string
	pkgPath  string
}

// filterTable maps a template-level filter name to its harvested entry. The
// template name is the std func name with its first rune lowercased.
type filterTable map[string]filterEntry

// FilterAlias is one explicit filter registration from gen.WithFilter: the
// short template Name, and the resolved Go target (PkgPath + FuncName) reflected
// from the registered function value. Aliases are harvested AFTER whole-package
// harvests in option order, participating in the same last-wins table.
type FilterAlias struct {
	Name     string // template-level filter name, e.g. "url"
	PkgPath  string // target package import path, e.g. "example.com/structpages"
	FuncName string // exported Go func name in that package, e.g. "URLFor"
}

// lookup returns the entry for a template-level filter name.
func (t filterTable) lookup(name string) (filterEntry, bool) {
	e, ok := t[name]
	return e, ok
}

// aliasForPath returns the reserved import alias registered for a filter
// package's import path, if ANY filter entry in this table belongs to that
// package — e.g. "github.com/gsxhq/gsx/std" -> "_gsxstd", a user filter
// package -> "_gsxf0", etc. Every entry harvested from the same package
// shares that package's one alias (see filterEntry.alias / filterAliases),
// so the first match found while ranging over the table is authoritative;
// there is no need to check every entry once one is found.
func (t filterTable) aliasForPath(path string) (string, bool) {
	for _, e := range t {
		if e.pkgPath == path {
			return e.alias, true
		}
	}
	return "", false
}

// stdImportPath is the gsx built-in filter package. It is always available
// (GenerateDirs defaults to it) and keeps the reserved _gsxstd
// alias so std-only generation is byte-identical regardless of any added
// packages.
const stdImportPath = "github.com/gsxhq/gsx/std"

// stdAlias is the reserved import alias for the std filter package. Preserving
// it keeps std-only generated output unchanged across the multi-package feature.
const stdAlias = "_gsxstd"

// filterAliases assigns a reserved import alias to each filter package path in
// pkgPaths: stdImportPath → _gsxstd (preserved); every other package → _gsxf<i>
// where i is its position AMONG THE NON-STD packages (std does not consume an
// index). The result is deterministic and independent of where std sits in the
// list, so a given non-std package always gets the same alias for a fixed
// non-std ordering. Aliases use the reserved _gsx prefix, so they never collide
// with a user symbol.
func filterAliases(pkgPaths []string) map[string]string {
	aliases := map[string]string{}
	nonStd := 0
	for _, p := range pkgPaths {
		if _, seen := aliases[p]; seen {
			continue
		}
		if p == stdImportPath {
			aliases[p] = stdAlias
			continue
		}
		aliases[p] = fmt.Sprintf("_gsxf%d", nonStd)
		nonStd++
	}
	return aliases
}

// loadFilterTable type-checks the std filter package and harvests its exported
// funcs by contract. It is the single-package entry point retained for callers
// that only need std; loadFilterTableMulti is the general multi-package form.
func loadFilterTable(dir string) (filterTable, error) {
	return loadFilterTableMulti(dir, []string{stdImportPath}, nil)
}

// loadFilterTableMulti type-checks every filter package in pkgPaths (one
// packages.Load over all patterns) and harvests their exported funcs by contract
// into a name→entry table. The table is built LAST-WINS: pkgPaths are processed
// in order, so a later package's filter shadows an earlier same-named one. Each
// entry records its owning package's reserved alias (see filterAliases) and
// import path, so lowerPipe qualifies the call and the caller imports the package
// under the same alias. dir anchors the load against the module's go.mod (incl.
// any test replace directive).
func loadFilterTableMulti(dir string, pkgPaths []string, aliases []FilterAlias) (filterTable, error) {
	if len(pkgPaths) == 0 && len(aliases) == 0 {
		return filterTable{}, nil
	}
	harvested, err := harvestFilters(dir, pkgPaths, aliases)
	if err != nil {
		return nil, err
	}
	table := filterTable{}
	for name, entries := range harvested {
		// Last-wins: the LAST entry for a template-level name (latest package in
		// pkgPaths order) is the winner. harvestFilters preserves package order
		// AND, within a package, scope.Names() order, matching the original
		// in-place table overwrite.
		table[name] = entries[len(entries)-1]
	}
	return table, nil
}

// harvestFilters type-checks every filter package in pkgPaths (one
// packages.Load over all patterns) and harvests their exported funcs by
// contract into a name→entries map. Entries for a given template-level name are
// ordered by package (pkgPaths order) and, within a package, by scope name
// order — so the LAST entry is the last-wins winner and the earlier entries are
// the ones it shadows. Each entry records its owning package's reserved alias
// (see filterAliases) and import path. dir anchors the load against the
// module's go.mod (incl. any test replace directive).
//
// This is the single harvest seam shared by loadFilterTableMulti (winner-only
// table) and ResolveFilters (full table + shadows), so both see the exact same
// precedence.
func harvestFilters(dir string, pkgPaths []string, explicitAliases []FilterAlias) (map[string][]filterEntry, error) {
	// Each alias's PkgPath also needs an import alias and must be loaded so its
	// target func's signature can be classified. Fold the alias package paths
	// into the alias-assignment set (after the whole-package paths, so non-std
	// _gsxf<i> indices stay stable for the package paths) and the load set.
	aliasPaths := pkgPaths
	for _, a := range explicitAliases {
		aliasPaths = append(aliasPaths, a.PkgPath)
	}
	aliases := filterAliases(aliasPaths)

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes |
			packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, aliasPaths...)
	if err != nil {
		return nil, fmt.Errorf("codegen: load filter packages: %w", err)
	}
	// Index loaded packages by import path so we can harvest in pkgPaths order
	// (the last-wins precedence depends on order, which packages.Load does not
	// guarantee to preserve).
	byPath := map[string]*packages.Package{}
	for _, pkg := range pkgs {
		byPath[pkg.PkgPath] = pkg
	}

	harvested := map[string][]filterEntry{}
	for _, path := range pkgPaths {
		pkg, ok := byPath[path]
		if !ok {
			return nil, fmt.Errorf("codegen: filter package %q not found in %s", path, dir)
		}
		if len(pkg.Errors) > 0 {
			return nil, fmt.Errorf("codegen: filter package %q type resolution failed: %s", path, pkg.Errors[0])
		}
		if pkg.Types == nil {
			return nil, fmt.Errorf("codegen: filter package %q has no type information", path)
		}
		alias := aliases[path]
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			fn, ok := obj.(*types.Func)
			if !ok {
				continue
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok {
				continue
			}
			wantsCtx, ok := classifyFilter(sig)
			if !ok {
				// A non-conforming func (incl. the removed curried shape) is simply
				// skipped during whole-package harvest, as before.
				continue
			}
			tname := lowerFirst(name)
			harvested[tname] = append(harvested[tname], filterEntry{
				funcName: name,
				wantsCtx: wantsCtx,
				hasErr:   sig.Results().Len() == 2,
				alias:    alias,
				pkgPath:  path,
			})
		}
	}

	// Register explicit aliases AFTER whole-package harvest, in option order, so
	// an alias can intentionally override a harvested same-named filter (last-wins).
	for _, a := range explicitAliases {
		pkg, ok := byPath[a.PkgPath]
		if !ok {
			return nil, fmt.Errorf("codegen: WithFilter %q: package %q not found in %s", a.Name, a.PkgPath, dir)
		}
		if len(pkg.Errors) > 0 {
			return nil, fmt.Errorf("codegen: WithFilter %q: package %q type resolution failed: %s", a.Name, a.PkgPath, pkg.Errors[0])
		}
		if pkg.Types == nil {
			return nil, fmt.Errorf("codegen: WithFilter %q: package %q has no type information", a.Name, a.PkgPath)
		}
		obj := pkg.Types.Scope().Lookup(a.FuncName)
		if obj == nil {
			return nil, fmt.Errorf("codegen: WithFilter %q: func %q not found in package %q", a.Name, a.FuncName, a.PkgPath)
		}
		fn, ok := obj.(*types.Func)
		if !ok {
			return nil, fmt.Errorf("codegen: WithFilter %q: %q in package %q is not a function", a.Name, a.FuncName, a.PkgPath)
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			return nil, fmt.Errorf("codegen: WithFilter %q: %q in package %q has no signature", a.Name, a.FuncName, a.PkgPath)
		}
		wantsCtx, ok := classifyFilter(sig)
		if !ok {
			if isCurriedShape(sig) {
				return nil, fmt.Errorf("codegen: WithFilter %q: filter %q uses the removed curried shape func(args) func(T) R; rewrite as seed-first func([ctx,] subject, args...)", a.Name, a.FuncName)
			}
			return nil, fmt.Errorf("codegen: WithFilter %q: func %q does not match the seed-first filter contract func([ctx,] subject, args...) (R[, error])", a.Name, a.FuncName)
		}
		harvested[a.Name] = append(harvested[a.Name], filterEntry{
			funcName: a.FuncName,
			wantsCtx: wantsCtx,
			hasErr:   sig.Results().Len() == 2,
			alias:    aliases[a.PkgPath],
			pkgPath:  a.PkgPath,
		})
	}
	return harvested, nil
}

// FilterInfo describes one resolved pipeline filter, for `gsx info`.
type FilterInfo struct {
	Name    string   // template name (first-rune-lowered), e.g. "upper"
	Pkg     string   // winning package import path
	Func    string   // exported Go func name, e.g. "Upper"
	Ctx     bool     // first parameter is context.Context (gsx injects ambient ctx)
	Shadows []string // import paths of EARLIER same-named filters this one overrides
}

// ResolveFilters harvests the filter packages (in order, last-wins) plus the
// explicit WithFilter aliases (appended after, in option order) and returns the
// resolved table sorted by Name, recording which earlier same-named filters each
// winner shadows. An empty filterPkgs defaults to [stdImportPath], matching
// GenerateDirs. dir anchors the go/packages load against the
// module's go.mod.
func ResolveFilters(dir string, filterPkgs []string, aliases []FilterAlias) ([]FilterInfo, error) {
	filterPkgs = dedupFilterPkgs(filterPkgs)
	harvested, err := harvestFilters(dir, filterPkgs, aliases)
	if err != nil {
		return nil, err
	}
	infos := make([]FilterInfo, 0, len(harvested))
	for name, entries := range harvested {
		winner := entries[len(entries)-1]
		var shadows []string
		for _, e := range entries[:len(entries)-1] {
			shadows = append(shadows, e.pkgPath)
		}
		infos = append(infos, FilterInfo{
			Name:    name,
			Pkg:     winner.pkgPath,
			Func:    winner.funcName,
			Ctx:     winner.wantsCtx,
			Shadows: shadows,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// classifyFilter inspects a func signature against the seed-first filter
// contract. It returns whether the filter takes an ambient context.Context as
// its first parameter (wantsCtx) and whether the signature is a valid filter at
// all (ok).
//
// A valid seed-first filter:
//   - has no receiver;
//   - after an optional leading context.Context parameter, has at least one MORE
//     parameter (the subject). So wantsCtx requires Params().Len() >= 2 with
//     param0 being context.Context; a ctx-less filter needs Params().Len() >= 1;
//   - returns exactly 1 result, OR exactly 2 results whose second is error.
//
// The removed curried shape (a single result that is itself a unary func
// func(T) R) is explicitly rejected (ok=false); use isCurriedShape to detect it
// for a migration diagnostic.
func classifyFilter(sig *types.Signature) (wantsCtx bool, ok bool) {
	if sig.Recv() != nil {
		return false, false
	}
	// Reject the removed curried shape func(args) func(T) R outright, even though
	// it has a single (func) result — a returned unary func is never a valid
	// seed-first result.
	if isCurriedShape(sig) {
		return false, false
	}
	if !validFilterResults(sig.Results()) {
		return false, false
	}
	params := sig.Params()
	if params.Len() >= 1 && isContextContext(params.At(0).Type()) {
		// A ctx-taking filter needs at least one MORE param after ctx (the subject).
		if params.Len() >= 2 {
			return true, true
		}
		return false, false
	}
	if params.Len() >= 1 {
		return false, true
	}
	return false, false
}

// validFilterResults reports whether a filter's results match the contract:
// exactly 1 result, or exactly 2 whose second is error.
func validFilterResults(res *types.Tuple) bool {
	switch res.Len() {
	case 1:
		return true
	case 2:
		return isErrorType(res.At(1).Type())
	default:
		return false
	}
}

// isErrorType reports whether t is the builtin error interface.
func isErrorType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	return named.Obj().Pkg() == nil && named.Obj().Name() == "error"
}

// isContextContext reports whether t is exactly context.Context: a *types.Named
// whose object lives in the standard "context" package and is named "Context".
func isContextContext(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

// isCurriedShape reports whether sig is the removed curried filter shape
// func(args…) func(T) R: a single result that is itself a unary func. The
// explicit-alias (WithFilter) path uses this to emit a clear migration
// diagnostic instead of silently skipping a func the author intended as a filter.
func isCurriedShape(sig *types.Signature) bool {
	if sig.Results().Len() != 1 {
		return false
	}
	inner, ok := sig.Results().At(0).Type().(*types.Signature)
	if !ok {
		return false
	}
	if inner.Params().Len() != 1 {
		return false
	}
	// The old curried shape returned either `R` or `(R, error)`; recognize both
	// so the migration diagnostic fires for the error-returning variant too.
	switch inner.Results().Len() {
	case 1:
		return true
	case 2:
		return isErrorType(inner.Results().At(1).Type())
	default:
		return false
	}
}

// lowerFirst lowercases only the first rune of s ("Upper"→"upper",
// "Truncate"→"truncate"). Initialism-aware naming ("URLEncode"→"urlEncode"
// rather than "uRLEncode") is a known rough edge, deferred.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[size:]
}
