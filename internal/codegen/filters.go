package codegen

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/ast"
)

// lowerPipe lowers a pipeline (a seed expression plus left-to-right stages) to a
// nested Go expression string of qualified filter calls: `{ x |> a |> b(n) }`
// becomes `<alias>.B(n)(<alias>.A((x)))`, where <alias> is each filter's OWNING
// package alias (e.g. _gsxstd for std, _gsxf0 for the first user package). The
// SAME string is used for the type probe (analyze.go) and the emitted render
// (emit.go), so type resolution and emission stay aligned (the order invariant).
//
// usedPkgs reports WHICH filter packages the lowered expression references, as a
// map alias→pkgPath, so the caller imports exactly those packages under exactly
// those aliases — the probe (skeleton) and the emit drive their import blocks
// from this SAME set, keeping resolution and emission in lockstep.
//
// Stage classification uses the parsed HasArgs flag (parens present) for arity
// checks against the filter's harvested kind: a bare filter must have no parens,
// a parameterized filter must have parens. Per-stage `?` (Try) is deferred and
// errors.
func lowerPipe(seed string, stages []ast.PipeStage, table filterTable) (expr string, usedPkgs map[string]string, err error) {
	acc := "(" + strings.TrimSpace(seed) + ")"
	usedPkgs = map[string]string{}
	for _, st := range stages {
		if st.Try {
			return "", nil, fmt.Errorf("codegen: `?` try-marker on filter %q not supported yet", st.Name)
		}
		e, ok := table.lookup(st.Name)
		if !ok {
			return "", nil, fmt.Errorf("codegen: unknown filter %q", st.Name)
		}
		usedPkgs[e.alias] = e.pkgPath
		switch e.kind {
		case filterBare:
			if st.HasArgs {
				return "", nil, fmt.Errorf("codegen: filter %q takes no arguments", st.Name)
			}
			acc = e.alias + "." + e.funcName + "(" + acc + ")"
		case filterParam:
			if !st.HasArgs {
				return "", nil, fmt.Errorf("codegen: filter %q requires arguments", st.Name)
			}
			acc = e.alias + "." + e.funcName + "(" + st.Args + ")(" + acc + ")"
		}
	}
	return acc, usedPkgs, nil
}

// filterKind distinguishes the two filter contract shapes harvested from std.
type filterKind int

const (
	// filterBare is a func(T) R — applied directly: _gsxstd.Upper(x).
	filterBare filterKind = iota
	// filterParam is a func(Args...) func(T) R — the outer call supplies the
	// filter arguments, the returned unary func is then applied:
	// _gsxstd.Truncate(5)(x).
	filterParam
)

// filterEntry is one harvested filter. funcName is the exported Go name in its
// owning package (e.g. "Upper"); alias is that package's reserved import alias
// (the caller qualifies the call as <alias>.<funcName>); pkgPath is the package's
// import path (so the caller can emit `<alias> "<pkgPath>"`).
type filterEntry struct {
	funcName string
	kind     filterKind
	alias    string
	pkgPath  string
}

// filterTable maps a template-level filter name to its harvested entry. The
// template name is the std func name with its first rune lowercased.
type filterTable map[string]filterEntry

// lookup returns the entry for a template-level filter name.
func (t filterTable) lookup(name string) (filterEntry, bool) {
	e, ok := t[name]
	return e, ok
}

// stdImportPath is the gsx built-in filter package. It is always available
// (GeneratePackageWithFilters defaults to it) and keeps the reserved _gsxstd
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
	return loadFilterTableMulti(dir, []string{stdImportPath})
}

// loadFilterTableMulti type-checks every filter package in pkgPaths (one
// packages.Load over all patterns) and harvests their exported funcs by contract
// into a name→entry table. The table is built LAST-WINS: pkgPaths are processed
// in order, so a later package's filter shadows an earlier same-named one. Each
// entry records its owning package's reserved alias (see filterAliases) and
// import path, so lowerPipe qualifies the call and the caller imports the package
// under the same alias. dir anchors the load against the module's go.mod (incl.
// any test replace directive), mirroring resolveTypesPkg.
func loadFilterTableMulti(dir string, pkgPaths []string) (filterTable, error) {
	if len(pkgPaths) == 0 {
		return filterTable{}, nil
	}
	harvested, err := harvestFilters(dir, pkgPaths)
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
// module's go.mod (incl. any test replace directive), mirroring resolveTypesPkg.
//
// This is the single harvest seam shared by loadFilterTableMulti (winner-only
// table) and ResolveFilters (full table + shadows), so both see the exact same
// precedence.
func harvestFilters(dir string, pkgPaths []string) (map[string][]filterEntry, error) {
	aliases := filterAliases(pkgPaths)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes |
			packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, pkgPaths...)
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
			kind, ok := classifyFilter(sig)
			if !ok {
				continue
			}
			tname := lowerFirst(name)
			harvested[tname] = append(harvested[tname], filterEntry{
				funcName: name,
				kind:     kind,
				alias:    alias,
				pkgPath:  path,
			})
		}
	}
	return harvested, nil
}

// FilterInfo describes one resolved pipeline filter, for `gsx info`.
type FilterInfo struct {
	Name    string   // template name (first-rune-lowered), e.g. "upper"
	Pkg     string   // winning package import path
	Func    string   // exported Go func name, e.g. "Upper"
	Param   bool     // parameterized func(Args...) func(T) R; else bare func(T) R
	Shadows []string // import paths of EARLIER same-named filters this one overrides
}

// ResolveFilters harvests the filter packages (in order, last-wins) and returns
// the resolved table sorted by Name, recording which earlier same-named filters
// each winner shadows. An empty filterPkgs defaults to [stdImportPath], matching
// GeneratePackageWithFilters. dir anchors the go/packages load against the
// module's go.mod.
func ResolveFilters(dir string, filterPkgs []string) ([]FilterInfo, error) {
	filterPkgs = dedupFilterPkgs(filterPkgs)
	harvested, err := harvestFilters(dir, filterPkgs)
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
			Param:   winner.kind == filterParam,
			Shadows: shadows,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// classifyFilter inspects a func signature against the filter contract. It
// returns the kind and whether the signature is a filter at all.
//
// The param shape is checked first: a single result that is itself a unary
// func is parameterized. Otherwise a 1-param/1-result func is bare. Anything
// else (a receiver, wrong arity, or a result func that is not unary) is not a
// filter.
func classifyFilter(sig *types.Signature) (filterKind, bool) {
	if sig.Recv() != nil {
		return 0, false
	}
	// param: exactly one result whose type is a unary func.
	if sig.Results().Len() == 1 {
		if inner, ok := sig.Results().At(0).Type().(*types.Signature); ok {
			if inner.Params().Len() == 1 && inner.Results().Len() == 1 {
				return filterParam, true
			}
		}
	}
	// bare: exactly one param and one result, not the param case above.
	if sig.Params().Len() == 1 && sig.Results().Len() == 1 {
		return filterBare, true
	}
	return 0, false
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
