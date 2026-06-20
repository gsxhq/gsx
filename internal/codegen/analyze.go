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
	// Import context under a RESERVED alias so each skeleton component func can
	// bind a real `ctx context.Context` (matching the emitted closure's ambient
	// param) — interp/attr exprs referencing `ctx` then type-check. The reserved
	// alias avoids any duplicate-import clash with a user GoChunk that also
	// imports "context" (Go rejects two plain imports of the same path); the type
	// _gsxctx.Context IS context.Context, so resolution is unaffected.
	sb.WriteString("import _gsxctx \"context\"\n")
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
	// Keep the reserved context import used even when a file has no non-method
	// component func that binds `ctx` (e.g. a method-only file) — otherwise an
	// import-unused error would mask the real diagnostic.
	sb.WriteString("var _ _gsxctx.Context\n")
	for _, body := range bodies {
		sb.WriteString(body)
		sb.WriteByte('\n')
	}
	for _, c := range comps {
		params, err := parseParams(c.Params)
		if err != nil {
			return "", nil, err
		}
		if err := checkReservedParams(params); err != nil {
			return "", nil, err
		}
		// MIRROR genComponent (emit.go): a method component emits a Go method whose
		// receiver var is in scope (so `p.Field` probes type-check against the real
		// receiver type), its props struct is named <RecvTypeName><Name>Props, and a
		// NULLARY method (no params, no children) gets NO props struct + no _gsxp
		// param. The receiver clause + props-struct name + nullary-no-props must be
		// byte-identical in shape to emission, else resolution disagrees.
		propsName := c.Name + "Props"
		// recvVar/recvTypeName stay "" for a function component; for a method
		// component they are passed to emitProbes so a dotted child tag whose left ==
		// recvVar is probed as a method call (mirroring the emitter's childInvocation).
		var recvVar, recvTypeName string
		if c.Recv != "" {
			var rerr error
			recvVar, _, recvTypeName, rerr = parseRecv(c.Recv)
			if rerr != nil {
				return "", nil, rerr
			}
			if rerr := checkReservedRecvVar(recvVar); rerr != nil {
				return "", nil, rerr
			}
			propsName = recvTypeName + c.Name + "Props"
		}
		// Synthesize the implicit `Children _gsxrt.Node` slot field + `children`
		// local in lockstep with genComponent (emit.go), so skeleton and emitted
		// code agree on the props shape and the `{children}` interp type-checks.
		hasChildren := usesChildren(c.Body)
		// MIRROR emit.go: only a method component suppresses the props struct when
		// nullary; a function component always keeps its (possibly empty) props
		// struct so the child-invocation path's `<Tag>Props{}` literal type-checks.
		hasProps := c.Recv == "" || len(params) > 0 || hasChildren
		if hasProps {
			fmt.Fprintf(&sb, "type %s struct {\n", propsName)
			for _, p := range params {
				fmt.Fprintf(&sb, "\t%s %s\n", fieldName(p.name), p.typ)
			}
			if hasChildren {
				sb.WriteString("\tChildren _gsxrt.Node\n")
			}
			sb.WriteString("}\n")
		}
		// Use the same reserved props-param name as the emitted code (_gsxp) so a
		// user param named `p` does not collide in the skeleton either. Emit the
		// receiver clause verbatim for a method component (its receiver var is in
		// scope, like the emitted method).
		if c.Recv != "" {
			fmt.Fprintf(&sb, "func %s %s(", c.Recv, c.Name)
		} else {
			fmt.Fprintf(&sb, "func %s(", c.Name)
		}
		if hasProps {
			fmt.Fprintf(&sb, "_gsxp %s", propsName)
		}
		sb.WriteString(") _gsxrt.Node {\n")
		// Bind the ambient `ctx` (matching the emitted closure's
		// `func(ctx context.Context, _gsxw io.Writer)` param) so probe exprs that
		// reference it — `{ fromCtx(ctx) }`, `id={ g(ctx) }` — type-check. The
		// `_ = ctx` keeps it used for components that don't reference ctx.
		sb.WriteString("\tvar ctx _gsxctx.Context\n\t_ = ctx\n")
		used := usedParams(c, params)
		for _, p := range params {
			if used[p.name] {
				fmt.Fprintf(&sb, "\t%s := _gsxp.%s\n\t_ = %s\n", p.name, fieldName(p.name), p.name)
			}
		}
		if hasChildren {
			sb.WriteString("\tchildren := _gsxp.Children\n\t_ = children\n")
		}
		if err := emitProbes(&sb, c.Body, table, recvVar, recvTypeName); err != nil {
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
				// A component's SIMPLE attrs (props) reject pipelines at emission, so
				// they contribute no _gsxstd call — but its named-slot (markup-attr)
				// values AND its slot children render in this parent scope and CAN carry
				// a pipeline, so recurse them.
				found := false
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if bodyHasPipeline(value) {
						found = true
					}
				})
				if found {
					return true
				}
				if bodyHasPipeline(t.Children) {
					return true
				}
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
// `_gsxuse(expr)`; child components are `_ = Child(ChildProps{})` (or, for a
// method invocation via the enclosing receiver, `_ = p.Method(...)`).
//
// recvVar/recvTypeName are the enclosing component's receiver var + type name
// (empty for a function component); they drive the same method-vs-package
// disambiguation as the emitter (childInvocation), so the probe type-checks the
// call against the real method/function signature + props struct identically.
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup, table filterTable, recvVar, recvTypeName string) error {
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
				// Emit the SAME call as genChildComponent (via childInvocation) so the
				// assignment type-checks each prop expr against the child's/method's real
				// field type. The shared childPropsLiteral builder guarantees the attr +
				// slot fields never drift. A nullary method invocation (no attrs, no
				// children) has no props struct → `_ = p.Method()`. Otherwise build the
				// props literal; named-slot/Children fields use a typed-nil so the
				// literal type-checks WITHOUT building the real slot closure here —
				// the slot content (markup-attr values then t.Children) is probed
				// SEPARATELY below (its interps become _gsxuse in the SAME order
				// collectExprs collected them; the props-literal exprs are NOT _gsxuse,
				// so they don't perturb the k-th alignment).
				callTarget, propsType, isMethod := childInvocation(t, recvVar, recvTypeName)
				if isMethod && len(t.Attrs) == 0 && len(t.Children) == 0 {
					fmt.Fprintf(sb, "_ = %s()\n", callTarget)
				} else {
					// Build the SAME props literal as the emitter via childPropsLiteral,
					// but with a typed-nil slotValue: each named-slot and Children field
					// is `_gsxrt.Node(nil)` so the literal type-checks WITHOUT the real
					// closure. The slot content (markup-attr values + children) is probed
					// SEPARATELY below, so its interps become the _gsxuse sequence in the
					// SAME order collectExprs collected them; the props-literal exprs are
					// NOT _gsxuse, so they don't perturb the k-th alignment.
					fields, err := childPropsLiteral(t, func(nodes []gsxast.Markup) (string, error) {
						return "_gsxrt.Node(nil)", nil
					})
					if err != nil {
						return err
					}
					fmt.Fprintf(sb, "_ = %s(%s{%s})\n", callTarget, propsType, fields)
				}
				// Probe slot content in the SAME canonical order collectExprs walks:
				// each markup-attr value (attr order) then the children.
				var probeErr error
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, recvVar, recvTypeName)
				})
				if probeErr != nil {
					return probeErr
				}
				if err := emitProbes(sb, t.Children, table, recvVar, recvTypeName); err != nil {
					return err
				}
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
				if err := emitProbes(sb, t.Children, table, recvVar, recvTypeName); err != nil {
					return err
				}
			}
		case *gsxast.Fragment:
			if err := emitProbes(sb, t.Children, table, recvVar, recvTypeName); err != nil {
				return err
			}
		case *gsxast.ForMarkup:
			fmt.Fprintf(sb, "for %s {\n", t.Clause)
			if err := emitProbes(sb, t.Body, table, recvVar, recvTypeName); err != nil {
				return err
			}
			sb.WriteString("}\n")
		case *gsxast.IfMarkup:
			fmt.Fprintf(sb, "if %s {\n", t.Cond)
			if err := emitProbes(sb, t.Then, table, recvVar, recvTypeName); err != nil {
				return err
			}
			sb.WriteString("}")
			if t.Else != nil {
				sb.WriteString(" else {\n")
				if err := emitProbes(sb, t.Else, table, recvVar, recvTypeName); err != nil {
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
				if err := emitProbes(sb, cc.Body, table, recvVar, recvTypeName); err != nil {
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
	// Key by receiver-type + method name, not name alone: two method components
	// with the same method name on different receivers (e.g. (UsersPage) Row and
	// (OrdersPage) Row) are distinct, and their skeleton funcs are distinct
	// methods — keying on name alone would map both skeleton funcs to one
	// component and leave the other's interps unresolved.
	byKey := map[string]*gsxast.Component{}
	for _, c := range comps {
		byKey[componentKey(c)] = c
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok {
			continue
		}
		c, ok := byKey[funcDeclKey(fd)]
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

// componentKey identifies a component by receiver-type + name, so same-named
// methods on different receivers are distinct. A function component (no receiver)
// keys on its name alone (with a leading "." marker so it can never collide with
// a method named the same on a receiver type called "").
func componentKey(c *gsxast.Component) string {
	if c.Recv == "" {
		return "." + c.Name
	}
	_, _, recvTypeName, err := parseRecv(c.Recv)
	if err != nil {
		// Should not happen: buildSkeleton already parsed this receiver before
		// harvest runs. Fall back to name-only rather than panic.
		return "." + c.Name
	}
	return recvTypeName + "." + c.Name
}

// funcDeclKey mirrors componentKey for a type-checked skeleton FuncDecl: a method
// keys on its receiver type name + method name; a plain func on "." + name.
func funcDeclKey(fd *goast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return "." + fd.Name.Name
	}
	return recvTypeIdent(fd.Recv.List[0].Type) + "." + fd.Name.Name
}

// recvTypeIdent extracts the receiver type's base name from a skeleton method's
// receiver expression: T, *T, T[X], *T[X] → "T".
func recvTypeIdent(e goast.Expr) string {
	switch t := e.(type) {
	case *goast.Ident:
		return t.Name
	case *goast.StarExpr:
		return recvTypeIdent(t.X)
	case *goast.IndexExpr:
		return recvTypeIdent(t.X)
	case *goast.IndexListExpr:
		return recvTypeIdent(t.X)
	}
	return ""
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
				// Child component: its SIMPLE attrs are props (type-checked via the
				// props literal, NOT _gsxuse), so they are NOT collected here. But a
				// MARKUP attr (named slot) value AND the children are SLOT content
				// rendered in THIS (parent) scope, so their interps/exprs ARE collected
				// — markup-attr values (in attr order) BEFORE children. emitProbes
				// recurses identically (same shared walkMarkupAttrs order), so the k-th
				// probe still maps to the k-th node.
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					collectExprs(value, out)
				})
				collectExprs(t.Children, out)
				continue
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

// walkMarkupAttrs invokes fn with the Value (markup node list) of each
// *MarkupAttr in an element's attr list, in source order. A markup attr is a
// NAMED slot: its value renders in the PARENT scope and carries interps needing
// types, so it must be collected/probed/bound BEFORE the element's children. This
// is the SINGLE walk shared by collectExprs and emitProbes (and the binding
// walks) so the markup-value recursion order cannot drift — exactly as
// walkAttrExprs unifies the CondAttr recursion.
func walkMarkupAttrs(attrs []gsxast.Attr, fn func(value []gsxast.Markup)) {
	for _, a := range attrs {
		if ma, ok := a.(*gsxast.MarkupAttr); ok {
			fn(ma.Value)
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
	// Child-component prop exprs (each <Child attr={expr}/>) are emitted verbatim
	// into the props literal — both the skeleton probe (`_ = Child(ChildProps{Attr:
	// expr})`) and the render call. A parent param referenced ONLY in such an expr
	// must therefore be bound as a local, else the generated code fails type-check
	// with `undefined: x`. These exprs are NOT in collectExprs/the _gsxuse sequence
	// (they're not probed via _gsxuse), so they need their own walk here.
	collectChildPropExprSrc(c.Body, addIdents)
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
			// Recurse children for BOTH plain elements and child components: a
			// component's slot content renders in THIS parent scope, so a control-flow
			// clause inside the slot (e.g. `for ... range items`) references a parent
			// local and must be bound. A component's MARKUP-attr (named slot) values
			// also render in this parent scope, so recurse them too. (A component's
			// SIMPLE attrs are props, not slot content, so they are not visited.)
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				collectClauseSrc(value, add)
			})
			collectClauseSrc(t.Children, add)
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
				// A component's SIMPLE attrs are props (handled via childPropsLiteral),
				// so they are skipped — but its named-slot (markup-attr) values AND its
				// slot children render in THIS parent scope, so a composable-class/
				// element-spread expr inside either references a parent local and must be
				// bound. Recurse the markup-attr values and the children (not the simple
				// attrs).
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					collectAttrExprSrc(value, add)
				})
				collectAttrExprSrc(t.Children, add)
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

// collectChildPropExprSrc visits markup in depth-first source order and feeds the
// Expr of every child-component *ExprAttr to add. Unlike collectAttrExprSrc (which
// SKIPS component tags), this walk descends INTO a component element to read its
// prop exprs — they are emitted verbatim into the props literal, so a param used
// only there must be bound. Pipelined prop exprs are rejected at emission, so only
// the bare Expr (no Stages args) is collected here. Non-component element children
// are still recursed so a child component nested inside a plain element is found.
func collectChildPropExprSrc(nodes []gsxast.Markup, add func(string)) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				for _, a := range t.Attrs {
					if ea, ok := a.(*gsxast.ExprAttr); ok {
						add(ea.Expr)
					}
				}
				// Recurse the named-slot (markup-attr) values AND the slot children: a
				// child component nested inside this component's named slot OR its
				// children renders in THIS parent scope, so its prop exprs reference
				// parent locals and must be bound.
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					collectChildPropExprSrc(value, add)
				})
				collectChildPropExprSrc(t.Children, add)
				continue
			}
			collectChildPropExprSrc(t.Children, add)
		case *gsxast.Fragment:
			collectChildPropExprSrc(t.Children, add)
		case *gsxast.ForMarkup:
			collectChildPropExprSrc(t.Body, add)
		case *gsxast.IfMarkup:
			collectChildPropExprSrc(t.Then, add)
			collectChildPropExprSrc(t.Else, add)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				collectChildPropExprSrc(cc.Body, add)
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

// parseRecv parses a method-component receiver clause (INCLUDING parens, e.g.
// "(p UsersPage)", "(f *Form)") into its variable name, full receiver type, and
// the bare type name used to prefix the props struct. It reuses go/parser on a
// synthesized method so it handles `*T`, named/unnamed, and spacing robustly.
//
// For "(p UsersPage)"  → recvVar "p", recvType "UsersPage",  recvTypeName "UsersPage".
// For "(f *Form)"      → recvVar "f", recvType "*Form",       recvTypeName "Form".
//
// An UNNAMED receiver ("(UsersPage)" / "(*Form)") is rejected: a method
// component needs the receiver var as its page-data handle (referenced in the
// body as `p.Field`). It is shared by genComponent (emit) and buildSkeleton
// (skeleton) so both agree on the signature, props-struct name, and reserved
// receiver-var check.
func parseRecv(recv string) (recvVar, recvType, recvTypeName string, err error) {
	src := strings.TrimSpace(recv)
	if src == "" {
		return "", "", "", fmt.Errorf("codegen: empty method-component receiver")
	}
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, "", "package _\nfunc "+src+" _m() {}", 0)
	if perr != nil {
		return "", "", "", fmt.Errorf("codegen: parse method-component receiver %q: %w", recv, perr)
	}
	fn, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", "", "", fmt.Errorf("codegen: invalid method-component receiver %q", recv)
	}
	field := fn.Recv.List[0]
	if len(field.Names) != 1 || field.Names[0].Name == "_" {
		return "", "", "", fmt.Errorf("codegen: method component receiver must be named, e.g. (p T) — got %q", recv)
	}
	recvVar = field.Names[0].Name
	var tb strings.Builder
	if err := printer.Fprint(&tb, fset, field.Type); err != nil {
		return "", "", "", err
	}
	recvType = tb.String()
	recvTypeName = strings.TrimPrefix(recvType, "*")
	return recvVar, recvType, recvTypeName, nil
}

// checkReservedRecvVar rejects a method-component receiver var that would
// collide with the ambient closure context (`ctx`), the generator's reserved
// `_gsx` namespace, or a package ident the emitter references in the body
// (gsx/strconv) — any of which would break the emitted method body where the
// receiver var is in scope.
func checkReservedRecvVar(recvVar string) error {
	if recvVar == "ctx" {
		return fmt.Errorf("codegen: method-component receiver var %q is reserved (ambient context)", recvVar)
	}
	if strings.HasPrefix(recvVar, "_gsx") {
		return fmt.Errorf("codegen: method-component receiver var %q uses the reserved _gsx prefix", recvVar)
	}
	if emittedImportIdent[recvVar] {
		return fmt.Errorf("codegen: method-component receiver var %q is reserved (shadows a generated import)", recvVar)
	}
	return nil
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
		if p.name == "children" {
			return fmt.Errorf("codegen: param name %q is reserved (implicit children slot)", p.name)
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
