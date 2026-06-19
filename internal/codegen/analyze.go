package codegen

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"go/types"
	"path/filepath"
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

	var sb strings.Builder
	fmt.Fprintf(&sb, "package %s\n", file.Package)
	sb.WriteString("import _gsxrt \"github.com/gsxhq/gsx\"\n")
	sb.WriteString("func _gsxuse(...any) {}\n")
	for _, d := range file.Decls {
		if gc, ok := d.(*gsxast.GoChunk); ok {
			sb.WriteString(gc.Src)
			sb.WriteByte('\n')
		}
	}
	for _, c := range comps {
		if c.Recv != "" {
			continue // SPIKE: method components handled later
		}
		params, err := parseParams(c.Params)
		if err != nil {
			return "", nil, err
		}
		fmt.Fprintf(&sb, "type %sProps struct {\n", c.Name)
		for _, p := range params {
			fmt.Fprintf(&sb, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		sb.WriteString("}\n")
		fmt.Fprintf(&sb, "func %s(p %sProps) _gsxrt.Node {\n", c.Name, c.Name)
		used := usedParams(c, params)
		for _, p := range params {
			if used[p.name] {
				fmt.Fprintf(&sb, "\t%s := p.%s\n\t_ = %s\n", p.name, fieldName(p.name), p.name)
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
	catInt
)

func classify(t types.Type) category {
	if b, ok := t.Underlying().(*types.Basic); ok {
		switch {
		case b.Info()&types.IsString != 0:
			return catString
		case b.Info()&types.IsInteger != 0:
			return catInt
		}
	}
	return catUnsupported
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

// fieldName maps a param name to its props struct field (first letter upper).
func fieldName(p string) string {
	if p == "" {
		return p
	}
	return strings.ToUpper(p[:1]) + p[1:]
}
