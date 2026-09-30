package codegen

import (
	"fmt"
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

// probeScope is everything emitProbes threads into a nested probe: a split Go
// expression's element/fragment IIFEs recurse into emitProbes, and its literal
// IIFEs into probeEmbeddedInterpIIFE, both against the enclosing component's
// scope.
type probeScope struct {
	table               funcTables
	recvVar             string
	recvTypeName        string
	usedFilters         map[string]string
	fset                *token.FileSet
	ctrlOff             map[gsxast.Node]int
	targetRegistry      *componentTargetMarkerRegistry
	gw                  *[][]gsxast.Markup
	bag                 *diag.Bag
	cfTemp              *int
	enclosingAttrsBound bool
}

// writeProbeGoParts writes parts as one Go expression into eb: GoText
// through writeSkeletonAuthoredAt, elements/fragments as _gsxelem(N)
// IIFEs, literals via probeEmbeddedInterpIIFE.
//
// Each GoText run is re-anchored with a block-form //line so positions keep
// mapping to .gsx source once an IIFE splice shifts byte offsets. An element's
// or fragment's gw index is reserved BEFORE probing its children, so nested
// embedded constructs take later indices and harvestEmbeddedElements resolves
// them off the shared gw slice. The element's own interps are probed inside its
// IIFE so they resolve against the enclosing component scope, matching emit's
// closure capture. A literal's IIFE returns the exact static type emit's
// lowering produces (f` → string, js` → RawJS, css` → RawCSS), so the
// surrounding Go type-checks against it (emit ≡ probe).
func writeProbeGoParts(eb skeletonWriter, parts []gsxast.GoPart, ps probeScope) error {
	for _, part := range parts {
		switch p := part.(type) {
		case gsxast.GoText:
			emitSkeletonBlockLine(eb, ps.fset, p.Pos())
			if err := writeSkeletonAuthoredAt(eb, ps.fset, p.Pos(), p.Src, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
				return err
			}
		case *gsxast.Element:
			if err := writeProbeElementIIFE(eb, []gsxast.Markup{p}, ps); err != nil {
				return err
			}
		case *gsxast.Fragment:
			if err := writeProbeElementIIFE(eb, p.Children, ps); err != nil {
				return err
			}
		case *gsxast.EmbeddedInterp:
			if len(p.Stages) > 0 {
				return fmt.Errorf("codegen: whole-literal pipelines on a Go-expression backtick literal are not supported")
			}
			if err := probeEmbeddedInterpIIFE(eb, p.Segments, p.Lang, ps.table, ps.recvVar, ps.recvTypeName, ps.usedFilters, ps.fset, ps.ctrlOff, ps.targetRegistry, ps.gw, ps.bag, ps.cfTemp); err != nil {
				return err
			}
		default:
			return fmt.Errorf("codegen: unsupported embedded interpolation part %T", part)
		}
	}
	return nil
}

// writeProbeElementIIFE writes the tagged `func() _gsxrt.Node { _gsxelem(N); … }()`
// probe for an element (markup = the element) or fragment (markup = its
// children), registering markup at gw index N first.
func writeProbeElementIIFE(eb skeletonWriter, markup []gsxast.Markup, ps probeScope) error {
	idx := len(*ps.gw)
	*ps.gw = append(*ps.gw, markup)
	eb.WriteString("func() _gsxrt.Node {\n")
	fmt.Fprintf(eb, "_gsxelem(%d)\n", idx)
	eb.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
	if err := emitProbes(eb, markup, ps.table, ps.recvVar, ps.recvTypeName, ps.usedFilters, ps.fset, ps.ctrlOff, ps.targetRegistry, ps.gw, ps.bag, ps.cfTemp, ps.enclosingAttrsBound); err != nil {
		return err
	}
	eb.WriteString("return nil\n}()")
	return nil
}

// writeEmbeddedProbe writes `parts |> stages` to sb with the mapped-seed and
// target-marker boundary handling the Interp case uses today.
//
// The parts are assembled in a child writer, then run through stages via the
// SAME probeExpr / lowerPipe path a plain expression uses, so the harvested
// type is the post-pipe type (emit lowers the pipeline over the spliced seed
// too). Because the pipeline rewrite is textual, the child's source-map
// segments and any component-target markers registered while writing the parts
// are carried across it with NUL-delimited boundaries and re-based onto sb.
// Only the probe expression is written: the caller owns the surrounding
// `_gsxuse(` … `)` (or other consuming form) and line anchor.
func writeEmbeddedProbe(sb skeletonWriter, parts []gsxast.GoPart, stages []gsxast.PipeStage, owner gsxast.Node, ps probeScope) error {
	targetMarkerStart := 0
	if ps.targetRegistry != nil {
		targetMarkerStart = len(ps.targetRegistry.ordered)
	}
	eb := newSkeletonWriterChild(sb)
	if err := writeProbeGoParts(eb, parts, ps); err != nil {
		return err
	}
	seed := eb.String()
	var mappedBoundary componentTargetSeedBoundary
	hasMappedChild := false
	if mapped, ok := eb.(*skeletonSourceWriter); ok && mapped.enabled && (len(mapped.segments) != 0 || len(mapped.regions) != 0) {
		mappedBoundary = componentTargetSeedBoundary{open: "\x00gsx-mapped-seed-open\x00", close: "\x00gsx-mapped-seed-close\x00"}
		seed = mappedBoundary.open + seed + mappedBoundary.close
		hasMappedChild = true
	}
	var boundary componentTargetSeedBoundary
	hasTargetMarkers := ps.targetRegistry != nil && len(ps.targetRegistry.ordered) > targetMarkerStart
	if hasTargetMarkers {
		seed, boundary = markComponentTargetSeed(ps.targetRegistry.ordered[targetMarkerStart].site, seed)
	}
	probe, err := probeExpr(seed, stages, ps.table, ps.usedFilters, owner, ps.bag)
	if err != nil {
		return err
	}
	seedOffset := 0
	if hasTargetMarkers {
		probe, seedOffset, err = unmarkComponentTargetSeed(probe, boundary)
		if err != nil {
			return err
		}
	}
	if hasMappedChild {
		probe, seedOffset, err = unmarkComponentTargetSeed(probe, mappedBoundary)
		if err != nil {
			return err
		}
	}
	probeStart := sb.Len()
	if hasMappedChild {
		writeSkeletonGenerated(sb, probe[:seedOffset])
		if err := appendSkeletonWriter(sb, eb); err != nil {
			return err
		}
		writeSkeletonGenerated(sb, probe[seedOffset+len(eb.String()):])
	} else {
		writeSkeletonGenerated(sb, probe)
	}
	if hasTargetMarkers {
		ps.targetRegistry.adjustFrom(targetMarkerStart, probeStart+seedOffset)
	}
	return nil
}

// writeFieldProbe writes the probe expression of one Go-expression field (no
// surrounding wrapper): its split overlay through writeEmbeddedProbe when the
// analysis split found a nested construct, else the verbatim text through
// writeSkeletonProbeExpr.
func (ps probeScope) writeFieldProbe(sb skeletonWriter, pos token.Pos, src string, embedded []gsxast.GoPart, stages []gsxast.PipeStage, owner gsxast.Node) error {
	if embedded != nil {
		return writeEmbeddedProbe(sb, embedded, stages, owner, ps)
	}
	return writeSkeletonProbeExpr(sb, ps.fset, pos, src, stages, ps.table, ps.usedFilters, owner, ps.bag)
}

// writeCanonicalFieldProbe writes `helper(<field probe>)\n` for one
// Go-expression field.
func (ps probeScope) writeCanonicalFieldProbe(sb skeletonWriter, helper string, pos token.Pos, src string, embedded []gsxast.GoPart, stages []gsxast.PipeStage, owner gsxast.Node) error {
	writeSkeletonGenerated(sb, helper+"(")
	if err := ps.writeFieldProbe(sb, pos, src, embedded, stages, owner); err != nil {
		return err
	}
	writeSkeletonGenerated(sb, ")\n")
	return nil
}

// embeddedStandInSeed returns parts as Go text with every nested construct
// replaced by a zero value of the exact static type its probe IIFE has
// (string, RawJS, RawCSS, or Node — embeddedProbeType / writeProbeElementIIFE).
// The result has the same static type as the full probe but registers nothing:
// it serves a second, harvest-only reference to a field whose full probe (with
// its IIFEs, gw entries and component-target bindings) is written exactly once
// elsewhere.
func embeddedStandInSeed(parts []gsxast.GoPart) (string, error) {
	var b strings.Builder
	for _, part := range parts {
		switch p := part.(type) {
		case gsxast.GoText:
			b.WriteString(p.Src)
		case *gsxast.Element, *gsxast.Fragment:
			b.WriteString("*new(_gsxrt.Node)")
		case *gsxast.EmbeddedInterp:
			retType, _, _ := embeddedProbeType(p.Lang)
			b.WriteString("*new(" + retType + ")")
		default:
			return "", fmt.Errorf("codegen: unsupported embedded interpolation part %T", part)
		}
	}
	return b.String(), nil
}
