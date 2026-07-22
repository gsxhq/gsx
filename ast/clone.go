package ast

import "fmt"

// CloneFile returns a deep copy of f in which every mutable node is a freshly
// allocated pointer, while immutable leaves (strings, token.Pos, bools, the
// embedded span) are copied by value and interned strings are shared. The clone
// shares no slice backing array and no node pointer with f, so a caller may run
// the codegen mutation passes (which populate Interp.Embedded / GoBlock.Embedded,
// minify js/css into Segment/Text values, rebase RawJS, and stamp
// Element.IsComponent) over the clone without contaminating f.
//
// This exists so a pristine parsed tree can be cached and re-served many times:
// each analysis gets its own independent tree, preserving codegen's invariant
// that every analyze() call walks a fresh AST (see Module.parsePackageWithFset).
//
// The switches are exhaustive over the sealed node families (Decl / Markup /
// Attr / GoPart) plus the standalone Node types they contain (CaseClause,
// ClassPart, OrderedPair, and the value-form control-flow nodes). An unhandled
// concrete type panics rather than silently sharing a mutable node — the corpus
// goldens, which exercise every node kind through Package/Generate, turn that
// panic into a loud test failure if a new node type is added without a clone
// case.
func CloneFile(f *File) *File {
	if f == nil {
		return nil
	}
	nf := *f
	nf.Decls = cloneDecls(f.Decls)
	return &nf
}

func cloneDecls(in []Decl) []Decl {
	if in == nil {
		return nil
	}
	out := make([]Decl, len(in))
	for i, d := range in {
		out[i] = cloneDecl(d)
	}
	return out
}

func cloneDecl(d Decl) Decl {
	switch v := d.(type) {
	case *GoChunk:
		n := *v
		return &n
	case *GoWithElements:
		n := *v
		n.Parts = cloneGoParts(v.Parts)
		return &n
	case *Component:
		n := *v
		n.Body = cloneMarkups(v.Body)
		return &n
	default:
		panic(fmt.Sprintf("ast.cloneDecl: unhandled Decl type %T", d))
	}
}

func cloneMarkups(in []Markup) []Markup {
	if in == nil {
		return nil
	}
	out := make([]Markup, len(in))
	for i, m := range in {
		out[i] = cloneMarkup(m)
	}
	return out
}

func cloneMarkup(m Markup) Markup {
	switch v := m.(type) {
	case *Element:
		n := *v
		n.Attrs = cloneAttrs(v.Attrs)
		n.Children = cloneMarkups(v.Children)
		return &n
	case *Fragment:
		n := *v
		n.Children = cloneMarkups(v.Children)
		return &n
	case *Text:
		n := *v
		return &n
	case *Doctype:
		n := *v
		return &n
	case *HTMLComment:
		n := *v
		return &n
	case *Comment:
		n := *v
		return &n
	case *Interp:
		n := *v
		n.Stages = clonePipeStages(v.Stages)
		n.Embedded = cloneGoParts(v.Embedded)
		return &n
	case *EmbeddedInterp:
		n := *v
		n.Segments = cloneMarkups(v.Segments)
		n.Stages = clonePipeStages(v.Stages)
		return &n
	case *GoBlock:
		n := *v
		n.Embedded = cloneGoParts(v.Embedded)
		if v.UnsupportedMarkup != nil {
			n.UnsupportedMarkup = cloneGoPart(v.UnsupportedMarkup)
		}
		return &n
	case *IfMarkup:
		n := *v
		n.Then = cloneMarkups(v.Then)
		n.Else = cloneMarkups(v.Else)
		return &n
	case *ForMarkup:
		n := *v
		n.Body = cloneMarkups(v.Body)
		return &n
	case *SwitchMarkup:
		n := *v
		n.Cases = cloneCaseClauses(v.Cases)
		return &n
	default:
		panic(fmt.Sprintf("ast.cloneMarkup: unhandled Markup type %T", m))
	}
}

func cloneCaseClauses(in []*CaseClause) []*CaseClause {
	if in == nil {
		return nil
	}
	out := make([]*CaseClause, len(in))
	for i, c := range in {
		n := *c
		n.Body = cloneMarkups(c.Body)
		out[i] = &n
	}
	return out
}

func cloneAttrs(in []Attr) []Attr {
	if in == nil {
		return nil
	}
	out := make([]Attr, len(in))
	for i, a := range in {
		out[i] = cloneAttr(a)
	}
	return out
}

func cloneAttr(a Attr) Attr {
	switch v := a.(type) {
	case *StaticAttr:
		n := *v
		return &n
	case *ExprAttr:
		n := *v
		n.Stages = clonePipeStages(v.Stages)
		return &n
	case *BoolAttr:
		n := *v
		return &n
	case *SpreadAttr:
		n := *v
		n.Stages = clonePipeStages(v.Stages)
		return &n
	case *MarkupAttr:
		n := *v
		n.Value = cloneMarkups(v.Value)
		return &n
	case *EmbeddedAttr:
		n := *v
		n.Segments = cloneMarkups(v.Segments)
		n.Stages = clonePipeStages(v.Stages)
		return &n
	case *CondAttr:
		n := *v
		n.Then = cloneAttrs(v.Then)
		n.Else = cloneAttrs(v.Else)
		return &n
	case *ClassAttr:
		n := *v
		n.Parts = cloneClassParts(v.Parts)
		return &n
	case *OrderedAttrsAttr:
		n := *v
		n.Pairs = cloneOrderedPairs(v.Pairs)
		return &n
	case *CommentAttr:
		n := *v
		return &n
	default:
		panic(fmt.Sprintf("ast.cloneAttr: unhandled Attr type %T", a))
	}
}

func cloneGoParts(in []GoPart) []GoPart {
	if in == nil {
		return nil
	}
	out := make([]GoPart, len(in))
	for i, p := range in {
		out[i] = cloneGoPart(p)
	}
	return out
}

func cloneGoPart(p GoPart) GoPart {
	switch v := p.(type) {
	case GoText:
		// GoText is stored by value and carries only immutable leaves.
		return v
	case *Element:
		return cloneMarkup(v).(*Element)
	case *Fragment:
		return cloneMarkup(v).(*Fragment)
	case *EmbeddedInterp:
		return cloneMarkup(v).(*EmbeddedInterp)
	default:
		panic(fmt.Sprintf("ast.cloneGoPart: unhandled GoPart type %T", p))
	}
}

func cloneClassParts(in []ClassPart) []ClassPart {
	if in == nil {
		return nil
	}
	out := make([]ClassPart, len(in))
	for i := range in {
		out[i] = cloneClassPart(in[i])
	}
	return out
}

func cloneClassPart(p ClassPart) ClassPart {
	// p is already a value copy; deep-copy its mutable slices/pointers.
	p.Stages = clonePipeStages(p.Stages)
	p.CSSSegments = cloneMarkups(p.CSSSegments)
	if p.CF != nil {
		p.CF = cloneValueCF(p.CF)
	}
	return p
}

func cloneValueCF(cf *ValueCF) *ValueCF {
	n := *cf
	if cf.If != nil {
		n.If = cloneValueIf(cf.If)
	}
	if cf.Switch != nil {
		n.Switch = cloneValueSwitch(cf.Switch)
	}
	return &n
}

func cloneValueIf(vi *ValueIf) *ValueIf {
	n := *vi
	if vi.Then != nil {
		n.Then = cloneValueArm(vi.Then)
	}
	if vi.ElseIf != nil {
		n.ElseIf = cloneValueIf(vi.ElseIf)
	}
	if vi.Else != nil {
		n.Else = cloneValueArm(vi.Else)
	}
	return &n
}

func cloneValueSwitch(vs *ValueSwitch) *ValueSwitch {
	n := *vs
	if vs.Cases != nil {
		n.Cases = make([]*ValueSwitchCase, len(vs.Cases))
		for i, c := range vs.Cases {
			cn := *c
			if c.Value != nil {
				cn.Value = cloneValueArm(c.Value)
			}
			n.Cases[i] = &cn
		}
	}
	return &n
}

func cloneValueArm(va *ValueArm) *ValueArm {
	n := *va
	n.Stages = clonePipeStages(va.Stages)
	return &n
}

func cloneOrderedPairs(in []OrderedPair) []OrderedPair {
	if in == nil {
		return nil
	}
	// OrderedPair carries only immutable leaves; a fresh backing array is all
	// that is needed so &out[i] differs from the pristine tree's pointers.
	out := make([]OrderedPair, len(in))
	copy(out, in)
	return out
}

func clonePipeStages(in []PipeStage) []PipeStage {
	if in == nil {
		return nil
	}
	out := make([]PipeStage, len(in))
	copy(out, in)
	return out
}
