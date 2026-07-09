package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
)

// missingFromSkeletons finds, per .gsx file, every qualifier used in a selector
// expression that go/types could not resolve to anything.
//
// The test is exact, not a heuristic: go/types records an object in info.Uses for
// an imported package (a *types.PkgName) and for a local variable, and in
// info.Defs for a declaration. An identifier at the root of a selector with NO
// entry in either map resolved to nothing — it is an undefined qualifier, which
// is what a missing import looks like. The alternative, scraping "undefined: fmt"
// out of type-error message text, is a heuristic and is not used.
//
// Positions are reported in the .gsx source: the skeleton carries //line
// directives, so gsxFset maps a skeleton position back to its .gsx origin, the
// same way diagnostics already do.
//
// analyze deliberately emits a SECOND copy of a component child-prop
// expression, under its own //line stamp, as an inference-harvest probe (see
// infer.go's inferRegistry doc) — so one source-level qualifier can appear
// TWICE in the skeleton: once at its real site, once inside the probe. The
// probe copy is identified the same way the type-error loop already
// disambiguates a probe-landed error (module_importer.go's probeSiteForError,
// called from analyze's type-error loop) — never by guessing at column
// numbers or scanning skeleton text. inferByXGo is keyed by the skeleton's
// absolute .x.go path; a nil registry entry (no probes recorded for that
// file) simply never matches, so every real file still gets reported.
//
// A second layer of dedupe collapses by (Name, Symbol) per file: two
// GENUINE uses of the same qualifier+symbol (e.g. fmt.Sprint on two
// different lines, neither inside a probe) still collapse to one entry,
// because the caller (organizeImports / the quickfix) adds one import either
// way — see add_imports_test.go's TestMissingImportsRepeatedGenuineUsesCollapse.
//
// Pure: walks ASTs analyze already parsed. No IO, no lock, no packages.Load, no
// importer call. probeSiteForError is a pure map/offset lookup. Safe on the
// Package() hot path.
func missingFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, info *types.Info, inferByXGo map[string]*inferRegistry) map[string][]MissingImport {
	if info == nil {
		return nil
	}
	out := map[string][]MissingImport{}
	for gsxPath, fs := range byGsx {
		var found []MissingImport
		seen := map[string]bool{} // name+symbol: one report per distinct qualifier+symbol per file
		goast.Inspect(fs.skel, func(n goast.Node) bool {
			se, ok := n.(*goast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := se.X.(*goast.Ident)
			if !ok {
				return true
			}
			if _, used := info.Uses[id]; used {
				return true // an imported package, a local, a field...
			}
			if _, defined := info.Defs[id]; defined {
				return true
			}
			if _, _, ok := probeSiteForError(inferByXGo, gsxFset, id.Pos()); ok {
				return true // inference-harvest probe copy of a child-prop expr; the real source occurrence is reported on its own
			}
			key := id.Name + "." + se.Sel.Name
			if seen[key] {
				return true
			}
			seen[key] = true
			found = append(found, MissingImport{Name: id.Name, Symbol: se.Sel.Name, Pos: gsxFset.Position(id.Pos())})
			return true
		})
		if len(found) > 0 {
			out[gsxPath] = found
		}
	}
	return out
}
