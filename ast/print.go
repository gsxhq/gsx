package ast

import (
	"fmt"
	"io"
	"strings"
)

// Fprint writes a canonical, deterministic, structure-only dump of an AST subtree
// to w. Positions are deliberately excluded so that golden outputs remain stable
// across source edits (positions are asserted elsewhere).
//
// One node per line, indented 2 spaces per depth level. Children are printed
// indented under their parent in source order.
func Fprint(w io.Writer, node Node) error {
	return fprintNode(w, node, 0)
}

func fprintNode(w io.Writer, node Node, depth int) error {
	indent := strings.Repeat("  ", depth)
	switch n := node.(type) {
	case *File:
		if _, err := fmt.Fprintf(w, "%sFile package=%s\n", indent, n.Package); err != nil {
			return err
		}
		for _, d := range n.Decls {
			if err := fprintNode(w, d, depth+1); err != nil {
				return err
			}
		}
	case *GoChunk:
		if _, err := fmt.Fprintf(w, "%sGoChunk len=%d\n", indent, len(n.Src)); err != nil {
			return err
		}
	case *Component:
		if _, err := fmt.Fprintf(w, "%sComponent name=%s recv=%q params=%q\n", indent, n.Name, n.Recv, n.Params); err != nil {
			return err
		}
		for _, m := range n.Body {
			if err := fprintNode(w, m, depth+1); err != nil {
				return err
			}
		}
	case *Element:
		if _, err := fmt.Fprintf(w, "%sElement tag=%s void=%v\n", indent, n.Tag, n.Void); err != nil {
			return err
		}
		for _, a := range n.Attrs {
			if err := fprintNode(w, a, depth+1); err != nil {
				return err
			}
		}
		for _, c := range n.Children {
			if err := fprintNode(w, c, depth+1); err != nil {
				return err
			}
		}
	case *Fragment:
		if _, err := fmt.Fprintf(w, "%sFragment\n", indent); err != nil {
			return err
		}
		for _, c := range n.Children {
			if err := fprintNode(w, c, depth+1); err != nil {
				return err
			}
		}
	case *Text:
		if _, err := fmt.Fprintf(w, "%sText value=%q\n", indent, n.Value); err != nil {
			return err
		}
	case *Doctype:
		if _, err := fmt.Fprintf(w, "%sDoctype text=%q\n", indent, n.Text); err != nil {
			return err
		}
	case *HTMLComment:
		if _, err := fmt.Fprintf(w, "%sHTMLComment text=%q\n", indent, n.Text); err != nil {
			return err
		}
	case *Interp:
		if _, err := fmt.Fprintf(w, "%sInterp expr=%q\n", indent, n.Expr); err != nil {
			return err
		}
		if err := fprintStages(w, indent, n.Stages); err != nil {
			return err
		}
	case *StaticAttr:
		if _, err := fmt.Fprintf(w, "%sStaticAttr name=%s value=%q\n", indent, n.Name, n.Value); err != nil {
			return err
		}
	case *ExprAttr:
		if _, err := fmt.Fprintf(w, "%sExprAttr name=%s expr=%q\n", indent, n.Name, n.Expr); err != nil {
			return err
		}
		if err := fprintStages(w, indent, n.Stages); err != nil {
			return err
		}
	case *BoolAttr:
		if _, err := fmt.Fprintf(w, "%sBoolAttr name=%s\n", indent, n.Name); err != nil {
			return err
		}
	case *SpreadAttr:
		if _, err := fmt.Fprintf(w, "%sSpreadAttr expr=%q\n", indent, n.Expr); err != nil {
			return err
		}
	case *MarkupAttr:
		if _, err := fmt.Fprintf(w, "%sMarkupAttr name=%s\n", indent, n.Name); err != nil {
			return err
		}
		for _, m := range n.Value {
			if err := fprintNode(w, m, depth+1); err != nil {
				return err
			}
		}
	case *GoBlock:
		if _, err := fmt.Fprintf(w, "%sGoBlock code=%q\n", indent, n.Code); err != nil {
			return err
		}
	case *IfMarkup:
		if _, err := fmt.Fprintf(w, "%sIfMarkup cond=%q\n", indent, n.Cond); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s  then:\n", indent); err != nil {
			return err
		}
		for _, c := range n.Then {
			if err := fprintNode(w, c, depth+2); err != nil {
				return err
			}
		}
		if n.Else != nil {
			if _, err := fmt.Fprintf(w, "%s  else:\n", indent); err != nil {
				return err
			}
			for _, c := range n.Else {
				if err := fprintNode(w, c, depth+2); err != nil {
					return err
				}
			}
		}
	case *ForMarkup:
		if _, err := fmt.Fprintf(w, "%sForMarkup clause=%q\n", indent, n.Clause); err != nil {
			return err
		}
		for _, c := range n.Body {
			if err := fprintNode(w, c, depth+1); err != nil {
				return err
			}
		}
	case *SwitchMarkup:
		if _, err := fmt.Fprintf(w, "%sSwitchMarkup tag=%q\n", indent, n.Tag); err != nil {
			return err
		}
		for _, cc := range n.Cases {
			if err := fprintNode(w, cc, depth+1); err != nil {
				return err
			}
		}
	case *CaseClause:
		if _, err := fmt.Fprintf(w, "%sCaseClause list=%q default=%v\n", indent, n.List, n.Default); err != nil {
			return err
		}
		for _, c := range n.Body {
			if err := fprintNode(w, c, depth+1); err != nil {
				return err
			}
		}
	case *CondAttr:
		if _, err := fmt.Fprintf(w, "%sCondAttr cond=%q\n", indent, n.Cond); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%s  then:\n", indent); err != nil {
			return err
		}
		for _, a := range n.Then {
			if err := fprintNode(w, a, depth+2); err != nil {
				return err
			}
		}
		if n.Else != nil {
			if _, err := fmt.Fprintf(w, "%s  else:\n", indent); err != nil {
				return err
			}
			for _, a := range n.Else {
				if err := fprintNode(w, a, depth+2); err != nil {
					return err
				}
			}
		}
	case *ClassAttr:
		if _, err := fmt.Fprintf(w, "%sClassAttr name=%s\n", indent, n.Name); err != nil {
			return err
		}
		for _, part := range n.Parts {
			if _, err := fmt.Fprintf(w, "%s  ClassPart expr=%q cond=%q\n", indent, part.Expr, part.Cond); err != nil {
				return err
			}
		}
	case *OrderedAttrsAttr:
		if _, err := fmt.Fprintf(w, "%sOrderedAttrsAttr name=%s\n", indent, n.Name); err != nil {
			return err
		}
		for _, pair := range n.Pairs {
			if _, err := fmt.Fprintf(w, "%s  OrderedPair key=%q value=%q\n", indent, pair.Key, pair.Value); err != nil {
				return err
			}
		}
	default:
		if _, err := fmt.Fprintf(w, "%s<unknown node %T>\n", indent, node); err != nil {
			return err
		}
	}
	return nil
}

// fprintStages renders a pipeline's filter stages as indented lines beneath
// their Interp/ExprAttr, mirroring the ClassPart convention.
func fprintStages(w io.Writer, indent string, stages []PipeStage) error {
	for _, st := range stages {
		if _, err := fmt.Fprintf(w, "%s  PipeStage name=%s args=%q hasArgs=%v\n",
			indent, st.Name, st.Args, st.HasArgs); err != nil {
			return err
		}
	}
	return nil
}
