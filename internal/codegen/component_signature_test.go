package codegen

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func TestParseComponentParamDecls(t *testing.T) {
	assertDeclParams := func(src string, wantNames, wantTypes []string, wantVariadic []bool) []componentParamDecl {
		t.Helper()
		got, err := parseComponentParamDecls(src)
		if err != nil {
			t.Fatalf("parseComponentParamDecls(%q): %v", src, err)
		}
		if len(got) != len(wantNames) {
			t.Fatalf("parseComponentParamDecls(%q) = %d params, want %d", src, len(got), len(wantNames))
		}
		for i, p := range got {
			if p.name != wantNames[i] || p.normalizedType != wantTypes[i] || p.variadic != wantVariadic[i] {
				t.Errorf("param %d of %q = {name:%q normalizedType:%q variadic:%t}, want {%q %q %t}",
					i, src, p.name, p.normalizedType, p.variadic, wantNames[i], wantTypes[i], wantVariadic[i])
			}
			trimmed := strings.TrimSpace(src)
			if p.typeOff < 0 || p.typeLen != len(p.typeSrc) || p.typeOff+p.typeLen > len(trimmed) {
				t.Fatalf("param %d of %q has invalid type span [%d,%d)", i, src, p.typeOff, p.typeOff+p.typeLen)
			}
			if gotType := trimmed[p.typeOff : p.typeOff+p.typeLen]; gotType != p.typeSrc {
				t.Errorf("param %d of %q type span = %q, want typeSrc %q", i, src, gotType, p.typeSrc)
			}
			if p.name != "" {
				if p.nameOff < 0 || p.nameOff+len(p.name) > len(trimmed) || trimmed[p.nameOff:p.nameOff+len(p.name)] != p.name {
					t.Errorf("param %d of %q has invalid name span at %d", i, src, p.nameOff)
				}
			}
		}
		return got
	}

	assertDeclParams(
		"a, b string, _ bool, rest ...byte",
		[]string{"a", "b", "_", "rest"},
		[]string{"string", "string", "bool", "...byte"},
		[]bool{false, false, false, true},
	)
	unnamed := assertDeclParams(
		"string, bool, ...byte",
		[]string{"", "", ""},
		[]string{"string", "bool", "...byte"},
		[]bool{false, false, true},
	)
	for i, p := range unnamed {
		if p.nameOff != -1 {
			t.Fatalf("unnamed param %d nameOff=%d, want -1", i, p.nameOff)
		}
	}

	roles, err := parseComponentParamDecls("children gsx.Node, value string, attrs ...gsx.Attr")
	if err != nil {
		t.Fatal(err)
	}
	wantRoles := []declarationParamRole{declarationParamChildren, declarationParamOrdinary, declarationParamAttrs}
	for i, p := range roles {
		if p.role != wantRoles[i] {
			t.Errorf("param %q role=%d, want %d", p.name, p.role, wantRoles[i])
		}
	}
}

func TestComponentDeclarationCanonical(t *testing.T) {
	a := mustParseComponent(t, "package v\ncomponent C(a, b string, attrs ...gsx.Attr) { <i/> }\n")
	b := mustParseComponent(t, "package v\ncomponent C(a string, b string, attrs ...gsx.Attr) { <b/> }\n")
	c := mustParseComponent(t, "package v\ncomponent C(b string, a string, attrs ...gsx.Attr) { <i/> }\n")
	d := mustParseComponent(t, "package v\ncomponent C(a string, b string, attrs []gsx.Attr) { <i/> }\n")
	sa, err := componentDeclarationFor(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := componentDeclarationFor(b)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := componentDeclarationFor(c)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := componentDeclarationFor(d)
	if err != nil {
		t.Fatal(err)
	}
	if sa.canonical() != sb.canonical() {
		t.Fatal("grouped and ungrouped logical parameters must match")
	}
	if sa.canonical() == sc.canonical() {
		t.Fatal("parameter reorder must change the contract")
	}
	if sa.canonical() == sd.canonical() {
		t.Fatal("variadic position must change the contract")
	}
}

func TestComponentDeclarationRenameChangesContract(t *testing.T) {
	a := mustParseComponent(t, "package v\ncomponent C(value string) { <i/> }\n")
	b := mustParseComponent(t, "package v\ncomponent C(label string) { <i/> }\n")
	sa, err := componentDeclarationFor(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := componentDeclarationFor(b)
	if err != nil {
		t.Fatal(err)
	}
	if sa.canonical() == sb.canonical() {
		t.Fatal("parameter name is part of the markup contract")
	}
}

func TestComponentDeclarationCanonicalIsCollisionSafe(t *testing.T) {
	a := componentDeclaration{params: []componentParamDecl{{name: "a", normalizedType: "bc"}}}
	b := componentDeclaration{params: []componentParamDecl{{name: "ab", normalizedType: "c"}}}
	if a.canonical() == b.canonical() {
		t.Fatal("length-prefixed fields must distinguish ambiguous concatenations")
	}
	ordinary := componentDeclaration{params: []componentParamDecl{{name: "attrs", normalizedType: "gsx.Attrs", role: declarationParamOrdinary}}}
	attrs := componentDeclaration{params: []componentParamDecl{{name: "attrs", normalizedType: "gsx.Attrs", role: declarationParamAttrs}}}
	if ordinary.canonical() == attrs.canonical() {
		t.Fatal("reserved role must be encoded in the declaration contract")
	}
}

func TestReservedInputRolesApplyOnlyToParameters(t *testing.T) {
	for _, receiver := range []string{"children", "attrs"} {
		if err := checkReservedRecvVar(receiver); err != nil {
			t.Errorf("receiver %q is not a parameter and must remain an ordinary Go binding: %v", receiver, err)
		}
	}
	for _, reserved := range []string{"ctx", "_gsxvalue"} {
		if err := checkReservedRecvVar(reserved); err == nil {
			t.Errorf("receiver %q must remain reserved", reserved)
		}
	}
}

type signatureRuntimeFixture struct {
	runtime runtimeContract
	pkg     *types.Package
}

func newSignatureRuntimeFixture(t *testing.T) signatureRuntimeFixture {
	t.Helper()
	pkg := types.NewPackage(gsxRuntimePath, "gsx")

	renderSig := types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false)
	render := types.NewFunc(token.NoPos, pkg, "Render", renderSig)
	nodeIface := types.NewInterfaceType([]*types.Func{render}, nil).Complete()
	node := testNamedType(t, pkg, "Node", nodeIface)
	attr := testNamedType(t, pkg, "Attr", types.NewStruct(nil, nil))
	attrs := testNamedType(t, pkg, "Attrs", types.NewSlice(attr))

	return signatureRuntimeFixture{
		pkg: pkg,
		runtime: runtimeContract{
			node:  node,
			attr:  attr,
			attrs: attrs,
		},
	}
}

func TestRuntimeContractFromAnalysisPackage(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	fx.pkg.MarkComplete()
	analysis := types.NewPackage("example.test/analysis", "analysis")
	analysis.SetImports([]*types.Package{fx.pkg})
	analysis.MarkComplete()

	got, err := runtimeContractFromAnalysisPackage(analysis)
	if err != nil {
		t.Fatal(err)
	}
	if !types.Identical(got.node, fx.runtime.node) ||
		!types.Identical(got.attr, fx.runtime.attr) ||
		!types.Identical(got.attrs, fx.runtime.attrs) {
		t.Fatalf("runtime contract = %#v, want exact imported Node/Attr/Attrs identities", got)
	}
}

func TestRuntimeContractFromAnalysisPackageUsesDirectSemanticImport(t *testing.T) {
	runtimePkg := types.NewPackage(gsxRuntimePath, "not_the_source_alias")
	node := testNamedType(t, runtimePkg, "Node", types.NewInterfaceType(nil, nil).Complete())
	attr := testNamedType(t, runtimePkg, "Attr", types.NewStruct(nil, nil))
	attrs := testNamedType(t, runtimePkg, "Attrs", types.NewSlice(attr))
	runtimePkg.MarkComplete()

	analysis := types.NewPackage("example.test/analysis", "analysis")
	analysis.SetImports([]*types.Package{runtimePkg})
	analysis.MarkComplete()
	got, err := runtimeContractFromAnalysisPackage(analysis)
	if err != nil {
		t.Fatal(err)
	}
	if got.node != node || got.attr != attr || got.attrs != attrs {
		t.Fatalf("runtime contract did not retain the direct semantic import identities: %#v", got)
	}

	transitive := types.NewPackage("example.test/transitive", "transitive")
	transitive.SetImports([]*types.Package{runtimePkg})
	transitive.MarkComplete()
	withoutDirectRuntime := types.NewPackage("example.test/without-direct-runtime", "withoutdirect")
	withoutDirectRuntime.SetImports([]*types.Package{transitive})
	withoutDirectRuntime.MarkComplete()
	if _, err := runtimeContractFromAnalysisPackage(withoutDirectRuntime); err == nil || !strings.Contains(err.Error(), "does not directly import") {
		t.Fatalf("transitive-only runtime import error = %v, want direct-import failure", err)
	}
}

func TestRuntimeContractFromAnalysisPackageRejectsInvalidRuntime(t *testing.T) {
	analysisWith := func(runtimePkg *types.Package) *types.Package {
		analysis := types.NewPackage("example.test/analysis", "analysis")
		if runtimePkg != nil {
			analysis.SetImports([]*types.Package{runtimePkg})
		}
		analysis.MarkComplete()
		return analysis
	}
	newRuntime := func(t *testing.T, define func(*types.Package)) *types.Package {
		t.Helper()
		pkg := types.NewPackage(gsxRuntimePath, "gsx")
		define(pkg)
		pkg.MarkComplete()
		return pkg
	}
	validTypes := func(t *testing.T, pkg *types.Package) (types.Type, types.Type, types.Type) {
		t.Helper()
		node := testNamedType(t, pkg, "Node", types.NewInterfaceType(nil, nil).Complete())
		attr := testNamedType(t, pkg, "Attr", types.NewStruct(nil, nil))
		attrs := testNamedType(t, pkg, "Attrs", types.NewSlice(attr))
		return node, attr, attrs
	}

	t.Run("nil analysis package", func(t *testing.T) {
		if _, err := runtimeContractFromAnalysisPackage(nil); err == nil || !strings.Contains(err.Error(), "nil analysis package") {
			t.Fatalf("error = %v, want nil-analysis failure", err)
		}
	})

	t.Run("missing direct import", func(t *testing.T) {
		if _, err := runtimeContractFromAnalysisPackage(analysisWith(nil)); err == nil || !strings.Contains(err.Error(), "does not directly import") {
			t.Fatalf("error = %v, want missing-runtime failure", err)
		}
	})

	t.Run("incomplete runtime package", func(t *testing.T) {
		pkg := types.NewPackage(gsxRuntimePath, "gsx")
		validTypes(t, pkg)
		if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), "is incomplete") {
			t.Fatalf("error = %v, want incomplete-runtime failure", err)
		}
	})

	t.Run("multiple same-path semantic identities", func(t *testing.T) {
		first := newRuntime(t, func(pkg *types.Package) { validTypes(t, pkg) })
		second := newRuntime(t, func(pkg *types.Package) { validTypes(t, pkg) })
		analysis := types.NewPackage("example.test/analysis", "analysis")
		analysis.SetImports([]*types.Package{first, second})
		analysis.MarkComplete()
		if _, err := runtimeContractFromAnalysisPackage(analysis); err == nil || !strings.Contains(err.Error(), "multiple semantic package identities") {
			t.Fatalf("error = %v, want ambiguous-semantic-identity failure", err)
		}
	})

	t.Run("foreign type object identity", func(t *testing.T) {
		pkg := types.NewPackage(gsxRuntimePath, "gsx")
		foreign := types.NewPackage(gsxRuntimePath, "gsx")
		foreignNode := testNamedType(t, foreign, "Node", types.NewInterfaceType(nil, nil).Complete())
		if alt := pkg.Scope().Insert(foreignNode.Obj()); alt != nil {
			t.Fatalf("insert foreign Node: conflict with %s", alt)
		}
		attr := testNamedType(t, pkg, "Attr", types.NewStruct(nil, nil))
		testNamedType(t, pkg, "Attrs", types.NewSlice(attr))
		pkg.MarkComplete()
		if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), "foreign semantic package identity") {
			t.Fatalf("error = %v, want foreign-object-identity failure", err)
		}
	})

	t.Run("incomplete type identity", func(t *testing.T) {
		pkg := newRuntime(t, func(pkg *types.Package) {
			testNamedType(t, pkg, "Node", types.NewInterfaceType(nil, nil).Complete())
			obj := types.NewTypeName(token.NoPos, pkg, "Attr", nil)
			types.NewAlias(obj, nil)
			if alt := pkg.Scope().Insert(obj); alt != nil {
				t.Fatalf("insert incomplete Attr: conflict with %s", alt)
			}
			testNamedType(t, pkg, "Attrs", types.NewSlice(types.NewStruct(nil, nil)))
		})
		if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), "Attr has an incomplete or invalid type") {
			t.Fatalf("error = %v, want incomplete-type failure", err)
		}
	})

	for _, name := range []string{"Node", "Attr", "Attrs"} {
		t.Run("missing "+name, func(t *testing.T) {
			pkg := newRuntime(t, func(pkg *types.Package) {
				if name != "Node" {
					testNamedType(t, pkg, "Node", types.NewInterfaceType(nil, nil).Complete())
				}
				var attr types.Type
				if name != "Attr" {
					attr = testNamedType(t, pkg, "Attr", types.NewStruct(nil, nil))
				}
				if name != "Attrs" {
					if attr == nil {
						attr = types.NewStruct(nil, nil)
					}
					testNamedType(t, pkg, "Attrs", types.NewSlice(attr))
				}
			})
			if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), "missing type "+name) {
				t.Fatalf("error = %v, want missing-%s failure", err, name)
			}
		})

		t.Run("non-type "+name, func(t *testing.T) {
			pkg := newRuntime(t, func(pkg *types.Package) {
				if name == "Node" {
					pkg.Scope().Insert(types.NewVar(token.NoPos, pkg, "Node", types.NewInterfaceType(nil, nil).Complete()))
				} else {
					testNamedType(t, pkg, "Node", types.NewInterfaceType(nil, nil).Complete())
				}
				var attr types.Type = types.NewStruct(nil, nil)
				if name == "Attr" {
					pkg.Scope().Insert(types.NewVar(token.NoPos, pkg, "Attr", attr))
				} else {
					attr = testNamedType(t, pkg, "Attr", attr)
				}
				attrsType := types.NewSlice(attr)
				if name == "Attrs" {
					pkg.Scope().Insert(types.NewVar(token.NoPos, pkg, "Attrs", attrsType))
				} else {
					testNamedType(t, pkg, "Attrs", attrsType)
				}
			})
			if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), name+" is not a type name") {
				t.Fatalf("error = %v, want non-type-%s failure", err, name)
			}
		})

		t.Run("invalid "+name, func(t *testing.T) {
			pkg := newRuntime(t, func(pkg *types.Package) {
				if name == "Node" {
					pkg.Scope().Insert(types.NewTypeName(token.NoPos, pkg, "Node", types.Typ[types.Invalid]))
				} else {
					testNamedType(t, pkg, "Node", types.NewInterfaceType(nil, nil).Complete())
				}
				var attr types.Type = types.NewStruct(nil, nil)
				if name == "Attr" {
					pkg.Scope().Insert(types.NewTypeName(token.NoPos, pkg, "Attr", types.Typ[types.Invalid]))
				} else {
					attr = testNamedType(t, pkg, "Attr", attr)
				}
				if name == "Attrs" {
					pkg.Scope().Insert(types.NewTypeName(token.NoPos, pkg, "Attrs", types.Typ[types.Invalid]))
				} else {
					testNamedType(t, pkg, "Attrs", types.NewSlice(attr))
				}
			})
			if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), name+" has an incomplete or invalid type") {
				t.Fatalf("error = %v, want invalid-%s failure", err, name)
			}
		})
	}

	t.Run("Attrs has wrong element", func(t *testing.T) {
		pkg := newRuntime(t, func(pkg *types.Package) {
			testNamedType(t, pkg, "Node", types.NewInterfaceType(nil, nil).Complete())
			testNamedType(t, pkg, "Attr", types.NewStruct(nil, nil))
			testNamedType(t, pkg, "Attrs", types.NewSlice(types.Typ[types.String]))
		})
		if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), "does not have underlying []") {
			t.Fatalf("error = %v, want exact-Attrs-element failure", err)
		}
	})

	t.Run("Attrs has same-path foreign Attr identity", func(t *testing.T) {
		pkg := types.NewPackage(gsxRuntimePath, "gsx")
		testNamedType(t, pkg, "Node", types.NewInterfaceType(nil, nil).Complete())
		testNamedType(t, pkg, "Attr", types.NewStruct(nil, nil))
		foreign := types.NewPackage(gsxRuntimePath, "gsx")
		foreignAttr := testNamedType(t, foreign, "Attr", types.NewStruct(nil, nil))
		testNamedType(t, pkg, "Attrs", types.NewSlice(foreignAttr))
		pkg.MarkComplete()
		if _, err := runtimeContractFromAnalysisPackage(analysisWith(pkg)); err == nil || !strings.Contains(err.Error(), "does not have underlying []") {
			t.Fatalf("error = %v, want semantic-identity failure", err)
		}
	})
}

func testNamedType(t *testing.T, pkg *types.Package, name string, underlying types.Type) *types.Named {
	t.Helper()
	obj := types.NewTypeName(token.NoPos, pkg, name, nil)
	named := types.NewNamed(obj, underlying, nil)
	if alt := pkg.Scope().Insert(obj); alt != nil {
		t.Fatalf("insert type %s: conflicts with %s", name, alt.Name())
	}
	return named
}

func testAliasType(t *testing.T, pkg *types.Package, name string, rhs types.Type) *types.Alias {
	t.Helper()
	obj := types.NewTypeName(token.NoPos, pkg, name, nil)
	alias := types.NewAlias(obj, rhs)
	if alt := pkg.Scope().Insert(obj); alt != nil {
		t.Fatalf("insert alias %s: conflicts with %s", name, alt.Name())
	}
	return alias
}

func testSignature(pkg *types.Package, recv *types.Var, params []*types.Var, results []types.Type, variadic bool) *types.Signature {
	resultVars := make([]*types.Var, len(results))
	for i, result := range results {
		resultVars[i] = types.NewVar(token.NoPos, pkg, "", result)
	}
	return types.NewSignatureType(recv, nil, nil, types.NewTuple(params...), types.NewTuple(resultVars...), variadic)
}

func testParam(pkg *types.Package, name string, typ types.Type) *types.Var {
	return types.NewVar(token.NoPos, pkg, name, typ)
}

func testAnyConstraint() *types.Interface {
	return types.NewInterfaceType(nil, nil).Complete()
}

func testRenderableType(t *testing.T, pkg *types.Package, name string, pointerOnly bool) *types.Named {
	t.Helper()
	named := testNamedType(t, pkg, name, types.NewStruct(nil, nil))
	var recvType types.Type = named
	if pointerOnly {
		recvType = types.NewPointer(named)
	}
	recv := types.NewVar(token.NoPos, pkg, "", recvType)
	renderSig := types.NewSignatureType(recv, nil, nil, types.NewTuple(), types.NewTuple(), false)
	named.AddMethod(types.NewFunc(token.NoPos, pkg, "Render", renderSig))
	return named
}

func TestAnalyzeComponentSignatureRoles(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	stringType := types.Typ[types.String]
	params := []*types.Var{
		testParam(fx.pkg, "title", stringType),
		testParam(fx.pkg, "_foo", stringType),
		testParam(fx.pkg, "Children", fx.runtime.node),
		testParam(fx.pkg, "Attrs", fx.runtime.attrs),
		testParam(fx.pkg, "children", fx.runtime.node),
		testParam(fx.pkg, "attrs", fx.runtime.attrs),
	}
	sig := testSignature(fx.pkg, nil, params, []types.Type{fx.runtime.node}, false)

	got, err := analyzeComponentSignature(sig, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got.goSig != sig || !types.Identical(got.result, fx.runtime.node) {
		t.Fatalf("model did not preserve signature/result: %#v", got)
	}
	wantRoles := []paramRole{roleProp, roleProp, roleProp, roleProp, roleChildren, roleAttrs}
	for i, param := range got.params {
		if param.variable != params[i] || param.origin != params[i] || param.name != params[i].Name() ||
			!types.Identical(param.typ, params[i].Type()) || param.index != i || param.role != wantRoles[i] {
			t.Errorf("param %d = %#v, want original var/type/name/index and role %d", i, param, wantRoles[i])
		}
	}
	if got.params[5].attrsMode != attrsDirect {
		t.Fatalf("canonical gsx.Attrs mode = %d, want attrsDirect", got.params[5].attrsMode)
	}
}

func TestAnalyzeComponentSignatureAllowsNullaryFuncProp(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	callback := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	param := testParam(fx.pkg, "callback", callback)
	got, err := analyzeComponentSignature(testSignature(fx.pkg, nil, []*types.Var{param}, []types.Type{fx.runtime.node}, false), fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got.params[0].role != roleProp {
		t.Fatalf("nullary callback role = %d, want roleProp", got.params[0].role)
	}
}

func TestAnalyzeComponentSignatureChildren(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)

	t.Run("variadic", func(t *testing.T) {
		p := testParam(fx.pkg, "children", types.NewSlice(fx.runtime.node))
		sig := testSignature(fx.pkg, nil, []*types.Var{p}, []types.Type{fx.runtime.node}, true)
		got, err := analyzeComponentSignature(sig, fx.runtime)
		if err != nil {
			t.Fatal(err)
		}
		if got.params[0].role != roleChildren {
			t.Fatalf("role = %d, want roleChildren", got.params[0].role)
		}
	})

	t.Run("exact alias", func(t *testing.T) {
		alias := testAliasType(t, types.NewPackage("example.test/child-alias", "childalias"), "NodeAlias", fx.runtime.node)
		p := testParam(fx.pkg, "children", alias)
		if _, err := analyzeComponentSignature(testSignature(fx.pkg, nil, []*types.Var{p}, []types.Type{fx.runtime.node}, false), fx.runtime); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("defined node-like is result-only", func(t *testing.T) {
		pkg := types.NewPackage("example.test/nodelike", "nodelike")
		render := types.NewFunc(token.NoPos, pkg, "Render", types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false))
		nodeLike := testNamedType(t, pkg, "NodeLike", types.NewInterfaceType([]*types.Func{render}, nil).Complete())
		if !types.AssignableTo(nodeLike, fx.runtime.node) {
			t.Fatal("test fixture NodeLike must be assignable to Node")
		}
		p := testParam(pkg, "children", nodeLike)
		if _, err := analyzeComponentSignature(testSignature(pkg, nil, []*types.Var{p}, []types.Type{nodeLike}, false), fx.runtime); err == nil {
			t.Fatal("defined Node-like interface accepted for exact children role")
		}
	})
}

func TestAnalyzeComponentSignatureAttrsFamily(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/bags", "bags")
	unnamed := types.NewSlice(fx.runtime.attr)
	attrsAlias := testAliasType(t, user, "AttrsAlias", fx.runtime.attrs)
	sliceAlias := testAliasType(t, user, "SliceAlias", unnamed)
	defined := testNamedType(t, user, "Bag", types.NewSlice(fx.runtime.attr))
	definedFromAttrs := testNamedType(t, user, "BagFromAttrs", fx.runtime.attrs.Underlying())
	definedAlias := testAliasType(t, user, "BagAlias", defined)

	bagTP := types.NewTypeParam(types.NewTypeName(token.NoPos, user, "T", nil), testAnyConstraint())
	genericBag := testNamedType(t, user, "GenericBag", types.NewSlice(bagTP))
	genericBag.SetTypeParams([]*types.TypeParam{bagTP})
	instantiatedBag, err := types.Instantiate(nil, genericBag, []types.Type{fx.runtime.attr}, true)
	if err != nil {
		t.Fatal(err)
	}

	aliasTP := types.NewTypeParam(types.NewTypeName(token.NoPos, user, "U", nil), testAnyConstraint())
	genericAliasObj := types.NewTypeName(token.NoPos, user, "GenericAlias", nil)
	genericAlias := types.NewAlias(genericAliasObj, types.NewSlice(aliasTP))
	genericAlias.SetTypeParams([]*types.TypeParam{aliasTP})
	if alt := user.Scope().Insert(genericAliasObj); alt != nil {
		t.Fatalf("insert GenericAlias: conflicts with %s", alt.Name())
	}
	instantiatedAlias, err := types.Instantiate(nil, genericAlias, []types.Type{fx.runtime.attr}, true)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		typ      types.Type
		variadic bool
		wantMode attrsParamMode
	}{
		{name: "canonical", typ: fx.runtime.attrs, wantMode: attrsDirect},
		{name: "unnamed", typ: unnamed, wantMode: attrsDirect},
		{name: "canonical alias", typ: attrsAlias, wantMode: attrsDirect},
		{name: "slice alias", typ: sliceAlias, wantMode: attrsDirect},
		{name: "defined", typ: defined, wantMode: attrsDefinedSlice},
		{name: "defined from canonical attrs", typ: definedFromAttrs, wantMode: attrsDefinedSlice},
		{name: "defined alias", typ: definedAlias, wantMode: attrsDefinedSlice},
		{name: "instantiated defined", typ: instantiatedBag, wantMode: attrsDefinedSlice},
		{name: "instantiated alias", typ: instantiatedAlias, wantMode: attrsDirect},
		{name: "variadic", typ: types.NewSlice(fx.runtime.attr), variadic: true, wantMode: attrsVariadic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testParam(user, "attrs", tc.typ)
			sig := testSignature(user, nil, []*types.Var{p}, []types.Type{fx.runtime.node}, tc.variadic)
			got, err := analyzeComponentSignature(sig, fx.runtime)
			if err != nil {
				t.Fatal(err)
			}
			if got.params[0].role != roleAttrs || got.params[0].attrsMode != tc.wantMode {
				t.Fatalf("role/mode = %d/%d, want roleAttrs/%d", got.params[0].role, got.params[0].attrsMode, tc.wantMode)
			}
		})
	}
}

func TestAnalyzeComponentSignatureDifferentlyNamedAttrsBagsAreOrdinary(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/ordinary-bags", "ordinarybags")
	myAttrs := testNamedType(t, user, "myAttrs", types.NewSlice(fx.runtime.attr))
	params := []*types.Var{
		testParam(user, "a", myAttrs),
		testParam(user, "someAttrs", fx.runtime.attrs),
	}
	model, err := analyzeComponentSignature(testSignature(user, nil, params, []types.Type{fx.runtime.node}, false), fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	for i, param := range model.params {
		if param.role != roleProp || param.name != params[i].Name() {
			t.Errorf("param %d = %+v, want ordinary exact-name prop %q", i, param, params[i].Name())
		}
	}
}

func TestAnalyzeComponentSignatureInstantiatedParamOrigin(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/generic-bag", "genericbag")

	bagTP := types.NewTypeParam(types.NewTypeName(token.NoPos, user, "E", nil), testAnyConstraint())
	bag := testNamedType(t, user, "Bag", types.NewSlice(bagTP))
	bag.SetTypeParams([]*types.TypeParam{bagTP})

	fnTP := types.NewTypeParam(types.NewTypeName(token.NoPos, user, "T", nil), testAnyConstraint())
	bagOfT, err := types.Instantiate(nil, bag, []types.Type{fnTP}, true)
	if err != nil {
		t.Fatal(err)
	}
	originParam := testParam(user, "attrs", bagOfT)
	origin := types.NewSignatureType(nil, nil, []*types.TypeParam{fnTP}, types.NewTuple(originParam), types.NewTuple(testParam(user, "", fx.runtime.node)), false)

	instType, err := types.Instantiate(nil, origin, []types.Type{fx.runtime.attr}, true)
	if err != nil {
		t.Fatal(err)
	}
	instantiated := instType.(*types.Signature)
	currentParam := instantiated.Params().At(0)
	if currentParam == originParam || currentParam.Origin() != originParam {
		t.Fatalf("fixture did not produce an instantiated parameter: current=%p origin=%p Var.Origin=%p", currentParam, originParam, currentParam.Origin())
	}
	got, err := analyzeComponentSignature(instantiated, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	param := got.params[0]
	if param.variable != currentParam || param.origin != originParam || !types.Identical(param.typ, currentParam.Type()) || param.attrsMode != attrsDefinedSlice {
		t.Fatalf("instantiated param = %#v; current=%v origin=%v", param, currentParam, originParam)
	}

	for _, badArg := range []types.Type{types.Typ[types.String], fnTP} {
		var badSig *types.Signature
		if badArg == fnTP {
			badSig = origin
		} else {
			badType, instErr := types.Instantiate(nil, origin, []types.Type{badArg}, true)
			if instErr != nil {
				t.Fatal(instErr)
			}
			badSig = badType.(*types.Signature)
		}
		if _, err := analyzeComponentSignature(badSig, fx.runtime); err == nil {
			t.Fatalf("attrs Bag[%s] accepted without exact Attr element", badArg)
		}
	}
}

func TestAnalyzeComponentSignatureRejectsInvalidParams(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/invalid-params", "invalidparams")
	myAttr := testNamedType(t, user, "MyAttr", types.NewStruct(nil, nil))
	render := types.NewFunc(token.NoPos, user, "Render", types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false))
	myNode := testNamedType(t, user, "MyNode", types.NewInterfaceType([]*types.Func{render}, nil).Complete())

	otherRuntime := types.NewPackage(gsxRuntimePath, "gsx")
	otherAttr := testNamedType(t, otherRuntime, "Attr", types.NewStruct(nil, nil))
	if types.Identical(otherAttr, fx.runtime.attr) {
		t.Fatal("same-path fixture unexpectedly shares type identity")
	}

	nodeConstraint := types.NewInterfaceType(nil, []types.Type{fx.runtime.node}).Complete()
	nodeTP := types.NewTypeParam(types.NewTypeName(token.NoPos, user, "N", nil), nodeConstraint)
	attrTerm := types.NewTerm(true, types.NewSlice(fx.runtime.attr))
	attrConstraint := types.NewInterfaceType(nil, []types.Type{types.NewUnion([]*types.Term{attrTerm})}).Complete()
	attrTP := types.NewTypeParam(types.NewTypeName(token.NoPos, user, "A", nil), attrConstraint)
	incompleteAlias := types.NewAlias(types.NewTypeName(token.NoPos, user, "Incomplete", nil), nil)

	cases := []struct {
		name       string
		paramName  string
		paramType  types.Type
		variadic   bool
		typeParams []*types.TypeParam
	}{
		{name: "blank fixed", paramName: "_", paramType: types.Typ[types.String]},
		{name: "unnamed fixed", paramType: types.Typ[types.String]},
		{name: "ordinary invalid", paramName: "value", paramType: types.Typ[types.Invalid]},
		{name: "ordinary nested invalid", paramName: "value", paramType: types.NewSlice(types.Typ[types.Invalid])},
		{name: "ordinary incomplete alias", paramName: "value", paramType: incompleteAlias},
		{name: "ordinary variadic invalid", paramName: "values", paramType: types.NewSlice(types.Typ[types.Invalid]), variadic: true},
		{name: "ctx", paramName: "ctx", paramType: types.Typ[types.String]},
		{name: "generated namespace", paramName: "_gsxtmp", paramType: types.Typ[types.String]},
		{name: "children scalar", paramName: "children", paramType: types.Typ[types.String]},
		{name: "children slice", paramName: "children", paramType: types.NewSlice(fx.runtime.node)},
		{name: "children defined variadic element", paramName: "children", paramType: types.NewSlice(myNode), variadic: true},
		{name: "children constrained type param", paramName: "children", paramType: nodeTP, typeParams: []*types.TypeParam{nodeTP}},
		{name: "attrs scalar", paramName: "attrs", paramType: types.Typ[types.String]},
		{name: "attrs defined element", paramName: "attrs", paramType: types.NewSlice(myAttr)},
		{name: "attrs foreign same path element", paramName: "attrs", paramType: types.NewSlice(otherAttr)},
		{name: "attrs constrained type param", paramName: "attrs", paramType: attrTP, typeParams: []*types.TypeParam{attrTP}},
		{name: "attrs of attrs variadic", paramName: "attrs", paramType: types.NewSlice(fx.runtime.attrs), variadic: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			param := testParam(user, tc.paramName, tc.paramType)
			sig := types.NewSignatureType(nil, nil, tc.typeParams, types.NewTuple(param), types.NewTuple(testParam(user, "", fx.runtime.node)), tc.variadic)
			if _, err := analyzeComponentSignature(sig, fx.runtime); err == nil {
				t.Fatalf("accepted invalid parameter %q of type %s", tc.paramName, tc.paramType)
			}
		})
	}
}

func TestAnalyzeComponentSignatureOrdinaryVariadics(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/variadic", "variadic")
	variadics := []struct {
		name string
		elem types.Type
	}{
		{name: "extra", elem: fx.runtime.attr},
		{name: "nodes", elem: fx.runtime.node},
	}
	for _, tc := range variadics {
		t.Run("name="+tc.name, func(t *testing.T) {
			param := testParam(user, tc.name, types.NewSlice(tc.elem))
			got, err := analyzeComponentSignature(testSignature(user, nil, []*types.Var{param}, []types.Type{fx.runtime.node}, true), fx.runtime)
			if err != nil {
				t.Fatal(err)
			}
			if got.params[0].role != roleGoOnlyVariadic {
				t.Fatalf("role = %d, want roleGoOnlyVariadic", got.params[0].role)
			}
		})
	}

	fixedSlice := testParam(user, "bags", types.NewSlice(fx.runtime.attr))
	trailing := testParam(user, "rest", types.NewSlice(types.Typ[types.String]))
	got, err := analyzeComponentSignature(testSignature(user, nil, []*types.Var{fixedSlice, trailing}, []types.Type{fx.runtime.node}, true), fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got.params[0].role != roleProp || got.params[1].role != roleGoOnlyVariadic {
		t.Fatalf("roles = %d,%d; a slice is not variadic without the signature bit", got.params[0].role, got.params[1].role)
	}
}

func TestAnalyzeComponentSignatureRequiresNamedParameters(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/named-params", "namedparams")

	tests := []struct {
		name      string
		paramName string
		variadic  bool
		want      string
	}{
		{name: "unnamed fixed", want: "function parameters must be named to be used as a component; parameter 0 is unnamed"},
		{name: "blank fixed", paramName: "_", want: "function parameters must be named to be used as a component; parameter 0 is blank"},
		{name: "unnamed variadic", variadic: true, want: "function parameters must be named to be used as a component; parameter 0 is unnamed"},
		{name: "blank variadic", paramName: "_", variadic: true, want: "function parameters must be named to be used as a component; parameter 0 is blank"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paramType := types.Type(types.Typ[types.String])
			if test.variadic {
				paramType = types.NewSlice(paramType)
			}
			param := testParam(user, test.paramName, paramType)
			sig := testSignature(user, nil, []*types.Var{param}, []types.Type{fx.runtime.node}, test.variadic)
			_, err := analyzeComponentSignature(sig, fx.runtime)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestComponentResultTypeDoesNotValidateParameters(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/results", "results")
	badAttrs := testParam(user, "attrs", types.Typ[types.String])
	sig := testSignature(user, nil, []*types.Var{badAttrs}, []types.Type{fx.runtime.node}, false)

	result, err := componentResultType(sig, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !types.Identical(result, fx.runtime.node) {
		t.Fatalf("result = %v, want canonical Node", result)
	}
	if _, err := analyzeComponentSignature(sig, fx.runtime); err == nil || !strings.Contains(err.Error(), "component-attrs-type") {
		t.Fatalf("full signature error = %v, want component-attrs-type", err)
	}
}

func TestAnalyzeComponentSignatureResultContract(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/results", "results")
	valueImpl := testRenderableType(t, user, "ValueImpl", false)
	pointerImpl := testRenderableType(t, user, "PointerImpl", true)
	nodeAlias := testAliasType(t, user, "NodeAlias", fx.runtime.node)
	render := types.NewFunc(token.NoPos, user, "Render", types.NewSignatureType(nil, nil, nil, types.NewTuple(), types.NewTuple(), false))
	derivedInterface := testNamedType(t, user, "DerivedInterface", types.NewInterfaceType([]*types.Func{render}, nil).Complete())

	valid := []types.Type{fx.runtime.node, nodeAlias, valueImpl, types.NewPointer(pointerImpl), derivedInterface}
	for _, result := range valid {
		t.Run("valid "+types.TypeString(result, nil), func(t *testing.T) {
			if _, err := analyzeComponentSignature(testSignature(user, nil, nil, []types.Type{result}, false), fx.runtime); err != nil {
				t.Fatal(err)
			}
		})
	}

	invalid := []types.Type{
		types.Typ[types.Invalid],
		types.Typ[types.String],
		testAnyConstraint(),
		pointerImpl,
	}
	for _, result := range invalid {
		t.Run("invalid "+types.TypeString(result, nil), func(t *testing.T) {
			if _, err := analyzeComponentSignature(testSignature(user, nil, nil, []types.Type{result}, false), fx.runtime); err == nil {
				t.Fatalf("accepted result %s", result)
			}
		})
	}

	if _, err := analyzeComponentSignature(testSignature(user, nil, nil, nil, false), fx.runtime); err == nil {
		t.Fatal("accepted zero-result signature")
	}
	if _, err := analyzeComponentSignature(testSignature(user, nil, nil, []types.Type{fx.runtime.node, fx.runtime.node}, false), fx.runtime); err == nil {
		t.Fatal("accepted multi-result signature")
	}
}

func TestAnalyzeComponentSignatureIgnoresReceiverAsValueParam(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/method", "method")
	recvType := testNamedType(t, user, "Receiver", types.NewStruct(nil, nil))
	recv := testParam(user, "r", recvType)
	value := testParam(user, "value", types.Typ[types.String])
	sig := testSignature(user, recv, []*types.Var{value}, []types.Type{fx.runtime.node}, false)
	got, err := analyzeComponentSignature(sig, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.params) != 1 || got.params[0].variable != value {
		t.Fatalf("receiver leaked into params: %#v", got.params)
	}
}
