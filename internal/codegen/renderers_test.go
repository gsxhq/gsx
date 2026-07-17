package codegen

import (
	"bytes"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
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
		// *T where the pointer elem is an alias to a named type pins the doc
		// claim that aliases are unwrapped on BOTH levels (outer and elem).
		{"pointer to alias", types.NewPointer(types.NewAlias(types.NewTypeName(token.NoPos, types.NewPackage("p", "p"), "PA", nil), text)), "*github.com/jackc/pgx/v5/pgtype.Text"},
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
		// The result is an UNNAMED struct: rendererKey returns "" for it (no
		// registration can ever key on it), so this can never reach the chain
		// branch — it's neither registered nor natively renderable, so the
		// plain message applies.
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
		// so which rendering applies is ambiguous. See "chain rejected when
		// intermediate result is unrenderable" below for the companion case
		// where the intermediate type is NOT independently renderable — the
		// chain message must still win there, since the type DOES have a
		// renderer registered.
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

	t.Run("self chain rejected when result is unrenderable", func(t *testing.T) {
		// st is a struct: classify(st) == catUnsupported on its own. But
		// Identity IS registered for st, so the type DOES have a renderer —
		// the chain message ("renderers apply once and never chain") must
		// win over the plain "not a renderable type" message, which would be
		// actively misleading here. This is the common real-world shape: a
		// non-renderable wrapper struct that itself carries a registration.
		st := namedType("example.com/pgtype", "Struct", types.NewStruct(nil, nil))
		stKey := "example.com/pgtype.Struct"
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"Identity": rendererSig([]*types.Var{rparam(st)}, []*types.Var{rresult(st)}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		_, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: stKey, PkgPath: "example.com/render", FuncName: "Identity"}}, nil)
		if err == nil || !strings.Contains(err.Error(), "returns its own registered type") {
			t.Fatalf("err = %v, want substring %q", err, "returns its own registered type")
		}
		if strings.Contains(err.Error(), "not a renderable type") {
			t.Fatalf("err = %v, masked by the plain unrenderable message", err)
		}
	})

	t.Run("chain rejected when intermediate result is unrenderable", func(t *testing.T) {
		// B is a struct (classify(B) == catUnsupported on its own) but IS
		// registered (RenderB), so RenderA's A→B chain must report the chain
		// message, not "not a renderable type" — B DOES have a renderer.
		b := namedType("example.com/pgtype", "B", types.NewStruct(nil, nil))
		bKey := "example.com/pgtype.B"
		pkg := rendererFixturePkg("example.com/render", map[string]*types.Signature{
			"RenderA": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(b)}, false),
			"RenderB": rendererSig([]*types.Var{rparam(b)}, []*types.Var{rresult(types.Typ[types.String])}, false),
		})
		byPath := map[string]*types.Package{"example.com/render": pkg}
		_, err := harvestRenderers(byPath, []RendererAlias{
			{TypeKey: textKey, PkgPath: "example.com/render", FuncName: "RenderA"},
			{TypeKey: bKey, PkgPath: "example.com/render", FuncName: "RenderB"},
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "never chain") {
			t.Fatalf("err = %v, want substring %q", err, "never chain")
		}
		if strings.Contains(err.Error(), "not a renderable type") {
			t.Fatalf("err = %v, masked by the plain unrenderable message", err)
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

func TestHarvestRendererEntriesDefersWholeTableValidation(t *testing.T) {
	text := namedType("example.com/pg", "Text", types.NewStruct(nil, nil))
	wrapped := namedType("example.com/pg", "Wrapped", types.NewStruct(nil, nil))
	pkgA := rendererFixturePkg("example.com/renda", map[string]*types.Signature{
		"Text": rendererSig([]*types.Var{rparam(text)}, []*types.Var{rresult(wrapped)}, false),
	})
	pkgB := rendererFixturePkg("example.com/rendb", map[string]*types.Signature{
		"Wrapped": rendererSig([]*types.Var{rparam(wrapped)}, []*types.Var{rresult(types.Typ[types.String])}, false),
	})
	aliases := map[string]string{"example.com/renda": "_gsxf0", "example.com/rendb": "_gsxf1"}
	entries, err := harvestRendererEntries(map[string]*types.Package{
		"example.com/renda": pkgA,
		"example.com/rendb": pkgB,
	}, []RendererAlias{
		{TypeKey: "example.com/pg.Text", PkgPath: "example.com/renda", FuncName: "Text"},
		{TypeKey: "example.com/pg.Wrapped", PkgPath: "example.com/rendb", FuncName: "Wrapped"},
	}, aliases)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRendererTable(entries); err == nil || !strings.Contains(err.Error(), "never chain") {
		t.Fatalf("validateRendererTable error = %v", err)
	}
}

func TestRendererTableForPackageMarksOnlyOwnerLocal(t *testing.T) {
	table := rendererTable{
		"p.A": {funcName: "A", alias: "_gsxf0", pkgPath: "app/renderers"},
		"p.B": {funcName: "B", alias: "_gsxf1", pkgPath: "external/renderers"},
	}.forPackage("app/renderers")
	if !table["p.A"].local || table["p.B"].local {
		t.Fatalf("localized table = %#v", table)
	}
}

func TestApplyRendererLocalCall(t *testing.T) {
	value := namedType("example.com/pg", "Text", types.NewStruct(nil, nil))
	table := funcTables{renderers: rendererTable{
		"example.com/pg.Text": {
			funcName: "Text", pkgPath: "example.com/app/renderers",
			local: true, result: types.Typ[types.String],
		},
	}}
	imports := map[string]bool{}
	var b bytes.Buffer
	got, _ := applyRenderer(&b, "v", value, table, imports, new(int), "return _gsxerr")
	if got != "Text((v))" {
		t.Fatalf("call = %q", got)
	}
	if len(imports) != 0 {
		t.Fatalf("imports = %v", imports)
	}
}

// TestHarvestRenderersCtx pins the #87 harvest of the two ctx-taking renderer
// shapes (func(ctx context.Context, T) R and func(ctx context.Context, T)
// (R, error)), mirroring classifyFilter's contract (see TestClassifyFilter in
// classify_test.go): a leading real context.Context is accepted only when
// followed by exactly one subject param. Real context.Context comes from
// typeCheckFuncs (classify_test.go) rather than a synthetic namedType, since
// isContextContext checks the actual stdlib package identity.
func TestHarvestRenderersCtx(t *testing.T) {
	t.Parallel()
	const src = `package x

import "context"

type Text struct{}

func Plain(t Text) string { return "" }
func Ctx(ctx context.Context, t Text) string { return "" }
func CtxErr(ctx context.Context, t Text) (string, error) { return "", nil }
func CtxOnly(ctx context.Context) string { return "" }
func CtxNotFirst(t Text, ctx context.Context) string { return "" }
func CtxThree(ctx context.Context, a, b Text) string { return "" }
`
	scope := typeCheckFuncs(t, src)
	pkg := scope.Lookup("Text").Pkg()
	textKey := pkg.Path() + ".Text"
	byPath := map[string]*types.Package{pkg.Path(): pkg}

	t.Run("ctx-taking string result", func(t *testing.T) {
		table, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: pkg.Path(), FuncName: "Ctx"}}, nil)
		if err != nil {
			t.Fatalf("harvestRenderers: %v", err)
		}
		e, ok := table[textKey]
		if !ok {
			t.Fatalf("missing entry for %q", textKey)
		}
		if !e.wantsCtx {
			t.Errorf("wantsCtx = false, want true")
		}
		if e.hasErr {
			t.Errorf("hasErr = true, want false")
		}
	})

	t.Run("ctx-taking string,error result", func(t *testing.T) {
		table, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: pkg.Path(), FuncName: "CtxErr"}}, nil)
		if err != nil {
			t.Fatalf("harvestRenderers: %v", err)
		}
		e := table[textKey]
		if !e.wantsCtx {
			t.Errorf("wantsCtx = false, want true")
		}
		if !e.hasErr {
			t.Errorf("hasErr = false, want true")
		}
	})

	t.Run("no ctx unchanged, wantsCtx false", func(t *testing.T) {
		table, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: pkg.Path(), FuncName: "Plain"}}, nil)
		if err != nil {
			t.Fatalf("harvestRenderers: %v", err)
		}
		if e := table[textKey]; e.wantsCtx {
			t.Errorf("wantsCtx = true, want false")
		}
	})

	rejectCases := []struct {
		name string
		fn   string
	}{
		{"ctx only, no subject after ctx", "CtxOnly"},
		{"ctx not first", "CtxNotFirst"},
		{"three params (ctx + two subjects)", "CtxThree"},
	}
	wantShapes := []string{
		"func(T) R",
		"func(T) (R, error)",
		"func(ctx context.Context, T) R",
		"func(ctx context.Context, T) (R, error)",
	}
	for _, c := range rejectCases {
		t.Run("contract violation: "+c.name, func(t *testing.T) {
			_, err := harvestRenderers(byPath, []RendererAlias{{TypeKey: textKey, PkgPath: pkg.Path(), FuncName: c.fn}}, nil)
			if err == nil || !strings.Contains(err.Error(), "renderer contract") {
				t.Fatalf("err = %v, want substring %q", err, "renderer contract")
			}
			for _, shape := range wantShapes {
				if !strings.Contains(err.Error(), shape) {
					t.Errorf("err = %v, missing contract shape %q", err, shape)
				}
			}
		})
	}
}

// TestRendererPkgLoadError pins the load-level validation of renderer packages
// on the packages.Load path (harvestFilters → checkRendererPkg): go/packages
// hands back best-effort non-nil Types even when pkg.Errors is populated, so
// without the check a renderer package with compile errors would be silently
// admitted and fail later with a misleading "func not found". Mirrors the
// broken_alias_pkg fixture in TestFilterHarvestErrorsMatch, framed against the
// [renderers] registration that pulled the package in — nothing else in the
// config mentions it.
func TestRendererPkgLoadError(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	must := func(p, c string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("badrender/bad.go", "package badrender\n\nfunc RenderText(s string) string { return undefinedIdent(s) }\n")

	renderers := []RendererAlias{{
		TypeKey:  "example.com/x/pg.Text",
		PkgPath:  "example.com/x/badrender",
		FuncName: "RenderText",
	}}
	_, _, err = loadFilterTableMulti(root, []string{stdImportPath}, nil, renderers)
	if err == nil {
		t.Fatal("expected error for broken renderer package, got nil")
	}
	for _, want := range []string{
		`renderer for "example.com/x/pg.Text"`,
		`package "example.com/x/badrender"`,
		"type resolution failed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestClassPartBareIdentUnregisteredParity pins the #85 fix's probe-parity
// claim for a class-part value that is a BARE IDENTIFIER of a plain struct
// type with NO [renderers] registration at all: before the fix, classEntryExpr's
// probe-mode stub only covered CALL exprs, so a bare non-string identifier
// flowed straight into the skeleton's _gsxrt.Class(expr) and failed go/types
// there — surfacing as a raw, unpositioned "cannot use val ... as string
// value" diagnostic instead of generating successfully. After the fix, the
// probe stubs the value regardless of shape, so generation SUCCEEDS with no
// diagnostics (parity with a call expr) and the wrong type is left to surface
// at `go build` of the emitted .x.go — exactly like a call-shaped part that
// returns a non-string with no renderer registered.
func TestClassPartBareIdentUnregisteredParity(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	writeMultiFile(t, tmp, "go.mod", "module gsxcb\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// box carries no [renderers] registration whatsoever — the probe must not
	// special-case "is this type registered", only "is this a call expr", so a
	// plain unregistered struct-typed bare identifier is the strongest witness
	// that the stub is now unconditional.
	writeMultiFile(t, viewsDir, "views.gsx", `package views

	import "github.com/gsxhq/gsx"

type box struct{ S string }

component Card(title string, attrs gsx.Attrs) { <div { attrs... }>{title}</div> }

component Page(val box) {
	<Card title="hi" class={ val }/>
}
`)

	genRes, err := GenerateDirs(tmp, []string{viewsDir}, Options{FilterPkgs: []string{stdImportPath}, CSSMinify: true, JSMinify: true}, nil)
	if err != nil {
		t.Fatalf("GenerateDirs: %v", err)
	}
	dr := genRes[viewsDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("GenerateDirs: unexpected diagnostics for a bare non-string, unregistered class part (probe must stub it, #85): %v", dr.Diags)
	}
	var genSrc string
	for _, src := range dr.Files {
		genSrc += string(src)
	}
	// Parity with a call-expr part: the wrong type reaches _gsxrt.Class(val)
	// verbatim in the emitted code, to fail at `go build` of the .x.go — NOT
	// inside gsx generation itself.
	if !strings.Contains(genSrc, "_gsxrt.Class(val)") {
		t.Fatalf("generated .x.go missing Class(val); got:\n%s", genSrc)
	}
	// Pin that the pre-fix failure mode is truly gone: no diagnostic anywhere
	// mentions the raw go/types skeleton error this used to surface as.
	for _, d := range dr.Diags {
		if strings.Contains(d.Message, "cannot use val") {
			t.Fatalf("pre-fix raw go/types skeleton error resurfaced: %v", d)
		}
	}
}
