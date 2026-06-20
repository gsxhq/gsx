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
func resolveTypesPkg(dir string, files map[string]*gsxast.File) (map[gsxast.Node]types.Type, filterTable, error) {
	table, err := loadFilterTable(dir)
	if err != nil {
		return nil, nil, err
	}
	overlay := map[string][]byte{}
	skelComps := map[string][]*gsxast.Component{}
	for path, file := range files {
		skel, comps, err := buildSkeleton(file, table)
		if err != nil {
			return nil, nil, err
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
		return nil, nil, err
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
		return nil, nil, fmt.Errorf("codegen: load package: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, nil, fmt.Errorf("codegen: no package found in %s", dir)
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return nil, nil, fmt.Errorf("codegen: type resolution failed: %s", pkg.Errors[0])
	}

	out := map[gsxast.Node]types.Type{}
	for _, f := range pkg.Syntax {
		fname := pkg.Fset.Position(f.Pos()).Filename
		comps, ok := skelComps[fname]
		if !ok {
			continue // a real .go file, not one of our overlays
		}
		harvest(f, comps, pkg.TypesInfo, out)
	}
	return out, table, nil
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
func buildSkeleton(file *gsxast.File, table filterTable) (string, []*gsxast.Component, error) {
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

	// Pre-scan for any pipeline in a component body. When present, the probes
	// reference _gsxstd.<Func>, so the skeleton must import std under the same
	// reserved alias the emitter uses. Only import when used — an unused import
	// fails the skeleton type-check.
	usesStd := false
	for _, c := range comps {
		if bodyHasPipeline(c.Body) {
			usesStd = true
			break
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "package %s\n", file.Package)
	sb.WriteString("import _gsxrt \"github.com/gsxhq/gsx\"\n")
	if usesStd {
		fmt.Fprintf(&sb, "import _gsxstd %q\n", stdImportPath)
	}
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
		if err := emitProbes(&sb, c.Body, table); err != nil {
			return "", nil, err
		}
		sb.WriteString("\treturn nil\n}\n")
	}
	return sb.String(), comps, nil
}

// bodyHasPipeline reports whether any interpolation or expr-attribute in the
// markup carries a pipeline (a non-empty Stages). It mirrors the markup walk of
// emitProbes/collectExprs so the pre-scan and the probe agree on what counts.
func bodyHasPipeline(nodes []gsxast.Markup) bool {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			if len(t.Stages) > 0 {
				return true
			}
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				continue
			}
			found := false
			walkAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
				if len(ea.Stages) > 0 {
					found = true
				}
			})
			if found {
				return true
			}
			if bodyHasPipeline(t.Children) {
				return true
			}
		case *gsxast.Fragment:
			if bodyHasPipeline(t.Children) {
				return true
			}
		case *gsxast.ForMarkup:
			if bodyHasPipeline(t.Body) {
				return true
			}
		case *gsxast.IfMarkup:
			if bodyHasPipeline(t.Then) || bodyHasPipeline(t.Else) {
				return true
			}
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				if bodyHasPipeline(cc.Body) {
					return true
				}
			}
		}
	}
	return false
}

// emitProbes writes type-resolution probes for a component body. It MIRRORS the
// control structure (real for/if/switch + {{ }} code) so interpolations that
// reference loop vars / block-locals type-check in scope. Each interpolation is
// `_gsxuse(expr)`; child components are `_ = Child(ChildProps{})`.
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup, table filterTable) error {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			probe, err := probeExpr(t.Expr, t.Stages, table)
			if err != nil {
				return err
			}
			fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				fmt.Fprintf(sb, "_ = %s(%sProps{})\n", t.Tag, t.Tag)
			} else {
				// Probe each attr-expr (top-level and CondAttr-nested) FLAT, in the
				// SAME canonical order collectExprs walks, so the k-th _gsxuse maps to
				// the k-th collected node. The nested exprs type-check regardless of
				// branch, so no real `if` wrapper is needed.
				var probeErr error
				walkAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
					if probeErr != nil {
						return
					}
					probe, err := probeExpr(ea.Expr, ea.Stages, table)
					if err != nil {
						probeErr = err
						return
					}
					fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
				})
				if probeErr != nil {
					return probeErr
				}
				if err := emitProbes(sb, t.Children, table); err != nil {
					return err
				}
			}
		case *gsxast.Fragment:
			if err := emitProbes(sb, t.Children, table); err != nil {
				return err
			}
		case *gsxast.ForMarkup:
			fmt.Fprintf(sb, "for %s {\n", t.Clause)
			if err := emitProbes(sb, t.Body, table); err != nil {
				return err
			}
			sb.WriteString("}\n")
		case *gsxast.IfMarkup:
			fmt.Fprintf(sb, "if %s {\n", t.Cond)
			if err := emitProbes(sb, t.Then, table); err != nil {
				return err
			}
			sb.WriteString("}")
			if t.Else != nil {
				sb.WriteString(" else {\n")
				if err := emitProbes(sb, t.Else, table); err != nil {
					return err
				}
				sb.WriteString("}")
			}
			sb.WriteString("\n")
		case *gsxast.SwitchMarkup:
			fmt.Fprintf(sb, "switch %s {\n", t.Tag)
			for _, cc := range t.Cases {
				if cc.Default {
					sb.WriteString("default:\n")
				} else {
					fmt.Fprintf(sb, "case %s:\n", cc.List)
				}
				if err := emitProbes(sb, cc.Body, table); err != nil {
					return err
				}
			}
			sb.WriteString("}\n")
		case *gsxast.GoBlock:
			sb.WriteString(t.Code)
			sb.WriteString("\n")
		}
	}
	return nil
}

// probeExpr returns the Go expression to probe for an interpolation / expr-attr.
// Without stages it is the trimmed seed; with stages it is the lowered pipeline
// (the SAME lowerPipe output the emitter uses), so the harvested type is the
// pipeline's RESULT type and resolution stays aligned with emission.
func probeExpr(seed string, stages []gsxast.PipeStage, table filterTable) (string, error) {
	if len(stages) == 0 {
		return strings.TrimSpace(seed), nil
	}
	lowered, _, err := lowerPipe(seed, stages, table)
	if err != nil {
		return "", err
	}
	return lowered, nil
}

// harvest reads each interpolation's resolved type from a type-checked skeleton
// file. An interpolation probe is now an ExprStmt whose call target is the
// identifier `_gsxuse`; harvest the single argument's type.
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[gsxast.Node]types.Type) {
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
		nodes := componentExprs(c)
		k := 0
		goast.Inspect(fd.Body, func(node goast.Node) bool {
			call, ok := node.(*goast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || id.Name != "_gsxuse" || len(call.Args) != 1 {
				return true
			}
			if k < len(nodes) {
				out[nodes[k]] = info.Types[call.Args[0]].Type
				k++
			}
			return true
		})
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

func componentExprs(c *gsxast.Component) []gsxast.Node {
	var out []gsxast.Node
	collectExprs(c.Body, &out)
	return out
}

// collectExprs gathers the type-needing expression nodes (*Interp and *ExprAttr)
// in depth-first source order — per element, attribute expressions BEFORE
// children — matching emitProbes/genNode traversal so the k-th probe aligns with
// the k-th node.
func collectExprs(nodes []gsxast.Markup, out *[]gsxast.Node) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			*out = append(*out, t)
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				continue // child component: props deferred; no attr exprs / children here
			}
			// Collect each attr-expr (top-level and CondAttr-nested) in canonical
			// order, before the element's children — emitProbes walks identically.
			walkAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
				*out = append(*out, ea)
			})
			collectExprs(t.Children, out)
		case *gsxast.Fragment:
			collectExprs(t.Children, out)
		case *gsxast.ForMarkup:
			collectExprs(t.Body, out)
		case *gsxast.IfMarkup:
			collectExprs(t.Then, out)
			collectExprs(t.Else, out)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				collectExprs(cc.Body, out)
			}
		}
	}
}

// walkAttrExprs invokes fn for each type-needing *ExprAttr in an element's attr
// list, in canonical source order: each top-level *ExprAttr where it sits, and —
// for a *CondAttr — its Then attr-exprs then its Else attr-exprs (recursing
// nested *CondAttrs, so an else-if chain is visited in order). Other attr kinds
// (Static/Bool/Class/Spread) contribute no expr node. This is the SINGLE walk
// shared by collectExprs (builds the ordered node list) and emitProbes (emits one
// _gsxuse per node) so the k-th probe always maps to the k-th node — no drift.
func walkAttrExprs(attrs []gsxast.Attr, fn func(*gsxast.ExprAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ExprAttr:
			fn(at)
		case *gsxast.CondAttr:
			walkAttrExprs(at.Then, fn)
			walkAttrExprs(at.Else, fn)
		}
	}
}

// usedParams reports which params are referenced (in value position) by any
// interpolation OR by any control-flow clause (for/if/switch/case head and {{ }}
// Go block), so only those are bound to locals. Control-flow clauses are emitted
// verbatim into both the skeleton probe and the render closure, so a param named
// in `range items` must be in scope there just like one in an interpolation.
func usedParams(c *gsxast.Component, params []param) map[string]bool {
	refs := map[string]bool{}
	addIdents := func(src string) {
		for id := range valueIdents(src) {
			refs[id] = true
		}
	}
	for _, n := range componentExprs(c) {
		var expr string
		var stages []gsxast.PipeStage
		switch v := n.(type) {
		case *gsxast.Interp:
			expr, stages = v.Expr, v.Stages
		case *gsxast.ExprAttr:
			expr, stages = v.Expr, v.Stages
		}
		addIdents(expr)
		// Filter arguments are emitted verbatim into the lowered call
		// (_gsxstd.Join(sep)(...)), so idents they reference — e.g. a component
		// param used only inside join(sep) — must be bound as locals too.
		for _, st := range stages {
			if st.Args != "" {
				addIdents(st.Args)
			}
		}
	}
	collectClauseSrc(c.Body, addIdents)
	// Composable class parts (Expr + Cond) and element-spread exprs are emitted
	// verbatim into the render closure (gsx.Class/ClassIf args, gw.Spread arg), so
	// a param referenced ONLY there must be bound as a local too — otherwise the
	// generated code fails type-check with `undefined: x`.
	collectAttrExprSrc(c.Body, addIdents)
	used := make(map[string]bool, len(params))
	for _, p := range params {
		used[p.name] = refs[p.name]
	}
	return used
}

// collectClauseSrc visits markup in depth-first source order and feeds every Go
// control-flow clause source (for clause, if cond, switch tag, case list, GoBlock
// code) to add. These fragments are emitted verbatim, so the idents they
// reference must be in scope wherever the markup renders.
func collectClauseSrc(nodes []gsxast.Markup, add func(string)) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Element:
			if !isComponentTag(t.Tag) {
				collectClauseSrc(t.Children, add)
			}
		case *gsxast.Fragment:
			collectClauseSrc(t.Children, add)
		case *gsxast.ForMarkup:
			add(t.Clause)
			collectClauseSrc(t.Body, add)
		case *gsxast.IfMarkup:
			add(t.Cond)
			collectClauseSrc(t.Then, add)
			collectClauseSrc(t.Else, add)
		case *gsxast.SwitchMarkup:
			add(t.Tag)
			for _, cc := range t.Cases {
				add(cc.List)
				collectClauseSrc(cc.Body, add)
			}
		case *gsxast.GoBlock:
			add(t.Code)
		}
	}
}

// collectAttrExprSrc visits markup in depth-first source order and feeds every
// composable-class part source (each Expr and Cond) and element-spread expr to
// add. These fragments are emitted verbatim into the render closure, so the
// idents they reference must be in scope wherever the markup renders. Component
// tags are skipped (their attrs are props, handled elsewhere — and isComponentTag
// routes them away from emitAttr).
func collectAttrExprSrc(nodes []gsxast.Markup, add func(string)) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				continue
			}
			collectAttrSrc(t.Attrs, add)
			collectAttrExprSrc(t.Children, add)
		case *gsxast.Fragment:
			collectAttrExprSrc(t.Children, add)
		case *gsxast.ForMarkup:
			collectAttrExprSrc(t.Body, add)
		case *gsxast.IfMarkup:
			collectAttrExprSrc(t.Then, add)
			collectAttrExprSrc(t.Else, add)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				collectAttrExprSrc(cc.Body, add)
			}
		}
	}
}

// collectAttrSrc feeds every verbatim-emitted Go fragment in an attr list to add:
// composable-class part Expr+Cond, element-spread Expr, conditional-attr Cond, and
// — recursing into a *CondAttr's Then/Else — the same for nested attrs. (Nested
// *ExprAttr exprs are bound via the componentExprs path in usedParams, but a param
// used ONLY inside a CondAttr branch's expr-attr value is still bound because
// componentExprs/collectExprs now also recurse CondAttr; the Cond and nested
// class/spread fragments are bound here.)
func collectAttrSrc(attrs []gsxast.Attr, add func(string)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ClassAttr:
			for _, p := range at.Parts {
				add(p.Expr)
				if p.Cond != "" {
					add(p.Cond)
				}
			}
		case *gsxast.SpreadAttr:
			add(at.Expr)
		case *gsxast.ExprAttr:
			add(at.Expr)
			for _, st := range at.Stages {
				if st.Args != "" {
					add(st.Args)
				}
			}
		case *gsxast.CondAttr:
			add(at.Cond)
			collectAttrSrc(at.Then, add)
			collectAttrSrc(at.Else, add)
		}
	}
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
		// Package identifiers the emitter references inside the closure body: a
		// same-named param would shadow them via local-binding and break the
		// generated code. (The runtime import and strconv are the only package
		// idents emitted into bodies today; a more robust fix would _gsx-alias
		// generator-emitted imports — tracked for phase 2.)
		if emittedImportIdent[p.name] {
			return fmt.Errorf("codegen: param name %q is reserved (shadows a generated import)", p.name)
		}
	}
	return nil
}

// emittedImportIdent is the set of package identifiers the emitter references in
// a render closure body (see genInterp/emitRender and genComponent).
var emittedImportIdent = map[string]bool{"gsx": true, "strconv": true}

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
