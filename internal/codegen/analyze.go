package codegen

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	gsxast "github.com/gsxhq/gsx/ast"
)

// resolveTypesPkg type-checks the package (real .go files + synthesized gsx
// component skeletons via Overlay) and returns each interpolation's type.
func resolveTypesPkg(dir string, files map[string]*gsxast.File) (map[*gsxast.Interp]types.Type, error) {
	overlay := map[string][]byte{}
	skelComps := map[string][]*gsxast.Component{}
	for path, file := range files {
		skel, comps, err := buildSkeleton(file)
		if err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(filepath.Base(path), ".gsx")
		xpath := filepath.Join(dir, base+".x.go")
		overlay[xpath] = []byte(skel)
		skelComps[xpath] = comps
	}

	// Declare the _gsxuse probe helper exactly once, in a shared overlay file, so
	// a multi-.gsx package doesn't redeclare it once per skeleton (which would
	// fail type-checking for the whole package). harvest keys on the _gsxuse
	// identifier; this file is absent from skelComps, so harvest skips it.
	pkgName := ""
	for _, f := range files {
		pkgName = f.Package
		break
	}
	// Pick an overlay filename that does NOT exist on disk in dir, so a real
	// gsxshared.x.go (or our own per-file <base>.x.go overlays) is never
	// clobbered. The file is overlay-only (never written to disk); it just needs
	// a free path within the package dir.
	sharedPath, err := freeOverlayPath(dir, "gsxshared", ".x.go", overlay)
	if err != nil {
		return nil, err
	}
	overlay[sharedPath] = []byte("package " + pkgName + "\n\nfunc _gsxuse(...any) {}\n")

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:     dir,
		Overlay: overlay,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return nil, fmt.Errorf("codegen: load package: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("codegen: no package found in %s", dir)
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return nil, fmt.Errorf("codegen: type resolution failed: %s", pkg.Errors[0])
	}

	out := map[*gsxast.Interp]types.Type{}
	for _, f := range pkg.Syntax {
		fname := pkg.Fset.Position(f.Pos()).Filename
		comps, ok := skelComps[fname]
		if !ok {
			continue // a real .go file, not one of our overlays
		}
		harvest(f, comps, pkg.TypesInfo, out)
	}
	return out, nil
}

// freeOverlayPath returns a path in dir of the form
// base+suffix, base+"1"+suffix, base+"2"+suffix, … — the first one that exists
// neither on disk nor already in the overlay map. The returned file is used as
// an overlay-only key, so it merely needs to be a free path within the package
// dir (avoiding both real source files and our own per-.gsx overlays).
func freeOverlayPath(dir, base, suffix string, overlay map[string][]byte) (string, error) {
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s%d", base, i)
		}
		p := filepath.Join(dir, name+suffix)
		if _, taken := overlay[p]; taken {
			continue
		}
		_, err := os.Stat(p)
		if os.IsNotExist(err) {
			return p, nil
		}
		if err != nil {
			return "", fmt.Errorf("codegen: probing overlay path %s: %w", p, err)
		}
		// exists on disk — try the next candidate
	}
}

// buildSkeleton synthesizes a Go file standing in for the gsx file during type
// resolution: the file's GoChunks, plus each component's real props struct and
// func signature, with a probe body (used-param locals, each interpolation as
// `_gsxuse(expr)`, each child component as `_ = Child(ChildProps{})`).
func buildSkeleton(file *gsxast.File) (string, []*gsxast.Component, error) {
	var comps []*gsxast.Component
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			comps = append(comps, c)
		}
	}

	// Split every GoChunk into its imports (hoisted ahead of all declarations)
	// and its non-import body (emitted after the synthesized _gsxuse func). A
	// single chunk may carry both — e.g. an import followed by type/func decls.
	var imports []importSpec
	var bodies []string
	for _, d := range file.Decls {
		if gc, ok := d.(*gsxast.GoChunk); ok {
			imps, body, err := splitChunk(gc.Src)
			if err != nil {
				return "", nil, err
			}
			imports = append(imports, imps...)
			if body != "" {
				bodies = append(bodies, body)
			}
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "package %s\n", file.Package)
	sb.WriteString("import _gsxrt \"github.com/gsxhq/gsx\"\n")
	for _, imp := range imports {
		if imp.name != "" {
			fmt.Fprintf(&sb, "import %s %q\n", imp.name, imp.path)
		} else {
			fmt.Fprintf(&sb, "import %q\n", imp.path)
		}
	}
	// Always reference _gsxrt so the import stays used even when the file has no
	// non-method components (e.g. a method-only file whose components are skipped
	// below) — otherwise the import-unused error masks the real diagnostic.
	sb.WriteString("var _ _gsxrt.Node\n")
	for _, body := range bodies {
		sb.WriteString(body)
		sb.WriteByte('\n')
	}
	for _, c := range comps {
		if c.Recv != "" {
			continue // SPIKE: method components handled later
		}
		params, err := parseParams(c.Params)
		if err != nil {
			return "", nil, err
		}
		if err := checkReservedParams(params); err != nil {
			return "", nil, err
		}
		fmt.Fprintf(&sb, "type %sProps struct {\n", c.Name)
		for _, p := range params {
			fmt.Fprintf(&sb, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		sb.WriteString("}\n")
		// Use the same reserved props-param name as the emitted code (_gsxp) so a
		// user param named `p` does not collide in the skeleton either.
		fmt.Fprintf(&sb, "func %s(_gsxp %sProps) _gsxrt.Node {\n", c.Name, c.Name)
		used := usedParams(c, params)
		for _, p := range params {
			if used[p.name] {
				fmt.Fprintf(&sb, "\t%s := _gsxp.%s\n\t_ = %s\n", p.name, fieldName(p.name), p.name)
			}
		}
		emitProbes(&sb, c.Body)
		sb.WriteString("\treturn nil\n}\n")
	}
	return sb.String(), comps, nil
}

// emitProbes writes the type-resolution probes for a component body:
// `_gsxuse(expr)` per interpolation (arity-safe — _gsxuse is variadic, so a
// (T,error) call spreads to two args and still type-checks), and a child call
// `Child(ChildProps{})` per child component.
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			fmt.Fprintf(sb, "\t_gsxuse(%s)\n", strings.TrimSpace(t.Expr))
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				fmt.Fprintf(sb, "\t_ = %s(%sProps{})\n", t.Tag, t.Tag)
			} else {
				emitProbes(sb, t.Children)
			}
		}
	}
}

// harvest reads each interpolation's resolved type from a type-checked skeleton
// file. An interpolation probe is now an ExprStmt whose call target is the
// identifier `_gsxuse`; harvest the single argument's type.
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[*gsxast.Interp]types.Type) {
	byName := map[string]*gsxast.Component{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok {
			continue
		}
		c, ok := byName[fd.Name.Name]
		if !ok || fd.Body == nil {
			continue
		}
		interps := componentInterps(c)
		k := 0
		for _, stmt := range fd.Body.List {
			es, ok := stmt.(*goast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := es.X.(*goast.CallExpr)
			if !ok {
				continue
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || id.Name != "_gsxuse" || len(call.Args) != 1 {
				continue // child-component probe or other
			}
			if k >= len(interps) {
				break
			}
			out[interps[k]] = info.Types[call.Args[0]].Type
			k++
		}
	}
}

type category int

const (
	catUnsupported category = iota
	catString
	catBytes
	catInt
	catUint
	catFloat
	catBool
	catNode
	catNodeSlice
	catStringer
)

// classify maps a resolved type to a render category using structural checks
// (method sets), so it needs no handle to the gsx.Node / fmt.Stringer interface
// types.
func classify(t types.Type) category {
	if t == nil {
		return catUnsupported
	}
	if implementsNode(t) {
		return catNode
	}
	if s, ok := t.Underlying().(*types.Slice); ok && implementsNode(s.Elem()) {
		return catNodeSlice
	}
	if implementsStringer(t) {
		return catStringer
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsString != 0:
			return catString
		case u.Info()&types.IsUnsigned != 0:
			return catUint
		case u.Info()&types.IsInteger != 0:
			return catInt
		case u.Info()&types.IsFloat != 0:
			return catFloat
		case u.Info()&types.IsBoolean != 0:
			return catBool
		}
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			return catBytes
		}
	}
	return catUnsupported
}

// implementsNode reports whether t has a method Render(context.Context, io.Writer) error.
func implementsNode(t types.Type) bool {
	m := lookupMethod(t, "Render")
	if m == nil {
		return false
	}
	sig := m.Type().(*types.Signature)
	if sig.Params().Len() != 2 || sig.Results().Len() != 1 {
		return false
	}
	if sig.Params().At(0).Type().String() != "context.Context" {
		return false
	}
	if sig.Params().At(1).Type().String() != "io.Writer" {
		return false
	}
	return sig.Results().At(0).Type().String() == "error"
}

// implementsStringer reports whether t has a method String() string.
func implementsStringer(t types.Type) bool {
	m := lookupMethod(t, "String")
	if m == nil {
		return false
	}
	sig := m.Type().(*types.Signature)
	return sig.Params().Len() == 0 && sig.Results().Len() == 1 &&
		sig.Results().At(0).Type().String() == "string"
}

// lookupMethod returns the method `name` in t's VALUE method set, or nil. It
// deliberately does NOT probe the pointer method set: classify uses this to
// decide whether to emit `gw.Node(ctx, expr)` / `(expr).String()`, both of which
// pass the value BY VALUE — Go does not auto-address an interface/method-value
// argument, so a pointer-receiver method is not callable there. A value type
// whose pointer (but not value) implements Render must be passed as `*T`.
func lookupMethod(t types.Type, name string) *types.Func {
	ms := types.NewMethodSet(t)
	if sel := ms.Lookup(nil, name); sel != nil {
		if fn, ok := sel.Obj().(*types.Func); ok {
			return fn
		}
	}
	return nil
}

func componentInterps(c *gsxast.Component) []*gsxast.Interp {
	var out []*gsxast.Interp
	collectInterps(c.Body, &out)
	return out
}

// collectInterps gathers interpolation nodes in source (depth-first) order — the
// same order genNode emits them, so the probe and the emission stay aligned.
func collectInterps(nodes []gsxast.Markup, out *[]*gsxast.Interp) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			*out = append(*out, t)
		case *gsxast.Element:
			if !isComponentTag(t.Tag) {
				collectInterps(t.Children, out)
			}
		}
	}
}

// usedParams reports which params are referenced (in value position) by any
// interpolation, so only those are bound to locals.
func usedParams(c *gsxast.Component, params []param) map[string]bool {
	refs := map[string]bool{}
	for _, in := range componentInterps(c) {
		for id := range valueIdents(in.Expr) {
			refs[id] = true
		}
	}
	used := make(map[string]bool, len(params))
	for _, p := range params {
		used[p.name] = refs[p.name]
	}
	return used
}

// valueIdents returns the identifiers used in value position in a Go expression
// (i.e. excluding selector fields after a '.'). Token-based, so it is precise
// without building/walking a full AST.
func valueIdents(exprSrc string) map[string]bool {
	out := map[string]bool{}
	fset := token.NewFileSet()
	f := fset.AddFile("", fset.Base(), len(exprSrc))
	var s scanner.Scanner
	s.Init(f, []byte(exprSrc), nil, 0)
	prevPeriod := false
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.IDENT && !prevPeriod {
			out[lit] = true
		}
		prevPeriod = tok == token.PERIOD
	}
	return out
}

type param struct{ name, typ string }

// parseParams parses an inline param list ("name string, user User") into
// (name, Go-type) pairs by reusing go/parser on a synthesized function.
func parseParams(src string) ([]param, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", "package p\nfunc _("+src+") {}", 0)
	if err != nil {
		return nil, fmt.Errorf("codegen: parse params %q: %w", src, err)
	}
	fn := f.Decls[0].(*goast.FuncDecl)
	var out []param
	for _, field := range fn.Type.Params.List {
		var tb strings.Builder
		if err := printer.Fprint(&tb, fset, field.Type); err != nil {
			return nil, err
		}
		typ := tb.String()
		for _, nm := range field.Names {
			out = append(out, param{name: nm.Name, typ: typ})
		}
	}
	return out, nil
}

// checkReservedParams rejects param names that would collide with the ambient
// closure context or the generator's reserved identifier namespace. The
// generated render closure exposes `ctx` (ambient — user interpolation exprs may
// reference it) and binds its internal machinery (props param, io.Writer, the
// gsx.Writer local, unwrap temps) under the `_gsx` prefix; a user param sharing
// either would produce non-compiling Go.
func checkReservedParams(params []param) error {
	for _, p := range params {
		if p.name == "ctx" {
			return fmt.Errorf("codegen: param name %q is reserved (ambient context)", p.name)
		}
		if strings.HasPrefix(p.name, "_gsx") {
			return fmt.Errorf("codegen: param name %q uses the reserved _gsx prefix", p.name)
		}
	}
	return nil
}

// importSpec is one parsed import hoisted from a pass-through Go chunk: an
// import path with an optional explicit name ("", a package alias, "." or "_").
type importSpec struct {
	name string // "" for the default import name
	path string // import path, unquoted
}

// splitChunk separates a pass-through Go chunk into its imports (to hoist ahead
// of all other declarations) and the remaining source (decls, comments) to emit
// verbatim in the body. The remainder is produced by byte-excising the import
// declarations from the chunk, so non-import content is preserved exactly.
//
// A chunk may freely mix an import with following type/func declarations (the
// common top-of-file layout); both parts are returned. If the chunk carries no
// imports, it is passed through unchanged as the body. If the chunk is invalid
// Go (e.g. an import after a func), an error is returned so the caller can
// surface a clean diagnostic instead of leaking it into a later resolution pass.
func splitChunk(src string) (imports []importSpec, body string, err error) {
	const prefix = "package _gsxp\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+src, parser.ParseComments)
	if err != nil {
		return nil, "", fmt.Errorf("codegen: invalid Go in pass-through block: %w", err)
	}
	const shift = len(prefix)
	type span struct{ lo, hi int }
	var cut []span
	for _, d := range f.Decls {
		gd, ok := d.(*goast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		for _, s := range gd.Specs {
			is := s.(*goast.ImportSpec)
			path, err := strconv.Unquote(is.Path.Value)
			if err != nil {
				continue
			}
			var name string
			if is.Name != nil {
				name = is.Name.Name
			}
			imports = append(imports, importSpec{name: name, path: path})
		}
		cut = append(cut, span{
			lo: fset.Position(gd.Pos()).Offset - shift,
			hi: fset.Position(gd.End()).Offset - shift,
		})
	}
	if len(cut) == 0 {
		return nil, src, nil
	}
	var b strings.Builder
	prev := 0
	for _, c := range cut {
		if c.lo < prev || c.hi > len(src) {
			continue
		}
		b.WriteString(src[prev:c.lo])
		prev = c.hi
	}
	b.WriteString(src[prev:])
	return imports, strings.TrimSpace(b.String()), nil
}

// fieldName maps a param name to its props struct field (first letter upper).
func fieldName(p string) string {
	if p == "" {
		return p
	}
	return strings.ToUpper(p[:1]) + p[1:]
}
