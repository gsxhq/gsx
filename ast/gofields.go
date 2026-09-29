package ast

import "go/token"

// GoField is one raw Go-expression field of a node — an attribute value, a
// spread, an ordered-attrs pair value, a class/style part or guard, a
// value-form or conditional-attribute header, a control-flow header — paired
// with its codegen-only Embedded overlay (see Interp.Embedded).
type GoField struct {
	// Owner is the node carrying the field.
	Owner Node
	// Src is the field's verbatim Go text; Pos is the position of its first
	// byte (NoPos when Src is not a verbatim source slice).
	Src string
	Pos token.Pos
	// Embedded points at the field's overlay: nil until codegen's split finds
	// a nested construct in Src.
	Embedded *[]GoPart
}

// GoFields calls fn for each Go-expression field node itself carries, in
// source order. It does not visit child nodes: pair it with Inspect, or use
// AttrGoFields / MarkupGoFields, to cover a subtree.
func GoFields(node Node, fn func(GoField)) {
	switch n := node.(type) {
	case *ExprAttr:
		fn(GoField{node, n.Expr, n.ExprPos, &n.Embedded})
	case *SpreadAttr:
		fn(GoField{node, n.Expr, n.ExprPos, &n.Embedded})
	case *OrderedPair:
		fn(GoField{node, n.Value, n.ValuePos, &n.Embedded})
	case *ComposedPart:
		fn(GoField{node, n.Expr, n.ExprPos, &n.ExprEmbedded})
		fn(GoField{node, n.Cond, n.CondPos, &n.CondEmbedded})
	case *ValueArm:
		fn(GoField{node, n.Expr, n.ExprPos, &n.Embedded})
	case *ValueIf:
		fn(GoField{node, n.Cond, n.CondPos, &n.CondEmbedded})
	case *ValueSwitch:
		fn(GoField{node, n.Tag, n.TagPos, &n.TagEmbedded})
	case *ValueSwitchCase:
		fn(GoField{node, n.List, n.ListPos, &n.ListEmbedded})
	case *CondAttr:
		fn(GoField{node, n.Cond, n.CondPos, &n.CondEmbedded})
	case *SwitchAttr:
		fn(GoField{node, n.Tag, n.TagPos, &n.TagEmbedded})
	case *AttrCaseClause:
		fn(GoField{node, n.List, n.ListPos, &n.ListEmbedded})
	case *IfMarkup:
		fn(GoField{node, n.Cond, n.CondPos, &n.CondEmbedded})
	case *ForMarkup:
		fn(GoField{node, n.Clause, n.ClausePos, &n.ClauseEmbedded})
	case *SwitchMarkup:
		fn(GoField{node, n.Tag, n.TagPos, &n.TagEmbedded})
	case *CaseClause:
		fn(GoField{node, n.List, n.ListPos, &n.ListEmbedded})
	}
}

// AttrGoFields calls fn for every Go-expression field in attr's attribute
// structure: the attribute's own fields, conditional-attribute branches and
// switch arms, class/style parts and their value-form control flow. It never
// enters markup (MarkupAttr values, literal segments), which markup walkers
// already visit themselves.
func AttrGoFields(attr Attr, fn func(GoField)) {
	Inspect(attr, func(n Node) bool {
		if n == nil {
			return false
		}
		if _, isMarkup := n.(Markup); isMarkup {
			return false
		}
		GoFields(n, fn)
		return true
	})
}

// MarkupGoFields calls fn for every Go-expression field markup node m itself
// carries: an element's attribute fields (via AttrGoFields), a processing
// instruction's name, a control-flow header and a switch's case lists. It
// does not descend into m's children.
func MarkupGoFields(m Markup, fn func(GoField)) {
	switch n := m.(type) {
	case *Element:
		for _, a := range n.Attrs {
			AttrGoFields(a, fn)
		}
	case *Marker:
		if n.Name != nil {
			AttrGoFields(n.Name, fn)
		}
	case *MarkerRegion:
		if n.Name != nil {
			AttrGoFields(n.Name, fn)
		}
	case *Fragment:
		// A fragment carries no Go-expression field; its children are the
		// caller's walk, as for every node here.
	case *IfMarkup, *ForMarkup:
		GoFields(n, fn)
	case *SwitchMarkup:
		GoFields(n, fn)
		for _, c := range n.Cases {
			GoFields(c, fn)
		}
	}
}
