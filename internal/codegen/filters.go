package codegen

import (
	"fmt"
	"go/types"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/ast"
)

// lowerPipe lowers a pipeline (a seed expression plus left-to-right stages) to a
// nested Go expression string of qualified std calls: `{ x |> a |> b(n) }`
// becomes `_gsxstd.B(n)(_gsxstd.A((x)))`. The SAME string is used for the type
// probe (analyze.go) and the emitted render (emit.go), so type resolution and
// emission stay aligned (the order invariant). usesStd reports whether any std
// call was emitted, so the caller adds the _gsxstd import.
//
// Stage classification uses the parsed HasArgs flag (parens present) for arity
// checks against the filter's harvested kind: a bare filter must have no parens,
// a parameterized filter must have parens. Per-stage `?` (Try) is deferred and
// errors.
func lowerPipe(seed string, stages []ast.PipeStage, table filterTable) (expr string, usesStd bool, err error) {
	acc := "(" + strings.TrimSpace(seed) + ")"
	for _, st := range stages {
		if st.Try {
			return "", false, fmt.Errorf("codegen: `?` try-marker on filter %q not supported yet", st.Name)
		}
		e, ok := table.lookup(st.Name)
		if !ok {
			return "", false, fmt.Errorf("codegen: unknown filter %q", st.Name)
		}
		switch e.kind {
		case filterBare:
			if st.HasArgs {
				return "", false, fmt.Errorf("codegen: filter %q takes no arguments", st.Name)
			}
			acc = "_gsxstd." + e.funcName + "(" + acc + ")"
		case filterParam:
			if !st.HasArgs {
				return "", false, fmt.Errorf("codegen: filter %q requires arguments", st.Name)
			}
			acc = "_gsxstd." + e.funcName + "(" + st.Args + ")(" + acc + ")"
		}
	}
	return acc, true, nil
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

// filterEntry is one harvested filter. funcName is the exported Go name in the
// std package (e.g. "Upper"); the caller qualifies it as _gsxstd.<funcName>.
type filterEntry struct {
	funcName string
	kind     filterKind
}

// filterTable maps a template-level filter name to its harvested entry. The
// template name is the std func name with its first rune lowercased.
type filterTable map[string]filterEntry

// lookup returns the entry for a template-level filter name.
func (t filterTable) lookup(name string) (filterEntry, bool) {
	e, ok := t[name]
	return e, ok
}

// stdImportPath is the fixed filter package the resolver harvests. The wider
// extensibility seam (user filter packages, precedence) is deferred.
const stdImportPath = "github.com/gsxhq/gsx/std"

// loadFilterTable type-checks the std package and harvests its exported funcs
// by contract into a name→func table. dir anchors the package load against the
// module's go.mod (incl. any test replace directive), mirroring the
// packages.Config style of resolveTypesPkg.
func loadFilterTable(dir string) (filterTable, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes |
			packages.NeedImports | packages.NeedDeps,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, stdImportPath)
	if err != nil {
		return nil, fmt.Errorf("codegen: load std package: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("codegen: no std package found in %s", dir)
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return nil, fmt.Errorf("codegen: std type resolution failed: %s", pkg.Errors[0])
	}

	table := filterTable{}
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
		table[lowerFirst(name)] = filterEntry{funcName: name, kind: kind}
	}
	return table, nil
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
