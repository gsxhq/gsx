// Package stdpath decides whether an import path is importable from ordinary
// user code, applying Go's own `internal`/`vendor` path rules rather than an
// approximation of them.
//
// It exists as its own leaf package (rather than living in package codegen or
// in mkstdlibindex) so that all sides that must agree on the rule — the
// stdlib-table generator (mkstdlibindex, package main), the table-freshness
// test (package codegen), and the dependency-graph candidate filter
// (add_imports.go, also package codegen) — share exactly one definition
// without the generator depending on the whole codegen package (packages.Load,
// the LSP machinery, etc. — irrelevant to a throwaway go:generate tool) or
// codegen depending on a generator nested under its own tree.
package stdpath

import "strings"

// Importable reports whether path is importable from user code.
//
// `internal` is a Go visibility rule on a path COMPONENT: a package is
// importable only by code rooted at the parent of the "internal" segment, so
// "net/http/internal" is never importable from outside net/http's tree
// regardless of where "internal" falls in the path. A substring test on
// "internal/" (the historical bug here) only catches an "internal" segment
// that has something AFTER it — it misses a path whose LAST component is
// "internal" (e.g. "encoding/json/internal"), silently admitting four
// unimportable std packages under the collided name "internal".
//
// `vendor/...` paths are not real packages (go list std surfaces them for
// std, but nothing may import them), so they are excluded too.
//
// This is a path-shape rule, not a stdlib-specific one, so callers also use it
// to filter the module's dependency graph (add_imports.go's
// depGraphPackages): a `false` there is always correct for a package outside
// the module (nothing external can ever be "rooted at the parent" of a
// GOROOT-internal directory), and conservative-but-safe for a same-module
// `internal` package, since the caller has no per-call knowledge of which
// package is asking.
func Importable(path string) bool {
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "internal" {
			return false
		}
	}
	return !strings.HasPrefix(path, "vendor/")
}
