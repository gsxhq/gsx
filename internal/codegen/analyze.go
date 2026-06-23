package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	gsxast "github.com/gsxhq/gsx/ast"
)

// errSkipComponent is a sentinel returned by emitComponentSkeleton when the
// component fails an early validation check (reserved param/recv, parse error)
// that will also be caught — with a positioned diagnostic — in genComponent at
// emit time. The caller (buildSkeleton) skips this component's skeleton and
// continues; the overall skeleton remains valid Go. Infrastructure errors
// (e.g. unknown filter in emitProbes) are NOT wrapped in this sentinel and
// propagate as fatal errors.
var errSkipComponent = errors.New("skip")


// resolveTypesPkg type-checks the package (real .go files + synthesized gsx
// component skeletons via Overlay) and returns each interpolation's type.
//
// propFields is the SAME AST-derived prop-field map GeneratePackage threads into
// emission (see componentPropFieldsFor); it drives the call-site split inside the
// PROBE (buildSkeleton/emitProbes) so the probe's child-props literal splits
// fallthrough attrs into an Attrs bag IDENTICALLY to emission — guaranteeing the
// generate-time type-check validates exactly what the emitter produces.
func resolveTypesPkg(dir string, files map[string]*gsxast.File, propFields map[string]map[string]bool, fset *token.FileSet) (map[gsxast.Node]types.Type, filterTable, error) {
	return resolveTypesPkgWithFilters(dir, files, propFields, []string{stdImportPath}, fset)
}

// resolveTypesPkgWithFilters is the multi-package form of resolveTypesPkg: it
// harvests the filter table from filterPkgs (last-wins precedence) and otherwise
// behaves identically. resolveTypesPkg is the std-only wrapper.
func resolveTypesPkgWithFilters(dir string, files map[string]*gsxast.File, propFields map[string]map[string]bool, filterPkgs []string, fset *token.FileSet) (map[gsxast.Node]types.Type, filterTable, error) {
	table, err := loadFilterTableMulti(dir, filterPkgs)
	if err != nil {
		return nil, nil, err
	}
	overlay := map[string][]byte{}
	skelComps := map[string][]*gsxast.Component{}
	for path, file := range files {
		skel, comps, err := buildSkeleton(file, table, propFields, fset)
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

// componentPropFieldsFor builds the call-site split's prop-field map purely from
// the parsed component ASTs — SAME-PACKAGE only, available BEFORE type resolution.
// It is keyed by props-struct TYPE NAME exactly as childInvocation produces it
// (bare <Name>Props for a function component, <RecvType><Name>Props for a method),
// with value the set of field NAMES the skeleton/emitter synthesize for that
// component:
//
//	propFields(c) = { fieldName(param) : param ∈ c.Params }
//	             ∪ { "Children" if usesChildren(c.Body) }
//	             ∪ { "Attrs"    if singleRoot(c.Body) }
//
// Because BOTH the probe (buildSkeleton/emitProbes) and emission
// (genChildComponent/childPropsLiteral) classify call-site attrs through THIS map,
// emit ≡ probe is guaranteed with no second type-check. A component's props type is
// absent from this map exactly when it is CROSS-PACKAGE (or otherwise unknown), so
// a lookup miss → graceful fallback (see isPropField): identifier attrs assumed
// props, non-identifier attrs fall through.
//
// A receiver parse failure is silently skipped (the component is simply omitted, so
// its call sites take the graceful cross-package path); buildSkeleton re-parses the
// same receiver and surfaces a clean error there.
func componentPropFieldsFor(files map[string]*gsxast.File) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	for _, file := range files {
		for _, d := range file.Decls {
			c, ok := d.(*gsxast.Component)
			if !ok {
				continue
			}
			params, err := parseParams(c.Params)
			if err != nil {
				return nil, err
			}
			propsName := c.Name + "Props"
			if c.Recv != "" {
				_, _, recvTypeName, rerr := parseRecv(c.Recv)
				if rerr != nil {
					continue // surfaced cleanly by buildSkeleton
				}
				propsName = recvTypeName + c.Name + "Props"
			}
			fields := map[string]bool{}
			for _, p := range params {
				fields[fieldName(p.name)] = true
			}
			hasChildren := usesChildren(c.Body)
			if hasChildren {
				fields["Children"] = true
			}
			// Mirror the Attrs synthesis gate in genComponent/buildSkeleton exactly
			// (hasFallthrough) so the map agrees with the struct that is actually
			// emitted — a single-root NULLARY METHOD has no props struct at all, so
			// it must NOT claim an Attrs field. MANUAL mode (a body referencing
			// `attrs`) forces the Attrs field regardless (incl. for a nullary method,
			// which then gains a props struct), so OR it in.
			_, hasRoot := singleRoot(c.Body)
			manual := usesAttrs(c.Body)
			if (hasRoot && (c.Recv == "" || len(params) > 0 || hasChildren)) || manual {
				fields["Attrs"] = true
			}
			out[propsName] = fields
		}
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
func buildSkeleton(file *gsxast.File, table filterTable, propFields map[string]map[string]bool, fset *token.FileSet) (string, []*gsxast.Component, error) {
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

	// Emit each component's probe body into a temp buffer, accumulating the
	// filter packages the probes actually reference (alias→pkgPath). The probes
	// reference <alias>.<Func>, so the skeleton must import each USED filter
	// package under the SAME reserved alias the emitter uses — driven by the same
	// lowerPipe report so probe (skeleton) and emit agree on which packages and
	// aliases are in play. Only USED packages are imported (an unused import fails
	// the skeleton type-check).
	usedFilters := map[string]string{} // alias -> pkgPath
	var compBuf strings.Builder
	// Keep only the components whose skeletons succeed. A validation error
	// (errSkipComponent — reserved param/recv, parse failure) means the component
	// is invalid for codegen; skip its skeleton so the overall file stays valid Go.
	// genComponent will re-encounter the same error at emit time and record a
	// positioned diagnostic via the bag. Any OTHER error is a real infrastructure
	// failure and must abort the whole skeleton build.
	var validComps []*gsxast.Component
	for _, c := range comps {
		if err := emitComponentSkeleton(&compBuf, c, table, propFields, usedFilters, fset); err != nil {
			if errors.Is(err, errSkipComponent) {
				// Validation failure: skip this component's skeleton; it will fail
				// again (with a positioned diagnostic) during generateFile.
				continue
			}
			return "", nil, err
		}
		validComps = append(validComps, c)
	}
	comps = validComps

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
	// Import each USED filter package under its reserved alias. std keeps its
	// _gsxstd alias and dedicated `import _gsxstd "<std>"` line (in alias order),
	// so std-only skeletons stay byte-identical to before.
	for _, alias := range sortedFilterAliases(usedFilters) {
		fmt.Fprintf(&sb, "import %s %q\n", alias, usedFilters[alias])
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
	sb.WriteString(compBuf.String())
	return sb.String(), comps, nil
}

// sortedFilterAliases returns the aliases of a usedFilters map (alias→pkgPath)
// in deterministic (sorted) order, so the skeleton's import block is stable.
func sortedFilterAliases(usedFilters map[string]string) []string {
	aliases := make([]string, 0, len(usedFilters))
	for a := range usedFilters {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	return aliases
}

// emitComponentSkeleton writes one component's probe skeleton (props struct +
// func/method signature + probe body) into sb, accumulating into usedFilters
// (alias→pkgPath) every filter package the component's probes reference — so the
// caller imports exactly those packages under those aliases.
func emitComponentSkeleton(sb *strings.Builder, c *gsxast.Component, table filterTable, propFields map[string]map[string]bool, usedFilters map[string]string, fset *token.FileSet) error {
	params, err := parseParams(c.Params)
	if err != nil {
		// Emit a minimal stub so the overall skeleton remains valid Go, keeping
		// any user GoChunk imports used. The parse error will be re-surfaced (with
		// position) by genComponent at emit time.
		emitComponentStub(sb, c, nil, true)
		return errSkipComponent
	}
	if err := checkReservedParams(params); err != nil {
		// Emit a stub that INCLUDES the props struct (keeping user-imported types
		// like gsx.Node used in the skeleton) so GoChunk imports don't spuriously
		// trigger "imported and not used". The reserved-param error will be
		// re-surfaced (with position) by genComponent at emit time.
		emitComponentStub(sb, c, params, true)
		return errSkipComponent
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
			// Recv parse failed — the receiver clause may be invalid Go; use a bare
			// function stub (no receiver) to keep the skeleton valid.
			emitComponentStub(sb, c, params, false)
			return errSkipComponent
		}
		if rerr := checkReservedRecvVar(recvVar); rerr != nil {
			emitComponentStub(sb, c, params, true)
			return errSkipComponent
		}
		propsName = recvTypeName + c.Name + "Props"
	}
	// Synthesize the implicit `Children _gsxrt.Node` slot field + `children`
	// local in lockstep with genComponent (emit.go), so skeleton and emitted
	// code agree on the props shape and the `{children}` interp type-checks.
	hasChildren := usesChildren(c.Body)
	// MIRROR emit.go: a single-root component synthesizes a fallthrough
	// `Attrs _gsxrt.Attrs` props field so the emitted props struct shape matches
	// (same gating into hasProps, same field order: params, Children, Attrs). The
	// skeleton body does NOT emit the root application (it emits probes); it only
	// needs the field present so any `_gsxp.Attrs` references / the field's
	// existence type-check identically to the emitted struct (unused is fine).
	_, hasRoot := singleRoot(c.Body)
	// MIRROR emit.go: MANUAL mode — a body referencing `attrs` forces fallthrough
	// eligibility (even a nullary method) and DISABLES auto root injection.
	manual := usesAttrs(c.Body)
	// MIRROR emit.go: a nullary method component (no params, no children) stays
	// nullary (no props struct, bare `p.Page()` call) — AUTO fallthrough is gated
	// out of that case so it does not force a props struct; manual forces it.
	hasFallthrough := (hasRoot && (c.Recv == "" || len(params) > 0 || hasChildren)) || manual
	// MIRROR emit.go: only a method component suppresses the props struct when
	// nullary; a function component always keeps its (possibly empty) props
	// struct so the child-invocation path's `<Tag>Props{}` literal type-checks.
	hasProps := c.Recv == "" || len(params) > 0 || hasChildren || hasFallthrough
	if hasProps {
		fmt.Fprintf(sb, "type %s struct {\n", propsName)
		for _, p := range params {
			fmt.Fprintf(sb, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		if hasChildren {
			sb.WriteString("\tChildren _gsxrt.Node\n")
		}
		if hasFallthrough {
			sb.WriteString("\tAttrs _gsxrt.Attrs\n")
		}
		sb.WriteString("}\n")
	}
	// Use the same reserved props-param name as the emitted code (_gsxp) so a
	// user param named `p` does not collide in the skeleton either. Emit the
	// receiver clause verbatim for a method component (its receiver var is in
	// scope, like the emitted method).
	if c.Recv != "" {
		fmt.Fprintf(sb, "func %s %s(", c.Recv, c.Name)
	} else {
		fmt.Fprintf(sb, "func %s(", c.Name)
	}
	if hasProps {
		fmt.Fprintf(sb, "_gsxp %s", propsName)
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
			fmt.Fprintf(sb, "\t%s := _gsxp.%s\n\t_ = %s\n", p.name, fieldName(p.name), p.name)
		}
	}
	if hasChildren {
		sb.WriteString("\tchildren := _gsxp.Children\n\t_ = children\n")
	}
	// MIRROR emit.go: in MANUAL mode bind the synthesized bag to `attrs` so the
	// probe type-checks the author's `{...attrs}` (probed as `_gsxgw.Spread(ctx,
	// attrs)`) and any `attrs.X()` reference identically to emitted code.
	if manual {
		sb.WriteString("\tattrs := _gsxp.Attrs\n\t_ = attrs\n")
	}
	if err := emitProbes(sb, c.Body, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
		return err
	}
	sb.WriteString("\treturn nil\n}\n")
	return nil
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
//
// usedFilters (alias→pkgPath) accumulates every filter package the probes
// reference, so the skeleton imports exactly those packages under those aliases
// — driven by the SAME lowerPipe report the emitter uses.
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup, table filterTable, propFields map[string]map[string]bool, recvVar, recvTypeName string, usedFilters map[string]string, fset *token.FileSet) error {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			probe, err := probeExpr(t.Expr, t.Stages, table, usedFilters)
			if err != nil {
				return err
			}
			emitSkeletonLine(sb, fset, t.Pos())
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
					emitSkeletonLine(sb, fset, t.Pos())
					fmt.Fprintf(sb, "_ = %s()\n", callTarget)
				} else {
					// Build the SAME props literal as the emitter via childPropsLiteral,
					// but with a typed-nil slotValue: each named-slot and Children field
					// is `_gsxrt.Node(nil)` so the literal type-checks WITHOUT the real
					// closure. The slot content (markup-attr values + children) is probed
					// SEPARATELY below, so its interps become the _gsxuse sequence in the
					// SAME order collectExprs collected them; the props-literal exprs are
					// NOT _gsxuse, so they don't perturb the k-th alignment.
					fields, err := childPropsLiteral(t, propsType, "_gsxrt", propFields, func(nodes []gsxast.Markup) (string, error) {
						return "_gsxrt.Node(nil)", nil
					})
					if err != nil {
						// childPropsLiteral returns an *attrError with the offending attr's
						// position embedded. Propagate it as-is so the batch.go sink can emit
						// a positioned diagnostic (not positionless).
						return err
					}
					emitSkeletonLine(sb, fset, t.Pos())
					fmt.Fprintf(sb, "_ = %s(%s{%s})\n", callTarget, propsType, fields)
				}
				// Probe slot content in the SAME canonical order collectExprs walks:
				// each markup-attr value (attr order) then the children.
				var probeErr error
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, propFields, recvVar, recvTypeName, usedFilters, fset)
				})
				if probeErr != nil {
					return probeErr
				}
				if err := emitProbes(sb, t.Children, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
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
					probe, err := probeExpr(ea.Expr, ea.Stages, table, usedFilters)
					if err != nil {
						probeErr = err
						return
					}
					emitSkeletonLine(sb, fset, ea.Pos())
					fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
				})
				if probeErr != nil {
					return probeErr
				}
				// Then probe each JS-attribute's @{ } interps, in attr source order —
				// collectExprs walks identically (same walkMarkupAttrs), so the k-th
				// _gsxuse maps to the k-th collected node.
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, propFields, recvVar, recvTypeName, usedFilters, fset)
				})
				if probeErr != nil {
					return probeErr
				}
				if err := emitProbes(sb, t.Children, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
					return err
				}
			}
		case *gsxast.Fragment:
			if err := emitProbes(sb, t.Children, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
				return err
			}
		case *gsxast.ForMarkup:
			fmt.Fprintf(sb, "for %s {\n", t.Clause)
			if err := emitProbes(sb, t.Body, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
				return err
			}
			sb.WriteString("}\n")
		case *gsxast.IfMarkup:
			fmt.Fprintf(sb, "if %s {\n", t.Cond)
			if err := emitProbes(sb, t.Then, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
				return err
			}
			sb.WriteString("}")
			if t.Else != nil {
				sb.WriteString(" else {\n")
				if err := emitProbes(sb, t.Else, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
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
				if err := emitProbes(sb, cc.Body, table, propFields, recvVar, recvTypeName, usedFilters, fset); err != nil {
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

// emitSkeletonLine writes a //line directive into a skeleton strings.Builder,
// mapping subsequent source to the .gsx file position, so go/types errors
// produced by the type-checker (via pkg.Fset which honors //line directives)
// resolve to .gsx file:line:col instead of the generated overlay .x.go.
// fset may be nil (e.g. in test-only callers); in that case no directive is emitted.
func emitSkeletonLine(sb *strings.Builder, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, p.Column)
}

// probeExpr returns the Go expression to probe for an interpolation / expr-attr.
// Without stages it is the trimmed seed; with stages it is the lowered pipeline
// (the SAME lowerPipe output the emitter uses), so the harvested type is the
// pipeline's RESULT type and resolution stays aligned with emission. The
// pipeline's used filter packages are merged into usedFilters so the skeleton
// imports each referenced package under its reserved alias.
func probeExpr(seed string, stages []gsxast.PipeStage, table filterTable, usedFilters map[string]string) (string, error) {
	if len(stages) == 0 {
		return strings.TrimSpace(seed), nil
	}
	lowered, used, err := lowerPipe(seed, stages, table)
	if err != nil {
		return "", err
	}
	for alias, path := range used {
		usedFilters[alias] = path
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

// isRawCSS reports whether t is the named type github.com/gsxhq/gsx.RawCSS —
// the author-vouched safe-CSS string, emitted raw in a CSS context.
func isRawCSS(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "RawCSS" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
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
			// Then each JS-attribute (e.g. x-data="… @{ x } …") @{ } interp, in
			// attr source order — emitProbes walks identically (same walkMarkupAttrs).
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				collectExprs(value, out)
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
		switch t := a.(type) {
		case *gsxast.MarkupAttr:
			fn(t.Value)
		case *gsxast.JSAttr:
			// A JS-context attribute value (e.g. x-data="{ tab: @{ tab } }") carries
			// @{ } interps that need types — yield its Segments so they are collected
			// and probed in the SAME order by collectExprs and emitProbes.
			fn(t.Segments)
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
		if p.name == "attrs" {
			return fmt.Errorf("codegen: param name %q is reserved (implicit fallthrough attributes)", p.name)
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

// emitComponentStub emits a minimal valid Go func/method stub for a component
// whose skeleton would otherwise be invalid (reserved param/recv, parse error).
// The stub keeps the skeleton valid Go and ensures user GoChunk imports are not
// spuriously flagged as "imported and not used". The body returns nil — no type
// probes are emitted; genComponent re-encounters the validation error at emit
// time and records a positioned diagnostic.
//
// params is the parsed parameter list (may be nil if parsing failed). When
// non-nil, a props struct is emitted so param type references (e.g. gsx.Node)
// in user GoChunk imports remain "used" in the skeleton.
//
// withRecv controls whether the method receiver clause is emitted (true for
// most cases; false when parseRecv itself failed and c.Recv is bad syntax).
//
// CRITICAL: the stub props struct MUST mirror the Children/Attrs field synthesis
// that emitComponentSkeleton/genComponent use — otherwise a sibling that
// instantiates the bad component WITH CHILDREN will get a spurious "unknown field
// Children" type error from the overlay, masking the real diagnostic. We use the
// SAME gating (usesChildren / singleRoot / usesAttrs) on the body so the stub
// struct shape matches what siblings reference.
func emitComponentStub(sb *strings.Builder, c *gsxast.Component, params []param, withRecv bool) {
	propsName := c.Name + "Props"
	// MIRROR emitComponentSkeleton: compute Children/Attrs gates from the body.
	hasChildren := usesChildren(c.Body)
	_, hasRoot := singleRoot(c.Body)
	manual := usesAttrs(c.Body)
	// MIRROR emitComponentSkeleton line 380: hasFallthrough gating.
	hasFallthrough := (hasRoot && (c.Recv == "" || len(params) > 0 || hasChildren)) || manual
	// MIRROR emitComponentSkeleton line 384: hasProps gating.
	// When params is nil (parse failed), treat as len(params)==0 for gating.
	hasProps := c.Recv == "" || len(params) > 0 || hasChildren || hasFallthrough
	// Track which field names the params already declare (e.g. a bad param named
	// "children" → field "Children") so we do not double-declare the synthesized
	// Children/Attrs fields and produce a "redeclared" type-error in the overlay.
	paramFields := make(map[string]bool, len(params))
	for _, p := range params {
		paramFields[fieldName(p.name)] = true
	}
	if hasProps {
		fmt.Fprintf(sb, "type %s struct {\n", propsName)
		for _, p := range params {
			fmt.Fprintf(sb, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		if hasChildren && !paramFields["Children"] {
			sb.WriteString("\tChildren _gsxrt.Node\n")
		}
		if hasFallthrough && !paramFields["Attrs"] {
			sb.WriteString("\tAttrs _gsxrt.Attrs\n")
		}
		sb.WriteString("}\n")
		if withRecv && c.Recv != "" {
			fmt.Fprintf(sb, "func %s %s(_gsxp %s) _gsxrt.Node { return nil }\n", c.Recv, c.Name, propsName)
		} else {
			fmt.Fprintf(sb, "func %s(_gsxp %s) _gsxrt.Node { return nil }\n", c.Name, propsName)
		}
	} else {
		if withRecv && c.Recv != "" {
			fmt.Fprintf(sb, "func %s %s() _gsxrt.Node { return nil }\n", c.Recv, c.Name)
		} else {
			fmt.Fprintf(sb, "func %s() _gsxrt.Node { return nil }\n", c.Name)
		}
	}
}

// fieldName maps a param name to its props struct field (first letter upper).
func fieldName(p string) string {
	if p == "" {
		return p
	}
	return strings.ToUpper(p[:1]) + p[1:]
}
