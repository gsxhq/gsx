package codegen

import (
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// forEachElement visits every *gsxast.Element under nodes (children, markup
// attrs, fragments, control flow) in source order. Unlike
// forEachComponentTagElement it does not filter by tag kind — the resolve
// pass IS the thing that decides tag kind.
//
// COMPLETENESS: this must cover every construct that can contain an Element.
// Checked against ast/ast.go's []Markup-bearing fields:
//   - Component.Body, Element.Children, Fragment.Children, IfMarkup.Then/Else,
//     ForMarkup.Body, CaseClause.Body (via SwitchMarkup.Cases) — all handled
//     below or by the caller (Component.Body/GoWithElements are walked by
//     resolveComponentTags directly, mirroring forEachComponentTagElement's
//     structure).
//   - MarkupAttr.Value — reached via walkMarkupAttrs, which also recurses
//     CondAttr.Then/Else, so a MarkupAttr nested inside a conditional attr is
//     covered too.
//   - EmbeddedAttr.Segments, EmbeddedInterp.Segments, ClassPart.CSSSegments —
//     also []Markup, but by construction (parser/attrs.go) contain only *Text
//     and *Interp, never *Element; walkMarkupAttrs still yields
//     EmbeddedAttr/ClassAttr segments to forEachElement's recursion, which is
//     a harmless no-op over them.
//   - Interp.Embedded ([]GoPart, can hold *Element/*Fragment) is populated
//     ONLY by codegen's later buildSkeleton pass (splitInterpEmbedded), never
//     by the parser — it is always nil on the freshly parsed AST this pass
//     runs over (resolveComponentTags is wired in analyze() before any
//     buildSkeleton call), so there is nothing to walk here.
func forEachElement(nodes []gsxast.Markup, fn func(*gsxast.Element)) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Element:
			fn(t)
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				forEachElement(value, fn)
			})
			forEachElement(t.Children, fn)
		case *gsxast.Fragment:
			forEachElement(t.Children, fn)
		case *gsxast.ForMarkup:
			forEachElement(t.Body, fn)
		case *gsxast.IfMarkup:
			forEachElement(t.Then, fn)
			forEachElement(t.Else, fn)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				forEachElement(cc.Body, fn)
			}
		}
	}
}

// resolveTag applies the resolution rule from the 2026-07-10 spec:
// capital/dotted → component; lowercase Go identifier → component iff a
// package-level declaration with that name exists AND the tag is not the
// enclosing declaration's own name (self-exclusion, wrapper pattern);
// everything else (dashes, unknown names) → leaf.
func resolveTag(tag string, declNames map[string]bool, exclude string) bool {
	if gsxast.IsComponentTag(tag) {
		return true
	}
	if !token.IsIdentifier(tag) {
		return false // <my-widget> etc. can never name a Go declaration
	}
	return tag != exclude && declNames[tag]
}

// resolveComponentTags stamps Element.IsComponent on every element in file.
// exclude for a Component body is the component's bare name (methods included
// — exclusion is keyed by name); for a GoWithElements, each element/fragment
// part's enclosing top-level declaration name.
func resolveComponentTags(file *gsxast.File, declNames map[string]bool, bag *diag.Bag) {
	stampAll := func(nodes []gsxast.Markup, exclude string) {
		forEachElement(nodes, func(el *gsxast.Element) {
			el.IsComponent = resolveTag(el.Tag, declNames, exclude)
		})
	}
	for _, d := range file.Decls {
		switch t := d.(type) {
		case *gsxast.Component:
			stampAll(t.Body, t.Name)
		case *gsxast.GoWithElements:
			excludes := goWithElementsExcludes(t)
			for i, p := range t.Parts {
				exclude := excludes[i]
				switch pt := p.(type) {
				case *gsxast.Element:
					pt.IsComponent = resolveTag(pt.Tag, declNames, exclude)
					walkMarkupAttrs(pt.Attrs, func(value []gsxast.Markup) {
						stampAll(value, exclude)
					})
					stampAll(pt.Children, exclude)
				case *gsxast.Fragment:
					// A fragment itself has no tag to resolve (it is never a
					// component/leaf choice), but elements nested inside it
					// still resolve against the SAME enclosing declaration's
					// exclusion — e.g. `var pair = <><chip/><pair/></>` must
					// self-exclude <pair/> just like a bare element part
					// would.
					stampAll(pt.Children, exclude)
				}
			}
		}
	}
}

// goWithElementsExcludes maps each part index of g to the name of the
// top-level Go declaration enclosing it, by re-parsing the reconstructed
// source with `nil` placeholders for element/fragment parts and matching part
// byte offsets against declaration spans. Element/fragment parts outside any
// declaration (unlikely) get "".
func goWithElementsExcludes(g *gsxast.GoWithElements) map[int]string {
	out := map[int]string{}
	const header = "package _gsxp\n"
	var b strings.Builder
	b.WriteString(header)
	partOff := map[int]int{} // part index -> byte offset in reconstructed src
	for i, p := range g.Parts {
		partOff[i] = b.Len()
		if gt, ok := p.(gsxast.GoText); ok {
			b.WriteString(gt.Src)
		} else {
			b.WriteString("nil") // placeholder occupying the element's slot
		}
	}
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "", b.String(), 0)
	if f == nil && err != nil {
		return out
	}
	declName := func(d goast.Decl, pos token.Pos) (string, bool) {
		switch dd := d.(type) {
		case *goast.FuncDecl:
			if dd.Name != nil {
				// Methods are included per spec: exclusion is keyed by name
				// regardless of whether the FuncDecl has a receiver.
				return dd.Name.Name, true
			}
		case *goast.GenDecl:
			// A GenDecl may group several specs (var ( a = 1; b = <el/> )):
			// the exclusion is the name of the SPEC containing pos, never the
			// group's first spec. Multi-NAME specs (var a, b = ..., ...) use
			// the containing spec's first name — good enough for a
			// diagnostic-grade exclusion; document if it ever matters.
			for _, spec := range dd.Specs {
				vs, ok := spec.(*goast.ValueSpec)
				if !ok || len(vs.Names) == 0 {
					continue
				}
				if vs.Pos() <= pos && pos < vs.End() {
					return vs.Names[0].Name, true
				}
			}
		}
		return "", false
	}
	tf := fset.File(f.Pos())
	for i := range g.Parts {
		switch g.Parts[i].(type) {
		case *gsxast.Element, *gsxast.Fragment:
		default:
			continue
		}
		pos := tf.Pos(partOff[i])
		for _, d := range f.Decls {
			if d.Pos() <= pos && pos < d.End() {
				if name, ok := declName(d, pos); ok {
					out[i] = name
				}
				break
			}
		}
	}
	return out
}
