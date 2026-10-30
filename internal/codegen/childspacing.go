package codegen

import (
	"go/token"
	"go/types"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/wsnorm"
)

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
	spacing  wsnorm.Spacing
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
	c.spacing = wsnorm.Spacing{Interp: c.interpEdge}
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
// whitespace run at an edge where the dropped break loses a space (see
// wsnorm.Spacing.DroppedBreakLoses). A run is dropped when it contains a line
// break. A boundary between two Texts that both dropped their facing run is
// reported once, on the first.
func (c *childSpacing) check(nodes []gsxast.Markup) {
	for i, n := range nodes {
		t, ok := n.(*gsxast.Text)
		if !ok {
			continue
		}
		lead, trail, ok := c.edgeRuns(t)
		if !ok {
			continue
		}
		if containsLineBreak(trail) {
			if j, loses := c.spacing.DroppedBreakLoses(nodes, i, 1); loses {
				c.report(t.End()-token.Pos(len(trail)), t.End(), "after", nodes[j], true)
			}
		}
		if containsLineBreak(lead) {
			if j, loses := c.spacing.DroppedBreakLoses(nodes, i, -1); loses && !c.dropsTrailing(nodes[j]) {
				c.report(t.Pos(), t.Pos()+token.Pos(len(lead)), "before", nodes[j], false)
			}
		}
	}
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

// interpEdge classifies an interpolation that is not a plain string literal:
// it is inline content exactly when its rendered type (after (T, error)
// unwrapping and any registered renderer) renders as escaped text; a node, a
// node slice or an unresolved type has unknown output.
func (c *childSpacing) interpEdge(n *gsxast.Interp) wsnorm.Edge {
	t, ok := c.resolved[n]
	if !ok || t == nil {
		return wsnorm.Edge{Kind: wsnorm.EdgeOpaque}
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elem, ok := tupleUnwrapType(t)
		if !ok {
			return wsnorm.Edge{Kind: wsnorm.EdgeOpaque}
		}
		t = elem
	}
	if e, ok := c.table.renderers[rendererKey(t)]; ok {
		t = e.result
	}
	switch classify(t) {
	case catString, catBytes, catStringSlice, catInt, catUint, catFloat, catBool, catStringer, catAnyMixed:
		return wsnorm.Edge{Kind: wsnorm.EdgeContent}
	}
	return wsnorm.Edge{Kind: wsnorm.EdgeOpaque}
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
		Code:     diag.CodeChildElementSpacing,
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
