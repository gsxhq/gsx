package codegen

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func namedType(pkgPath, name string, underlying types.Type) *types.Named {
	pkg := types.NewPackage(pkgPath, "x")
	tn := types.NewTypeName(token.NoPos, pkg, name, nil)
	return types.NewNamed(tn, underlying, nil)
}

func TestRendererKey(t *testing.T) {
	text := namedType("github.com/jackc/pgx/v5/pgtype", "Text", types.NewStruct(nil, nil))
	cases := []struct {
		name string
		t    types.Type
		want string
	}{
		{"named", text, "github.com/jackc/pgx/v5/pgtype.Text"},
		{"pointer", types.NewPointer(text), "*github.com/jackc/pgx/v5/pgtype.Text"},
		{"alias", types.NewAlias(types.NewTypeName(token.NoPos, types.NewPackage("p", "p"), "A", nil), text), "github.com/jackc/pgx/v5/pgtype.Text"},
		{"basic", types.Typ[types.String], ""},
		{"unnamed struct", types.NewStruct(nil, nil), ""},
	}
	for _, c := range cases {
		if got := rendererKey(c.t); got != c.want {
			t.Errorf("%s: rendererKey = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestRendererKeyGenericAndTypeParam pins the two shapes rendererKey must
// reject even though they surface as *types.Named-ish: a generic
// instantiation (TypeArgs().Len() > 0) and a bare type parameter. Neither can
// ever match a [renderers] registration, which is keyed on a concrete named
// type.
func TestRendererKeyGenericAndTypeParam(t *testing.T) {
	pkg := types.NewPackage("github.com/example/generic", "generic")
	tparamName := types.NewTypeName(token.NoPos, pkg, "T", nil)
	tparam := types.NewTypeParam(tparamName, types.NewInterfaceType(nil, nil))

	tn := types.NewTypeName(token.NoPos, pkg, "Box", nil)
	named := types.NewNamed(tn, types.NewStruct(nil, nil), nil)
	named.SetTypeParams([]*types.TypeParam{tparam})

	inst, err := types.Instantiate(nil, named, []types.Type{types.Typ[types.Int]}, false)
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	if got := rendererKey(inst); got != "" {
		t.Errorf("generic instantiation: rendererKey = %q, want \"\"", got)
	}
	if got := rendererKey(tparam); got != "" {
		t.Errorf("type param: rendererKey = %q, want \"\"", got)
	}
}

// rendererFixturePkg builds a synthetic *types.Package with a scope populated
// by the given funcs (name -> signature), standing in for an already-loaded
// renderer package. No packages.Load is needed since only Signature shape and
// result-type classification matter to harvestRenderers.
func rendererFixturePkg(path string, funcs map[string]*types.Signature) *types.Package {
	pkg := types.NewPackage(path, "x")
	scope := pkg.Scope()
	for name, sig := range funcs {
		scope.Insert(types.NewFunc(token.NoPos, pkg, name, sig))
	}
	pkg.MarkComplete()
	return pkg
}

func rendererSig(params, results []*types.Var, variadic bool) *types.Signature {
	return types.NewSignatureType(nil, nil, nil, types.NewTuple(params...), types.NewTuple(results...), variadic)
}

func rparam(t types.Type) *types.Var  { return types.NewVar(token.NoPos, nil, "", t) }
func rresult(t types.Type) *types.Var { return types.NewVar(token.NoPos, nil, "", t) }

// errorType is the real universe-scope error interface (types.Universe's
// "error"), so it matches isErrorType's check that the named object's Pkg is
// nil and its name is "error".
func errorType() types.Type {
	return types.Universe.Lookup("error").Type()
}

func TestHarvestRenderers(t *testing.T) {
	text := namedType("example.com/pgtype", "Text", types.NewStruct(nil, nil))
	textKey := "example.com/pgtype.Text"

	t.Run("valid string result", func(t *testing.T) {
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"RenderText": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(types.Typ[types.String])}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		aliases := map[string]string{"example.com/render": "_gsxf0"}
		table, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderText"}}, aliases)
		if err != nil {
			t.Fatalf("harvestRenderers: %v", err)
		}
		e, ok := table[textKey]
		if !ok {
			t.Fatalf("missing entry for %q", textKey)
		}
		if e.hasErr {
			t.Errorf("hasErr = true, want false")
		}
		if e.alias != "_gsxf0" {
			t.Errorf("alias = %q, want _gsxf0", e.alias)
		}
		if e.result != types.Typ[types.String] {
			t.Errorf("result = %v, want string", e.result)
		}
		if e.funcName != "RenderText" || e.pkgPath != "example.com/render" {
			t.Errorf("unexpected entry: %+v", e)
		}
	})

	t.Run("valid string,error result", func(t *testing.T) {
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"RenderText": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(types.Typ[types.String]), rresult(errorType())}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		table, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderText"}}, nil)
		if err != nil {
			t.Fatalf("harvestRenderers: %v", err)
		}
		if e := table[textKey]; !e.hasErr {
			t.Errorf("hasErr = false, want true")
		}
	})

	t.Run("param type key mismatch", func(t *testing.T) {
		other := namedType("example.com/pgtype", "Other", types.NewStruct(nil, nil))
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"RenderText": rendererSig([]*types.Var{rparam(other)}, []*types.Var{rresult(types.Typ[types.String])}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		_, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderText"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "registered for") {
			t.Fatalf("err = %v, want substring %q", err, "registered for")
		}
	})

	contractCases := []struct {
		name string
		sig  *types.Signature
	}{
		{"variadic", rendererSig([]*types.Var{rparam(types.NewSlice(text))}, []*types.Var{rresult(types.Typ[types.String])}, true)},
		{"two params", rendererSig([]*types.Var{rparam(text), rparam(types.Typ[types.Int])}, []*types.Var{rresult(types.Typ[types.String])}, false)},
		{"zero results", rendererSig([]*types.Var{rparam(text)}, nil, false)},
		{"second result not error", rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(types.Typ[types.String]), rresult(types.Typ[types.Int])}, false)},
		{"three results", rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(types.Typ[types.String]), rresult(errorType()), rresult(types.Typ[types.Int])}, false)},
	}
	for _, c := range contractCases {
		t.Run("contract violation: "+c.name, func(t *testing.T) {
			pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{"RenderText": c.sig})
			byPath := map[string]*types.Package{"example.com/render": pkg}
			_, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderText"}}, nil)
			if err == nil || !strings.Contains(err.Error(), "renderer contract") {
				t.Fatalf("err = %v, want substring %q", err, "renderer contract")
			}
		})
	}

	t.Run("generic func rejected", func(t *testing.T) {
		tp := types.NewTypeParam(types.NewTypeName(token.NoPos, nil, "T", nil), types.NewInterfaceType(nil, nil))
		s := types.NewSignatureType(nil, nil, []*types.TypeParam{tp}, types.NewTuple(rparam(text)), types.NewTuple(rresult(types.Typ[types.String])), false)
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{"RenderText": s})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		_, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderText"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "renderer contract") {
			t.Fatalf("err = %v, want substring %q", err, "renderer contract")
		}
	})

	t.Run("result not renderable", func(t *testing.T) {
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"RenderText": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(types.NewStruct(nil, nil))}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		_, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderText"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "not a renderable type") {
			t.Fatalf("err = %v, want substring %q", err, "not a renderable type")
		}
	})

	t.Run("chain rejected", func(t *testing.T) {
		// loopy's underlying is string, so classify(loopy) is natively
		// renderable (catString) — the chain-rejection guard is meant for
		// exactly this case: a renderer's result type IS renderable on its
		// own, but that type ALSO carries its own [renderers] registration,
		// so which rendering applies is ambiguous. A perpetually-unsupported
		// struct result would already fail the earlier "not a renderable
		// type" check regardless of registration, so it can't reach here.
		loopy := namedType("example.com/pgtype", "Loopy", types.Typ[types.String])
		loopyKey := "example.com/pgtype.Loopy"
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"RenderText":  rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(loopy)}, false),
			"RenderLoopy": rendererSig([]*types.Var{rparam(loopy)}, []*types.Var{rresult(types.Typ[types.String])}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		_, err := harvestRenderers(byPath, []RendererAlias{
			{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderText"},
			{TypeKey: loopyKey, PkgPath: "example.com/render", FuncName: "RenderLoopy"},
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "never chain") {
			t.Fatalf("err = %v, want substring %q", err, "never chain")
		}
	})

	t.Run("self chain rejected", func(t *testing.T) {
		// loopy's underlying is string (see "chain rejected" above) so its own
		// return type passes the renderability check and the self-chain guard
		// is what rejects func(Loopy) Loopy registered for Loopy.
		loopy := namedType("example.com/pgtype", "Loopy", types.Typ[types.String])
		loopyKey := "example.com/pgtype.Loopy"
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"Identity": rendererSig([]*types.Var{rparam(loopy)}, []*types.Var{rresult(loopy)}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		_, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: loopyKey, PkgPath: "example.com/render", FuncName: "Identity"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "returns its own registered type") {
			t.Fatalf("err = %v, want substring %q", err, "returns its own registered type")
		}
	})

	t.Run("duplicate TypeKey last wins", func(t *testing.T) {
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"RenderA": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(types.Typ[types.String])}, false),
			"RenderB": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(types.Typ[types.Int])}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		table, err := harvestRenderers(byPath, []RendererAlias{
			{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderA"},
			{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderB"},
		}, nil)
		if err != nil {
			t.Fatalf("harvestRenderers: %v", err)
		}
		if e := table[textKey]; e.funcName != "RenderB" {
			t.Errorf("funcName = %q, want RenderB (last wins)", e.funcName)
		}
	})

	t.Run("package not loaded", func(t *testing.T) {
		_, err := harvestRenderers(map[string]*types.Package{}, []RendererAlias{{TypeKey: textKey, PkgPath: "example.com/missing", FuncName: "RenderText"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "was not loaded") {
			t.Fatalf("err = %v, want substring %q", err, "was not loaded")
		}
	})

	t.Run("empty renderers returns empty table", func(t *testing.T) {
		table, err := harvestRenderers(nil, nil, nil)
		if err != nil {
			t.Fatalf("harvestRenderers: %v", err)
		}
		if len(table) != 0 {
			t.Errorf("table = %+v, want empty", table)
		}
	})
}
