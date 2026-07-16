package codegen

import (
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/token"
	"go/types"
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// suppliedOperand is one authored value that survives into the real call: the
// parameter it fills, its authored Go expression, and its resolved go/types
// TypeAndValue. Omitted parameters are never represented here — inference and
// evaluation-order planning see authored operands ONLY.
type suppliedOperand struct {
	paramIndex int
	expr       goast.Expr
	tv         types.TypeAndValue
}

// inferenceContext carries the semantic environment the carrier check runs in.
// pkg is the analysis package whose objects the operand and type-argument types
// belong to; fset positions diagnostics; scope is the enclosing lexical scope
// (retained for callers that install the carrier in a live checker scope).
type inferenceContext struct {
	pkg   *types.Package
	fset  *token.FileSet
	scope *types.Scope
}

// inferAuthoredInstance performs Go type inference for a generic component
// target using authored operands ONLY. It assembles a transient semantic
// carrier types.Func whose type parameters, constraints, and supplied parameter
// types are copied — as go/types objects, never as source — from the origin
// signature, omitting every unsupplied value parameter. It then calls that
// carrier with the authored type-argument prefix plus the authored operands and
// harvests the inferred instantiation. Because the carrier is built from
// go/types objects, imported unexported constraints remain usable without being
// named. Omitted-parameter zeros are never carrier arguments.
//
// Diagnostic precedence follows Go's failure class: an incomplete inference
// alone yields the explicit-type-argument hint; an inferred or explicit
// argument that violates a constraint yields the native constraint diagnostic
// (go/types records Info.Instances even on constraint failure, so a complete
// instance under an error is a constraint failure, not incompleteness).
func inferAuthoredInstance(ic inferenceContext, target componentTargetFact, operands []suppliedOperand) (types.Instance, []diag.Diagnostic) {
	origin := target.raw
	if origin == nil {
		return types.Instance{}, []diag.Diagnostic{targetPositioned(ic, target, "component-inference", "component target has no callable signature")}
	}
	tparams := origin.TypeParams()
	if tparams == nil || tparams.Len() == 0 {
		// Nothing to infer; the raw signature is already the instance type.
		return types.Instance{Type: origin}, nil
	}

	// Fresh carrier type parameters carrying substituted constraints. The origin
	// type-parameter objects are already bound to the origin signature and cannot
	// be rebound, so a faithful substitution reproduces every constraint over the
	// fresh objects.
	fresh := make([]*types.TypeParam, tparams.Len())
	subst := map[*types.TypeParam]*types.TypeParam{}
	for i := range fresh {
		op := tparams.At(i)
		fresh[i] = types.NewTypeParam(types.NewTypeName(token.NoPos, ic.pkg, op.Obj().Name(), nil), nil)
		subst[op] = fresh[i]
	}
	for i := range fresh {
		constraint := substituteTypeParams(tparams.At(i).Constraint(), subst)
		iface, ok := constraint.(*types.Interface)
		if !ok {
			return types.Instance{}, []diag.Diagnostic{targetPositioned(ic, target, "component-inference", "component target constraint is not an interface after substitution")}
		}
		fresh[i].SetConstraint(iface)
	}

	// Carrier value parameters: only the supplied parameters, substituted onto
	// the fresh type parameters, in operand order.
	carrierVars := make([]*types.Var, 0, len(operands))
	for i, op := range operands {
		if op.paramIndex < 0 || op.paramIndex >= origin.Params().Len() {
			return types.Instance{}, []diag.Diagnostic{targetPositioned(ic, target, "component-inference", fmt.Sprintf("operand %d refers to parameter %d outside the signature", i, op.paramIndex))}
		}
		paramType := substituteTypeParams(origin.Params().At(op.paramIndex).Type(), subst)
		carrierVars = append(carrierVars, types.NewVar(token.NoPos, ic.pkg, fmt.Sprintf("_gsxp%d", i), paramType))
	}
	carrierSig := types.NewSignatureType(nil, nil, fresh, types.NewTuple(carrierVars...), types.NewTuple(), false)

	// A throwaway package hosts the carrier, the synthetic operand variables, and
	// the synthetic type-argument aliases so no foreign type is ever spelled as
	// source. Every referenced type is a resolved go/types object, so the check
	// needs no importer.
	probe := types.NewPackage("gsxinfer", "gsxinfer")
	carrier := types.NewFunc(token.NoPos, probe, "_gsxcarrier", carrierSig)
	probe.Scope().Insert(carrier)

	var call strings.Builder
	call.WriteString("_gsxcarrier")
	if len(target.authoredTypeArgs) > 0 {
		call.WriteByte('[')
		for i, ta := range target.authoredTypeArgs {
			if i > 0 {
				call.WriteString(", ")
			}
			name := fmt.Sprintf("_gsxta%d", i)
			aliasObj := types.NewTypeName(token.NoPos, probe, name, nil)
			types.NewAlias(aliasObj, ta.typ)
			probe.Scope().Insert(aliasObj)
			call.WriteString(name)
		}
		call.WriteByte(']')
	}
	call.WriteByte('(')
	for i, op := range operands {
		if i > 0 {
			call.WriteString(", ")
		}
		call.WriteString(operandArgSource(op, probe, i))
	}
	call.WriteByte(')')

	fset := token.NewFileSet()
	src := "package gsxinfer\nfunc _gsxprobe() { " + call.String() + " }\n"
	file, err := goparser.ParseFile(fset, "infer.go", src, goparser.SkipObjectResolution)
	if err != nil {
		return types.Instance{}, []diag.Diagnostic{targetPositioned(ic, target, "component-inference", fmt.Sprintf("internal inference probe failed to parse: %v", err))}
	}
	info := &types.Info{
		Instances: map[*goast.Ident]types.Instance{},
		Uses:      map[*goast.Ident]types.Object{},
		Defs:      map[*goast.Ident]types.Object{},
		Types:     map[goast.Expr]types.TypeAndValue{},
	}
	var probeErrs []types.Error
	config := types.Config{Error: func(e error) {
		if te, ok := e.(types.Error); ok {
			probeErrs = append(probeErrs, te)
		}
	}}
	types.NewChecker(&config, fset, probe, info).Files([]*goast.File{file})

	var carrierInst types.Instance
	for id, inst := range info.Instances {
		if id.Name == "_gsxcarrier" {
			carrierInst = inst
			break
		}
	}
	complete := carrierInst.TypeArgs != nil && carrierInst.TypeArgs.Len() == tparams.Len()

	if len(probeErrs) > 0 {
		if !complete {
			// Incomplete inference: the explicit-type-argument hint.
			return types.Instance{}, []diag.Diagnostic{incompleteInferenceDiagnostic(ic, target)}
		}
		// A complete instance under an error is a constraint violation; surface
		// the native diagnostics without claiming explicit arguments would help.
		diags := make([]diag.Diagnostic, 0, len(probeErrs))
		for _, te := range probeErrs {
			diags = append(diags, nativeInferenceDiagnostic(ic, target, te))
		}
		return types.Instance{}, diags
	}
	if !complete {
		return types.Instance{}, []diag.Diagnostic{incompleteInferenceDiagnostic(ic, target)}
	}

	// Instantiate the origin signature with the inferred type arguments, stripped
	// of the synthetic type-argument aliases.
	targs := make([]types.Type, carrierInst.TypeArgs.Len())
	for i := range targs {
		targs[i] = types.Unalias(carrierInst.TypeArgs.At(i))
	}
	instantiated, err := types.Instantiate(nil, origin, targs, true)
	if err != nil {
		return types.Instance{}, []diag.Diagnostic{targetPositioned(ic, target, "component-inference", fmt.Sprintf("instantiating component target failed: %v", err))}
	}
	return types.Instance{TypeArgs: carrierInst.TypeArgs, Type: instantiated}, nil
}

// operandArgSource returns the source text used to pass one operand to the
// carrier. An untyped nil is emitted literally (it carries no type to infer
// from, exactly as authored); every other operand is passed through a synthetic
// variable of its resolved type — untyped constants default first, matching
// Go's inference of a type parameter from an untyped constant argument — so no
// foreign type is ever spelled.
func operandArgSource(op suppliedOperand, probe *types.Package, index int) string {
	t := op.tv.Type
	if basic, ok := t.(*types.Basic); ok && basic.Kind() == types.UntypedNil {
		return "nil"
	}
	if isUntypedBasic(t) {
		t = types.Default(t)
	}
	name := fmt.Sprintf("_gsxarg%d", index)
	probe.Scope().Insert(types.NewVar(token.NoPos, probe, name, t))
	return name
}

func isUntypedBasic(t types.Type) bool {
	basic, ok := t.(*types.Basic)
	return ok && basic.Info()&types.IsUntyped != 0
}

func targetPositioned(ic inferenceContext, target componentTargetFact, code, message string) diag.Diagnostic {
	d := diag.Diagnostic{Severity: diag.Error, Code: code, Message: message, Source: "codegen"}
	if ic.fset != nil && target.expr != nil && target.expr.Pos().IsValid() {
		d.Start = ic.fset.Position(target.expr.Pos())
		d.End = ic.fset.Position(target.expr.End())
	}
	return d
}

func incompleteInferenceDiagnostic(ic inferenceContext, target componentTargetFact) diag.Diagnostic {
	return targetPositioned(ic, target, "component-type-args",
		"cannot infer type arguments from the supplied attributes; instantiate the component explicitly, e.g. <Tag[T] .../>")
}

func nativeInferenceDiagnostic(ic inferenceContext, target componentTargetFact, te types.Error) diag.Diagnostic {
	d := diag.Diagnostic{Severity: diag.Error, Code: "component-constraint", Message: te.Msg, Source: "types"}
	if ic.fset != nil && target.expr != nil && target.expr.Pos().IsValid() {
		d.Start = ic.fset.Position(target.expr.Pos())
		d.End = d.Start
	}
	return d
}

// substituteTypeParams reconstructs t with every origin type parameter in subst
// replaced by its fresh counterpart. It is a faithful structural rewrite (not a
// text or printed-type transform). It terminates because a *types.Named is
// treated as opaque: the walk descends only into a named type's type ARGUMENTS,
// never into its (potentially self-referential) definition, so a recursive type
// such as `type List struct { next *List }` cannot cause unbounded recursion.
func substituteTypeParams(t types.Type, subst map[*types.TypeParam]*types.TypeParam) types.Type {
	if t == nil {
		return nil
	}
	switch t := t.(type) {
	case *types.TypeParam:
		if fresh, ok := subst[t]; ok {
			return fresh
		}
		return t
	case *types.Basic:
		return t
	case *types.Alias:
		return substituteTypeParams(types.Unalias(t), subst)
	case *types.Named:
		if t.TypeArgs() == nil || t.TypeArgs().Len() == 0 {
			return t
		}
		args := make([]types.Type, t.TypeArgs().Len())
		for i := range args {
			args[i] = substituteTypeParams(t.TypeArgs().At(i), subst)
		}
		inst, err := types.Instantiate(nil, t.Origin(), args, false)
		if err != nil {
			return t
		}
		return inst
	case *types.Pointer:
		return types.NewPointer(substituteTypeParams(t.Elem(), subst))
	case *types.Slice:
		return types.NewSlice(substituteTypeParams(t.Elem(), subst))
	case *types.Array:
		return types.NewArray(substituteTypeParams(t.Elem(), subst), t.Len())
	case *types.Map:
		return types.NewMap(substituteTypeParams(t.Key(), subst), substituteTypeParams(t.Elem(), subst))
	case *types.Chan:
		return types.NewChan(t.Dir(), substituteTypeParams(t.Elem(), subst))
	case *types.Tuple:
		vars := make([]*types.Var, t.Len())
		for i := range vars {
			v := t.At(i)
			vars[i] = types.NewVar(v.Pos(), v.Pkg(), v.Name(), substituteTypeParams(v.Type(), subst))
		}
		return types.NewTuple(vars...)
	case *types.Signature:
		params, _ := substituteTypeParams(t.Params(), subst).(*types.Tuple)
		results, _ := substituteTypeParams(t.Results(), subst).(*types.Tuple)
		return types.NewSignatureType(nil, nil, nil, params, results, t.Variadic())
	case *types.Struct:
		fields := make([]*types.Var, t.NumFields())
		tags := make([]string, t.NumFields())
		for i := range fields {
			f := t.Field(i)
			fields[i] = types.NewField(f.Pos(), f.Pkg(), f.Name(), substituteTypeParams(f.Type(), subst), f.Embedded())
			tags[i] = t.Tag(i)
		}
		return types.NewStruct(fields, tags)
	case *types.Union:
		terms := make([]*types.Term, t.Len())
		for i := range terms {
			term := t.Term(i)
			terms[i] = types.NewTerm(term.Tilde(), substituteTypeParams(term.Type(), subst))
		}
		return types.NewUnion(terms)
	case *types.Interface:
		methods := make([]*types.Func, t.NumExplicitMethods())
		for i := range methods {
			m := t.ExplicitMethod(i)
			sig, _ := substituteTypeParams(m.Type(), subst).(*types.Signature)
			methods[i] = types.NewFunc(m.Pos(), m.Pkg(), m.Name(), sig)
		}
		embeddeds := make([]types.Type, t.NumEmbeddeds())
		for i := range embeddeds {
			embeddeds[i] = substituteTypeParams(t.EmbeddedType(i), subst)
		}
		return types.NewInterfaceType(methods, embeddeds).Complete()
	}
	return t
}

// typeSpellingContext is the environment for spelling a caller-visible type
// expression. typeParams maps every type parameter in scope at the call site to
// the name that spells it; imports allocates the reserved generated aliases for
// foreign packages.
type typeSpellingContext struct {
	pkg        *types.Package
	typeParams map[*types.TypeParam]string
	imports    *generatedImportAllocator
}

// zeroCandidate is one inline zero spelling for an omitted parameter together
// with the import transaction it needs. imports is nil for a type-independent
// literal (no foreign package). Only the winning candidate's transaction is
// committed, so a rejected spelling leaks no import.
type zeroCandidate struct {
	expr    string
	imports *generatedImportTxn
}

// semanticZeroLiteral returns the type-independent literal that is the actual
// semantic zero of t, when one exists: "" for strings, 0 for numerics, false
// for booleans, nil for pointers, slices, maps, channels, functions,
// interfaces, and unsafe.Pointer. For a type parameter it evaluates the
// complete type set and returns a literal only when the same literal is the
// zero of every permitted type; a mixed type set (for example int | string)
// has no such literal. It never infers a zero from exported spelling or an
// underlying-kind shortcut.
func semanticZeroLiteral(t types.Type) (string, bool) {
	if t == nil {
		return "", false
	}
	t = types.Unalias(t)
	if tp, ok := t.(*types.TypeParam); ok {
		return typeSetZeroLiteral(tp)
	}
	return underlyingZeroLiteral(t.Underlying())
}

func underlyingZeroLiteral(u types.Type) (string, bool) {
	switch u := u.(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsString != 0:
			return `""`, true
		case u.Info()&types.IsBoolean != 0:
			return "false", true
		case u.Info()&types.IsNumeric != 0:
			return "0", true
		case u.Kind() == types.UnsafePointer:
			return "nil", true
		}
		return "", false
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan, *types.Signature, *types.Interface:
		return "nil", true
	}
	return "", false
}

func typeSetZeroLiteral(tp *types.TypeParam) (string, bool) {
	iface, ok := tp.Constraint().Underlying().(*types.Interface)
	if !ok {
		return "", false
	}
	var terms []*types.Term
	collectConstraintTerms(iface, &terms, map[*types.Interface]bool{})
	if len(terms) == 0 {
		return "", false
	}
	lit := ""
	for i, term := range terms {
		l, ok := semanticZeroLiteral(term.Type())
		if !ok {
			return "", false
		}
		if i == 0 {
			lit = l
		} else if l != lit {
			return "", false
		}
	}
	return lit, true
}

func collectConstraintTerms(iface *types.Interface, out *[]*types.Term, seen map[*types.Interface]bool) {
	if iface == nil || seen[iface] {
		return
	}
	seen[iface] = true
	for embedded := range iface.EmbeddedTypes() {
		switch e := embedded.(type) {
		case *types.Union:
			for term := range e.Terms() {
				*out = append(*out, term)
			}
		case *types.Interface:
			collectConstraintTerms(e, out, seen)
		case *types.Named:
			if ei, ok := e.Underlying().(*types.Interface); ok {
				collectConstraintTerms(ei, out, seen)
			} else {
				*out = append(*out, types.NewTerm(false, e))
			}
		default:
			*out = append(*out, types.NewTerm(false, e))
		}
	}
}

// zeroCandidates enumerates the inline zero spellings for an omitted parameter
// of type t, in the order lowering should try them: the semantic literal, the
// exact nameable type via *new(T), and the accessible unnamed underlying shape
// via *new(U). Each foreign spelling carries a candidate-local import
// transaction; the caller validates each with go/types and commits only the
// winner. An empty result means the omission is a positioned required attribute.
func zeroCandidates(t types.Type, ctx typeSpellingContext) []zeroCandidate {
	var out []zeroCandidate
	if lit, ok := semanticZeroLiteral(t); ok {
		out = append(out, zeroCandidate{expr: lit})
	}
	if txn, spell, ok := spellType(t, ctx); ok {
		// The exact type is nameable; the unnamed-underlying fallback is only for
		// an otherwise-unspellable named type, so nothing further is attempted.
		return append(out, zeroCandidate{expr: "*new(" + spell + ")", imports: txn})
	}
	if named, ok := types.Unalias(t).(*types.Named); ok {
		under := named.Underlying()
		if _, basic := under.(*types.Basic); !basic {
			// A composite unnamed underlying yields a value assignable to the
			// defined type (Go assignability: identical underlying, one side
			// unnamed). A basic underlying does not, so it is not attempted.
			if txn, spell, ok := spellType(under, ctx); ok {
				out = append(out, zeroCandidate{expr: "*new(" + spell + ")", imports: txn})
			}
		}
	}
	return out
}

// spellType produces a caller-package type expression identical to t, or
// reports that t is not nameable in ctx. Foreign packages are qualified through
// a fresh transaction so a rejected spelling commits no import.
func spellType(t types.Type, ctx typeSpellingContext) (*generatedImportTxn, string, bool) {
	if ctx.imports == nil {
		return nil, "", false
	}
	if !typeSpellable(t, ctx, map[types.Type]bool{}) {
		return nil, "", false
	}
	txn := ctx.imports.begin()
	str := types.TypeString(t, txn.qualifier(ctx.pkg))
	return txn, str, true
}

// typeSpellable reports whether t can be written as a type expression in
// ctx.pkg that type-checks as identical to t: predeclared and same-package or
// exported named types are spellable, an unnamed composite is spellable only
// when every member is, and an unexported foreign member (a named type, a
// struct field, or an interface method) is not.
func typeSpellable(t types.Type, ctx typeSpellingContext, seen map[types.Type]bool) bool {
	if t == nil {
		return false
	}
	if seen[t] {
		return true
	}
	seen[t] = true
	switch t := t.(type) {
	case *types.Basic:
		return t.Kind() != types.Invalid
	case *types.Alias:
		return typeSpellable(types.Unalias(t), ctx, seen)
	case *types.Named:
		obj := t.Obj()
		if obj == nil {
			return false
		}
		if obj.Pkg() != nil && obj.Pkg() != ctx.pkg && !obj.Exported() {
			return false
		}
		for arg := range t.TypeArgs().Types() {
			if !typeSpellable(arg, ctx, seen) {
				return false
			}
		}
		return true
	case *types.Pointer:
		return typeSpellable(t.Elem(), ctx, seen)
	case *types.Slice:
		return typeSpellable(t.Elem(), ctx, seen)
	case *types.Array:
		return typeSpellable(t.Elem(), ctx, seen)
	case *types.Map:
		return typeSpellable(t.Key(), ctx, seen) && typeSpellable(t.Elem(), ctx, seen)
	case *types.Chan:
		return typeSpellable(t.Elem(), ctx, seen)
	case *types.Signature:
		return tupleSpellable(t.Params(), ctx, seen) && tupleSpellable(t.Results(), ctx, seen)
	case *types.Struct:
		for f := range t.Fields() {
			if !f.Exported() && f.Pkg() != nil && f.Pkg() != ctx.pkg {
				return false
			}
			if !typeSpellable(f.Type(), ctx, seen) {
				return false
			}
		}
		return true
	case *types.Interface:
		for m := range t.Methods() {
			if !m.Exported() && m.Pkg() != nil && m.Pkg() != ctx.pkg {
				return false
			}
			if !typeSpellable(m.Type(), ctx, seen) {
				return false
			}
		}
		for embedded := range t.EmbeddedTypes() {
			if !typeSpellable(embedded, ctx, seen) {
				return false
			}
		}
		return true
	case *types.TypeParam:
		_, ok := ctx.typeParams[t]
		return ok
	case *types.Union:
		for term := range t.Terms() {
			if !typeSpellable(term.Type(), ctx, seen) {
				return false
			}
		}
		return true
	}
	return false
}

func tupleSpellable(tuple *types.Tuple, ctx typeSpellingContext, seen map[types.Type]bool) bool {
	if tuple == nil {
		return true
	}
	for v := range tuple.Variables() {
		if !typeSpellable(v.Type(), ctx, seen) {
			return false
		}
	}
	return true
}

// expressionFact is the go/types-derived classification of one authored operand
// used by evaluation-order planning. A constant-valued or untyped-nil
// expression stays contextual and is never materialized; a tuple is consumed as
// one value before positional assembly.
type expressionFact struct {
	tv                  types.TypeAndValue
	isNil               bool
	hasOrderedOperation bool
	tuple               *types.Tuple
}

// expressionHasOrderedOperation reports whether evaluating expr executes an
// operation whose relative order Go defines lexically: a function or method
// call, a receive, or logical short-circuiting. It walks the exact expression
// AST retained by target discovery; source reconstruction or reparsing would
// lose the authority of the type-checked artifact. Function-literal bodies are
// skipped because creating a function value does not execute its body (an
// immediately invoked literal is still caught by its outer CallExpr).
func expressionHasOrderedOperation(expr goast.Expr) bool {
	found := false
	goast.Inspect(expr, func(node goast.Node) bool {
		if found {
			return false
		}
		switch node := node.(type) {
		case *goast.FuncLit:
			return false
		case *goast.CallExpr:
			found = true
			return false
		case *goast.UnaryExpr:
			if node.Op == token.ARROW {
				found = true
				return false
			}
		case *goast.BinaryExpr:
			if node.Op == token.LAND || node.Op == token.LOR {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func (f expressionFact) contextual() bool {
	return f.isNil || f.tv.Value != nil
}

// materializedValue records the lowering decision for one authored value in
// source (lexical) order. temp is the local name when the value is hoisted to a
// `:=` statement; inline values keep temp empty. unwrapTuple marks a
// (T, error) value consumed before positional assembly.
type materializedValue struct {
	valueIndex  int
	node        gsxast.Node
	temp        string
	inline      bool
	unwrapTuple bool
}

// materializationPlan is the ordered set of lowering decisions for a component
// call. Values are listed in authored (lexical) evaluation order.
type materializationPlan struct {
	values []materializedValue
}

// planComponentMaterialization decides, for each authored operand, whether it
// stays contextual/inline or must be materialized to a temporary. Authored
// expressions evaluate exactly once in source (authored) order; only their
// already-evaluated values are rearranged into signature/param order for the
// call. The rule is relational, not intrinsic: a value must be hoisted to a
// source-order temp whenever its movement into call order CROSSES a Go-ordered
// operation (a call, receive, or logical short-circuit) or a (T, error) tuple —
// i.e. its relative order versus such an ordered value differs between source
// order and call order. Otherwise the value observably reorders across a side
// effect. Constant and untyped-nil values carry no side effect and are never
// materialized; every (T, error) tuple additionally needs statement lowering
// (unwrap + error check) and is always a temp. Two side-effect-free reads that
// merely swap relative to each other cross no ordered work and stay inline —
// reordering them is unobservable.
func planComponentMaterialization(plan componentCallPlan, facts map[gsxast.Node]expressionFact) materializationPlan {
	type entry struct {
		valueIndex int
		value      componentInputValue
		fact       expressionFact
		hasFact    bool
		callOrder  [2]int
	}
	entries := make([]entry, 0, len(plan.values))
	for i, v := range plan.values {
		fact, ok := facts[v.node]
		entries = append(entries, entry{
			valueIndex: i,
			value:      v,
			fact:       fact,
			hasFact:    ok,
			// Call/assembly order is signature-parameter order, then attrs
			// contributor order within the attrs slot.
			callOrder: [2]int{v.paramIndex, v.contributorIndex},
		})
	}

	// entries are in authored (source) order. Rank each entry in call order so a
	// relative-order change (a "crossing") between the two orders is detectable.
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := entries[order[i]].callOrder, entries[order[j]].callOrder
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		return a[1] < b[1]
	})
	callRank := make([]int, len(entries))
	for rank, idx := range order {
		callRank[idx] = rank
	}

	isOrderedWork := func(e entry) bool {
		return e.hasFact && (e.fact.hasOrderedOperation || e.fact.tuple != nil)
	}
	// crosses reports whether entries i and j change relative order between source
	// order (their entries index) and call order (callRank).
	crosses := func(i, j int) bool {
		return (i < j) != (callRank[i] < callRank[j])
	}
	// crossesOrderedWork reports whether value i moves across any OTHER Go-ordered
	// operation or tuple between source and call order.
	crossesOrderedWork := func(i int) bool {
		for j, other := range entries {
			if j == i || !isOrderedWork(other) {
				continue
			}
			if crosses(i, j) {
				return true
			}
		}
		return false
	}

	out := materializationPlan{}
	tempN := 0
	for i, e := range entries {
		mv := materializedValue{valueIndex: e.valueIndex, node: e.value.node}
		switch {
		case e.hasFact && e.fact.tuple != nil:
			// A (T, error) tuple always lowers to a statement: unwrap and check
			// the error before positional assembly.
			mv.temp = fmt.Sprintf("_gsxv%d", tempN)
			mv.unwrapTuple = true
			tempN++
		case e.hasFact && e.fact.contextual():
			// Constant / untyped-nil: no side effect, no context loss; stays inline.
			mv.inline = true
		case crossesOrderedWork(i):
			// A non-constant value whose movement crosses ordered work must be
			// pinned to a source-order temp to preserve authored evaluation order.
			mv.temp = fmt.Sprintf("_gsxv%d", tempN)
			tempN++
		default:
			mv.inline = true
		}
		out.values = append(out.values, mv)
	}
	return out
}
