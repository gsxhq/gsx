// Package codegen lowers a parsed gsx AST to Go source (.x.go) targeting the
// gsx runtime.
//
// It handles components (inline params + receiver/method components), pass-through
// Go (GoChunks: types/helpers), static markup, control flow (if/for/switch,
// fragments), context-aware attributes (static/bool/expr, composable class, element
// spread, conditional), child-component invocation with props/{children}/named
// slots + explicit attribute forwarding, and type-aware interpolation resolved by go/types
// in the component's scope. Used params bind to same-named locals so interpolation
// expressions emit VERBATIM (e.g. {user.Name} -> gw.Text(user.Name) after
// `user := p.User`). A `(T, error)` value auto-unwraps (the error propagates out of
// Render). The `|>` pipeline lowers to nested filter calls in every interpolation
// context (text/attr/<style>/<script>/JS-attr). Escaping is context-aware
// (HTML/attr/URL/JS-JSON; CSS value-filtered) with typed gsx.Raw* opt-outs.
package codegen

// dedupFilterPkgs returns filterPkgs with duplicate import paths removed and the
// built-in std package guaranteed present as the FIRST (lowest-precedence)
// entry, so callers always have std available and a user package or alias can
// shadow an individual std filter by name (last-wins) without dropping the rest
// of std. First-seen order is preserved among the remaining packages.
func dedupFilterPkgs(filterPkgs []string) []string {
	seen := map[string]bool{stdImportPath: true}
	out := []string{stdImportPath}
	for _, p := range filterPkgs {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
