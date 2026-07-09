package codegen

import (
	goast "go/ast"
	"go/token"
	"go/types"
)

// missingFromSkeletons finds, per .gsx file, every qualifier used in a selector
// expression that go/types could not resolve to anything.
//
// The test is exact, not a heuristic: go/types records an object in info.Uses
// for an imported package (a *types.PkgName), a local variable, or any other
// resolved reference. A selector root is always a reference occurrence (never
// a declaration site, so info.Defs is not relevant here), so an identifier at
// the root of a selector with no entry in info.Uses resolved to nothing — it
// is an undefined qualifier, which is what a missing import looks like. The
// alternative, scraping "undefined: fmt" out of type-error message text, is a
// heuristic and is not used.
//
// Positions are reported in the .gsx source: the skeleton carries //line
// directives, so gsxFset maps a skeleton position back to its .gsx origin, the
// same way diagnostics already do.
//
// analyze deliberately emits a SECOND copy of a component child-prop
// expression, under its own //line stamp, as an inference-harvest probe (see
// infer.go's inferRegistry doc) — so one source-level qualifier can appear
// TWICE in the skeleton: once at its real site (the props literal / gsx.Attrs
// assignment), once inside a _gsxuseq(...) probe call. The type-error loop
// (module_importer.go) suppresses type errors landing inside a probe span
// because "the props literal reports it" — so the ONE diagnostic the user
// actually sees anchors at the props-literal copy, never the probe copy.
// MissingImport.Pos is documented to mirror that diagnostic, so this function
// must skip the same probe spans, computed by the same harvestProbeSpans
// helper the type-error loop's quietSpans is built from
// (module_importer.go). This is NOT the same thing as probeSiteForError:
// that reports membership in an inferRegistry TYPE-INFERENCE span, which for
// a GENERIC tag covers the props-literal occurrence (it is the one
// participating in inference) — the occurrence we must KEEP — so filtering
// on it used to drop the wrong copy for generics while accidentally working
// for plain tags (where it never matched anything, and the (Name, Symbol)
// dedupe below did all the work by keeping whichever copy ast.Inspect visited
// first).
//
// A second layer of dedupe collapses by (Name, Symbol) per file: two
// GENUINE uses of the same qualifier+symbol (e.g. fmt.Sprint on two
// different lines, neither inside a probe) still collapse to one entry,
// because the caller (organizeImports / the quickfix) adds one import either
// way — see add_imports_test.go's TestMissingImportsRepeatedGenuineUsesCollapse.
// With the probe copies now filtered out up front, the surviving Pos for a
// deduped (Name, Symbol) is always the FIRST non-probe occurrence in
// ast.Inspect order — for the child-prop case, that is the props-literal
// occurrence, matching the diagnostic.
//
// Pure: walks ASTs analyze already parsed. No IO, no lock, no packages.Load, no
// importer call. spansByFile is computed once per file by analyze (shared with
// the type-error loop's quietSpans) and passed in, so this function itself does
// no AST walk of its own beyond the SelectorExpr inspection below; per ident,
// inHarvestProbe scans that file's span slice, so the cost is O(idents × spans)
// — spans are few (one per child-prop/spread probe), so this stays cheap. Safe
// on the Package() hot path.
func missingFromSkeletons(byGsx map[string]fileSkeleton, gsxFset *token.FileSet, info *types.Info, spansByFile map[*goast.File][]posSpan) map[string][]MissingImport {
	if info == nil {
		return nil
	}
	out := map[string][]MissingImport{}
	for gsxPath, fs := range byGsx {
		probeSpans := spansByFile[fs.skel]
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
			// A selector root (se.X) is always a reference occurrence, never a
			// declaration site, so only info.Uses (checked above) can resolve it.
			if inHarvestProbe(probeSpans, id.Pos()) {
				return true // _gsxuseq harvest-probe copy of a child-prop expr; the props-literal occurrence is reported instead
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

// inHarvestProbe reports whether pos falls inside one of spans (each a
// harvestProbeSpans result for the same file/fset — see that function's doc).
func inHarvestProbe(spans []posSpan, pos token.Pos) bool {
	for _, s := range spans {
		if s.start <= pos && pos < s.end {
			return true
		}
	}
	return false
}
