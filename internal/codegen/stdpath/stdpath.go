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

// internalPrefix reports the path prefix rooted at the parent of path's first
// "internal" path component, and whether path has one at all. The prefix is
// the segments before "internal" joined back with "/" (empty when "internal"
// is path's own first segment). E.g.:
//
//	"bytes"             -> ("",              false)
//	"internal/foo"      -> ("",              true)
//	"a/internal/b"      -> ("a",             true)
//	"a/b/internal/c/d"  -> ("a/b",           true)
//
// This is the one place both InternalVisible and Importable read "does path
// have an internal component, and where" from — the visibility rule itself
// (a component test, not a substring test: "encoding/json/internal" has its
// LAST component equal to "internal", which `strings.Contains(path,
// "internal/")` — the historical bug here — does not catch) is defined once.
func internalPrefix(path string) (prefix string, hasInternal bool) {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if seg == "internal" {
			return strings.Join(segs[:i], "/"), true
		}
	}
	return "", false
}

// InternalVisible reports whether importPath may be imported by a package
// whose import path is importerPath, per Go's internal-visibility rule: a
// path with an "internal" component is importable only by code in the
// directory tree rooted at that component's parent. A path with no
// "internal" component is always visible. A `vendor/...` path is never
// visible (not a real package — `go list std` surfaces vendored deps, but
// nothing may import them).
//
// One rule covers both the standard library and every other module: for
// candidate "encoding/json/internal", the prefix is "encoding/json" — no
// importer outside GOROOT is ever under that tree, so it is excluded
// automatically, with no std-specific case. For candidate
// "example.com/u/internal/db", the prefix is "example.com/u" — an importer at
// "example.com/u" or "example.com/u/views" (which has that prefix + "/") IS
// under it, so it is visible from there.
func InternalVisible(importPath, importerPath string) bool {
	if strings.HasPrefix(importPath, "vendor/") {
		return false
	}
	prefix, hasInternal := internalPrefix(importPath)
	if !hasInternal {
		return true
	}
	return importerPath == prefix || strings.HasPrefix(importerPath, prefix+"/")
}

// Importable reports whether path could ever be imported by ANY code outside
// the tree that "internal" itself protects — i.e. path has no "internal"
// component at all (and is not a `vendor/...` path). Used only where no
// importer context exists: mkstdlibindex builds ONE global name -> path
// table with no per-call caller, so it cannot ask InternalVisible's
// per-importer question and instead asks the narrower, importer-free one
// that is still correct for its purpose — a std path with an "internal"
// component is never importable by user code (nothing outside GOROOT is ever
// "rooted at" a GOROOT-internal parent), so excluding it unconditionally from
// the baked table is always right. The resolver (add_imports.go), which DOES
// have importer context, uses InternalVisible instead so it can still offer a
// project's own internal/... packages.
func Importable(path string) bool {
	_, hasInternal := internalPrefix(path)
	return !hasInternal && !strings.HasPrefix(path, "vendor/")
}
