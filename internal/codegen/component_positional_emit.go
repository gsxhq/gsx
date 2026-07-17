package codegen

import (
	"bytes"
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
)

type positionalEmitContext struct {
	currentPkg          *types.Package
	resolved            map[gsxast.Node]types.Type
	table               funcTables
	imports             map[string]bool
	rt                  rtImports
	importAliases       map[string]string
	boundNames          map[string]string
	typeArgAliases      map[string]string
	interpTemp          *int
	fset                *token.FileSet
	recvVar             string
	recvTypeName        string
	cls                 *attrclass.Classifier
	bag                 *diag.Bag
	mergeExpr           string
	enclosingAttrsBound bool
	positionalPlan      componentPositionalPackagePlan
	errReturn           string
}

type positionalLoweringOutcome uint8

const (
	positionalLoweringUnsupported positionalLoweringOutcome = iota
	positionalLoweringReady
	positionalLoweringDiagnosed
)

// positionalValueLowering makes failure ownership explicit. Diagnosed means a
// helper already emitted the precise user-facing diagnostic; unsupported means
// the caller still owns the generic internal lowering diagnostic.
type positionalValueLowering struct {
	expr    string
	used    map[string]string
	outcome positionalLoweringOutcome
}

func readyPositionalValue(expr string, used map[string]string) positionalValueLowering {
	return positionalValueLowering{expr: expr, used: used, outcome: positionalLoweringReady}
}

func diagnosedPositionalValue() positionalValueLowering {
	return positionalValueLowering{outcome: positionalLoweringDiagnosed}
}

func (ctx positionalEmitContext) errorReturn() string {
	if ctx.errReturn != "" {
		return ctx.errReturn
	}
	return "return _gsxerr"
}

func (ctx positionalEmitContext) pipeWrap(b *bytes.Buffer) func(string) string {
	return pipeWrapReturning(b, ctx.interpTemp, ctx.errorReturn())
}

// emitPositionalComponentCall lowers one semantically completed call plan. It
// owns only the call assembly and source-order materialization; target/signature
// eligibility, inference, assignment, and zero spelling were already proved by
// planComponentPositionalCalls.
func emitPositionalComponentCall(
	b *bytes.Buffer,
	el *gsxast.Element,
	plan componentPositionalSitePlan,
	ctx positionalEmitContext,
) bool {
	values := make(map[int]string, len(plan.call.values))
	adapters := make(map[int]componentOperandAdapter, len(plan.operands))
	for _, operand := range plan.operands {
		adapters[operand.valueIndex] = operand.adapter
	}
	type loweredValue struct {
		expr       string
		statements []byte
		ready      bool
	}
	lowered := make([]loweredValue, len(plan.call.values))
	for valueIndex, value := range plan.call.values {
		// Children are deferred Node closures, not eagerly evaluated call-site
		// operands. The validated assembly owns their scalar/variadic lowering at
		// the final argument slots; lowering them here would build a duplicate,
		// unused closure and incorrectly subject it to eager materialization.
		if value.kind == componentInputBody {
			continue
		}
		var statements bytes.Buffer
		valueLowering := positionalValueExpr(&statements, value, plan, ctx)
		if valueLowering.outcome != positionalLoweringReady {
			if valueLowering.outcome == positionalLoweringDiagnosed {
				return false
			}
			ctx.bag.Errorf(value.node.Pos(), value.node.End(), "component-positional-emission",
				"component input %T is not yet lowered by positional emission", value.node)
			return false
		}
		expr, used := valueLowering.expr, valueLowering.used
		if exprAttr, ok := value.node.(*gsxast.ExprAttr); ok && value.attrsNode == nil && len(exprAttr.Stages) != 0 {
			var err error
			expr, used, err = lowerPipe(exprAttr.Expr, exprAttr.Stages, ctx.table, ctx.pipeWrap(&statements))
			if err != nil {
				ctx.bag.Errorf(exprAttr.Pos(), exprAttr.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
				return false
			}
		}
		for _, path := range used {
			ctx.imports[path] = true
		}
		lowered[valueIndex] = loweredValue{expr: expr, statements: bytes.Clone(statements.Bytes()), ready: true}
	}

	// The lowering buffers are the source of truth for eager statement effects.
	// This avoids a parallel syntax classifier that can drift whenever a lowerer
	// gains a new hoist (for example, a fallible non-final pipeline stage).
	materializationFacts := positionalMaterializationFacts(plan.call, plan.expressionFacts, plan.runtime)
	for valueIndex, result := range lowered {
		if !result.ready || len(result.statements) == 0 {
			continue
		}
		value := plan.call.values[valueIndex]
		fact := materializationFacts[value.node]
		fact.emitsStatements = true
		materializationFacts[value.node] = fact
	}
	materialization := planComponentMaterialization(plan.call, materializationFacts)
	materialized := make(map[int]materializedValue, len(materialization.values))
	for _, value := range materialization.values {
		materialized[value.valueIndex] = value
	}

	for valueIndex, value := range plan.call.values {
		if value.kind == componentInputBody {
			continue
		}
		result := lowered[valueIndex]
		if !result.ready {
			ctx.bag.Errorf(value.node.Pos(), value.node.End(), "component-positional-emission", "validated component input was not lowered")
			return false
		}
		b.Write(result.statements)
		decision := materialized[valueIndex]
		expr := result.expr
		switch {
		case decision.unwrapTuple:
			temp := nextPositionalArgumentTemp(ctx.interpTemp)
			fmt.Fprintf(b, "%s, _gsxerr := %s\n", temp, expr)
			fmt.Fprintf(b, "if _gsxerr != nil { %s }\n", ctx.errorReturn())
			expr = temp
		case decision.temp != "":
			temp := nextPositionalArgumentTemp(ctx.interpTemp)
			fmt.Fprintf(b, "%s := %s\n", temp, expr)
			expr = temp
		}
		expr = applyPositionalOperandAdapter(expr, adapters[valueIndex], ctx.rt)
		values[valueIndex] = normalizePositionalAttrsContributor(expr, value, plan, ctx)
	}

	args := make([]string, 0, len(plan.assembly.arguments))
	for _, argument := range plan.assembly.arguments {
		if argument.paramIndex < 0 || argument.paramIndex >= len(plan.call.args) {
			ctx.bag.Errorf(el.Pos(), el.End(), "component-positional-emission", "validated argument refers to an invalid parameter")
			return false
		}
		slot := plan.call.args[argument.paramIndex]
		switch argument.kind {
		case componentPositionalArgumentZero:
			args = append(args, argument.zero.expr)
		case componentPositionalArgumentProp:
			expr, ok := values[argument.valueIndex]
			if !ok {
				ctx.bag.Errorf(el.Pos(), el.End(), "component-positional-emission",
					"component parameter %q has no lowered authored value", slot.param.name)
				return false
			}
			args = append(args, expr)
		case componentPositionalArgumentChildren:
			value := plan.call.values[argument.valueIndex]
			nodes := value.children
			if argument.childIndex >= 0 {
				if argument.childIndex >= len(nodes) {
					ctx.bag.Errorf(el.Pos(), el.End(), "component-positional-emission", "validated children argument is outside the body")
					return false
				}
				nodes = []gsxast.Markup{nodes[argument.childIndex]}
			}
			expr, ok := positionalSlotClosure(nodes, ctx)
			if !ok {
				return false
			}
			args = append(args, expr)
		case componentPositionalArgumentAttrs:
			attrsArg, ok := positionalAttrsArg(slot, values, ctx)
			if !ok {
				return false
			}
			args = append(args, attrsArg)
		default:
			ctx.bag.Errorf(el.Pos(), el.End(), "component-positional-emission", "validated argument has an unknown kind")
			return false
		}
	}

	typeArgs := ""
	if len(plan.typeArgExprs) != 0 {
		typeArgs = "[" + strings.Join(plan.typeArgExprs, ", ") + "]"
	}
	fmt.Fprintf(b, "_gsxgw.Node(ctx, %s%s(%s))\n", el.Tag, typeArgs, strings.Join(args, ", "))
	return true
}

func applyPositionalOperandAdapter(expr string, adapter componentOperandAdapter, rt rtImports) string {
	switch adapter {
	case componentAdapterIdentity:
		return expr
	case componentAdapterNodeText:
		return rt.rt() + ".Text(" + expr + ")"
	case componentAdapterNodeVal:
		return rt.rt() + ".Val(" + expr + ")"
	default:
		panic(fmt.Sprintf("codegen: unknown component operand adapter %d", adapter))
	}
}

func normalizePositionalAttrsContributor(expr string, value componentInputValue, plan componentPositionalSitePlan, ctx positionalEmitContext) string {
	if value.attrsNode == nil || (value.attrsNode.kind != componentAttrsStreamSpread && value.attrsNode.kind != componentAttrsStreamContributor) {
		return expr
	}
	fact, ok := plan.expressionFacts[value.node]
	if !ok || fact.tv.Type == nil {
		return expr // planning already owns the missing-fact diagnostic
	}
	typ := fact.tv.Type
	if fact.tuple != nil {
		var valid bool
		typ, valid = tupleUnwrapType(fact.tuple)
		if !valid {
			return expr // operand validation already owns the tuple diagnostic
		}
	}
	if plan.runtime.attrs == nil || types.AssignableTo(typ, plan.runtime.attrs) {
		return expr
	}
	return ctx.rt.rt() + ".Attrs(" + expr + ")"
}

func nextPositionalArgumentTemp(counter *int) string {
	name := fmt.Sprintf("_gsxa%d", *counter)
	*counter++
	return name
}

func positionalValueExpr(b *bytes.Buffer, value componentInputValue, plan componentPositionalSitePlan, ctx positionalEmitContext) positionalValueLowering {
	if value.attrsNode != nil {
		return positionalAttrsValueExpr(b, *value.attrsNode, plan, ctx)
	}
	switch node := value.node.(type) {
	case *gsxast.StaticAttr:
		return readyPositionalValue(strconv.Quote(node.Value), nil)
	case *gsxast.BoolAttr:
		return readyPositionalValue("true", nil)
	case *gsxast.ExprAttr:
		return readyPositionalValue(strings.TrimSpace(node.Expr), nil)
	case *gsxast.MarkupAttr:
		expr, ok := positionalSlotClosure(node.Value, ctx)
		if !ok {
			return diagnosedPositionalValue()
		}
		return readyPositionalValue(expr, nil)
	case *gsxast.OrderedAttrsAttr:
		return positionalOrderedAttrsExpr(b, node, plan, ctx)
	case *gsxast.ClassAttr:
		if node.Name == "style" {
			expr, _, ok := rootStyleString(b, node, nil, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.resolved)
			if !ok {
				return diagnosedPositionalValue()
			}
			return readyPositionalValue(expr, nil)
		}
		expr, used, err := classEntryExpr(b, ctx.interpTemp, node, ctx.rt.rt(), classMergeExpr(ctx.mergeExpr, ctx.rt), ctx.table, ctx.resolved, false, ctx.pipeWrap(b), ctx.errorReturn())
		if err != nil {
			positionalAttrsError(node, err, ctx)
			return diagnosedPositionalValue()
		}
		return readyPositionalValue(expr, used)
	case *gsxast.EmbeddedAttr:
		return positionalEmbeddedValueExpr(b, node, ctx)
	case *gsxast.Element:
		if value.kind != componentInputBody {
			return positionalValueLowering{outcome: positionalLoweringUnsupported}
		}
		expr, ok := positionalSlotClosure(value.children, ctx)
		if !ok {
			return diagnosedPositionalValue()
		}
		return readyPositionalValue(expr, nil)
	default:
		return positionalValueLowering{outcome: positionalLoweringUnsupported}
	}
}

func positionalAttrsValueExpr(b *bytes.Buffer, node componentAttrsStreamNode, plan componentPositionalSitePlan, ctx positionalEmitContext) positionalValueLowering {
	switch node.kind {
	case componentAttrsStreamPair, componentAttrsStreamSpread:
		if embedded, ok := node.attr.(*gsxast.EmbeddedAttr); ok && (embedded.Lang == gsxast.EmbeddedJS || embedded.Lang == gsxast.EmbeddedCSS) {
			lowering := positionalEmbeddedValueExpr(b, embedded, ctx)
			if lowering.outcome != positionalLoweringReady {
				return lowering
			}
			return readyPositionalValue(fmt.Sprintf("%s.Attrs{{Key: %s, Value: %s}}", ctx.rt.rt(), strconv.Quote(embedded.Name), lowering.expr), nil)
		}
		expr, used, err := composeBag(b, ctx.interpTemp, ctx.pipeWrap(b), false, []gsxast.Attr{node.attr}, ctx.rt.rt(), plan.call.call.Tag, classMergeExpr(ctx.mergeExpr, ctx.rt), ctx.table, ctx.resolved, ctx.imports, ctx.rt, ctx.bag, ctx.errorReturn(), bagComponentCond)
		if err != nil {
			positionalAttrsError(node.attr, err, ctx)
			return diagnosedPositionalValue()
		}
		return readyPositionalValue(expr, used)
	case componentAttrsStreamContributor:
		switch attr := node.attr.(type) {
		case *gsxast.ExprAttr:
			expr := strings.TrimSpace(attr.Expr)
			used := map[string]string(nil)
			if len(attr.Stages) != 0 {
				var err error
				expr, used, err = lowerPipe(attr.Expr, attr.Stages, ctx.table, ctx.pipeWrap(b))
				if err != nil {
					ctx.bag.Errorf(attr.Pos(), attr.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
					return diagnosedPositionalValue()
				}
			}
			return readyPositionalValue(expr, used)
		case *gsxast.OrderedAttrsAttr:
			return positionalOrderedAttrsExpr(b, attr, plan, ctx)
		case *gsxast.EmbeddedAttr:
			return positionalEmbeddedValueExpr(b, attr, ctx)
		default:
			return positionalValueLowering{outcome: positionalLoweringUnsupported}
		}
	case componentAttrsStreamConditional:
		return positionalConditionalAttrsExpr(b, node, plan, ctx)
	default:
		return positionalValueLowering{outcome: positionalLoweringUnsupported}
	}
}

func positionalAttrsError(node gsxast.Node, err error, ctx positionalEmitContext) {
	if err == nil || err == errBagDiagReported {
		return
	}
	if ae, ok := err.(*attrError); ok {
		ctx.bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
		return
	}
	ctx.bag.Errorf(node.Pos(), node.End(), "component-positional-emission", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
}

func positionalEmbeddedValueExpr(b *bytes.Buffer, attr *gsxast.EmbeddedAttr, ctx positionalEmitContext) positionalValueLowering {
	switch attr.Lang {
	case gsxast.EmbeddedText:
		expr, ok := componentEmbeddedTextValueExpr(b, attr, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.errorReturn())
		if !ok {
			return diagnosedPositionalValue()
		}
		return readyPositionalValue(expr, nil)
	case gsxast.EmbeddedJS:
		expr, ok := embeddedJSValueExpr(b, attr.Segments, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.errorReturn(), false, false)
		if !ok {
			return diagnosedPositionalValue()
		}
		lowering := positionalEmbeddedPipeline(b, attr, expr, ctx)
		if lowering.outcome != positionalLoweringReady {
			return lowering
		}
		return readyPositionalValue(ctx.rt.rt()+".RawJS("+lowering.expr+")", nil)
	case gsxast.EmbeddedCSS:
		expr, ok := embeddedCSSValueExpr(b, attr.Segments, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.errorReturn(), false, false)
		if !ok {
			return diagnosedPositionalValue()
		}
		lowering := positionalEmbeddedPipeline(b, attr, expr, ctx)
		if lowering.outcome != positionalLoweringReady {
			return lowering
		}
		return readyPositionalValue(ctx.rt.rt()+".RawCSS("+lowering.expr+")", nil)
	default:
		ctx.bag.Errorf(attr.Pos(), attr.End(), "component-positional-emission", "unknown embedded literal language %d", attr.Lang)
		return diagnosedPositionalValue()
	}
}

func positionalEmbeddedPipeline(b *bytes.Buffer, attr *gsxast.EmbeddedAttr, expr string, ctx positionalEmitContext) positionalValueLowering {
	if len(attr.Stages) == 0 {
		return readyPositionalValue(expr, nil)
	}
	lowered, used, err := lowerPipe(expr, attr.Stages, ctx.table, ctx.pipeWrap(b))
	if err != nil {
		ctx.bag.Errorf(attr.Pos(), attr.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return diagnosedPositionalValue()
	}
	for _, path := range used {
		ctx.imports[path] = true
	}
	if fact, exists := ctx.resolved[attr]; exists {
		if tuple, isTuple := fact.(*types.Tuple); isTuple {
			if _, valid := tupleUnwrapType(tuple); !valid {
				ctx.bag.Errorf(attr.Pos(), attr.End(), "invalid-tuple", "component attribute %q pipeline returns %s; only (T, error) is supported", attr.Name, tuple)
				return diagnosedPositionalValue()
			}
			lowered = hoistTupleReturning(b, lowered, ctx.interpTemp, ctx.errorReturn())
		}
	}
	return readyPositionalValue(lowered, nil)
}

func positionalConditionalAttrsExpr(b *bytes.Buffer, node componentAttrsStreamNode, plan componentPositionalSitePlan, ctx positionalEmitContext) positionalValueLowering {
	cond, ok := node.attr.(*gsxast.CondAttr)
	if !ok {
		return positionalValueLowering{outcome: positionalLoweringUnsupported}
	}
	thenLowering := positionalAttrsBranchThunk(node.then, plan, ctx)
	if thenLowering.outcome != positionalLoweringReady {
		return thenLowering
	}
	elseExpr := "nil"
	used := thenLowering.used
	if len(node.otherwise) != 0 {
		elseLowering := positionalAttrsBranchThunk(node.otherwise, plan, ctx)
		if elseLowering.outcome != positionalLoweringReady {
			return elseLowering
		}
		elseExpr = elseLowering.expr
		if used == nil {
			used = make(map[string]string)
		}
		maps.Copy(used, elseLowering.used)
	}
	expr := fmt.Sprintf("%s.AttrsCond(%s, %s, %s)", ctx.rt.rt(), strings.TrimSpace(cond.Cond), thenLowering.expr, elseExpr)
	name := fmt.Sprintf("_gsxv%d", *ctx.interpTemp)
	*ctx.interpTemp++
	fmt.Fprintf(b, "%s, _gsxerr := %s\n", name, expr)
	fmt.Fprintf(b, "if _gsxerr != nil { %s }\n", ctx.errorReturn())
	return readyPositionalValue(name, used)
}

func positionalAttrsBranchThunk(nodes []componentAttrsStreamNode, plan componentPositionalSitePlan, ctx positionalEmitContext) positionalValueLowering {
	var body bytes.Buffer
	ctx.errReturn = "return nil, _gsxerr"
	parts := make([]string, 0, len(nodes))
	used := make(map[string]string)
	attrsExpr := func(parts []string) string {
		switch len(parts) {
		case 0:
			return ctx.rt.rt() + ".Attrs{}"
		case 1:
			return parts[0]
		default:
			return fmt.Sprintf("%s.ConcatAttrs(%s)", ctx.rt.rt(), strings.Join(parts, ", "))
		}
	}
	for _, node := range nodes {
		var statements bytes.Buffer
		lowering := positionalAttrsValueExpr(&statements, node, plan, ctx)
		if lowering.outcome != positionalLoweringReady {
			return lowering
		}
		// The lowering buffer is authoritative for eager statement work, just as
		// it is for the outer positional call. If this contributor emitted any,
		// evaluate every earlier contributor before those statements; otherwise
		// the earlier expressions would remain deferred in the final return and
		// execute after the later contributor's hoists.
		if statements.Len() != 0 && len(parts) != 0 {
			name := fmt.Sprintf("_gsxv%d", *ctx.interpTemp)
			*ctx.interpTemp++
			fmt.Fprintf(&body, "%s := %s\n", name, attrsExpr(parts))
			parts = []string{name}
		}
		body.Write(statements.Bytes())
		parts = append(parts, lowering.expr)
		maps.Copy(used, lowering.used)
	}
	expr := attrsExpr(parts)
	var thunk strings.Builder
	fmt.Fprintf(&thunk, "func() (%s.Attrs, error) {\n", ctx.rt.rt())
	for line := range strings.SplitSeq(strings.TrimSuffix(body.String(), "\n"), "\n") {
		if line != "" {
			thunk.WriteString("\t")
			thunk.WriteString(line)
			thunk.WriteByte('\n')
		}
	}
	fmt.Fprintf(&thunk, "\treturn %s, nil\n} ", expr)
	return readyPositionalValue(strings.TrimSpace(thunk.String()), used)
}

func positionalSlotClosure(nodes []gsxast.Markup, ctx positionalEmitContext) (string, bool) {
	return emitSlotClosure(nodes, ctx.currentPkg, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.importAliases, ctx.boundNames, ctx.typeArgAliases, ctx.interpTemp, ctx.fset, ctx.recvVar, ctx.recvTypeName, ctx.cls, ctx.bag, ctx.mergeExpr, ctx.enclosingAttrsBound, ctx.positionalPlan)
}

func positionalOrderedAttrsExpr(b *bytes.Buffer, attr *gsxast.OrderedAttrsAttr, plan componentPositionalSitePlan, ctx positionalEmitContext) positionalValueLowering {
	entries := make([]string, 0, len(attr.Pairs))
	for i := range attr.Pairs {
		pair := &attr.Pairs[i]
		expr := strings.TrimSpace(pair.Value)
		fact, hasFact := plan.expressionFacts[pair]
		// The pair value's semantic type drives renderer application below. A
		// (T, error) authored value is unwrapped first (matching every other
		// emit site), leaving the renderer to act on the unwrapped T.
		var valueType types.Type
		if hasFact {
			valueType = fact.tv.Type
		}
		if hasFact && fact.tuple != nil {
			unwrapped, valid := tupleUnwrapType(fact.tuple)
			if !valid {
				ctx.bag.Errorf(pair.Pos(), pair.End(), "invalid-tuple", "ordered attrs value %q returns %s; only (T, error) is supported", pair.Value, fact.tuple)
				return diagnosedPositionalValue()
			}
			name := fmt.Sprintf("_gsxv%d", *ctx.interpTemp)
			*ctx.interpTemp++
			fmt.Fprintf(b, "%s, _gsxerr := %s\n", name, expr)
			fmt.Fprintf(b, "if _gsxerr != nil { %s }\n", ctx.errorReturn())
			expr = name
			valueType = unwrapped
		}
		// A registered [renderers] entry for the value's type rewrites the
		// expression into the renderer call, exactly as applyRenderer does at
		// every other render boundary — so a renderer-typed value in an
		// attrs={{…}} bag renders identically to the same value inline. Without
		// this the raw value reaches the Attrs pair and renders via Go %v.
		if valueType != nil {
			expr, _ = applyRenderer(b, expr, valueType, ctx.table, ctx.imports, ctx.interpTemp, ctx.errorReturn())
		}
		entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(pair.Key), expr))
	}
	return readyPositionalValue(fmt.Sprintf("%s.Attrs{%s}", ctx.rt.rt(), strings.Join(entries, ", ")), nil)
}

func positionalAttrsArg(slot componentArgSlot, values map[int]string, ctx positionalEmitContext) (string, bool) {
	parts := make([]string, 0, len(slot.valueIndexes))
	for _, valueIndex := range slot.valueIndexes {
		expr, ok := values[valueIndex]
		if !ok {
			ctx.bag.Errorf(slot.param.variable.Pos(), slot.param.variable.Pos(), "component-positional-emission", "attrs contributor has no lowered value")
			return "", false
		}
		parts = append(parts, expr)
	}
	expr := ctx.rt.rt() + ".Attrs{}"
	if len(parts) == 1 {
		expr = parts[0]
	} else if len(parts) > 1 {
		expr = fmt.Sprintf("%s.ConcatAttrs(%s)", ctx.rt.rt(), strings.Join(parts, ", "))
	}
	switch slot.param.attrsMode {
	case attrsDirect:
		return expr, true
	case attrsDefinedSlice:
		return "[]" + ctx.rt.rt() + ".Attr(" + expr + ")", true
	case attrsVariadic:
		return expr + "...", true
	default:
		ctx.bag.Errorf(slot.param.variable.Pos(), slot.param.variable.Pos(), "component-positional-emission", "unknown attrs parameter mode")
		return "", false
	}
}
