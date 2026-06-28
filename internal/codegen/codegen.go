// Package codegen lowers a parsed gsx AST to Go source (.x.go) targeting the
// gsx runtime.
//
// It handles components (inline params + receiver/method components), pass-through
// Go (GoChunks: types/helpers), static markup, control flow (if/for/switch,
// fragments), context-aware attributes (static/bool/expr, composable class, element
// spread, conditional), child-component invocation with props/{children}/named
// slots + attribute fallthrough, and type-aware interpolation resolved by go/types
// in the component's scope. Used params bind to same-named locals so interpolation
// expressions emit VERBATIM (e.g. {user.Name} -> gw.Text(user.Name) after
// `user := p.User`). A `(T, error)` value auto-unwraps (the error propagates out of
// Render). The `|>` pipeline lowers to nested filter calls in every interpolation
// context (text/attr/<style>/<script>/JS-attr). Escaping is context-aware
// (HTML/attr/URL/JS-JSON; CSS value-filtered) with typed gsx.Raw* opt-outs.
package codegen

// dedupFilterPkgs returns filterPkgs with duplicate import paths removed,
// preserving first-seen order. An empty (or nil) list defaults to just the gsx
// built-in std filter package, so callers always get std available.
func dedupFilterPkgs(filterPkgs []string) []string {
	if len(filterPkgs) == 0 {
		return []string{stdImportPath}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(filterPkgs))
	for _, p := range filterPkgs {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
