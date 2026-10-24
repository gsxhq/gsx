package codegen

import (
	"go/token"
	"go/types"
	"html"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/wsnorm"
)

// childElementSpacingCode is the lint reported when JSX whitespace
// normalization removes the only whitespace between text and adjacent inline
// content, so the rendered words run together.
const childElementSpacingCode = "child-element-spacing"

// inlineTextElements are the HTML elements whose content flows inside the
// surrounding line of text: the WHATWG "text-level semantics" elements (§4.5)
// plus the edit elements ins and del (§4.7). Excluded from §4.5 are br and wbr,
// which exist to break the line, and rt/rp, which only annotate inside a ruby.
// Embedded and interactive phrasing content (img, svg, input, button, select,
// label, …) is deliberately absent: those render as boxes whose distance from
// the text is set by layout (margin, gap), not by a space character.
var inlineTextElements = map[string]bool{
	"a": true, "abbr": true, "b": true, "bdi": true, "bdo": true,
	"cite": true, "code": true, "data": true, "del": true, "dfn": true,
	"em": true, "i": true, "ins": true, "kbd": true, "mark": true,
	"q": true, "ruby": true, "s": true, "samp": true, "small": true,
	"span": true, "strong": true, "sub": true, "sup": true, "time": true,
	"u": true, "var": true,
}

// spacingEdge classifies what a child renders at one of its edges.
type spacingEdge uint8

const (
	// edgeNone: nothing renders there (a comment, a Go block, an empty body).
	edgeNone spacingEdge = iota
	// edgeSpace: a whitespace character that normalization keeps.
	edgeSpace
	// edgeContent: inline text or text-level content.
	edgeContent
	// edgeOpaque: block-level, component, empty-element or otherwise unknown
	// output.
	edgeOpaque
)

// facing is what a child renders at one edge: its kind and, for content
// whose text is known statically, the character at that edge (0 otherwise).
// optional marks a control-flow block that may render nothing at all (an if
// branch, a loop with no iterations, a switch with no matching case), so the
// next sibling may face the boundary instead.
type facing struct {
	kind     spacingEdge
	r        rune
	optional bool
}

// childSpacing reports child-element-spacing for one .gsx file. It runs on
// the normalized, component-resolved tree: Text values are already
// normalized, so the authored whitespace of a Text is re-read from src through
// its span, which normalization keeps.
type childSpacing struct {
	file     *token.File
	src      []byte
	resolved map[gsxast.Node]types.Type
	table    funcTables
	bag      *diag.Bag
}

// reportChildElementSpacing walks every children list of f that whitespace
// normalization processes (the same lists, with the same pre/textarea/script/
// style preserve rule, as wsnorm.Normalize and wsnorm.NormalizeGoParts) and
// reports each dropped whitespace run that separated text from inline
// content. src is the file's authored source.
func reportChildElementSpacing(f *gsxast.File, src []byte, fset *token.FileSet, resolved map[gsxast.Node]types.Type, table funcTables, bag *diag.Bag) {
	if f == nil || src == nil || !f.Pos().IsValid() {
		return
	}
	file := fset.File(f.Pos())
	if file == nil || file.Size() != len(src) {
		return
	}
	c := &childSpacing{file: file, src: src, resolved: resolved, table: table, bag: bag}
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *gsxast.Component:
			c.list(v.Body, false)
		case *gsxast.GoWithElements:
			c.parts(v.Parts)
		}
	}
}

func (c *childSpacing) list(nodes []gsxast.Markup, preserve bool) {
	if !preserve {
		c.check(nodes)
	}
	for _, n := range nodes {
		c.node(n, preserve)
	}
}

func (c *childSpacing) node(n gsxast.Markup, preserve bool) {
	gsxast.MarkupGoFields(n, func(f gsxast.GoField) { c.parts(*f.Embedded) })
	switch v := n.(type) {
	case *gsxast.Element:
		c.attrs(v.Attrs)
		c.list(v.Children, preserve || wsnorm.IsPreserveTag(v.Tag))
	case *gsxast.Fragment:
		c.list(v.Children, preserve)
	case *gsxast.MarkerRegion:
		c.list(v.Children, preserve)
	case *gsxast.IfMarkup:
		c.list(v.Then, preserve)
		c.list(v.Else, preserve)
	case *gsxast.ForMarkup:
		c.list(v.Body, preserve)
	case *gsxast.SwitchMarkup:
		for _, cc := range v.Cases {
			c.list(cc.Body, preserve)
		}
	case *gsxast.Interp:
		c.parts(v.Embedded)
	case *gsxast.GoBlock:
		c.parts(v.Embedded)
	}
}

// parts visits the element literals of a Go expression; each is a fresh
// (non-preserved) context, as in wsnorm.NormalizeGoParts.
func (c *childSpacing) parts(parts []gsxast.GoPart) {
	for _, p := range parts {
		switch v := p.(type) {
		case *gsxast.Element:
			c.node(v, false)
		case *gsxast.Fragment:
			c.node(v, false)
		}
	}
}

// attrs visits markup slots, mirroring wsnorm's normalizeAttrs.
func (c *childSpacing) attrs(attrs []gsxast.Attr) {
	for _, a := range attrs {
		switch v := a.(type) {
		case *gsxast.MarkupAttr:
			c.list(v.Value, false)
		case *gsxast.CondAttr:
			c.attrs(v.Then)
			c.attrs(v.Else)
		case *gsxast.SwitchAttr:
			for _, cc := range v.Cases {
				c.attrs(cc.Body)
			}
		}
	}
}

// check reports, for each Text in one normalized children list, a dropped
// whitespace run at an edge that faces inline content in a sibling. A run is
// dropped when it contains a line break. Siblings that render nothing
// (comments, Go blocks, HTML comments) are looked through. A boundary between
// two Texts that both dropped their facing run is reported once, on the first.
// A boundary where typography never puts a space (see noSpaceBoundary) is not
// reported.
func (c *childSpacing) check(nodes []gsxast.Markup) {
	for i, n := range nodes {
		t, ok := n.(*gsxast.Text)
		if !ok || strings.TrimSpace(t.Value) == "" {
			continue
		}
		lead, trail, ok := c.edgeRuns(t)
		if !ok {
			continue
		}
		if containsLineBreak(trail) {
			if j, next := c.neighbour(nodes, i, 1); j >= 0 && !noSpaceBoundary(textEdge(t.Value, false).r, next.r) {
				c.report(t.End()-token.Pos(len(trail)), t.End(), "after", nodes[j], true)
			}
		}
		if containsLineBreak(lead) {
			if j, prev := c.neighbour(nodes, i, -1); j >= 0 && !c.dropsTrailing(nodes[j]) && !noSpaceBoundary(prev.r, textEdge(t.Value, true).r) {
				c.report(t.Pos(), t.Pos()+token.Pos(len(lead)), "before", nodes[j], false)
			}
		}
	}
}

// noSpaceBoundary reports whether left followed by right is a boundary where
// running text never takes a space: after an opening bracket or initial quote
// (Unicode Ps, Pi), or before a closing bracket, final quote or terminal
// punctuation (Unicode Pe, Pf, Terminal_Punctuation: . , ; : ! ?). There
// the dropped line break is what the author meant (`</a>` then `.` on the
// next line). An unknown character (0) never exempts.
func noSpaceBoundary(left, right rune) bool {
	if left != 0 && unicode.In(left, unicode.Ps, unicode.Pi) {
		return true
	}
	return right != 0 && unicode.In(right, unicode.Pe, unicode.Pf, unicode.Terminal_Punctuation)
}

// neighbour returns the index of the first sibling of nodes[i] in direction
// dir that renders something, with its facing edge, when that edge is inline
// content; -1 otherwise.
func (c *childSpacing) neighbour(nodes []gsxast.Markup, i, dir int) (int, facing) {
	j, f := c.scan(nodes, i+dir, dir)
	if f.kind != edgeContent {
		return -1, facing{}
	}
	return j, f
}

// scan computes the edge that nodes[from], nodes[from+dir], … present at the
// boundary they face: the first child that renders something decides it,
// except that a child which may render nothing (facing.optional) is combined
// with whatever follows it. It returns the index of the child that supplied
// inline content (-1 when none did).
func (c *childSpacing) scan(nodes []gsxast.Markup, from, dir int) (int, facing) {
	acc, at := facing{}, -1
	for j := from; j >= 0 && j < len(nodes); j += dir {
		f := c.edge(nodes[j], dir > 0)
		if f.kind == edgeNone {
			continue
		}
		if f.kind == edgeContent && at < 0 {
			at = j
		}
		acc = combineEdges(acc, f)
		if !f.optional {
			return at, acc
		}
	}
	acc.optional = true
	return at, acc
}

// dropsTrailing reports whether n is a Text whose authored trailing
// whitespace contains a line break: that boundary was already considered from
// n's side.
func (c *childSpacing) dropsTrailing(n gsxast.Markup) bool {
	t, ok := n.(*gsxast.Text)
	if !ok {
		return false
	}
	_, trail, ok := c.edgeRuns(t)
	return ok && containsLineBreak(trail)
}

// edge classifies what n renders at its leading (leading=true) or trailing
// edge.
func (c *childSpacing) edge(n gsxast.Markup, leading bool) facing {
	switch v := n.(type) {
	case *gsxast.Text:
		return textEdge(v.Value, leading)
	case *gsxast.Element:
		// A text-level element is part of the line of text, so its edge is
		// that of its own content. One with no content at that edge (an
		// empty <span> used as a rule or an icon) is a styled box: opaque.
		if v.IsComponent || !inlineTextElements[v.Tag] {
			return facing{kind: edgeOpaque}
		}
		if f := c.listEdge(v.Children, leading); f.kind != edgeNone {
			f.optional = false
			return f
		}
		return facing{kind: edgeOpaque}
	case *gsxast.Interp:
		return c.interpEdge(v, leading)
	case *gsxast.EmbeddedInterp:
		return facing{kind: edgeContent}
	case *gsxast.Comment:
		// A bare `//` comment beside text is already the
		// bare-comment-touches-text error; it never separates text from text.
		if v.Bare {
			return facing{kind: edgeOpaque}
		}
		return facing{}
	case *gsxast.GoBlock, *gsxast.HTMLComment:
		return facing{}
	case *gsxast.Fragment:
		return c.listEdge(v.Children, leading)
	case *gsxast.MarkerRegion:
		return c.listEdge(v.Children, leading)
	case *gsxast.IfMarkup:
		return branches(c.listEdge(v.Then, leading), c.listEdge(v.Else, leading))
	case *gsxast.ForMarkup:
		f := c.listEdge(v.Body, leading)
		f.optional = true
		return f
	case *gsxast.SwitchMarkup:
		f, hasDefault := facing{}, false
		for i, cc := range v.Cases {
			hasDefault = hasDefault || cc.Default
			if i == 0 {
				f = c.listEdge(cc.Body, leading)
				continue
			}
			f = branches(f, c.listEdge(cc.Body, leading))
		}
		f.optional = f.optional || !hasDefault
		return f
	}
	return facing{kind: edgeOpaque}
}

// listEdge is the edge of a children list at its start (leading) or end.
func (c *childSpacing) listEdge(nodes []gsxast.Markup, leading bool) facing {
	from, dir := 0, 1
	if !leading {
		from, dir = len(nodes)-1, -1
	}
	_, f := c.scan(nodes, from, dir)
	if f.kind == edgeNone {
		f.optional = true
	}
	return f
}

// branches merges the edges of two alternative bodies; the result may render
// nothing when either body may.
func branches(a, b facing) facing {
	f := combineEdges(a, b)
	f.optional = a.optional || b.optional || a.kind == edgeNone || b.kind == edgeNone
	return f
}

// combineEdges merges two edges that can each face the boundary (alternative
// branches, or an optional block and what follows it): the result is inline
// content when either is, since that rendering loses the space. Contents
// with different edge characters leave the character unknown. optional is
// left for the caller to set.
func combineEdges(a, b facing) facing {
	switch {
	case a.kind == edgeContent && b.kind == edgeContent:
		if a.r != b.r {
			return facing{kind: edgeContent}
		}
		return facing{kind: edgeContent, r: a.r}
	case a.kind == edgeContent:
		return facing{kind: edgeContent, r: a.r}
	case b.kind == edgeContent:
		return facing{kind: edgeContent, r: b.r}
	case a.kind == edgeOpaque || b.kind == edgeOpaque:
		return facing{kind: edgeOpaque}
	case a.kind == edgeSpace || b.kind == edgeSpace:
		return facing{kind: edgeSpace}
	}
	return facing{}
}

// interpEdge classifies an interpolation. A string-literal interpolation is
// classified by its value ({" "} is the canonical explicit space). Otherwise
// it is inline content exactly when its rendered type (after (T, error)
// unwrapping and any registered renderer) renders as escaped text; a node,
// a node slice or an unresolved type has unknown output.
func (c *childSpacing) interpEdge(n *gsxast.Interp, leading bool) facing {
	if len(n.Stages) == 0 && n.Embedded == nil {
		if s, err := strconv.Unquote(strings.TrimSpace(n.Expr)); err == nil {
			return valueEdge(s, leading)
		}
	}
	t, ok := c.resolved[n]
	if !ok || t == nil {
		return facing{kind: edgeOpaque}
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elem, ok := tupleUnwrapType(t)
		if !ok {
			return facing{kind: edgeOpaque}
		}
		t = elem
	}
	if e, ok := c.table.renderers[rendererKey(t)]; ok {
		t = e.result
	}
	switch classify(t) {
	case catString, catBytes, catStringSlice, catInt, catUint, catFloat, catBool, catStringer, catAnyMixed:
		return facing{kind: edgeContent}
	}
	return facing{kind: edgeOpaque}
}

// textEdge classifies normalized static text. Static text renders verbatim,
// so its character references are decoded to find the edge character.
func textEdge(s string, leading bool) facing {
	return valueEdge(html.UnescapeString(s), leading)
}

// valueEdge classifies a rendered string by its first or last character.
func valueEdge(s string, leading bool) facing {
	if s == "" {
		return facing{}
	}
	var r rune
	if leading {
		r, _ = utf8.DecodeRuneInString(s)
	} else {
		r, _ = utf8.DecodeLastRuneInString(s)
	}
	if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
		return facing{kind: edgeSpace}
	}
	return facing{kind: edgeContent, r: r}
}

// edgeRuns returns the authored leading and trailing whitespace runs of t,
// read from the source through t's span.
func (c *childSpacing) edgeRuns(t *gsxast.Text) (lead, trail string, ok bool) {
	start, end := t.Pos(), t.End()
	if !start.IsValid() || !end.IsValid() {
		return "", "", false
	}
	base := token.Pos(c.file.Base())
	if start < base || end < start || int(end-base) > len(c.src) {
		return "", "", false
	}
	raw := string(c.src[start-base : end-base])
	core := strings.TrimLeft(raw, " \t\r\n")
	lead = raw[:len(raw)-len(core)]
	trimmed := strings.TrimRight(core, " \t\r\n")
	trail = core[len(trimmed):]
	return lead, trail, true
}

func containsLineBreak(s string) bool { return strings.ContainsAny(s, "\n\r") }

// report records one dropped run [pos, end). where is "after" when the run
// trails the text (the neighbour follows), "before" when it leads it.
func (c *childSpacing) report(pos, end token.Pos, where string, neighbour gsxast.Markup, follows bool) {
	what := spacingNeighbourName(neighbour, follows)
	side := "after"
	if follows {
		side = "before"
	}
	c.bag.Add(diag.Diagnostic{
		Start:    c.file.Position(pos),
		End:      c.file.Position(end),
		Severity: diag.Warning,
		Code:     childElementSpacingCode,
		Message:  "line break " + where + " this text is dropped: no space renders " + side + " " + what,
		Help:     `join the lines, or write {" "} where the space should be`,
		Source:   "codegen",
	})
}

func spacingNeighbourName(n gsxast.Markup, follows bool) string {
	switch v := n.(type) {
	case *gsxast.Element:
		return "<" + v.Tag + ">"
	case *gsxast.Interp, *gsxast.EmbeddedInterp:
		return "the interpolation"
	case *gsxast.IfMarkup:
		return "the if block"
	case *gsxast.ForMarkup:
		return "the for block"
	case *gsxast.SwitchMarkup:
		return "the switch block"
	case *gsxast.Fragment:
		return "the fragment"
	case *gsxast.MarkerRegion:
		return "the region"
	case *gsxast.Text:
		if follows {
			return "the following text"
		}
		return "the preceding text"
	}
	return "its neighbour"
}
