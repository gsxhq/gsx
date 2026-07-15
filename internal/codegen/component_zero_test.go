package codegen

import (
	"go/constant"
	"go/token"
	"go/types"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func inferTestPkg() *types.Package { return types.NewPackage("example.com/infer", "p") }

func anyConstraint() *types.Interface { return types.NewInterfaceType(nil, nil).Complete() }

func newTypeParam(pkg *types.Package, name string, constraint *types.Interface) *types.TypeParam {
	tp := types.NewTypeParam(types.NewTypeName(token.NoPos, pkg, name, nil), nil)
	tp.SetConstraint(constraint)
	return tp
}

func TestInferAuthoredInstance(t *testing.T) {
	t.Parallel()

	t.Run("Infer[T](*T) with nil fails inference", func(t *testing.T) {
		pkg := inferTestPkg()
		tp := newTypeParam(pkg, "T", anyConstraint())
		raw := types.NewSignatureType(nil, nil, []*types.TypeParam{tp},
			types.NewTuple(types.NewVar(token.NoPos, pkg, "x", types.NewPointer(tp))),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])), false)
		fact := componentTargetFact{raw: raw}
		ops := []suppliedOperand{{paramIndex: 0, tv: types.TypeAndValue{Type: types.Typ[types.UntypedNil]}}}
		inst, diags := inferAuthoredInstance(inferenceContext{pkg: pkg}, fact, ops)
		if inst.Type != nil {
			t.Fatalf("expected no instance, got %v", inst.Type)
		}
		if len(diags) != 1 || diags[0].Code != "component-type-args" {
			t.Fatalf("want one component-type-args diagnostic, got %+v", diags)
		}
	})

	t.Run("Infer[T](*T) with *int infers T=int", func(t *testing.T) {
		pkg := inferTestPkg()
		tp := newTypeParam(pkg, "T", anyConstraint())
		raw := types.NewSignatureType(nil, nil, []*types.TypeParam{tp},
			types.NewTuple(types.NewVar(token.NoPos, pkg, "x", types.NewPointer(tp))),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])), false)
		fact := componentTargetFact{raw: raw}
		ops := []suppliedOperand{{paramIndex: 0, tv: types.TypeAndValue{Type: types.NewPointer(types.Typ[types.Int])}}}
		inst, diags := inferAuthoredInstance(inferenceContext{pkg: pkg}, fact, ops)
		if len(diags) != 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		sig, ok := inst.Type.(*types.Signature)
		if !ok || sig.Params().At(0).Type().String() != "*int" {
			t.Fatalf("want instantiated func(*int), got %v", inst.Type)
		}
	})

	t.Run("constraint-derived inference", func(t *testing.T) {
		// F[T any, S ~[]T](s S): supplying s of type []int infers T=int, S=[]int.
		pkg := inferTestPkg()
		tT := newTypeParam(pkg, "T", anyConstraint())
		sConstraint := types.NewInterfaceType(nil, []types.Type{
			types.NewUnion([]*types.Term{types.NewTerm(true, types.NewSlice(tT))}),
		}).Complete()
		tS := newTypeParam(pkg, "S", sConstraint)
		raw := types.NewSignatureType(nil, nil, []*types.TypeParam{tT, tS},
			types.NewTuple(types.NewVar(token.NoPos, pkg, "s", tS)),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])), false)
		fact := componentTargetFact{raw: raw}
		ops := []suppliedOperand{{paramIndex: 0, tv: types.TypeAndValue{Type: types.NewSlice(types.Typ[types.Int])}}}
		inst, diags := inferAuthoredInstance(inferenceContext{pkg: pkg}, fact, ops)
		if len(diags) != 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		if inst.TypeArgs == nil || inst.TypeArgs.Len() != 2 ||
			types.Unalias(inst.TypeArgs.At(0)).String() != "int" ||
			types.Unalias(inst.TypeArgs.At(1)).String() != "[]int" {
			t.Fatalf("want inferred [int, []int], got %v", inst.TypeArgs)
		}
	})

	t.Run("imported unexported constraint is usable without naming", func(t *testing.T) {
		// Origin constraint is an unexported foreign interface{ M() }; supplying a
		// concrete implementer infers the type parameter with no source spelling.
		dep := types.NewPackage("example.com/dep", "dep")
		mSig := types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false)
		secret := types.NewInterfaceType([]*types.Func{types.NewFunc(token.NoPos, dep, "M", mSig)}, nil).Complete()
		impl := types.NewNamed(types.NewTypeName(token.NoPos, dep, "Impl", nil), types.NewStruct(nil, nil), nil)
		impl.AddMethod(types.NewFunc(token.NoPos, dep, "M", types.NewSignatureType(types.NewVar(token.NoPos, dep, "", impl), nil, nil, types.NewTuple(), types.NewTuple(), false)))

		pkg := inferTestPkg()
		tp := newTypeParam(pkg, "T", secret)
		raw := types.NewSignatureType(nil, nil, []*types.TypeParam{tp},
			types.NewTuple(types.NewVar(token.NoPos, pkg, "x", tp)),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])), false)
		fact := componentTargetFact{raw: raw}
		ops := []suppliedOperand{{paramIndex: 0, tv: types.TypeAndValue{Type: impl}}}
		inst, diags := inferAuthoredInstance(inferenceContext{pkg: pkg}, fact, ops)
		if len(diags) != 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		if inst.TypeArgs == nil || inst.TypeArgs.Len() != 1 || types.Unalias(inst.TypeArgs.At(0)).String() != "example.com/dep.Impl" {
			t.Fatalf("want inferred [dep.Impl], got %v", inst.TypeArgs)
		}
	})

	t.Run("explicit prefix completes the rest", func(t *testing.T) {
		// Pair[A, B any](a A, b B): explicit [int] plus a string operand for b.
		pkg := inferTestPkg()
		tA := newTypeParam(pkg, "A", anyConstraint())
		tB := newTypeParam(pkg, "B", anyConstraint())
		raw := types.NewSignatureType(nil, nil, []*types.TypeParam{tA, tB},
			types.NewTuple(
				types.NewVar(token.NoPos, pkg, "a", tA),
				types.NewVar(token.NoPos, pkg, "b", tB),
			),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])), false)
		fact := componentTargetFact{raw: raw, authoredTypeArgs: []authoredTypeArgFact{{typ: types.Typ[types.Int]}}}
		ops := []suppliedOperand{{paramIndex: 1, tv: types.TypeAndValue{Type: types.Typ[types.String]}}}
		inst, diags := inferAuthoredInstance(inferenceContext{pkg: pkg}, fact, ops)
		if len(diags) != 0 {
			t.Fatalf("unexpected diagnostics: %+v", diags)
		}
		if inst.TypeArgs == nil || inst.TypeArgs.Len() != 2 ||
			types.Unalias(inst.TypeArgs.At(0)).String() != "int" ||
			types.Unalias(inst.TypeArgs.At(1)).String() != "string" {
			t.Fatalf("want [int, string], got %v", inst.TypeArgs)
		}
	})

	t.Run("constraint violation with a complete instance is the native diagnostic", func(t *testing.T) {
		// F[T int | float64](x T): supplying a string operand infers T=string,
		// which go/types records as a complete instance UNDER a constraint error.
		// That is a constraint failure, not incompleteness: the native
		// component-constraint diagnostic is surfaced and no instance is returned
		// (so lowering never zero-fills).
		pkg := inferTestPkg()
		number := types.NewInterfaceType(nil, []types.Type{
			types.NewUnion([]*types.Term{
				types.NewTerm(false, types.Typ[types.Int]),
				types.NewTerm(false, types.Typ[types.Float64]),
			}),
		}).Complete()
		tp := newTypeParam(pkg, "T", number)
		raw := types.NewSignatureType(nil, nil, []*types.TypeParam{tp},
			types.NewTuple(types.NewVar(token.NoPos, pkg, "x", tp)),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])), false)
		fact := componentTargetFact{raw: raw}
		ops := []suppliedOperand{{paramIndex: 0, tv: types.TypeAndValue{Type: types.Typ[types.String]}}}
		inst, diags := inferAuthoredInstance(inferenceContext{pkg: pkg}, fact, ops)
		if inst.Type != nil {
			t.Fatalf("a constraint violation must not zero-fill; got instance %v", inst.Type)
		}
		if len(diags) == 0 {
			t.Fatal("want a native constraint diagnostic, got none")
		}
		for _, d := range diags {
			if d.Code != "component-constraint" {
				t.Fatalf("want component-constraint diagnostics, got %+v", diags)
			}
		}
	})

	t.Run("non-generic target returns raw", func(t *testing.T) {
		pkg := inferTestPkg()
		raw := types.NewSignatureType(nil, nil, nil,
			types.NewTuple(types.NewVar(token.NoPos, pkg, "s", types.Typ[types.String])),
			types.NewTuple(types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])), false)
		fact := componentTargetFact{raw: raw}
		inst, diags := inferAuthoredInstance(inferenceContext{pkg: pkg}, fact, nil)
		if len(diags) != 0 || inst.Type != raw {
			t.Fatalf("non-generic target should return raw with no diags, got %v %+v", inst.Type, diags)
		}
	})
}

func TestSemanticZeroLiteral(t *testing.T) {
	t.Parallel()
	pkg := inferTestPkg()
	cases := []struct {
		name    string
		typ     types.Type
		wantLit string
		wantOK  bool
	}{
		{"string", types.Typ[types.String], `""`, true},
		{"int", types.Typ[types.Int], "0", true},
		{"float64", types.Typ[types.Float64], "0", true},
		{"bool", types.Typ[types.Bool], "false", true},
		{"pointer", types.NewPointer(types.Typ[types.Int]), "nil", true},
		{"slice", types.NewSlice(types.Typ[types.Int]), "nil", true},
		{"map", types.NewMap(types.Typ[types.String], types.Typ[types.Int]), "nil", true},
		{"any", types.Universe.Lookup("any").Type(), "nil", true},
		{"non-empty interface", types.NewInterfaceType([]*types.Func{
			types.NewFunc(token.NoPos, pkg, "M", types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false)),
		}, nil).Complete(), "nil", true},
		{"struct has no literal", types.NewStruct(nil, nil), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lit, ok := semanticZeroLiteral(tc.typ)
			if lit != tc.wantLit || ok != tc.wantOK {
				t.Fatalf("semanticZeroLiteral(%s) = (%q,%v), want (%q,%v)", tc.name, lit, ok, tc.wantLit, tc.wantOK)
			}
		})
	}

	t.Run("string-only type set", func(t *testing.T) {
		tp := newTypeParam(pkg, "T", types.NewInterfaceType(nil, []types.Type{
			types.NewUnion([]*types.Term{types.NewTerm(true, types.Typ[types.String])}),
		}).Complete())
		if lit, ok := semanticZeroLiteral(tp); !ok || lit != `""` {
			t.Fatalf("string type set = (%q,%v), want (%q,true)", lit, ok, `""`)
		}
	})

	t.Run("mixed type set has no literal", func(t *testing.T) {
		tp := newTypeParam(pkg, "T", types.NewInterfaceType(nil, []types.Type{
			types.NewUnion([]*types.Term{
				types.NewTerm(false, types.Typ[types.Int]),
				types.NewTerm(false, types.Typ[types.String]),
			}),
		}).Complete())
		if lit, ok := semanticZeroLiteral(tp); ok {
			t.Fatalf("mixed type set should have no literal, got %q", lit)
		}
	})
}

func TestZeroCandidates(t *testing.T) {
	t.Parallel()
	caller := types.NewPackage("example.com/caller", "caller")
	dep := types.NewPackage("example.com/dep", "dep")

	newCtx := func() typeSpellingContext {
		return typeSpellingContext{pkg: caller, imports: newGeneratedImportAllocator("_gsxty")}
	}

	t.Run("numeric literal first", func(t *testing.T) {
		got := zeroCandidates(types.Typ[types.Int], newCtx())
		if len(got) == 0 || got[0].expr != "0" {
			t.Fatalf("want leading 0 candidate, got %+v", got)
		}
	})

	t.Run("nameable exact *new(T) for a same-package struct", func(t *testing.T) {
		local := types.NewNamed(types.NewTypeName(token.NoPos, caller, "Local", nil), types.NewStruct(nil, nil), nil)
		got := zeroCandidates(local, newCtx())
		if len(got) != 1 || got[0].expr != "*new(Local)" {
			t.Fatalf("want single *new(Local) candidate, got %+v", got)
		}
	})

	t.Run("exported foreign type gets a reserved alias", func(t *testing.T) {
		widget := types.NewNamed(types.NewTypeName(token.NoPos, dep, "Widget", nil), types.NewStruct(nil, nil), nil)
		ctx := newCtx()
		got := zeroCandidates(widget, ctx)
		if len(got) != 1 || got[0].expr != "*new(_gsxty1.Widget)" || got[0].imports == nil {
			t.Fatalf("want aliased *new candidate, got %+v", got)
		}
		if len(ctx.imports.specs()) != 0 {
			t.Fatal("no import may be committed before the winner commits")
		}
		got[0].imports.commit()
		specs := ctx.imports.specs()
		if len(specs) != 1 || specs[0].name != "_gsxty1" || specs[0].path != "example.com/dep" {
			t.Fatalf("committed specs = %+v", specs)
		}
	})

	t.Run("accessible unnamed underlying *new(U)", func(t *testing.T) {
		// Unexported foreign named type whose underlying struct has exported
		// fields: exact spelling fails, the unnamed underlying succeeds.
		fields := []*types.Var{
			types.NewField(token.NoPos, dep, "X", types.Typ[types.Int], false),
			types.NewField(token.NoPos, dep, "Y", types.Typ[types.Int], false),
		}
		point := types.NewNamed(types.NewTypeName(token.NoPos, dep, "point", nil), types.NewStruct(fields, nil), nil)
		got := zeroCandidates(point, newCtx())
		if len(got) != 1 || got[0].expr != "*new(struct{X int; Y int})" {
			t.Fatalf("want unnamed-underlying candidate, got %+v", got)
		}
	})

	t.Run("imported opaque struct has no candidate", func(t *testing.T) {
		id := types.NewField(token.NoPos, dep, "id", types.Typ[types.Uint64], false)
		theme := types.NewNamed(types.NewTypeName(token.NoPos, dep, "theme", nil), types.NewStruct([]*types.Var{id}, nil), nil)
		if got := zeroCandidates(theme, newCtx()); len(got) != 0 {
			t.Fatalf("opaque struct must yield no candidate, got %+v", got)
		}
	})

	t.Run("opaque numeric type keeps the literal", func(t *testing.T) {
		count := types.NewNamed(types.NewTypeName(token.NoPos, dep, "count", nil), types.Typ[types.Int], nil)
		got := zeroCandidates(count, newCtx())
		if len(got) != 1 || got[0].expr != "0" {
			t.Fatalf("opaque numeric must keep the 0 literal, got %+v", got)
		}
	})

	t.Run("exact rejected, unnamed-underlying commits only the winner", func(t *testing.T) {
		fields := []*types.Var{types.NewField(token.NoPos, dep, "W", types.NewNamed(types.NewTypeName(token.NoPos, dep, "Widget", nil), types.NewStruct(nil, nil), nil), false)}
		box := types.NewNamed(types.NewTypeName(token.NoPos, dep, "box", nil), types.NewStruct(fields, nil), nil)
		ctx := newCtx()
		got := zeroCandidates(box, ctx)
		if len(got) != 1 {
			t.Fatalf("want one candidate, got %+v", got)
		}
		if len(ctx.imports.specs()) != 0 {
			t.Fatal("nothing committed yet")
		}
		got[0].imports.commit()
		if specs := ctx.imports.specs(); len(specs) != 1 || specs[0].path != "example.com/dep" {
			t.Fatalf("committed specs = %+v", specs)
		}
	})

	t.Run("accessible unnamed containing an exported foreign type", func(t *testing.T) {
		widget := types.NewNamed(types.NewTypeName(token.NoPos, dep, "Widget", nil), types.NewStruct(nil, nil), nil)
		got := zeroCandidates(types.NewSlice(widget), newCtx())
		// A slice is nilable, so the literal comes first.
		if len(got) == 0 || got[0].expr != "nil" {
			t.Fatalf("slice zero literal should be nil, got %+v", got)
		}
	})

	t.Run("two packages with the same declared name get distinct aliases", func(t *testing.T) {
		depA := types.NewPackage("example.com/a/util", "util")
		depB := types.NewPackage("example.com/b/util", "util")
		wa := types.NewNamed(types.NewTypeName(token.NoPos, depA, "A", nil), types.NewStruct(nil, nil), nil)
		wb := types.NewNamed(types.NewTypeName(token.NoPos, depB, "B", nil), types.NewStruct(nil, nil), nil)
		fields := []*types.Var{
			types.NewField(token.NoPos, caller, "A", wa, false),
			types.NewField(token.NoPos, caller, "B", wb, false),
		}
		ctx := newCtx()
		txn, spell, ok := spellType(types.NewStruct(fields, nil), ctx)
		if !ok {
			t.Fatal("struct of two exported foreign types must be spellable")
		}
		txn.commit()
		if spell == "" || len(ctx.imports.specs()) != 2 {
			t.Fatalf("want two distinct aliases, got %q %+v", spell, ctx.imports.specs())
		}
	})

	t.Run("repeated references to one path reuse one alias", func(t *testing.T) {
		widget := types.NewNamed(types.NewTypeName(token.NoPos, dep, "Widget", nil), types.NewStruct(nil, nil), nil)
		ctx := newCtx()
		txn, _, ok := spellType(types.NewMap(widget, widget), ctx)
		if !ok {
			t.Fatal("map[Widget]Widget must be spellable")
		}
		txn.commit()
		if len(ctx.imports.specs()) != 1 {
			t.Fatalf("repeated path must reuse one alias, got %+v", ctx.imports.specs())
		}
	})
}

func TestPlanComponentMaterialization(t *testing.T) {
	t.Parallel()

	t.Run("authored order inversion materializes both calls", func(t *testing.T) {
		first := &gsxast.ExprAttr{Name: "b", Expr: "first()"}
		second := &gsxast.ExprAttr{Name: "a", Expr: "second()"}
		plan := componentCallPlan{values: []componentInputValue{
			{kind: componentInputProp, paramIndex: 1, contributorIndex: -1, node: first},
			{kind: componentInputProp, paramIndex: 0, contributorIndex: -1, node: second},
		}}
		facts := map[gsxast.Node]expressionFact{
			first:  {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
			second: {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
		}
		got := planComponentMaterialization(plan, facts)
		if len(got.values) != 2 || got.values[0].temp == "" || got.values[1].temp == "" {
			t.Fatalf("both reordered calls must materialize, got %+v", got.values)
		}
		if got.values[0].inline || got.values[1].inline {
			t.Fatal("materialized values must not be inline")
		}
	})

	t.Run("untyped constant stays inline", func(t *testing.T) {
		n := &gsxast.ExprAttr{Name: "n", Expr: "min(1, 2)"}
		plan := componentCallPlan{values: []componentInputValue{
			{kind: componentInputProp, paramIndex: 0, contributorIndex: -1, node: n},
		}}
		facts := map[gsxast.Node]expressionFact{
			n: {tv: types.TypeAndValue{Type: types.Typ[types.UntypedInt], Value: constant.MakeInt64(1)}},
		}
		got := planComponentMaterialization(plan, facts)
		if len(got.values) != 1 || !got.values[0].inline || got.values[0].temp != "" {
			t.Fatalf("constant must stay inline, got %+v", got.values)
		}
	})

	t.Run("tuple consumed before assembly", func(t *testing.T) {
		n := &gsxast.ExprAttr{Name: "v", Expr: "load()"}
		plan := componentCallPlan{values: []componentInputValue{
			{kind: componentInputProp, paramIndex: 0, contributorIndex: -1, node: n},
		}}
		tuple := types.NewTuple(
			types.NewVar(token.NoPos, nil, "", types.Typ[types.String]),
			types.NewVar(token.NoPos, nil, "", types.Universe.Lookup("error").Type()),
		)
		facts := map[gsxast.Node]expressionFact{
			n: {tv: types.TypeAndValue{Type: tuple}, tuple: tuple, hasOrderedOperation: true},
		}
		got := planComponentMaterialization(plan, facts)
		if len(got.values) != 1 || !got.values[0].unwrapTuple || got.values[0].temp == "" {
			t.Fatalf("tuple value must unwrap to a temp, got %+v", got.values)
		}
	})

	t.Run("pure value reordered across a side effect is materialized", func(t *testing.T) {
		// Authored order: x (a pure, non-constant read) then sink() (a call).
		// x ends up at paramIndex 1, sink() at paramIndex 0, so a naive inline
		// call `Comp(sink(), x)` would read x AFTER sink() runs. x must be
		// hoisted to a source-order temp so it observes the authored-order value.
		x := &gsxast.ExprAttr{Name: "a", Expr: "x"}
		sink := &gsxast.ExprAttr{Name: "b", Expr: "sink()"}
		plan := componentCallPlan{values: []componentInputValue{
			{kind: componentInputProp, paramIndex: 1, contributorIndex: -1, node: x},
			{kind: componentInputProp, paramIndex: 0, contributorIndex: -1, node: sink},
		}}
		facts := map[gsxast.Node]expressionFact{
			x:    {tv: types.TypeAndValue{Type: types.Typ[types.String]}},
			sink: {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
		}
		got := planComponentMaterialization(plan, facts)
		// out.values are in authored order: x first, sink second.
		if got.values[0].inline || got.values[0].temp == "" {
			t.Fatalf("x crosses a side effect and must be materialized, got %+v", got.values[0])
		}
		if !got.values[1].inline || got.values[1].temp != "" {
			t.Fatalf("sink() crosses no other ordered value and stays inline, got %+v", got.values[1])
		}
	})

	t.Run("single ordered value not reordered stays inline", func(t *testing.T) {
		n := &gsxast.ExprAttr{Name: "a", Expr: "call()"}
		plan := componentCallPlan{values: []componentInputValue{
			{kind: componentInputProp, paramIndex: 0, contributorIndex: -1, node: n},
		}}
		facts := map[gsxast.Node]expressionFact{
			n: {tv: types.TypeAndValue{Type: types.Typ[types.String]}, hasOrderedOperation: true},
		}
		got := planComponentMaterialization(plan, facts)
		if !got.values[0].inline || got.values[0].temp != "" {
			t.Fatalf("a lone ordered value needs no temp, got %+v", got.values)
		}
	})
}
