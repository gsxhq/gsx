package codegen

import (
	"go/token"
	"go/types"
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// buildCrossNav builds a package's component cross-index (componentKey ->
// CrossRef with .gsx Decl + in-package Refs) and the navigable-reference index.
// gsxFset resolves .gsx declaration positions; skelFset (the skeleton fset)
// resolves use positions (//line-mapped back to .gsx for skeleton refs, real
// .go for hand-written refs). Shared by the Module core (both codegen.GenerateDirs
// and the LSP path) to produce identical indexes.
func buildCrossNav(
	compByKey map[string][]*gsxast.Component,
	objKey map[types.Object]string,
	gsxFset, skelFset *token.FileSet,
	info *types.Info,
) (map[string]CrossRef, []NavRef) {
	// Sort each variant slice in place so every consumer below (Decls and the
	// info.Uses nav target) agrees on the
	// same deterministic "primary" variant, rather than following randomized
	// map-population order. Safe to mutate in place: these slices belong to
	// the freshly-built analyzed result, and the only other reader
	// (module_importer.go's compByKey existence check) doesn't care about
	// order.
	for _, comps := range compByKey {
		sortComponents(comps, gsxFset)
	}

	index := map[string]CrossRef{}
	for key, comps := range compByKey {
		if len(comps) == 0 {
			continue
		}
		cr := CrossRef{Name: comps[0].Name}
		for _, c := range comps {
			cr.Decls = append(cr.Decls, gsxFset.Position(c.NamePos)) // gsx fset → .gsx position
		}
		// cr.Decls is already ordered here: comps was sorted in place above, so no
		// separate sortPositions(cr.Decls) call is needed (and would be redundant).
		cr.Decl = cr.Decls[0] // primary — back-compat
		index[key] = cr
	}

	var navIndex []NavRef
	for id, obj := range info.Uses {
		p := skelFset.Position(id.Pos())
		if strings.HasSuffix(p.Filename, ".x.go") {
			continue // synthetic skeleton position with no //line — skip
		}
		// Case 1: component func reference → .gsx component decl.
		if key, ok := objKey[obj]; ok {
			comps := compByKey[key]
			if len(comps) == 0 {
				continue
			}
			c := comps[0] // deterministic primary variant (sorted above); any variant's NamePos is a valid jump target
			cr := index[key]
			cr.Refs = append(cr.Refs, p)
			index[key] = cr
			navIndex = append(navIndex, NavRef{
				From: p,
				Name: id.Name,
				To:   gsxFset.Position(c.NamePos),
			})
			continue
		}
	}
	return index, navIndex
}

// addLocalComponentCallRefs adds authored markup calls to their exact logical
// component family. Go references remain sourced from types.Info in
// buildCrossNav; markup references come only from ComponentCalls so no
// generated ABI shape participates in navigation.
func addLocalComponentCallRefs(index map[string]CrossRef, calls map[*gsxast.Element]ComponentCallFact, fset *token.FileSet, packagePath string) {
	for element, call := range calls {
		if element == nil || call.TargetPackage != packagePath || call.TargetKey == "" || !element.TagPos.IsValid() {
			continue
		}
		ref, ok := index[call.TargetKey]
		if !ok {
			continue
		}
		ref.Refs = append(ref.Refs, fset.Position(element.TagPos))
		index[call.TargetKey] = ref
	}
}

// sortComponents sorts a compByKey[key] variant slice in place, deterministically,
// by its NamePos resolved through gsxFset: filename then byte offset. All
// consumers of compByKey (CrossRef.Decls/Decl and the info.Uses nav target)
// must agree on this order so the chosen
// "primary" variant is stable across runs, independent of Go's randomized
// map-population order.
func sortComponents(comps []*gsxast.Component, gsxFset *token.FileSet) {
	posOf := func(c *gsxast.Component) token.Position { return gsxFset.Position(c.NamePos) }
	sort.Slice(comps, func(i, j int) bool {
		pi, pj := posOf(comps[i]), posOf(comps[j])
		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}
		return pi.Offset < pj.Offset
	})
}
