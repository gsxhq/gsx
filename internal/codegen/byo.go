package codegen

import (
	goast "go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	gsxast "github.com/gsxhq/gsx/ast"
)

// byoData captures the package's bring-your-own (byo) Props facts: which
// components use an author-declared struct DIRECTLY as their sole non-receiver
// parameter (so gsx generates NO <Name>Props wrapper), and — for those structs
// — the exported field set the field-build call-site logic needs.
//
// It is derived BEFORE the skeleton is built (componentPropFieldsFor) and made
// available to BOTH emit and probe, so the two paths build the identical
// field-build literal from the identical field set (the emit ≡ probe invariant).
//
//   - compStruct maps a component KEY (componentKey: ".Name" for a function
//     component, "RecvType.Name" for a method) to the struct TYPE NAME it uses
//     directly. A component absent from this map is on the generated/nullary path.
//   - structs records, per byo struct type name, the field facts (used to error
//     on a missing Children/Attrs field and to drive node-prop promotion). The
//     struct's exported field set + node fields are ALSO published into the shared
//     propFields/nodeProps maps under the struct's type name, so childPropsLiteral
//     keys on it exactly as for a generated props type.
//   - inGsx records which byo structs are declared IN a .gsx GoChunk (so gsx may
//     auto-add a Children field on the {children} path; an external .go struct
//     never grows under the author — a missing field is a clear error).
type byoData struct {
	compStruct map[string]string // componentKey -> struct type name
	structs    map[string]byoStruct
	inGsx      map[string]bool // struct type name -> declared in a .gsx GoChunk
	// nullaryFuncs is the set of same-package top-level funcs (in hand-written .go
	// files) that take zero params and return one value — the shape that backs a
	// bare `<F/>` invocation. Populated by a parse-only scan in
	// componentPropFieldsFor; consulted by isBareCallCandidate so an arity ≥ 1
	// hand-written func keeps the XxxProps convention (and its generate-time prop
	// type-check) rather than being mis-treated as a bare call.
	nullaryFuncs map[string]bool
}

// byoStruct is one author struct's field facts.
type byoStruct struct {
	hasChildren bool // has a `Children gsx.Node` field
	hasAttrs    bool // has an `Attrs gsx.Attrs` field
}

// newByoData returns an empty, ready-to-populate byoData.
func newByoData() *byoData {
	return &byoData{
		compStruct:   map[string]string{},
		structs:      map[string]byoStruct{},
		inGsx:        map[string]bool{},
		nullaryFuncs: map[string]bool{},
	}
}

// isNullaryFunc reports whether name is a same-package hand-written func that
// takes zero params (the shape that backs a bare `<name/>` call). nil-safe.
func (b *byoData) isNullaryFunc(name string) bool {
	return b != nil && b.nullaryFuncs[name]
}

// structTypeName returns the byo struct type name for a component, or "" if the
// component is not byo. byo is nil-safe (treated as no byo components).
func (b *byoData) structTypeName(key string) (string, bool) {
	if b == nil {
		return "", false
	}
	s, ok := b.compStruct[key]
	return s, ok
}

// isByoStruct reports whether propsType is a byo author struct (so
// childPropsLiteral applies the explicit-field rules: error on a missing
// Attrs/Children field rather than silently auto-synthesizing one).
func (b *byoData) isByoStruct(propsType string) (byoStruct, bool) {
	if b == nil {
		return byoStruct{}, false
	}
	s, ok := b.structs[propsType]
	return s, ok
}

// packageNullaryFuncs parses the package's hand-written .go files (parse-only, no
// type-check — cheap) and returns the set of top-level funcs that take zero
// parameters and return exactly one value: the shape that can back a bare `<F/>`
// invocation. Receiver methods, generated .x.go, and _test.go files are skipped.
// Param/result counts come straight from the AST, so the result is exact
// regardless of import aliasing. (A non-gsx.Node return is still admitted here —
// it surfaces as a clean `does not implement gsx.Node` build error, which beats
// the `undefined: FProps` the convention path would give.)
func packageNullaryFuncs(dir string) map[string]bool {
	out := map[string]bool{}
	if dir == "" {
		return out
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".x.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			continue
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*goast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue // skip methods
			}
			if fd.Type.Params.NumFields() != 0 {
				continue // takes params → keeps the XxxProps convention
			}
			if fd.Type.Results == nil || fd.Type.Results.NumFields() != 1 {
				continue // must return exactly one value (the rendered node)
			}
			out[fd.Name.Name] = true
		}
	}
	return out
}

// soleParamTypeName returns the bare type name of a component's SOLE non-receiver
// parameter when that parameter's declared type is a simple (possibly pointer-
// stripped) identifier — the only shape a byo struct can take. It returns ""
// when there is not exactly one parameter, or the type is not a bare identifier
// (a scalar like `string`, a qualified `pkg.T`, a slice, a func, gsx.Node, …).
// A qualified type (cross-package struct) is deliberately NOT byo here: byo
// requires a SAME-package author struct whose fields we can enumerate.
func soleParamTypeName(params []param) string {
	if len(params) != 1 {
		return ""
	}
	typ := strings.TrimSpace(params[0].typ)
	// A pointer / slice / map / qualified / func type is never a (same-package
	// named) struct on the byo path: byo requires a bare same-package struct name
	// whose fields we can enumerate and zero-value as defaults. A `*User` param is
	// the GENERATED path (a single non-struct param → <Name>Props), matching the
	// spec's "resolved type is a named struct" discriminator.
	if !token.IsIdentifier(typ) {
		return ""
	}
	// A bare builtin (string/int/bool/…) or gsx.Node-shaped name is never a
	// struct; the struct lookup below filters non-structs, but reject obvious
	// builtins early to avoid a needless lookup.
	switch typ {
	case "string", "bool", "byte", "rune", "error", "any",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"float32", "float64", "complex64", "complex128":
		return ""
	}
	return typ
}

// gsxStructDecls scans every GoChunk in the .gsx files for top-level
// `type X struct { … }` declarations and returns them keyed by type name. The
// chunk Go source is parsed with go/parser; a chunk that does not parse on its
// own is skipped (buildSkeleton re-parses and surfaces a clean diagnostic).
func gsxStructDecls(files map[string]*gsxast.File) map[string]*goast.StructType {
	out := map[string]*goast.StructType{}
	for _, file := range files {
		for _, d := range file.Decls {
			gc, ok := d.(*gsxast.GoChunk)
			if !ok {
				continue
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "", "package _gsxp\n"+gc.Src, 0)
			if err != nil {
				continue
			}
			for _, decl := range f.Decls {
				gd, ok := decl.(*goast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts, ok := spec.(*goast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*goast.StructType)
					if !ok {
						continue
					}
					out[ts.Name.Name] = st
				}
			}
		}
	}
	return out
}

// fieldsFromGsxStruct enumerates a GoChunk-declared struct's exported fields from
// its AST: the exported field set (capitalized field names → true), the node
// field set (fields whose type is exactly gsx.Node), and whether it has the
// special Children gsx.Node / Attrs gsx.Attrs fields. No type resolution is
// needed — the field shape is read syntactically, exactly as the author wrote it.
func fieldsFromGsxStruct(st *goast.StructType) (fields, nodeFields map[string]bool, s byoStruct) {
	fields = map[string]bool{}
	nodeFields = map[string]bool{}
	for _, f := range st.Fields.List {
		typ := typeString(f.Type)
		for _, nm := range f.Names {
			if !nm.IsExported() {
				continue
			}
			fields[nm.Name] = true
			if isGsxNodeType(typ) {
				nodeFields[nm.Name] = true
			}
			if nm.Name == "Children" && isGsxNodeType(typ) {
				s.hasChildren = true
			}
			if nm.Name == "Attrs" && isGsxAttrsType(typ) {
				s.hasAttrs = true
			}
		}
	}
	return fields, nodeFields, s
}

// typeString renders a (simple) Go type expression to source — enough to detect
// gsx.Node / gsx.Attrs. It handles the identifier and selector forms a struct
// field type takes; anything else is rendered best-effort and simply won't match
// the gsx.Node/gsx.Attrs probes (so it is treated as a plain field).
func typeString(e goast.Expr) string {
	switch t := e.(type) {
	case *goast.Ident:
		return t.Name
	case *goast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	case *goast.StarExpr:
		return "*" + typeString(t.X)
	}
	return ""
}

// isGsxAttrsType reports whether a field's declared type string is exactly
// gsx.Attrs (ignoring surrounding whitespace).
func isGsxAttrsType(typ string) bool {
	return strings.TrimSpace(typ) == "gsx.Attrs"
}

// loadExternalStructFields does a preliminary go/packages load of the package
// directory to enumerate the exported fields of each requested struct type. It
// mirrors the type-resolution discipline of checkSkeletonPackage, but with NO
// overlay — packages.Load(cfg, ".") loads the package's existing on-disk files
// (hand-written .go and any previously generated .x.go) without the not-yet-
// generated .x.go for the current run. The struct's field set is still reliable
// because struct declarations always live in hand-written .go files; any .x.go
// present on disk can only add functions (never struct fields). wanted is the
// set of struct type names to enumerate; the return maps are keyed by type name.
// A type absent from .go files (e.g. it is declared in a .gsx GoChunk, already
// handled) is simply absent from the result.
//
// Load failures and type errors are swallowed: at this stage the package's .go
// files may legitimately be incomplete (they reference funcs the .x.go will
// provide), so a load error must not abort codegen. The caller already handles a
// missing field set gracefully (a byo struct whose fields we could not learn
// falls back to the cross-package isPropField path). Only structs we positively
// resolve are returned.
func loadExternalStructFields(dir string, wanted map[string]bool) (fields, nodeFields map[string]map[string]bool, structs map[string]byoStruct) {
	fields = map[string]map[string]bool{}
	nodeFields = map[string]map[string]bool{}
	structs = map[string]byoStruct{}
	if len(wanted) == 0 {
		return fields, nodeFields, structs
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil || len(pkgs) == 0 {
		return fields, nodeFields, structs
	}
	pkg := pkgs[0]
	if pkg.Types == nil {
		return fields, nodeFields, structs
	}
	scope := pkg.Types.Scope()
	for name := range wanted {
		obj := scope.Lookup(name)
		if obj == nil {
			continue
		}
		tn, ok := obj.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		st, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		f := map[string]bool{}
		nf := map[string]bool{}
		var bs byoStruct
		for i := 0; i < st.NumFields(); i++ {
			fld := st.Field(i)
			if !fld.Exported() {
				continue
			}
			f[fld.Name()] = true
			ft := fld.Type()
			if isGsxNodeNamed(ft) {
				nf[fld.Name()] = true
			}
			if fld.Name() == "Children" && isGsxNodeNamed(ft) {
				bs.hasChildren = true
			}
			if fld.Name() == "Attrs" && isGsxAttrsNamed(ft) {
				bs.hasAttrs = true
			}
		}
		fields[name] = f
		nodeFields[name] = nf
		structs[name] = bs
	}
	return fields, nodeFields, structs
}

// isGsxAttrsNamed reports whether a resolved type is github.com/gsxhq/gsx.Attrs.
func isGsxAttrsNamed(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Attrs" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
}

// isGsxNodeNamed reports whether a resolved type is exactly
// github.com/gsxhq/gsx.Node — the interface type, not merely something that
// implements it. This mirrors the syntactic isGsxNodeType check used by the
// .gsx GoChunk path (fieldsFromGsxStruct), which tests for the literal string
// "gsx.Node". Using implementsNode here would be WRONG: any concrete type with
// Render(context.Context, io.Writer) error would be classified as a node-field,
// causing non-node attrs to be wrapped in gsx.Val — a type mismatch.
func isGsxNodeNamed(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Node" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
}
