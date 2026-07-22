package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/constant"
	goparser "go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"maps"
	"sort"
	"strconv"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

type componentPositionalPlanningInput struct {
	callSites       *callSiteRegistry
	targets         map[callSiteID]componentTargetFact
	expressionFacts map[gsxast.Node]expressionFact
	runtime         runtimeContract
	analysisPackage *types.Package
	fset            *token.FileSet
}

type componentPositionalPackagePlan struct {
	sites     map[callSiteID]componentPositionalSitePlan
	byElement map[*gsxast.Element]callSiteID
	imports   map[string]*generatedImportAllocator
}

type componentPositionalSitePlan struct {
	runtime         runtimeContract
	call            componentCallPlan
	target          componentTargetFact
	instance        types.Instance
	signature       componentSignatureModel
	typeArgs        []types.Type
	typeArgExprs    []string
	operands        []suppliedOperand
	expressionFacts expressionFactSet
	zeros           []componentZeroArgument
	assembly        componentPositionalAssembly
	directTarget    *directComponentFamily
}

type componentZeroArgument struct {
	paramIndex int
	expr       string
}

type componentPositionalArgumentKind uint8

const (
	componentPositionalArgumentZero componentPositionalArgumentKind = iota
	componentPositionalArgumentProp
	componentPositionalArgumentChildren
	componentPositionalArgumentAttrs
)

// componentPositionalArgument is one argument in the final emitted call.
// Variadic children produce one entry per child; an attrs entry retains whether
// the one composed bag is expanded. Both semantic validation and emission
// consume this exact ordered artifact.
type componentPositionalArgument struct {
	kind       componentPositionalArgumentKind
	paramIndex int
	valueIndex int
	childIndex int
	operand    suppliedOperand
	zero       componentZeroArgument
}

type componentPositionalAssembly struct {
	arguments []componentPositionalArgument
}

func (p componentPositionalPackagePlan) siteForElement(element *gsxast.Element) (componentPositionalSitePlan, bool) {
	site, ok := p.byElement[element]
	if !ok {
		return componentPositionalSitePlan{}, false
	}
	plan, ok := p.sites[site]
	return plan, ok
}

func planComponentPositionalCalls(input componentPositionalPlanningInput) (componentPositionalPackagePlan, []diag.Diagnostic) {
	result := componentPositionalPackagePlan{
		sites:     make(map[callSiteID]componentPositionalSitePlan),
		byElement: make(map[*gsxast.Element]callSiteID),
		imports:   make(map[string]*generatedImportAllocator),
	}
	if input.callSites == nil || input.analysisPackage == nil || input.fset == nil {
		return result, []diag.Diagnostic{{
			Severity: diag.Error,
			Code:     "component-positional-plan",
			Message:  "component positional planning requires call sites, the analysis package, and its FileSet",
			Source:   "codegen",
		}}
	}

	var diagnostics []diag.Diagnostic
	for _, record := range input.callSites.records {
		if record.disposition != componentSitePlanned {
			continue
		}
		fact, ok := input.targets[record.id]
		if !ok {
			diagnostics = append(diagnostics, positionalDiagnostic(record.element, input.fset, "component-target-fact", "component target has no semantic discovery fact"))
			continue
		}
		if fact.raw == nil {
			if len(fact.targetDiags) != 0 {
				diagnostics = append(diagnostics, fact.targetDiags...)
			} else {
				diagnostics = append(diagnostics, positionalDiagnostic(record.element, input.fset, "invalid-component-target", "component target has no callable signature"))
			}
			continue
		}

		originModel, err := analyzeComponentSignature(fact.raw, input.runtime)
		if err != nil {
			diagnostics = append(diagnostics, positionalSignatureDiagnostic(record.element, input.fset, err))
			continue
		}
		call, callDiags := planComponentInputs(record.id, record.element, originModel, input.fset)
		if len(callDiags) != 0 {
			diagnostics = append(diagnostics, callDiags...)
			continue
		}
		siteFacts, factDiags := completeComponentOperandFacts(call, input.expressionFacts, input.runtime, input.fset)
		call, operandDiags := validateComponentOperands(call, siteFacts, input.runtime)
		operandDiags = append(operandDiags, validateRecursiveAttrsOperands(call, siteFacts, input.runtime)...)
		operandDiags = append(operandDiags, validateRequiredAttrsFacts(call, siteFacts, input.fset)...)
		operands, suppliedDiags := positionalSuppliedOperands(call, siteFacts, input.runtime, input.fset)
		factDiags = append(factDiags, suppliedDiags...)
		operandDiags = append(operandDiags, factDiags...)
		if len(operandDiags) != 0 {
			diagnostics = append(diagnostics, operandDiags...)
			continue
		}
		// Target/type-argument diagnostics are deliberately deferred until syntax
		// and authored operands have been checked. This preserves the specified
		// diagnostic precedence without allowing an invalid target to reach
		// inference or zero-fill.
		if fact.provenance == 0 || len(fact.targetDiags) != 0 {
			if len(fact.targetDiags) != 0 {
				diagnostics = append(diagnostics, fact.targetDiags...)
			} else {
				diagnostics = append(diagnostics, positionalDiagnostic(record.element, input.fset, "invalid-component-target", "component target provenance is not callable from markup"))
			}
			continue
		}

		instance, inferenceDiags := inferAuthoredInstance(inferenceContext{
			pkg:   input.analysisPackage,
			fset:  input.fset,
			scope: input.analysisPackage.Scope().Innermost(fact.exprPos()),
			tag:   record.element.Tag,
		}, fact, operands)
		if len(inferenceDiags) != 0 {
			diagnostics = append(diagnostics, inferenceDiags...)
			continue
		}
		instantiated, ok := instance.Type.(*types.Signature)
		if !ok {
			diagnostics = append(diagnostics, positionalDiagnostic(record.element, input.fset, "component-inference", "component inference did not produce a callable signature"))
			continue
		}
		model, err := analyzeComponentSignature(instantiated, input.runtime)
		if err != nil {
			diagnostics = append(diagnostics, positionalSignatureDiagnostic(record.element, input.fset, err))
			continue
		}
		call.target = model
		for i := range call.args {
			call.args[i].param = model.params[i]
		}
		assignmentDiags := validatePositionalOperandAssignments(call, operands, input.analysisPackage, input.fset)
		if len(assignmentDiags) != 0 {
			diagnostics = append(diagnostics, assignmentDiags...)
			continue
		}

		allocator := result.imports[record.path]
		if allocator == nil {
			allocator = newGeneratedImportAllocator("_gsxty")
			result.imports[record.path] = allocator
		}
		typeArgs, typeArgExprs, typeArgDiags := planComponentTypeArguments(fact, instance, input.analysisPackage, allocator, input.fset)
		if len(typeArgDiags) != 0 {
			diagnostics = append(diagnostics, typeArgDiags...)
			continue
		}
		zeros, zeroDiags := planComponentZeros(call, fact, input.analysisPackage, input.fset, allocator)
		if len(zeroDiags) != 0 {
			diagnostics = append(diagnostics, zeroDiags...)
			continue
		}
		assembly, err := assemblePositionalCall(call, operands, zeros)
		if err != nil {
			diagnostics = append(diagnostics, positionalDiagnostic(record.element, input.fset, "component-positional-call", fmt.Sprintf("cannot assemble completed component call: %v", err)))
			continue
		}
		if err := validateAssembledPositionalCall(call, assembly, input.runtime); err != nil {
			diagnostics = append(diagnostics, positionalDiagnostic(record.element, input.fset, "component-positional-call", fmt.Sprintf("completed component call is invalid: %v", err)))
			continue
		}
		result.sites[record.id] = componentPositionalSitePlan{
			runtime:         input.runtime,
			call:            call,
			target:          fact,
			instance:        instance,
			signature:       model,
			typeArgs:        typeArgs,
			typeArgExprs:    typeArgExprs,
			operands:        operands,
			expressionFacts: siteFacts,
			zeros:           zeros,
			assembly:        assembly,
			directTarget:    directComponentTarget(fact, input.analysisPackage),
		}
		result.byElement[record.element] = record.id
	}
	return result, diagnostics
}

func assemblePositionalCall(plan componentCallPlan, operands []suppliedOperand, zeros []componentZeroArgument) (componentPositionalAssembly, error) {
	var assembly componentPositionalAssembly
	byParam := make(map[int]suppliedOperand, len(operands))
	for _, operand := range operands {
		if _, exists := byParam[operand.paramIndex]; exists {
			return assembly, fmt.Errorf("parameter %d has more than one authored operand", operand.paramIndex)
		}
		byParam[operand.paramIndex] = operand
	}
	zeroByParam := make(map[int]componentZeroArgument, len(zeros))
	for _, zero := range zeros {
		if _, exists := zeroByParam[zero.paramIndex]; exists {
			return assembly, fmt.Errorf("parameter %d has more than one zero", zero.paramIndex)
		}
		zeroByParam[zero.paramIndex] = zero
	}
	consumedOperands := make(map[int]bool, len(operands))
	consumedZeros := make(map[int]bool, len(zeros))
	for paramIndex, slot := range plan.args {
		if slot.omitted {
			if plan.target.goSig.Variadic() && paramIndex == len(plan.args)-1 {
				continue
			}
			zero, ok := zeroByParam[paramIndex]
			if !ok {
				return assembly, fmt.Errorf("parameter %d is omitted without a zero", paramIndex)
			}
			consumedZeros[paramIndex] = true
			assembly.arguments = append(assembly.arguments, componentPositionalArgument{
				kind: componentPositionalArgumentZero, paramIndex: paramIndex, zero: zero,
			})
			continue
		}

		switch slot.param.role {
		case roleProp:
			if len(slot.valueIndexes) != 1 {
				return assembly, fmt.Errorf("ordinary parameter %d has %d planned values", paramIndex, len(slot.valueIndexes))
			}
			operand, ok := byParam[paramIndex]
			if !ok {
				return assembly, fmt.Errorf("ordinary parameter %d has no authored operand", paramIndex)
			}
			consumedOperands[paramIndex] = true
			assembly.arguments = append(assembly.arguments, componentPositionalArgument{
				kind: componentPositionalArgumentProp, paramIndex: paramIndex,
				valueIndex: slot.valueIndexes[0], childIndex: -1, operand: operand,
			})
		case roleChildren:
			if len(slot.valueIndexes) != 1 || slot.valueIndexes[0] < 0 || slot.valueIndexes[0] >= len(plan.values) {
				return assembly, fmt.Errorf("children parameter %d has an invalid body slot", paramIndex)
			}
			valueIndex := slot.valueIndexes[0]
			if plan.target.goSig.Variadic() && paramIndex == len(plan.args)-1 {
				for childIndex := range plan.values[valueIndex].children {
					assembly.arguments = append(assembly.arguments, componentPositionalArgument{
						kind: componentPositionalArgumentChildren, paramIndex: paramIndex,
						valueIndex: valueIndex, childIndex: childIndex,
					})
				}
			} else {
				assembly.arguments = append(assembly.arguments, componentPositionalArgument{
					kind: componentPositionalArgumentChildren, paramIndex: paramIndex,
					valueIndex: valueIndex, childIndex: -1,
				})
			}
		case roleAttrs:
			if len(slot.valueIndexes) == 0 {
				return assembly, fmt.Errorf("attrs parameter %d is populated without contributors", paramIndex)
			}
			assembly.arguments = append(assembly.arguments, componentPositionalArgument{
				kind: componentPositionalArgumentAttrs, paramIndex: paramIndex, childIndex: -1,
			})
		case roleGoOnlyVariadic:
			return assembly, fmt.Errorf("Go-only variadic parameter %d was populated from markup", paramIndex)
		default:
			return assembly, fmt.Errorf("parameter %d has unknown role %d", paramIndex, slot.param.role)
		}
	}
	if len(consumedOperands) != len(byParam) {
		return assembly, fmt.Errorf("%d authored operands were not assembled", len(byParam)-len(consumedOperands))
	}
	if len(consumedZeros) != len(zeroByParam) {
		return assembly, fmt.Errorf("%d planned zeros were not assembled", len(zeroByParam)-len(consumedZeros))
	}
	return assembly, nil
}

// validateAssembledPositionalCall asks go/types to check the one final call
// artifact later consumed by emission. Individual operand and zero checks own
// their positioned diagnostics; this check proves the completed artifact's
// arity, order, reserved-role conversions, and variadic expansion.
func validateAssembledPositionalCall(plan componentCallPlan, assembly componentPositionalAssembly, runtime runtimeContract) error {
	if plan.target.goSig == nil {
		return fmt.Errorf("missing instantiated signature")
	}

	probe := types.NewPackage("gsxcall", "gsxcall")
	sig := plan.target.goSig
	callSig := types.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic())
	probe.Scope().Insert(types.NewFunc(token.NoPos, probe, "_gsxcomponent", callSig))
	var args []string
	argN := 0
	addVar := func(typ types.Type) string {
		name := fmt.Sprintf("_gsxarg%d", argN)
		argN++
		probe.Scope().Insert(types.NewVar(token.NoPos, probe, name, typ))
		return name
	}
	addAlias := func(typ types.Type) string {
		name := fmt.Sprintf("_gsxtype%d", argN)
		argN++
		obj := types.NewTypeName(token.NoPos, probe, name, nil)
		types.NewAlias(obj, typ)
		probe.Scope().Insert(obj)
		return name
	}
	for _, argument := range assembly.arguments {
		if argument.paramIndex < 0 || argument.paramIndex >= len(plan.target.params) {
			return fmt.Errorf("argument refers to parameter %d outside the signature", argument.paramIndex)
		}
		param := plan.target.params[argument.paramIndex]
		switch argument.kind {
		case componentPositionalArgumentZero:
			if literal, ok := semanticZeroLiteral(param.typ); ok && argument.zero.expr == literal {
				args = append(args, literal)
			} else {
				// The exact emitted zero spelling was already checked in its lexical
				// package. Represent its proven value type here so this final check
				// remains semantic when that spelling names an outer type parameter
				// or a generated import alias unavailable in the throwaway package.
				args = append(args, addVar(param.typ))
			}
		case componentPositionalArgumentProp:
			args = append(args, assignmentOperandArgSource(argument.operand, probe, argN))
			argN++
		case componentPositionalArgumentChildren:
			args = append(args, addVar(runtime.node))
		case componentPositionalArgumentAttrs:
			attrs := addVar(runtime.attrs)
			switch param.attrsMode {
			case attrsDirect:
				args = append(args, attrs)
			case attrsDefinedSlice:
				args = append(args, "[]"+addAlias(runtime.attr)+"("+attrs+")")
			case attrsVariadic:
				args = append(args, attrs+"...")
			default:
				return fmt.Errorf("attrs parameter %d has unknown mode %d", argument.paramIndex, param.attrsMode)
			}
		default:
			return fmt.Errorf("argument for parameter %d has unknown kind %d", argument.paramIndex, argument.kind)
		}
	}

	source := "package gsxcall\nfunc _gsxprobe() { _gsxcomponent(" + strings.Join(args, ", ") + ") }\n"
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "call.go", source, goparser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse final call probe: %w", err)
	}
	var checkErrs []error
	config := types.Config{Error: func(err error) { checkErrs = append(checkErrs, err) }}
	types.NewChecker(&config, fset, probe, nil).Files([]*goast.File{file})
	if len(checkErrs) != 0 {
		return checkErrs[0]
	}
	return nil
}

func (f componentTargetFact) exprPos() token.Pos {
	if f.expr == nil {
		return token.NoPos
	}
	return f.expr.Pos()
}

func positionalDiagnostic(node gsxast.Node, fset *token.FileSet, code, message string) diag.Diagnostic {
	d := diag.Diagnostic{Severity: diag.Error, Code: code, Message: message, Source: "codegen"}
	if node != nil && fset != nil && node.Pos().IsValid() {
		d.Start = fset.Position(node.Pos())
		d.End = fset.Position(node.End())
	}
	return d
}

func positionalSignatureDiagnostic(node gsxast.Node, fset *token.FileSet, err error) diag.Diagnostic {
	message := err.Error()
	code := "component-signature"
	if before, _, ok := strings.Cut(message, ":"); ok && strings.HasPrefix(before, "component-") {
		code = before
	}
	return positionalDiagnostic(node, fset, code, message)
}

// expressionFactSet is a two-layer view of expression facts: a shared,
// immutable package-wide base map plus a small per-call overlay consulted
// first. It replaces per-call maps.Clone of the whole-package facts map, which
// was O(component-calls × package-facts) transient allocation (measured as 67%
// of all allocation churn on a 105-file package; see the perf investigation
// 2026-07-22). The base map is the whole-package `expressionFacts` harvested
// once per package (harvestComponentTargetExpressionFacts) and is NEVER mutated
// after construction — every per-call addition or override lands in the overlay.
// expressionFactSet layers a per-call overlay over a shared base of expression facts.
// The zero value is invalid; always construct via newExpressionFactSet or derive.
// The overlay must not be nil when set or derive are called.
type expressionFactSet struct {
	base    map[gsxast.Node]expressionFact // shared, immutable
	overlay map[gsxast.Node]expressionFact // per-call additions/overrides; must not be nil
}

// newExpressionFactSet layers an eagerly-allocated per-call overlay over the
// shared base. The overlay is allocated up front so the struct may be passed by
// value while still sharing one overlay map (set writes are visible to every
// copy). The base is shared, not cloned.
func newExpressionFactSet(base map[gsxast.Node]expressionFact) expressionFactSet {
	return expressionFactSet{base: base, overlay: make(map[gsxast.Node]expressionFact)}
}

// get consults the overlay first (so a per-call override wins) then the base.
func (s expressionFactSet) get(node gsxast.Node) (expressionFact, bool) {
	if fact, ok := s.overlay[node]; ok {
		return fact, true
	}
	fact, ok := s.base[node]
	return fact, ok
}

// set records a per-call addition or override in the overlay, never touching
// the shared base.
func (s expressionFactSet) set(node gsxast.Node, fact expressionFact) {
	s.overlay[node] = fact
}

// derive returns an independent set sharing the same base but with a fresh
// overlay seeded from this set's overlay. Mutations to the result never leak
// back to the parent. The clone is O(per-call overlay entries) — a handful —
// not O(package facts), which is the whole point.
func (s expressionFactSet) derive() expressionFactSet {
	return expressionFactSet{base: s.base, overlay: maps.Clone(s.overlay)}
}

func completeComponentOperandFacts(plan componentCallPlan, discovered map[gsxast.Node]expressionFact, runtime runtimeContract, fset *token.FileSet) (expressionFactSet, []diag.Diagnostic) {
	facts := newExpressionFactSet(discovered)
	var diagnostics []diag.Diagnostic

	var completeNode func(gsxast.Node) bool
	completeNode = func(node gsxast.Node) bool {
		if node == nil {
			return false
		}
		if _, ok := facts.get(node); ok {
			return true
		}
		fact, ok := syntaxDefinedComponentFact(node, facts, runtime)
		if !ok {
			return false
		}
		facts.set(node, fact)
		return true
	}
	var completeAttrsTree func(componentAttrsStreamNode)
	completeAttrsTree = func(node componentAttrsStreamNode) {
		if node.kind == componentAttrsStreamConditional {
			for _, child := range node.then {
				completeAttrsTree(child)
			}
			for _, child := range node.otherwise {
				completeAttrsTree(child)
			}
			if _, exists := facts.get(node.attr); !exists {
				facts.set(node.attr, expressionFact{tv: types.TypeAndValue{Type: runtime.attrs}, hasOrderedOperation: true})
			}
			return
		}
		completeNode(node.attr)
	}

	for _, value := range plan.values {
		if value.attrsNode != nil {
			completeAttrsTree(*value.attrsNode)
		}
		if value.kind == componentInputBody {
			facts.set(value.node, expressionFact{tv: types.TypeAndValue{Type: runtime.node}})
		}
		if !completeNode(value.node) {
			diagnostics = append(diagnostics, positionalDiagnostic(value.node, fset, "component-operand-fact", fmt.Sprintf("component input %T has no exact syntax-derived or go/types operand fact", value.node)))
			continue
		}
	}
	return facts, diagnostics
}

func syntaxDefinedComponentFact(node gsxast.Node, nested expressionFactSet, runtime runtimeContract) (expressionFact, bool) {
	switch node := node.(type) {
	case *gsxast.StaticAttr:
		return expressionFact{tv: types.TypeAndValue{Type: types.Typ[types.UntypedString], Value: constant.MakeString(node.Value)}}, true
	case *gsxast.BoolAttr:
		return expressionFact{tv: types.TypeAndValue{Type: types.Typ[types.UntypedBool], Value: constant.MakeBool(true)}}, true
	case *gsxast.MarkupAttr:
		return expressionFact{tv: types.TypeAndValue{Type: runtime.node}}, true
	case *gsxast.OrderedAttrsAttr:
		ordered, complete := aggregateNestedComponentFacts(node, nested)
		if !complete && len(node.Pairs) != 0 {
			return expressionFact{}, false
		}
		return expressionFact{tv: types.TypeAndValue{Type: runtime.attrs}, hasOrderedOperation: ordered}, true
	case *gsxast.ClassAttr:
		ordered, complete := aggregateNestedComponentFacts(node, nested)
		if !complete && len(node.Parts) != 0 {
			return expressionFact{}, false
		}
		return expressionFact{tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: ordered}, true
	case *gsxast.EmbeddedAttr:
		ordered, _ := aggregateNestedComponentFacts(node, nested)
		var typ types.Type
		switch node.Lang {
		case gsxast.EmbeddedText:
			typ = types.Typ[types.String]
		case gsxast.EmbeddedJS:
			typ = runtimePackageType(runtime, "RawJS")
		case gsxast.EmbeddedCSS:
			typ = runtimePackageType(runtime, "RawCSS")
		}
		if typ == nil {
			return expressionFact{}, false
		}
		return expressionFact{tv: types.TypeAndValue{Type: typ}, hasOrderedOperation: ordered}, true
	case *gsxast.CondAttr:
		return expressionFact{tv: types.TypeAndValue{Type: runtime.attrs}, hasOrderedOperation: true}, true
	case *gsxast.Element:
		return expressionFact{tv: types.TypeAndValue{Type: runtime.node}}, true
	}
	return expressionFact{}, false
}

func aggregateNestedComponentFacts(root gsxast.Node, facts expressionFactSet) (ordered, complete bool) {
	complete = true
	seenValue := false
	gsxast.Inspect(root, func(node gsxast.Node) bool {
		if node == nil || node == root {
			return true
		}
		if nestedComponentFactBearingNode(node) {
			seenValue = true
			fact, ok := facts.get(node)
			if !ok || fact.tv.Type == nil {
				complete = false
				return true
			}
			ordered = ordered || fact.hasOrderedOperation || fact.tuple != nil
		}
		if _, ok := node.(*gsxast.ValueCF); ok {
			ordered = true
		}
		return true
	})
	if !seenValue {
		complete = true
	}
	return ordered, complete
}

// nestedComponentFactBearingNode mirrors the authoritative expression probes
// in analyze.go. A plain class/style expression is probed on its ClassPart;
// value-form control flow is probed on each ValueArm instead; a composed CSS
// literal has no ClassPart expression and delegates to its Interp holes.
// Ordered attrs likewise publish one fact per OrderedPair. This function only
// identifies facts that analysis actually publishes: callers must not invent a
// type when any required fact is absent.
func nestedComponentFactBearingNode(node gsxast.Node) bool {
	switch node := node.(type) {
	case *gsxast.Interp, *gsxast.OrderedPair, *gsxast.ValueArm:
		return true
	case *gsxast.ClassPart:
		return node.CF == nil && node.CSSSegments == nil
	default:
		return false
	}
}

func runtimePackageType(runtime runtimeContract, name string) types.Type {
	var pkg *types.Package
	collectNamedTypes(runtime.node, func(named *types.Named) {
		if pkg == nil && named.Obj() != nil {
			pkg = named.Obj().Pkg()
		}
	})
	if pkg == nil {
		return nil
	}
	obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
	if !ok {
		return nil
	}
	return obj.Type()
}

// positionalMaterializationFacts describes the final expression returned by
// each syntax-specific lowering path. Compound values such as f/js/css
// literals, class/style values, ordered attrs, and attrs pairs consume their
// own nested (T, error) operations while assembling that final expression.
// The outer positional materializer must still treat that work as ordered, but
// must not try to unwrap the already-consumed tuple a second time.
func positionalMaterializationFacts(plan componentCallPlan, facts expressionFactSet, runtime runtimeContract) expressionFactSet {
	result := facts.derive()
	for _, value := range plan.values {
		if !positionalLoweringOwnsTuple(value) {
			continue
		}
		fact, ok := result.get(value.node)
		if !ok || fact.tuple == nil {
			continue
		}
		fact.tuple = nil
		fact.tv.Value = nil
		fact.isNil = false
		fact.hasOrderedOperation = true
		if value.attrsNode != nil {
			fact.tv.Type = runtime.attrs
		} else if lowered, ok := syntaxDefinedComponentFact(value.node, facts, runtime); ok {
			fact.tv.Type = lowered.tv.Type
		}
		result.set(value.node, fact)
	}
	return result
}

func positionalLoweringOwnsTuple(value componentInputValue) bool {
	if value.attrsNode != nil {
		switch value.attrsNode.kind {
		case componentAttrsStreamPair, componentAttrsStreamSpread, componentAttrsStreamConditional:
			return true
		case componentAttrsStreamContributor:
			switch value.attrsNode.attr.(type) {
			case *gsxast.OrderedAttrsAttr, *gsxast.EmbeddedAttr:
				return true
			}
		}
		return false
	}
	switch value.node.(type) {
	case *gsxast.ClassAttr, *gsxast.EmbeddedAttr, *gsxast.OrderedAttrsAttr:
		return true
	default:
		return false
	}
}

func validateRecursiveAttrsOperands(plan componentCallPlan, facts expressionFactSet, runtime runtimeContract) []diag.Diagnostic {
	var diagnostics []diag.Diagnostic
	var visit func(componentAttrsStreamNode)
	visit = func(node componentAttrsStreamNode) {
		if node.kind == componentAttrsStreamConditional {
			for _, child := range node.then {
				visit(child)
			}
			for _, child := range node.otherwise {
				visit(child)
			}
			return
		}
		leaf := componentCallPlan{callStart: plan.callStart, callEnd: plan.callEnd, values: []componentInputValue{{
			kind:      componentInputAttrsPair,
			node:      node.attr,
			attrsNode: &node,
		}}}
		switch node.kind {
		case componentAttrsStreamSpread:
			leaf.values[0].kind = componentInputAttrsSegment
		case componentAttrsStreamContributor:
			leaf.values[0].kind = componentInputAttrsContributor
		}
		_, leafDiags := validateComponentOperands(leaf, facts, runtime)
		diagnostics = append(diagnostics, leafDiags...)
	}
	for _, value := range plan.values {
		if value.attrsNode != nil && value.attrsNode.kind == componentAttrsStreamConditional {
			visit(*value.attrsNode)
		}
	}
	return diagnostics
}

func validateRequiredAttrsFacts(plan componentCallPlan, facts expressionFactSet, fset *token.FileSet) []diag.Diagnostic {
	var diagnostics []diag.Diagnostic
	var visit func(componentAttrsStreamNode)
	visit = func(node componentAttrsStreamNode) {
		if node.kind == componentAttrsStreamConditional {
			for _, child := range node.then {
				visit(child)
			}
			for _, child := range node.otherwise {
				visit(child)
			}
			return
		}
		if node.kind != componentAttrsStreamSpread && node.kind != componentAttrsStreamContributor {
			return
		}
		fact, ok := facts.get(node.attr)
		if !ok || fact.tv.Type == nil {
			diagnostics = append(diagnostics, positionalDiagnostic(node.attr, fset, "component-operand-fact", "attrs contributor has no authoritative go/types operand fact"))
		}
	}
	for _, value := range plan.values {
		if value.attrsNode != nil {
			visit(*value.attrsNode)
		}
	}
	return diagnostics
}

func positionalSuppliedOperands(plan componentCallPlan, facts expressionFactSet, runtime runtimeContract, fset *token.FileSet) ([]suppliedOperand, []diag.Diagnostic) {
	var operands []suppliedOperand
	var diagnostics []diag.Diagnostic
	for valueIndex, value := range plan.values {
		if value.kind != componentInputProp {
			continue
		}
		fact, ok := facts.get(value.node)
		if !ok || fact.tv.Type == nil {
			diagnostics = append(diagnostics, positionalDiagnostic(value.node, fset, "component-operand-fact", "component prop has no authoritative go/types operand fact"))
			continue
		}
		tv := fact.tv
		if fact.tuple != nil {
			unwrapped, ok := tupleUnwrapType(fact.tuple)
			if !ok {
				continue // validateComponentOperands owns the positioned diagnostic.
			}
			tv = types.TypeAndValue{Type: unwrapped}
		}
		adapter := componentAdapterIdentity
		if value.paramIndex >= 0 && value.paramIndex < len(plan.target.params) {
			want := plan.target.params[value.paramIndex].typ
			if types.Identical(want, runtime.node) && !types.AssignableTo(tv.Type, want) {
				adapter = componentAdapterNodeVal
				switch node := value.node.(type) {
				case *gsxast.StaticAttr:
					adapter = componentAdapterNodeText
				case *gsxast.EmbeddedAttr:
					if node.Lang == gsxast.EmbeddedText {
						adapter = componentAdapterNodeText
					}
				}
				tv = types.TypeAndValue{Type: runtime.node}
			}
		}
		operands = append(operands, suppliedOperand{
			valueIndex: valueIndex,
			paramIndex: value.paramIndex,
			adapter:    adapter,
			tv:         tv,
		})
	}
	return operands, diagnostics
}

func validatePositionalOperandAssignments(plan componentCallPlan, operands []suppliedOperand, pkg *types.Package, fset *token.FileSet) []diag.Diagnostic {
	var diagnostics []diag.Diagnostic
	for _, operand := range operands {
		if operand.paramIndex < 0 || operand.paramIndex >= len(plan.target.params) {
			diagnostics = append(diagnostics, positionalDiagnostic(plan.call, fset, "component-operand", "component operand refers to a parameter outside the instantiated signature"))
			continue
		}
		want := plan.target.params[operand.paramIndex].typ
		if err := validateTypeAndValueAssignment(operand.tv, want, pkg); err != nil {
			name := plan.target.params[operand.paramIndex].name
			diagnostics = append(diagnostics, positionalDiagnostic(plan.call, fset, "component-prop-type", fmt.Sprintf("attribute %q cannot be passed to %s: %v", name, want, err)))
		}
	}
	return diagnostics
}

func planComponentTypeArguments(target componentTargetFact, instance types.Instance, pkg *types.Package, allocator *generatedImportAllocator, fset *token.FileSet) ([]types.Type, []string, []diag.Diagnostic) {
	if instance.TypeArgs == nil || instance.TypeArgs.Len() == 0 {
		return nil, nil, nil
	}
	typesOut := make([]types.Type, 0, instance.TypeArgs.Len())
	exprs := make([]string, 0, instance.TypeArgs.Len())
	ctx := typeSpellingContext{pkg: pkg, imports: allocator, typeParams: visibleTargetTypeParams(target, pkg)}
	for i := 0; i < instance.TypeArgs.Len(); i++ {
		typ := types.Unalias(instance.TypeArgs.At(i))
		typesOut = append(typesOut, typ)
		if i < len(target.authoredTypeArgs) && target.authoredTypeArgs[i].expr != nil {
			var printed bytes.Buffer
			if err := printer.Fprint(&printed, fset, target.authoredTypeArgs[i].expr); err != nil {
				return nil, nil, []diag.Diagnostic{targetPositioned(inferenceContext{pkg: pkg, fset: fset}, target, "component-type-args", fmt.Sprintf("cannot preserve authored type argument %d: %v", i, err))}
			}
			exprs = append(exprs, printed.String())
			continue
		}
		txn, spelling, ok := spellType(typ, ctx)
		if !ok {
			return nil, nil, []diag.Diagnostic{targetPositioned(inferenceContext{pkg: pkg, fset: fset}, target, "component-type-args", fmt.Sprintf("inferred type argument %s cannot be named at this component call", typ))}
		}
		if err := validateTypeArgumentSpelling(spelling, typ, target, pkg, fset, txn); err != nil {
			return nil, nil, []diag.Diagnostic{targetPositioned(inferenceContext{pkg: pkg, fset: fset}, target, "component-type-args", fmt.Sprintf("inferred type argument %s cannot be emitted: %v", typ, err))}
		}
		txn.commit()
		exprs = append(exprs, spelling)
	}
	return typesOut, exprs, nil
}

func validateTypeArgumentSpelling(expr string, want types.Type, target componentTargetFact, pkg *types.Package, fset *token.FileSet, txn *generatedImportTxn) error {
	parsed, err := goparser.ParseExpr(expr)
	if err != nil {
		return err
	}
	// The strict caller-scope check type-checks the spelling against the real
	// caller package: it knows the authored imports (and, via target.expr's
	// position, any type parameters in scope) but NOT the yet-to-be-emitted
	// _gsxtyN generated-import aliases. It is therefore valid ONLY when the
	// spelling references no generated import. `len(txn.work.order) ==
	// txn.baseLen` is not that condition: a spelling that REUSES a generated
	// import already committed at an earlier call site (same cross-package type
	// inferred twice) allocates no new order entry, yet still names _gsxtyN — it
	// must go through the synthetic probe, which declares those aliases.
	if !spellingReferencesGeneratedImport(parsed, txn) && target.expr != nil && target.expr.Pos().IsValid() {
		info := &types.Info{Types: make(map[goast.Expr]types.TypeAndValue)}
		if err := types.CheckExpr(fset, pkg, target.expr.Pos(), parsed, info); err != nil {
			return err
		}
		if got := info.Types[parsed].Type; !types.Identical(got, want) {
			return fmt.Errorf("type expression denotes %v, want %v", got, want)
		}
		return nil
	}
	return validateSyntheticTypeExpression(expr, want, pkg, txn)
}

// spellingReferencesGeneratedImport reports whether expr uses any selector
// qualifier that names a generated (_gsxtyN) import alias. txn.work.order holds
// every alias visible at this call site, including ones committed by prior
// sites, so reused aliases are detected as well as freshly allocated ones.
func spellingReferencesGeneratedImport(expr goast.Expr, txn *generatedImportTxn) bool {
	if txn == nil {
		return false
	}
	generated := make(map[string]bool, len(txn.work.order))
	for _, spec := range txn.work.order {
		generated[spec.name] = true
	}
	found := false
	goast.Inspect(expr, func(node goast.Node) bool {
		if found {
			return false
		}
		if sel, ok := node.(*goast.SelectorExpr); ok {
			if id, ok := sel.X.(*goast.Ident); ok && generated[id.Name] {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func validateSyntheticTypeExpression(expr string, want types.Type, pkg *types.Package, txn *generatedImportTxn) error {
	parsedExpr, err := goparser.ParseExpr(expr)
	if err != nil {
		return err
	}
	usedAliases := make(map[string]bool)
	goast.Inspect(parsedExpr, func(node goast.Node) bool {
		if sel, ok := node.(*goast.SelectorExpr); ok {
			if id, ok := sel.X.(*goast.Ident); ok {
				usedAliases[id.Name] = true
			}
		}
		return true
	})
	var specs []importSpec
	for _, spec := range txn.work.order {
		if usedAliases[spec.name] {
			specs = append(specs, spec)
		}
	}
	var source strings.Builder
	source.WriteString("package gsxtypearg\n")
	if len(specs) != 0 {
		source.WriteString("import (\n")
		for _, spec := range specs {
			fmt.Fprintf(&source, "%s %s\n", spec.name, strconv.Quote(spec.path))
		}
		source.WriteString(")\n")
	}
	source.WriteString("type _gsxgot = ")
	source.WriteString(expr)
	source.WriteByte('\n')
	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "typearg.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		return err
	}
	probe := types.NewPackage("gsxtypearg", "gsxtypearg")
	installSyntheticLocalTypeAliases(probe, pkg, want)
	packages := make(map[string]*types.Package)
	collectTypePackages(want, packages)
	var checkErrs []error
	config := types.Config{Importer: exactPackageImporter(packages), Error: func(err error) { checkErrs = append(checkErrs, err) }}
	info := &types.Info{Defs: make(map[*goast.Ident]types.Object)}
	types.NewChecker(&config, fset, probe, info).Files([]*goast.File{file})
	if len(checkErrs) != 0 {
		return checkErrs[0]
	}
	obj := probe.Scope().Lookup("_gsxgot")
	if obj == nil || !types.Identical(obj.Type(), want) {
		return fmt.Errorf("type expression denotes %v, want %v", objectType(obj), want)
	}
	return nil
}

func objectType(obj types.Object) types.Type {
	if obj == nil {
		return nil
	}
	return obj.Type()
}

func planComponentZeros(plan componentCallPlan, target componentTargetFact, pkg *types.Package, fset *token.FileSet, allocator *generatedImportAllocator) ([]componentZeroArgument, []diag.Diagnostic) {
	ctx := typeSpellingContext{pkg: pkg, imports: allocator, typeParams: visibleTargetTypeParams(target, pkg)}
	var zeros []componentZeroArgument
	var diagnostics []diag.Diagnostic
	for i, slot := range plan.args {
		if !slot.omitted {
			continue
		}
		if plan.target.goSig.Variadic() && i == len(plan.args)-1 {
			continue
		}
		var winner *zeroCandidate
		for _, candidate := range zeroCandidates(slot.param.typ, ctx) {
			if err := validateZeroCandidate(candidate, slot.param.typ, ctx, target, fset); err == nil {
				accepted := candidate
				winner = &accepted
				break
			}
		}
		if winner == nil {
			message := fmt.Sprintf("attribute %q is required here: its zero value cannot be expressed inline", slot.param.name)
			diagnostics = append(diagnostics, positionalDiagnostic(plan.call, fset, "component-required-attribute", message))
			continue
		}
		if winner.imports != nil {
			winner.imports.commit()
		}
		zeros = append(zeros, componentZeroArgument{paramIndex: i, expr: winner.expr})
	}
	return zeros, diagnostics
}

func visibleTargetTypeParams(target componentTargetFact, pkg *types.Package) map[*types.TypeParam]string {
	visible := make(map[*types.TypeParam]string)
	if target.raw == nil {
		return visible
	}
	pos := target.exprPos()
	scope := pkg.Scope().Innermost(pos)
	collectTypeParams(target.raw, func(tp *types.TypeParam) {
		obj := tp.Obj()
		if obj == nil || obj.Name() == "" {
			return
		}
		if scope == nil || !pos.IsValid() {
			return
		}
		_, resolved := scope.LookupParent(obj.Name(), pos)
		if resolved == obj {
			visible[tp] = obj.Name()
		}
	})
	return visible
}

func collectTypeParams(t types.Type, yield func(*types.TypeParam)) {
	seen := make(map[types.Type]bool)
	var visit func(types.Type)
	visit = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.TypeParam:
			yield(t)
		case *types.Alias:
			visit(types.Unalias(t))
		case *types.Named:
			for arg := range t.TypeArgs().Types() {
				visit(arg)
			}
		case *types.Pointer:
			visit(t.Elem())
		case *types.Slice:
			visit(t.Elem())
		case *types.Array:
			visit(t.Elem())
		case *types.Map:
			visit(t.Key())
			visit(t.Elem())
		case *types.Chan:
			visit(t.Elem())
		case *types.Tuple:
			for v := range t.Variables() {
				visit(v.Type())
			}
		case *types.Signature:
			for tp := range t.TypeParams().TypeParams() {
				yield(tp)
			}
			visit(t.Params())
			visit(t.Results())
		case *types.Struct:
			for f := range t.Fields() {
				visit(f.Type())
			}
		case *types.Interface:
			for m := range t.Methods() {
				visit(m.Type())
			}
			for embedded := range t.EmbeddedTypes() {
				visit(embedded)
			}
		case *types.Union:
			for term := range t.Terms() {
				visit(term.Type())
			}
		}
	}
	visit(t)
}

func validateZeroCandidate(candidate zeroCandidate, want types.Type, ctx typeSpellingContext, target componentTargetFact, fset *token.FileSet) error {
	// Candidates without generated imports can be checked directly in the exact
	// lexical package scope, which is essential when the type expression names a
	// caller type parameter.
	if candidate.imports == nil && target.expr != nil && target.expr.Pos().IsValid() {
		expr, err := goparser.ParseExpr(candidate.expr)
		if err != nil {
			return err
		}
		info := &types.Info{Types: make(map[goast.Expr]types.TypeAndValue)}
		if err := types.CheckExpr(fset, ctx.pkg, target.expr.Pos(), expr, info); err != nil {
			return err
		}
		tv := info.Types[expr]
		if !types.AssignableTo(tv.Type, want) {
			return fmt.Errorf("%s is not assignable to %s", tv.Type, want)
		}
		return nil
	}
	return validateSyntheticExpressionAssignment(candidate.expr, want, ctx.pkg, candidate.imports)
}

func validateTypeAndValueAssignment(tv types.TypeAndValue, want types.Type, pkg *types.Package) error {
	source := "_gsxvalue"
	valueType := tv.Type
	if tv.IsNil() {
		source = "nil"
		valueType = nil
	} else if tv.Value != nil {
		var ok bool
		source, ok = constantSource(tv.Value)
		if !ok {
			return fmt.Errorf("unsupported constant %s", tv.Value)
		}
		valueType = nil
	}
	return validateSyntheticAssignment(source, want, pkg, nil, valueType)
}

func validateSyntheticExpressionAssignment(expr string, want types.Type, pkg *types.Package, txn *generatedImportTxn) error {
	return validateSyntheticAssignment(expr, want, pkg, txn, nil)
}

func validateSyntheticAssignment(expr string, want types.Type, pkg *types.Package, txn *generatedImportTxn, valueType types.Type) error {
	parsedExpr, err := goparser.ParseExpr(expr)
	if err != nil {
		return err
	}
	usedAliases := make(map[string]bool)
	goast.Inspect(parsedExpr, func(node goast.Node) bool {
		if sel, ok := node.(*goast.SelectorExpr); ok {
			if id, ok := sel.X.(*goast.Ident); ok {
				usedAliases[id.Name] = true
			}
		}
		return true
	})

	var specs []importSpec
	if txn != nil {
		for _, spec := range txn.work.order {
			if usedAliases[spec.name] {
				specs = append(specs, spec)
			}
		}
	}
	var source strings.Builder
	source.WriteString("package gsxzero\n")
	if len(specs) != 0 {
		source.WriteString("import (\n")
		for _, spec := range specs {
			fmt.Fprintf(&source, "%s %s\n", spec.name, strconv.Quote(spec.path))
		}
		source.WriteString(")\n")
	}
	source.WriteString("var _ _gsxwant = ")
	source.WriteString(expr)
	source.WriteByte('\n')

	fset := token.NewFileSet()
	file, err := goparser.ParseFile(fset, "zero.go", source.String(), goparser.SkipObjectResolution)
	if err != nil {
		return err
	}
	probe := types.NewPackage("gsxzero", "gsxzero")
	wantObj := types.NewTypeName(token.NoPos, probe, "_gsxwant", nil)
	types.NewAlias(wantObj, want)
	probe.Scope().Insert(wantObj)
	if valueType != nil {
		probe.Scope().Insert(types.NewVar(token.NoPos, probe, "_gsxvalue", valueType))
	}
	installSyntheticLocalTypeAliases(probe, pkg, want)
	packages := make(map[string]*types.Package)
	collectTypePackages(want, packages)
	if valueType != nil {
		collectTypePackages(valueType, packages)
	}
	var checkErrs []error
	config := types.Config{Importer: exactPackageImporter(packages), Error: func(err error) { checkErrs = append(checkErrs, err) }}
	types.NewChecker(&config, fset, probe, nil).Files([]*goast.File{file})
	if len(checkErrs) != 0 {
		return checkErrs[0]
	}
	return nil
}

type exactPackageImporter map[string]*types.Package

func (i exactPackageImporter) Import(path string) (*types.Package, error) {
	if pkg := i[path]; pkg != nil {
		return pkg, nil
	}
	return nil, fmt.Errorf("semantic package %q is unavailable", path)
}

func installSyntheticLocalTypeAliases(probe, current *types.Package, t types.Type) {
	named := make(map[string]types.Type)
	collectNamedTypes(t, func(n *types.Named) {
		if n.Obj() != nil && n.Obj().Pkg() == current {
			named[n.Obj().Name()] = n
		}
	})
	names := make([]string, 0, len(named))
	for name := range named {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		obj := types.NewTypeName(token.NoPos, probe, name, nil)
		types.NewAlias(obj, named[name])
		probe.Scope().Insert(obj)
	}
}

func collectNamedTypes(t types.Type, yield func(*types.Named)) {
	seen := make(map[types.Type]bool)
	var visit func(types.Type)
	visit = func(t types.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch t := t.(type) {
		case *types.Alias:
			visit(types.Unalias(t))
		case *types.Named:
			yield(t)
			for arg := range t.TypeArgs().Types() {
				visit(arg)
			}
		case *types.Pointer:
			visit(t.Elem())
		case *types.Slice:
			visit(t.Elem())
		case *types.Array:
			visit(t.Elem())
		case *types.Map:
			visit(t.Key())
			visit(t.Elem())
		case *types.Chan:
			visit(t.Elem())
		case *types.Tuple:
			for v := range t.Variables() {
				visit(v.Type())
			}
		case *types.Signature:
			visit(t.Params())
			visit(t.Results())
		case *types.Struct:
			for f := range t.Fields() {
				visit(f.Type())
			}
		case *types.Interface:
			for m := range t.Methods() {
				visit(m.Type())
			}
			for embedded := range t.EmbeddedTypes() {
				visit(embedded)
			}
		case *types.Union:
			for term := range t.Terms() {
				visit(term.Type())
			}
		}
	}
	visit(t)
}

func collectTypePackages(t types.Type, out map[string]*types.Package) {
	collectNamedTypes(t, func(n *types.Named) {
		if obj := n.Obj(); obj != nil && obj.Pkg() != nil {
			out[obj.Pkg().Path()] = obj.Pkg()
		}
	})
}

func constantSource(value constant.Value) (string, bool) {
	switch value.Kind() {
	case constant.Bool, constant.String, constant.Int:
		return value.ExactString(), true
	case constant.Float:
		return exactFloatSource(value), true
	case constant.Complex:
		realPart := exactFloatSource(constant.Real(value))
		imagPart := exactFloatSource(constant.Imag(value))
		return "complex(" + realPart + "," + imagPart + ")", true
	}
	return "", false
}

func exactFloatSource(value constant.Value) string {
	exact := value.ExactString()
	if numerator, denominator, ok := strings.Cut(exact, "/"); ok {
		return "(" + numerator + ".0/" + denominator + ".0)"
	}
	if !strings.ContainsAny(exact, ".eEpP") {
		return exact + ".0"
	}
	return exact
}
