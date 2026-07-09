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
// Pure: walks ASTs analyze already parsed. No IO, no lock, no packages.Load, no
// importer call. Safe on the Package() hot path.
func missingFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, info *types.Info) map[string][]MissingImport {
	if info == nil {
		return nil
	}
	out := map[string][]MissingImport{}
	for gsxPath, fs := range byGsx {
		var found []MissingImport
		seen := map[string]bool{} // name+symbol+line: one report per distinct site
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
			pos := gsxFset.Position(id.Pos())
			key := id.Name + "." + se.Sel.Name + ":" + pos.String()
			if seen[key] {
				return true
			}
			seen[key] = true
			found = append(found, MissingImport{Name: id.Name, Symbol: se.Sel.Name, Pos: pos})
			return true
		})
		if len(found) > 0 {
			out[gsxPath] = found
		}
	}
	return out
}
