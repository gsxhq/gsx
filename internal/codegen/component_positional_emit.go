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
		expr, used, ok := positionalValueExpr(&statements, value, plan, ctx)
		if !ok {
			ctx.bag.Errorf(value.node.Pos(), value.node.End(), "component-positional-emission",
				"component input %T is not yet lowered by positional emission", value.node)
			return false
		}
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

func positionalValueExpr(b *bytes.Buffer, value componentInputValue, plan componentPositionalSitePlan, ctx positionalEmitContext) (string, map[string]string, bool) {
	if value.attrsNode != nil {
		return positionalAttrsValueExpr(b, *value.attrsNode, plan, ctx)
	}
	switch node := value.node.(type) {
	case *gsxast.StaticAttr:
		return strconv.Quote(node.Value), nil, true
	case *gsxast.BoolAttr:
		return "true", nil, true
	case *gsxast.ExprAttr:
		return strings.TrimSpace(node.Expr), nil, true
	case *gsxast.MarkupAttr:
		expr, ok := positionalSlotClosure(node.Value, ctx)
		return expr, nil, ok
	case *gsxast.OrderedAttrsAttr:
		expr, ok := positionalOrderedAttrsExpr(b, node, plan, ctx)
		return expr, nil, ok
	case *gsxast.ClassAttr:
		if node.Name == "style" {
			expr, _, ok := rootStyleString(b, node, nil, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.resolved)
			return expr, nil, ok
		}
		expr, used, err := classEntryExpr(b, ctx.interpTemp, node, ctx.rt.rt(), classMergeExpr(ctx.mergeExpr, ctx.rt), ctx.table, ctx.resolved, false, ctx.pipeWrap(b), ctx.errorReturn())
		if err != nil {
			positionalAttrsError(node, err, ctx)
			return "", nil, false
		}
		return expr, used, true
	case *gsxast.EmbeddedAttr:
		expr, ok := positionalEmbeddedValueExpr(b, node, ctx)
		return expr, nil, ok
	case *gsxast.Element:
		if value.kind != componentInputBody {
			return "", nil, false
		}
		expr, ok := positionalSlotClosure(value.children, ctx)
		return expr, nil, ok
	default:
		return "", nil, false
	}
}

func positionalAttrsValueExpr(b *bytes.Buffer, node componentAttrsStreamNode, plan componentPositionalSitePlan, ctx positionalEmitContext) (string, map[string]string, bool) {
	switch node.kind {
	case componentAttrsStreamPair, componentAttrsStreamSpread:
		if embedded, ok := node.attr.(*gsxast.EmbeddedAttr); ok && (embedded.Lang == gsxast.EmbeddedJS || embedded.Lang == gsxast.EmbeddedCSS) {
			expr, ok := positionalEmbeddedValueExpr(b, embedded, ctx)
			if !ok {
				return "", nil, false
			}
			return fmt.Sprintf("%s.Attrs{{Key: %s, Value: %s}}", ctx.rt.rt(), strconv.Quote(embedded.Name), expr), nil, true
		}
		expr, used, err := composeBag(b, ctx.interpTemp, ctx.pipeWrap(b), false, []gsxast.Attr{node.attr}, ctx.rt.rt(), plan.call.call.Tag, classMergeExpr(ctx.mergeExpr, ctx.rt), ctx.table, ctx.resolved, ctx.imports, ctx.rt, ctx.bag, ctx.errorReturn(), bagComponentCond)
		if err != nil {
			positionalAttrsError(node.attr, err, ctx)
			return "", nil, false
		}
		return expr, used, true
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
					return "", nil, false
				}
			}
			return expr, used, true
		case *gsxast.OrderedAttrsAttr:
			expr, ok := positionalOrderedAttrsExpr(b, attr, plan, ctx)
			return expr, nil, ok
		case *gsxast.EmbeddedAttr:
			expr, ok := positionalEmbeddedValueExpr(b, attr, ctx)
			return expr, nil, ok
		default:
			return "", nil, false
		}
	case componentAttrsStreamConditional:
		return positionalConditionalAttrsExpr(b, node, plan, ctx)
	default:
		return "", nil, false
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

func positionalEmbeddedValueExpr(b *bytes.Buffer, attr *gsxast.EmbeddedAttr, ctx positionalEmitContext) (string, bool) {
	switch attr.Lang {
	case gsxast.EmbeddedText:
		return componentEmbeddedTextValueExpr(b, attr, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.errorReturn())
	case gsxast.EmbeddedJS:
		expr, ok := embeddedJSValueExpr(b, attr.Segments, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.errorReturn(), false, false)
		expr, ok = positionalEmbeddedPipeline(b, attr, expr, ok, ctx)
		if !ok {
			return "", false
		}
		return ctx.rt.rt() + ".RawJS(" + expr + ")", true
	case gsxast.EmbeddedCSS:
		expr, ok := embeddedCSSValueExpr(b, attr.Segments, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.interpTemp, ctx.bag, ctx.errorReturn(), false, false)
		expr, ok = positionalEmbeddedPipeline(b, attr, expr, ok, ctx)
		if !ok {
			return "", false
		}
		return ctx.rt.rt() + ".RawCSS(" + expr + ")", true
	default:
		ctx.bag.Errorf(attr.Pos(), attr.End(), "component-positional-emission", "unknown embedded literal language %d", attr.Lang)
		return "", false
	}
}

func positionalEmbeddedPipeline(b *bytes.Buffer, attr *gsxast.EmbeddedAttr, expr string, ok bool, ctx positionalEmitContext) (string, bool) {
	if !ok || len(attr.Stages) == 0 {
		return expr, ok
	}
	lowered, used, err := lowerPipe(expr, attr.Stages, ctx.table, ctx.pipeWrap(b))
	if err != nil {
		ctx.bag.Errorf(attr.Pos(), attr.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return "", false
	}
	for _, path := range used {
		ctx.imports[path] = true
	}
	if fact, exists := ctx.resolved[attr]; exists {
		if tuple, isTuple := fact.(*types.Tuple); isTuple {
			if _, valid := tupleUnwrapType(tuple); !valid {
				ctx.bag.Errorf(attr.Pos(), attr.End(), "invalid-tuple", "component attribute %q pipeline returns %s; only (T, error) is supported", attr.Name, tuple)
				return "", false
			}
			lowered = hoistTupleReturning(b, lowered, ctx.interpTemp, ctx.errorReturn())
		}
	}
	return lowered, true
}

func positionalConditionalAttrsExpr(b *bytes.Buffer, node componentAttrsStreamNode, plan componentPositionalSitePlan, ctx positionalEmitContext) (string, map[string]string, bool) {
	cond, ok := node.attr.(*gsxast.CondAttr)
	if !ok {
		return "", nil, false
	}
	thenExpr, thenUsed, ok := positionalAttrsBranchThunk(node.then, plan, ctx)
	if !ok {
		return "", nil, false
	}
	elseExpr := "nil"
	used := thenUsed
	if len(node.otherwise) != 0 {
		var elseUsed map[string]string
		elseExpr, elseUsed, ok = positionalAttrsBranchThunk(node.otherwise, plan, ctx)
		if !ok {
			return "", nil, false
		}
		if used == nil {
			used = make(map[string]string)
		}
		maps.Copy(used, elseUsed)
	}
	expr := fmt.Sprintf("%s.AttrsCond(%s, %s, %s)", ctx.rt.rt(), strings.TrimSpace(cond.Cond), thenExpr, elseExpr)
	name := fmt.Sprintf("_gsxv%d", *ctx.interpTemp)
	*ctx.interpTemp++
	fmt.Fprintf(b, "%s, _gsxerr := %s\n", name, expr)
	fmt.Fprintf(b, "if _gsxerr != nil { %s }\n", ctx.errorReturn())
	return name, used, true
}

func positionalAttrsBranchThunk(nodes []componentAttrsStreamNode, plan componentPositionalSitePlan, ctx positionalEmitContext) (string, map[string]string, bool) {
	var body bytes.Buffer
	ctx.errReturn = "return nil, _gsxerr"
	parts := make([]string, 0, len(nodes))
	used := make(map[string]string)
	for _, node := range nodes {
		expr, nodeUsed, ok := positionalAttrsValueExpr(&body, node, plan, ctx)
		if !ok {
			return "", nil, false
		}
		parts = append(parts, expr)
		maps.Copy(used, nodeUsed)
	}
	expr := ctx.rt.rt() + ".Attrs{}"
	if len(parts) == 1 {
		expr = parts[0]
	} else if len(parts) > 1 {
		expr = fmt.Sprintf("%s.ConcatAttrs(%s)", ctx.rt.rt(), strings.Join(parts, ", "))
	}
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
	return strings.TrimSpace(thunk.String()), used, true
}

func positionalSlotClosure(nodes []gsxast.Markup, ctx positionalEmitContext) (string, bool) {
	return emitSlotClosure(nodes, ctx.currentPkg, ctx.resolved, ctx.table, ctx.imports, ctx.rt, ctx.importAliases, ctx.boundNames, ctx.typeArgAliases, ctx.interpTemp, ctx.fset, ctx.recvVar, ctx.recvTypeName, ctx.cls, ctx.bag, ctx.mergeExpr, ctx.enclosingAttrsBound, ctx.positionalPlan)
}

func positionalOrderedAttrsExpr(b *bytes.Buffer, attr *gsxast.OrderedAttrsAttr, plan componentPositionalSitePlan, ctx positionalEmitContext) (string, bool) {
	entries := make([]string, 0, len(attr.Pairs))
	for i := range attr.Pairs {
		pair := &attr.Pairs[i]
		expr := strings.TrimSpace(pair.Value)
		if fact, ok := plan.expressionFacts[pair]; ok && fact.tuple != nil {
			if _, valid := tupleUnwrapType(fact.tuple); !valid {
				ctx.bag.Errorf(pair.Pos(), pair.End(), "invalid-tuple", "ordered attrs value %q returns %s; only (T, error) is supported", pair.Value, fact.tuple)
				return "", false
			}
			name := fmt.Sprintf("_gsxv%d", *ctx.interpTemp)
			*ctx.interpTemp++
			fmt.Fprintf(b, "%s, _gsxerr := %s\n", name, expr)
			fmt.Fprintf(b, "if _gsxerr != nil { %s }\n", ctx.errorReturn())
			expr = name
		}
		entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(pair.Key), expr))
	}
	return fmt.Sprintf("%s.Attrs{%s}", ctx.rt.rt(), strings.Join(entries, ", ")), true
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
