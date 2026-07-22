package codegen

import (
	"fmt"
	"go/token"
	"go/types"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// componentInputKind identifies the syntax-level value represented by one
// planner entry. Each top-level authored attribute has exactly one entry;
// ordered-literal pair expressions remain nested on its OrderedAttrsAttr so an
// empty literal is still a real operand and later semantic planning retains the
// exact container that owns every pair.
type componentInputKind uint8

const (
	componentInputProp componentInputKind = iota
	componentInputBody
	componentInputAttrsPair
	componentInputAttrsSegment
	componentInputAttrsContributor
	componentInputOmitted
)

// componentInputValue is one authored value-producing part of a component
// invocation. sourceIndex is the top-level attribute index (or len(Attrs) for
// the body). contributorIndex orders only the attrs stream and is -1 for
// ordinary props and children. attrsNode is the already-classified recursive
// stream root for attrs values and nil for ordinary props and children.
type componentInputValue struct {
	kind             componentInputKind
	sourceIndex      int
	paramIndex       int
	contributorIndex int
	node             gsxast.Node
	attrsNode        *componentAttrsStreamNode
	children         []gsxast.Markup // source-comment-free body values; body entries only
}

// componentAttrsStreamNode is one syntax-normalized attrs contribution. A
// conditional remains one node whose branch slices retain their own authored
// order; Task 5 consumes this tree without classifying the AST again.
type componentAttrsStreamKind uint8

const (
	componentAttrsStreamPair componentAttrsStreamKind = iota
	componentAttrsStreamSpread
	componentAttrsStreamContributor
	componentAttrsStreamConditional
)

type componentAttrsStreamNode struct {
	kind        componentAttrsStreamKind
	sourceIndex int // index in the immediately containing attribute list
	attr        gsxast.Attr
	then        []componentAttrsStreamNode
	otherwise   []componentAttrsStreamNode
	// hasSyntaxError records invalid descendants omitted from then/otherwise.
	// It suppresses a cascading missing-attrs error for an invalid-only subtree.
	hasSyntaxError bool
}

type componentArgSlot struct {
	param        componentParam
	valueIndexes []int
	omitted      bool
}

type componentCallPlan struct {
	site callSiteID
	// call retains the authored invocation so later omitted-argument diagnostics
	// use its exact range instead of reconstructing a position from the tag.
	call      *gsxast.Element
	callStart token.Position
	callEnd   token.Position
	target    componentSignatureModel
	args      []componentArgSlot
	values    []componentInputValue
}

// planComponentInputs performs only syntax-level routing. In particular, a
// spread is always an attrs-stream segment here; Task 5's semantic operand pass
// proves that its type is an attrs bag and rejects struct splats.
func planComponentInputs(site callSiteID, el *gsxast.Element, target componentSignatureModel, fset *token.FileSet) (componentCallPlan, []diag.Diagnostic) {
	plan := componentCallPlan{
		site:   site,
		call:   el,
		target: target,
		args:   make([]componentArgSlot, len(target.params)),
	}
	for i, param := range target.params {
		plan.args[i] = componentArgSlot{param: param, omitted: true}
	}

	if fset == nil {
		return plan, []diag.Diagnostic{{
			Severity: diag.Error,
			Code:     "component-call-plan",
			Message:  "component input planning requires the package FileSet",
			Source:   "codegen",
		}}
	}
	if el == nil {
		return plan, []diag.Diagnostic{{
			Severity: diag.Error,
			Code:     "component-call-plan",
			Message:  "cannot plan inputs for a nil component element",
			Source:   "codegen",
		}}
	}
	plan.callStart = fset.Position(el.Pos())
	plan.callEnd = fset.Position(el.End())

	ordinary := make(map[string]int, len(target.params))
	attrsParam := -1
	childrenParam := -1
	for i, param := range target.params {
		switch param.role {
		case roleProp:
			ordinary[param.name] = i
		case roleGoOnlyVariadic:
			ordinary[param.name] = i
		case roleAttrs:
			attrsParam = i
		case roleChildren:
			childrenParam = i
		}
	}

	var diagnostics []diag.Diagnostic
	addDiagnostic := func(node gsxast.Node, code, message string) {
		diagnostic := diag.Diagnostic{
			Severity: diag.Error,
			Code:     code,
			Message:  message,
			Source:   "codegen",
		}
		if fset != nil && node != nil {
			diagnostic.Start = fset.Position(node.Pos())
			diagnostic.End = fset.Position(node.End())
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	addValue := func(kind componentInputKind, sourceIndex, paramIndex, contributorIndex int, node gsxast.Node) {
		valueIndex := len(plan.values)
		plan.values = append(plan.values, componentInputValue{
			kind:             kind,
			sourceIndex:      sourceIndex,
			paramIndex:       paramIndex,
			contributorIndex: contributorIndex,
			node:             node,
		})
		plan.args[paramIndex].valueIndexes = append(plan.args[paramIndex].valueIndexes, valueIndex)
		plan.args[paramIndex].omitted = false
	}
	requireAttrs := func(node gsxast.Node, input string) bool {
		if attrsParam >= 0 {
			return true
		}
		addDiagnostic(node, "component-missing-attrs", fmt.Sprintf("%s has no attrs param; unexpected %s", el.Tag, input))
		return false
	}

	contributorIndex := 0
	addAttrsValue := func(attrsNode componentAttrsStreamNode, currentContributor int) {
		valueIndex := len(plan.values)
		var inputKind componentInputKind
		switch attrsNode.kind {
		case componentAttrsStreamPair:
			inputKind = componentInputAttrsPair
		case componentAttrsStreamContributor:
			inputKind = componentInputAttrsContributor
		case componentAttrsStreamSpread, componentAttrsStreamConditional:
			inputKind = componentInputAttrsSegment
		default:
			panic(fmt.Sprintf("codegen: invalid component attrs stream kind %d", attrsNode.kind))
		}
		addValue(inputKind, attrsNode.sourceIndex, attrsParam, currentContributor, attrsNode.attr)
		plan.values[valueIndex].attrsNode = &attrsNode
	}
	addTopLevelAttrs := func(attrsNode componentAttrsStreamNode, input string) {
		currentContributor := contributorIndex
		contributorIndex++
		if !requireAttrs(attrsNode.attr, input) {
			return
		}
		addAttrsValue(attrsNode, currentContributor)
	}
	addOrdinary := func(node gsxast.Node, sourceIndex, paramIndex int) {
		if !plan.args[paramIndex].omitted {
			addDiagnostic(node, "duplicate-prop", fmt.Sprintf("component <%s> prop %q is supplied more than once", el.Tag, target.params[paramIndex].name))
			return
		}
		addValue(componentInputProp, sourceIndex, paramIndex, -1, node)
	}
	reservedAttrsNode := func(attr gsxast.Attr, sourceIndex int) (*componentAttrsStreamNode, string, bool) {
		name, named := componentInputAttrName(attr)
		if !named {
			return nil, "", false
		}
		switch name {
		case "children":
			addDiagnostic(attr, "reserved-input-form", fmt.Sprintf("children on <%s> is populated only by the component body", el.Tag))
			return nil, "", true
		case "attrs":
			valid := false
			switch attr := attr.(type) {
			case *gsxast.ExprAttr, *gsxast.OrderedAttrsAttr:
				valid = true
			case *gsxast.EmbeddedAttr:
				valid = attr.Braced
			}
			if !valid {
				addDiagnostic(attr, "reserved-input-form", fmt.Sprintf("attrs on <%s> accepts only attrs={expr} or attrs={{...}}", el.Tag))
				return nil, "", true
			}
			return &componentAttrsStreamNode{
				kind:        componentAttrsStreamContributor,
				sourceIndex: sourceIndex,
				attr:        attr,
			}, "attrs contributor", true
		default:
			return nil, "", false
		}
	}

	var normalizeAttrsList func([]gsxast.Attr) ([]componentAttrsStreamNode, bool)
	normalizeAttrsList = func(attrs []gsxast.Attr) ([]componentAttrsStreamNode, bool) {
		nodes := make([]componentAttrsStreamNode, 0, len(attrs))
		hasSyntaxError := false
		for sourceIndex, attr := range attrs {
			if _, sourceOnly := attr.(*gsxast.CommentAttr); sourceOnly {
				continue
			}
			if node, _, reserved := reservedAttrsNode(attr, sourceIndex); reserved {
				if node != nil {
					nodes = append(nodes, *node)
				} else {
					hasSyntaxError = true
				}
				continue
			}
			if _, named := componentInputAttrName(attr); named {
				nodes = append(nodes, componentAttrsStreamNode{
					kind:        componentAttrsStreamPair,
					sourceIndex: sourceIndex,
					attr:        attr,
				})
				continue
			}
			switch attr := attr.(type) {
			case *gsxast.SpreadAttr:
				nodes = append(nodes, componentAttrsStreamNode{
					kind:        componentAttrsStreamSpread,
					sourceIndex: sourceIndex,
					attr:        attr,
				})
			case *gsxast.CondAttr:
				then, thenSyntaxError := normalizeAttrsList(attr.Then)
				otherwise, elseSyntaxError := normalizeAttrsList(attr.Else)
				nodes = append(nodes, componentAttrsStreamNode{
					kind:           componentAttrsStreamConditional,
					sourceIndex:    sourceIndex,
					attr:           attr,
					then:           then,
					otherwise:      otherwise,
					hasSyntaxError: thenSyntaxError || elseSyntaxError,
				})
				hasSyntaxError = hasSyntaxError || thenSyntaxError || elseSyntaxError
			default:
				addDiagnostic(attr, "unsupported-component-attr", fmt.Sprintf("unsupported component attribute %T on <%s>", attr, el.Tag))
				hasSyntaxError = true
			}
		}
		return nodes, hasSyntaxError
	}
	reportMissingAttrsTree := func(root componentAttrsStreamNode) {
		var report func(componentAttrsStreamNode)
		report = func(node componentAttrsStreamNode) {
			if node.kind != componentAttrsStreamConditional {
				input := "attrs-bag contributor"
				if name, named := componentInputAttrName(node.attr); named {
					input = fmt.Sprintf("attribute %q", name)
				}
				requireAttrs(node.attr, input)
				return
			}
			for _, child := range node.then {
				report(child)
			}
			for _, child := range node.otherwise {
				report(child)
			}
			if len(node.then) == 0 && len(node.otherwise) == 0 && !node.hasSyntaxError {
				requireAttrs(node.attr, "conditional attrs contributor")
			}
		}
		report(root)
	}

	for sourceIndex, attr := range el.Attrs {
		if _, ok := attr.(*gsxast.CommentAttr); ok {
			continue
		}

		if attrsNode, input, reserved := reservedAttrsNode(attr, sourceIndex); reserved {
			if attrsNode != nil {
				addTopLevelAttrs(*attrsNode, input)
			}
			continue
		}

		name, named := componentInputAttrName(attr)
		if named {
			if token.IsIdentifier(name) {
				if paramIndex, ok := ordinary[name]; ok {
					if target.params[paramIndex].role == roleGoOnlyVariadic {
						addDiagnostic(attr, "ordinary-variadic-prop", fmt.Sprintf("ordinary variadic parameter %q on <%s> is Go-callable only", name, el.Tag))
						continue
					}
					addOrdinary(attr, sourceIndex, paramIndex)
					continue
				}
			}

			addTopLevelAttrs(componentAttrsStreamNode{
				kind:        componentAttrsStreamPair,
				sourceIndex: sourceIndex,
				attr:        attr,
			}, fmt.Sprintf("attribute %q", name))
			continue
		}

		switch attr := attr.(type) {
		case *gsxast.SpreadAttr:
			addTopLevelAttrs(componentAttrsStreamNode{
				kind:        componentAttrsStreamSpread,
				sourceIndex: sourceIndex,
				attr:        attr,
			}, "attrs-bag contributor")
		case *gsxast.CondAttr:
			currentContributor := contributorIndex
			contributorIndex++
			then, thenSyntaxError := normalizeAttrsList(attr.Then)
			otherwise, elseSyntaxError := normalizeAttrsList(attr.Else)
			attrsNode := componentAttrsStreamNode{
				kind:           componentAttrsStreamConditional,
				sourceIndex:    sourceIndex,
				attr:           attr,
				then:           then,
				otherwise:      otherwise,
				hasSyntaxError: thenSyntaxError || elseSyntaxError,
			}
			if attrsParam >= 0 {
				addAttrsValue(attrsNode, currentContributor)
			} else {
				reportMissingAttrsTree(attrsNode)
			}
		default:
			addDiagnostic(attr, "unsupported-component-attr", fmt.Sprintf("unsupported component attribute %T on <%s>", attr, el.Tag))
		}
	}

	if componentBodyPresent(el.Children) {
		semanticChildren := componentSemanticChildren(el.Children)
		if childrenParam < 0 {
			addDiagnostic(el, "component-missing-children", fmt.Sprintf("%s has no children param; component body cannot be passed", el.Tag))
		} else {
			addValue(componentInputBody, len(el.Attrs), childrenParam, -1, el)
			plan.values[len(plan.values)-1].children = semanticChildren
		}
	}

	return plan, diagnostics
}

// validateComponentOperands is the semantic operand pass Task 4's syntax router
// defers to. It consumes the syntax-only componentCallPlan plus each authored
// value's go/types fact and proves the operand-level contract the router could
// not: every attrs spread and explicit attrs contributor must carry an
// attrs-bag type (a struct or other non-bag spread is rejected as
// component-attrs-spread-type, since component struct-splat was cut), and every
// tuple-returning value must be (T, error) so it can be consumed as one value
// before positional assembly. The plan is returned unchanged on success; the
// diagnostics are positioned at the offending authored node.
func validateComponentOperands(plan componentCallPlan, facts expressionFactSet, runtime runtimeContract) (componentCallPlan, []diag.Diagnostic) {
	var diagnostics []diag.Diagnostic
	report := func(node gsxast.Node, code, message string) {
		d := diag.Diagnostic{Severity: diag.Error, Code: code, Message: message, Source: "codegen"}
		if node != nil {
			if pos, ok := plan.nodePosition(node); ok {
				d.Start = pos.start
				d.End = pos.end
			}
		}
		diagnostics = append(diagnostics, d)
	}

	for _, value := range plan.values {
		fact, ok := facts.get(value.node)
		if !ok {
			continue
		}
		switch value.kind {
		case componentInputAttrsSegment, componentInputAttrsContributor:
			// A spread or explicit attrs={expr} contributor must be an attrs bag.
			// Conditional segments carry no single value type; their leaves are
			// validated individually by the caller.
			if value.attrsNode != nil && value.attrsNode.kind == componentAttrsStreamConditional {
				continue
			}
			if fact.tuple != nil {
				// (T, error) auto-unwrap: the unwrapped T must itself be an attrs
				// bag, so `attrs={f()}` where f returns (nonBag, error) is rejected.
				unwrapped, ok := tupleUnwrapType(fact.tuple)
				if !ok {
					report(value.node, "invalid-tuple",
						fmt.Sprintf("value returns %s; only (T, error) is consumed as one value", fact.tuple))
					continue
				}
				if !isAttrsBagValue(unwrapped, runtime) {
					report(value.node, "component-attrs-spread-type",
						fmt.Sprintf("attrs contributor has type %s; a gsx.Attrs bag ([]gsx.Attr family) is required", unwrapped))
				}
				continue
			}
			if fact.tv.Type != nil && !isAttrsBagValue(fact.tv.Type, runtime) {
				report(value.node, "component-attrs-spread-type",
					fmt.Sprintf("attrs contributor has type %s; a gsx.Attrs bag ([]gsx.Attr family) is required", fact.tv.Type))
			}
		default:
			validateTupleFact(value.node, fact, report)
		}
	}
	return plan, diagnostics
}

func validateTupleFact(node gsxast.Node, fact expressionFact, report func(gsxast.Node, string, string)) {
	if fact.tuple == nil {
		return
	}
	if _, ok := tupleUnwrapType(fact.tuple); !ok {
		report(node, "invalid-tuple",
			fmt.Sprintf("value returns %s; only (T, error) is consumed as one value", fact.tuple))
	}
}

// isAttrsBagValue reports whether a value of type t may be normalized to the
// canonical gsx.Attrs bag: the exact attrs type, a bare []gsx.Attr slice, or a
// defined/aliased slice whose element type is exactly gsx.Attr after alias
// resolution.
func isAttrsBagValue(t types.Type, runtime runtimeContract) bool {
	if t == nil {
		return false
	}
	if types.Identical(t, runtime.attrs) {
		return true
	}
	unaliased := types.Unalias(t)
	if slice, ok := unaliased.(*types.Slice); ok {
		return types.Identical(slice.Elem(), runtime.attr)
	}
	if named, ok := unaliased.(*types.Named); ok {
		return attrsSliceHasExactElement(named, runtime.attr)
	}
	return false
}

// nodePosition resolves an authored value node to its call-relative source
// range for diagnostics, falling back to the call element's own range.
func (p componentCallPlan) nodePosition(node gsxast.Node) (struct{ start, end token.Position }, bool) {
	var out struct{ start, end token.Position }
	if node == nil {
		return out, false
	}
	if !node.Pos().IsValid() {
		return out, false
	}
	out.start = p.callStart
	out.end = p.callEnd
	return out, true
}

func componentBodyPresent(children []gsxast.Markup) bool {
	for _, child := range children {
		if !componentSourceOnlyChild(child) {
			return true
		}
	}
	return false
}

// componentSemanticChildren removes source comments from the authored static
// child list. The planner and scalar/variadic lowering share this exact
// classifier so a comment can never become a rendered value or consume a
// variadic position.
func componentSemanticChildren(children []gsxast.Markup) []gsxast.Markup {
	semantic := make([]gsxast.Markup, 0, len(children))
	for _, child := range children {
		if componentSourceOnlyChild(child) {
			continue
		}
		semantic = append(semantic, child)
	}
	return semantic
}

func componentSourceOnlyChild(child gsxast.Markup) bool {
	_, sourceOnly := child.(*gsxast.Comment)
	return sourceOnly
}

func componentInputAttrName(attr gsxast.Attr) (string, bool) {
	switch attr := attr.(type) {
	case *gsxast.StaticAttr:
		return attr.Name, true
	case *gsxast.ExprAttr:
		return attr.Name, true
	case *gsxast.BoolAttr:
		return attr.Name, true
	case *gsxast.MarkupAttr:
		return attr.Name, true
	case *gsxast.EmbeddedAttr:
		return attr.Name, true
	case *gsxast.ClassAttr:
		return attr.Name, true
	case *gsxast.OrderedAttrsAttr:
		return attr.Name, true
	default:
		return "", false
	}
}
