package codegen

import (
	"bytes"
	"errors"
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/cssmin"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsmin"
)

// generateFile emits the .x.go for a parsed gsx file given already-resolved
// interpolation types. It returns (nil, false) if any component failed; all
// component errors are recorded in bag (component-boundary recovery continues
// to the next component on failure, so multiple errors are always reported).
func generateFile(file *ast.File, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool, merger *ClassMergerRef) ([]byte, bool) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
	fm = fieldMatcherOrDefault(fm)
	interpTemp := 0
	// Minify the static CSS of <style> blocks. cssMin is nil for the built-in
	// safe minifier, or a custom override (e.g. tdewolff) threaded from
	// gen.WithCSSMinifier. Custom minifiers only ever receive complete, valid CSS
	// (holeless blocks); holey blocks always use the built-in hole-aware pass
	// regardless of cssMin.
	if cssMinify {
		if err := cssmin.MinifyFile(file, cssMin); err != nil {
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "codegen"})
			return nil, false
		}
	}
	// Minify the static JS of <script> blocks. jsMin is nil for the built-in
	// safe minifier (ASI-safe, newline-keeping), or a custom override threaded
	// from gen.WithJSMinifier. Only holeless <script> blocks are minified; a
	// script carrying any @{ } hole is left un-minified for safety (see jsmin).
	if jsMinify {
		if err := jsmin.MinifyFile(file, jsMin); err != nil {
			bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "codegen"})
			return nil, false
		}
	}
	imports := map[string]bool{
		"context":              true,
		"io":                   true,
		"github.com/gsxhq/gsx": true,
	}
	// Each GoChunk is split: its imports are folded into the import region (they
	// must precede all other declarations) and its non-import remainder flows
	// into the body. A single chunk may carry both — e.g. an import followed by
	// type/func decls. Aliased / dot / blank imports are kept verbatim; plain
	// imports merge into the import set so they dedupe against the runtime imports
	// the generator already needs.
	var aliased []importSpec
	// userPlainImports records the paths the user plain-imported in a GoChunk
	// (referenced by their own package name in user Go code). When such a path is
	// ALSO a filter package, the filter calls qualify it under its reserved alias
	// (_gsxf<i>) while user code still says `<pkg>.X` — so writeImports must emit
	// BOTH the reserved-alias line and the plain line (Go allows the same path
	// under different names). This mirrors the probe skeleton (analyze.go), keeping
	// emit ≡ probe.
	userPlainImports := map[string]bool{}
	mergeExpr := "gsx.DefaultClassMerge"
	if merger != nil {
		mergeExpr = classMergerAlias + "." + merger.FuncName
	}
	var body bytes.Buffer
	ok := true
	for _, d := range file.Decls {
		switch v := d.(type) {
		case *ast.GoChunk:
			specs, rest, _, err := splitChunk(v.Src)
			if err != nil {
				bag.Errorf(v.Pos(), v.End(), "invalid-syntax", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
				return nil, false
			}
			for _, s := range specs {
				if s.name == "" {
					imports[s.path] = true
					userPlainImports[s.path] = true
				} else {
					aliased = append(aliased, s)
				}
			}
			if rest != "" {
				body.WriteString(rest)
				body.WriteString("\n\n")
			}
		case *ast.Component:
			// Recovery boundary: each component emits into its OWN temp buffer.
			// On failure, the diagnostic is already in bag — skip this component's
			// output and continue to the next (report ALL components' errors).
			var cbuf bytes.Buffer
			if genComponent(&cbuf, v, resolved, table, structFields, nodeProps, attrsProps, byo, imports, &interpTemp, fset, cls, fm, bag, mergeExpr) {
				body.Write(cbuf.Bytes())
			} else {
				ok = false
			}
		}
	}

	if !ok {
		return nil, false
	}

	// Build the path→reserved-alias map for the FILTER packages, harvested from
	// the table (every entry records its owning package's alias + path). A path in
	// `imports` that is a filter package is emitted under its reserved alias; std
	// keeps _gsxstd so std-only output is byte-identical to before.
	// The class merger package (if configured) is registered here under its
	// reserved alias _gsxcm so writeImports emits `_gsxcm "<pkgPath>"` — but
	// only if the body actually contains at least one merge-site reference to
	// avoid emitting an unused import in files with no class attributes.
	filterAlias := map[string]string{}
	for _, e := range table {
		filterAlias[e.pkgPath] = e.alias
	}
	if merger != nil && bytes.Contains(body.Bytes(), []byte(classMergerAlias+".")) {
		imports[merger.PkgPath] = true
		filterAlias[merger.PkgPath] = classMergerAlias
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by gsx; DO NOT EDIT.\n\npackage %s\n\n", file.Package)
	writeImports(&b, imports, aliased, filterAlias, userPlainImports)
	b.Write(body.Bytes())
	// Merge runs of adjacent static `_gsxgw.S("…")` writes into one before
	// formatting — fewer generated lines and fewer runtime writes. Done on the AST
	// (coalesceStaticWrites), so //line directives are preserved.
	src, err := format.Source(coalesceStaticWrites(b.Bytes()))
	if err != nil {
		bag.Add(diag.Diagnostic{Severity: diag.Error, Message: fmt.Sprintf("format generated source: %s", err), Source: "codegen"})
		return nil, false
	}
	return src, true
}

// writeImports emits the generated file's import region. imports is the set of
// plain import paths the generator needs; aliased carries verbatim user GoChunk
// imports; filterAlias (path→reserved alias) names any FILTER package among
// `imports` so its import line uses the reserved alias the lowered calls
// reference (e.g. `_gsxstd "<std>"`, `_gsxf0 "<user>"`). Filter imports sort by
// alias and are emitted after the plain external imports, before user-aliased
// ones — std-only output keeps its single `_gsxstd "<std>"` line, byte-identical
// to before.
func writeImports(b *bytes.Buffer, imports map[string]bool, aliased []importSpec, filterAlias map[string]string, userPlainImports map[string]bool) {
	var std, ext []string
	type filterImp struct{ alias, path string }
	var filters []filterImp
	for imp := range imports {
		switch {
		case filterAlias[imp] != "":
			// A filter package: the lowered calls reference <alias>.<Func>; the import
			// MUST use exactly that reserved alias (collision-safe — no user symbol can
			// start with _gsx). Emit it separately, not in the plain ext loop.
			filters = append(filters, filterImp{alias: filterAlias[imp], path: imp})
			// If the user ALSO plain-imports this filter package, their Go code
			// references it by its own name (`<pkg>.X`), so emit the plain import
			// line too — alongside the reserved alias. Go permits the same path
			// under two names; this mirrors the probe skeleton.
			if userPlainImports[imp] {
				if strings.Contains(imp, ".") {
					ext = append(ext, imp)
				} else {
					std = append(std, imp)
				}
			}
		case strings.Contains(imp, "."):
			ext = append(ext, imp)
		default:
			std = append(std, imp)
		}
	}
	sort.Strings(std)
	sort.Strings(ext)
	sort.Slice(filters, func(i, j int) bool { return filters[i].alias < filters[j].alias })
	sort.Slice(aliased, func(i, j int) bool { return aliased[i].path < aliased[j].path })
	b.WriteString("import (\n")
	for _, imp := range std {
		fmt.Fprintf(b, "\t%q\n", imp)
	}
	if len(std) > 0 && (len(ext) > 0 || len(aliased) > 0 || len(filters) > 0) {
		b.WriteString("\n")
	}
	for _, imp := range ext {
		fmt.Fprintf(b, "\t%q\n", imp)
	}
	for _, f := range filters {
		fmt.Fprintf(b, "\t%s %q\n", f.alias, f.path)
	}
	for _, imp := range aliased {
		fmt.Fprintf(b, "\t%s %q\n", imp.name, imp.path)
	}
	b.WriteString(")\n\n")
}

func genComponent(b *bytes.Buffer, c *ast.Component, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	params, err := parseParams(c.Params)
	if err != nil {
		bag.Errorf(c.Pos(), c.End(), "invalid-syntax", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}
	if err := checkReservedParams(params); err != nil {
		bag.Errorf(c.Pos(), c.End(), "reserved-param", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}

	// A method component (non-empty Recv) emits a Go method whose receiver var is
	// in scope in the body (so `{p.Field}` works); its props struct (if any) is
	// named <RecvTypeName><Name>Props, and the receiver clause is emitted verbatim
	// in the signature. A free function component has no receiver: props struct
	// <Name>Props and a plain `func <Name>`.
	propsName := c.Name + "Props"
	// recvVar/recvTypeName stay "" for a function component; for a method component
	// they are threaded into genNode → genChildComponent so a dotted child tag whose
	// left == recvVar lowers to a method call.
	var recvVar, recvTypeName string
	if c.Recv != "" {
		var rerr error
		recvVar, _, recvTypeName, rerr = parseRecv(c.Recv)
		if rerr != nil {
			bag.Errorf(c.Pos(), c.End(), "invalid-recv", "%s", strings.TrimPrefix(rerr.Error(), "codegen: "))
			return false
		}
		if rerr := checkReservedRecvVar(recvVar); rerr != nil {
			bag.Errorf(c.Pos(), c.End(), "reserved-recv", "%s", strings.TrimPrefix(rerr.Error(), "codegen: "))
			return false
		}
		propsName = recvTypeName + c.Name + "Props"
	}

	// BYO (author-owns-Props): the sole non-receiver param is an author-declared
	// struct used DIRECTLY — gsx generates NO props struct and emits the real param
	// (name + type verbatim from the .gsx). The body refers to the param's fields
	// (`p.Field`) directly; there is NO {children}/attrs magic (the author exposes
	// Children/Attrs as real fields and renders them explicitly). MIRRORS the byo
	// branch in emitComponentSkeleton so emit ≡ probe.
	if _, isByo := byo.structTypeName(componentKey(c)); isByo {
		// Anchor the generated func declaration to the `component` decl position
		// so go/types (and thus LSP go-to-definition) reports the component's true
		// .gsx location, not a line drifted from the previous //line directive.
		emitLine(b, fset, c.Pos())
		if c.Recv != "" {
			fmt.Fprintf(b, "func %s %s(%s) gsx.Node {\n", c.Recv, c.Name, strings.TrimSpace(c.Params))
		} else {
			fmt.Fprintf(b, "func %s(%s) gsx.Node {\n", c.Name, strings.TrimSpace(c.Params))
		}
		b.WriteString("\treturn gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {\n")
		b.WriteString("\t\t_gsxgw := gsx.W(_gsxw)\n")
		emitNumScratch(b, c.Body, resolved)
		for _, m := range c.Body {
			if !genNode(b, m, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
		b.WriteString("\t\treturn _gsxgw.Err()\n")
		b.WriteString("\t})\n}\n\n")
		return true
	}

	// Props struct (field types are syntactic — straight from the param list).
	// When the body references `{children}`, synthesize an implicit
	// `Children gsx.Node` slot field after the param fields; the parent fills it
	// with a render closure (genChildComponent). A NULLARY component (function OR
	// method: no params, no children, no fallthrough) gets NO props struct and NO
	// _gsxp param — emitted as `func Name() gsx.Node`, called as `Name()`. A
	// component grows a props struct ONLY when it has params, uses `{children}`,
	// or uses fallthrough attrs (auto single-root or manual `{...attrs}`).
	hasChildren := usesChildren(c.Body)
	// MANUAL mode: a component whose body references the identifier `attrs` (a
	// `{...attrs}` element spread, or `attrs.X()` in an interp/expr/clause) takes
	// over fallthrough placement itself. Explicit forwarding is required: we only
	// synthesize an `Attrs gsx.Attrs` field when the author actually references
	// `attrs`; there is no implicit single-root auto-injection path.
	manual := usesAttrs(c.Body)
	hasFallthrough := manual
	hasProps := len(params) > 0 || hasChildren || hasFallthrough
	if hasProps {
		fmt.Fprintf(b, "type %s struct {\n", propsName)
		for _, p := range params {
			fmt.Fprintf(b, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		if hasChildren {
			b.WriteString("\tChildren gsx.Node\n")
		}
		if hasFallthrough {
			b.WriteString("\tAttrs gsx.Attrs\n")
		}
		fmt.Fprintf(b, "}\n\n")
	}

	// Render func/method. The only differences between function and method
	// components are the signature (receiver clause + props-struct name) and
	// whether a props struct exists; the render-closure body is identical.
	// Anchor the func declaration to the `component` decl position so go/types /
	// LSP go-to-definition reports the component's true .gsx location (not a line
	// drifted from the previous //line directive).
	emitLine(b, fset, c.Pos())
	if c.Recv != "" {
		fmt.Fprintf(b, "func %s %s(", c.Recv, c.Name)
	} else {
		fmt.Fprintf(b, "func %s(", c.Name)
	}
	if hasProps {
		fmt.Fprintf(b, "_gsxp %s", propsName)
	}
	b.WriteString(") gsx.Node {\n")

	// Bind each USED param to a same-named local so interpolation expressions can
	// be emitted verbatim. The props param, io.Writer closure param, and
	// gsx.Writer local use the reserved _gsx* namespace so a user param named
	// p/w/gw cannot collide with them. ctx stays ambient (user interpolation exprs
	// may reference it). For a method component the receiver var is already in
	// scope (it's the method receiver) and is NOT bound as a prop local.
	b.WriteString("\treturn gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {\n")
	used := usedParams(c, params)
	for _, p := range params {
		if used[p.name] {
			fmt.Fprintf(b, "\t\t%s := _gsxp.%s\n", p.name, fieldName(p.name))
		}
	}
	if hasChildren {
		// Bind the implicit slot local; the bare `{children}` interp resolves to
		// this gsx.Node and renders via the catNode path (gw.Node, nil-safe).
		b.WriteString("\t\tchildren := _gsxp.Children\n")
	}
	if manual {
		// MANUAL mode: bind the synthesized bag to a same-named local so the author's
		// `{...attrs}` element spread (emitted as `gw.Spread(ctx, attrs)`) and any
		// `attrs.X()` reference resolve. Nil-safe: a nil bag spreads/queries to
		// nothing. usesAttrs guarantees that lowering consumes this binding.
		b.WriteString("\t\tattrs := _gsxp.Attrs\n")
	}
	b.WriteString("\t\t_gsxgw := gsx.W(_gsxw)\n")
	emitNumScratch(b, c.Body, resolved)
	for _, m := range c.Body {
		if !genNode(b, m, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
			return false
		}
	}
	b.WriteString("\t\treturn _gsxgw.Err()\n")
	b.WriteString("\t})\n}\n\n")
	return true
}

// emitRootElement emits a single-root element with CALLER-WINS attribute
// fallthrough applied: the bag (`_gsxp.Attrs`) merges its class into the root's
// class (last-wins → caller-wins) and merges its style over the root's style
// (property last-wins → caller-wins), and every OTHER root attr is emitted under
// a runtime guard (`if !_gsxp.Attrs.Has(name)`) so a bag entry of the same name
// SHADOWS it — the guarded attr is skipped and the bag's value spreads instead
// (caller-wins, D5 flipped). Each guarded attr keeps its own context escaper
// (gw.URL / gw.JSValAttr / gw.AttrValue …) since its body is exactly the normal
// emitAttr output. The root's CHILDREN render via normal genNode — the bag does
// NOT propagate. When the bag is nil/empty, every guard is true (Has is nil-safe),
// ClassMerged/StyleMerged add nothing, and Spread(empty) writes nothing — so the
// output is byte-equivalent to the normal Element path.
//
// A void root cannot receive fallthrough (it has no place for it to matter beyond
// attrs, but void roots ARE handled: class/style-merge + guarded attrs + spread
// then `/>`).
func emitRootElement(b *bytes.Buffer, el *ast.Element, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	emitLine(b, fset, el.Pos())
	emitS(b, "<"+el.Tag)
	// AUTO mode: there is no author `{...attrs}`, so the bag spreads at the END and
	// every root attr is overridable. splitIdx == len(el.Attrs) means "all attrs
	// precede the (synthetic) spread" → all guarded.
	if !emitFallthroughAttrs(b, el.Tag, el.Attrs, len(el.Attrs), resolved, table, imports, interpTemp, cls, bag, mergeExpr, "_gsxp.Attrs") {
		return false
	}
	if el.Void {
		emitS(b, "/>")
		return true
	}
	emitS(b, ">")
	if strings.EqualFold(el.Tag, "style") {
		for _, c := range el.Children {
			if !genStyleChild(b, c, resolved, table, imports, interpTemp, bag) {
				return false
			}
		}
	} else if strings.EqualFold(el.Tag, "script") {
		for _, c := range el.Children {
			if !genScriptChild(b, c, resolved, table, imports, interpTemp, bag) {
				return false
			}
		}
	} else {
		for _, c := range el.Children {
			if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
	}
	emitS(b, "</"+el.Tag+">")
	return true
}

// emitFallthroughAttrs emits the caller-wins attribute section (between `<tag`
// and the closing `>` / `/>`) shared by AUTO mode (emitRootElement) and MANUAL
// mode (emitManualSpreadElement). splitIdx is the index in attrs at which the
// bag spreads:
//   - AUTO mode: splitIdx == len(attrs) — the (synthetic) bag spread is at the
//     end, so EVERY scalar root attr is OVERRIDABLE (guarded `if !Has(name)`,
//     caller-wins).
//   - MANUAL mode: splitIdx is the position of the author's `{...attrs}` — scalar
//     attrs BEFORE it are overridable (guarded); scalar attrs AFTER it are FORCED
//     (emitted UNGUARDED so the root always wins) and their names are excluded
//     from the bag spread so a same-named bag entry can never emit (root wins).
//
// class/style are positional-exempt: wherever they appear they MERGE caller-last
// (ClassMerged / StyleMerged), emitted once at the spread position. The author's
// `{...attrs}` SpreadAttr itself (when present at splitIdx) is consumed here, not
// emitted via emitAttr.
func emitFallthroughAttrs(b *bytes.Buffer, tag string, attrs []ast.Attr, splitIdx int, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr, bagExpr string) bool {
	// Find a composed/static class attr to merge the bag's class into, and a
	// composed/static style attr whose declarations the bag's style merges over.
	var classAttr *ast.ClassAttr    // composed class={ … }
	var staticClass *ast.StaticAttr // static class="x"
	var styleAttr *ast.ClassAttr    // composed style={ … } (CtxCSS ClassAttr)
	var staticStyle *ast.StaticAttr // static style="x"
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.ClassAttr:
			switch t.Name {
			case "class":
				classAttr = t
			case "style":
				styleAttr = t
			}
		case *ast.StaticAttr:
			switch t.Name {
			case "class":
				staticClass = t
			case "style":
				staticStyle = t
			}
		}
	}
	// forcedNames collects the names of post-spread scalar attrs (MANUAL mode) so
	// the bag spread drops them — the unguarded root emit then wins.
	var forcedNames []string

	// emitScalar emits one non-class/style attr, either guarded (overridable) or
	// unguarded (forced).
	emitScalar := func(a ast.Attr, guarded bool) bool {
		name, ok := rootAttrName(a)
		if !ok {
			// No single static name (Cond) — no caller-wins shadow target; emit
			// unguarded. (A SpreadAttr at the split position is consumed below, not
			// here.)
			return emitAttr(b, tag, attrs, a, resolved, table, imports, interpTemp, cls, bag, mergeExpr)
		}
		if !guarded {
			forcedNames = append(forcedNames, name)
			return emitAttr(b, tag, attrs, a, resolved, table, imports, interpTemp, cls, bag, mergeExpr)
		}
		fmt.Fprintf(b, "\t\tif !%s.Has(%s) {\n", bagExpr, strconv.Quote(name))
		if !emitAttr(b, tag, attrs, a, resolved, table, imports, interpTemp, cls, bag, mergeExpr) {
			return false
		}
		b.WriteString("\t\t}\n")
		return true
	}

	// emitSpread emits the class merge, style merge, then the bag spread (dropping
	// class/style + any forced names). Called once, at the split position.
	emitSpread := func() bool {
		// If the root had NO class attr at all, emit a merged class in attr position —
		// writes class only when the bag contributes a non-empty token set.
		if classAttr == nil && staticClass == nil {
			fmt.Fprintf(b, "\t\t_gsxgw.ClassMerged(%s, %s.Class())\n", mergeExpr, bagExpr)
		}
		// Style: when the caller set a `style`, merge it OVER the root's style
		// property-last-wins (StyleMerged, caller-wins). When the caller did NOT set a
		// style, emit the root's own style via its normal context path UNCHANGED — this
		// preserves the composable-style CSS sanitizer (gw.Style → the ZgotmplZ failsafe
		// for an injection, and intra-style duplicate properties) which StyleMerged's
		// `prop: value` parser would lose (it drops colon-less fragments and dedupes
		// properties). The empty-bag case takes the else branch → byte-identical output.
		if styleAttr != nil || staticStyle != nil {
			styleStr, styleParts, ok := rootStyleString(b, styleAttr, staticStyle, table, imports, interpTemp, bag, resolved)
			if !ok {
				return false
			}
			fmt.Fprintf(b, "\t\tif %s.Has(\"style\") {\n", bagExpr)
			fmt.Fprintf(b, "\t\t\t_gsxgw.StyleMerged(%s, %s.Style())\n", styleStr, bagExpr)
			b.WriteString("\t\t} else {\n")
			if staticStyle != nil {
				if !emitAttr(b, tag, attrs, staticStyle, resolved, table, imports, interpTemp, cls, bag, mergeExpr) {
					return false
				}
			} else {
				fmt.Fprintf(b, "\t\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+styleAttr.Name+`="`))
				fmt.Fprintf(b, "\t\t\t_gsxgw.Style(%s)\n", strings.Join(styleParts, ", "))
				fmt.Fprintf(b, "\t\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
			}
			b.WriteString("\t\t}\n")
		} else {
			// No root style: emit StyleMerged so a caller-only style still appears (it is
			// a no-op when the bag has no style either).
			fmt.Fprintf(b, "\t\t_gsxgw.StyleMerged(\"\", %s.Style())\n", bagExpr)
		}
		// Spread the rest of the bag, dropping class/style (both merged above) plus
		// any forced names (excluded so the unguarded root emit wins — caller can't
		// override a post-spread attr). Every other bag attr spreads, including one
		// that shadows a guarded (overridable) root attr (which was then skipped).
		without := append([]string{"class", "style"}, forcedNames...)
		quoted := make([]string, len(without))
		for i, n := range without {
			quoted[i] = strconv.Quote(n)
		}
		fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s.Without(%s))\n", bagExpr, strings.Join(quoted, ", "))
		return true
	}

	// In MANUAL mode the author's `{...attrs}` lives at splitIdx; collecting the
	// forced names requires emitting post-spread scalars BEFORE the spread call (so
	// forcedNames is populated). We therefore emit in three phases:
	//   1. overridable scalars (idx < splitIdx, guarded)
	//   2. forced scalars (idx > splitIdx, unguarded, names collected)
	//   3. the spread (class/style merge + bag, dropping the forced names)
	// then re-walk to interleave nothing else — class/style are handled by phase 3.
	// Emission order in the output: overridable scalars, then the spread, then the
	// forced scalars — matching `<div a {...attrs} b/>` (a, bag, b). We achieve that
	// by buffering the forced-scalar bytes.
	var forcedBuf bytes.Buffer
	for i, a := range attrs {
		// class is merged in place here (emitRoot{Composed,Static}Class); style is
		// skipped here and merged by emitSpread (phase 3). Both ignore spread position.
		switch t := a.(type) {
		case *ast.ClassAttr:
			if t == classAttr {
				if !emitRootComposedClass(b, t, table, imports, interpTemp, bag, mergeExpr, bagExpr, resolved) {
					return false
				}
				continue
			}
			if t == styleAttr {
				continue
			}
		case *ast.StaticAttr:
			if t == staticClass {
				emitRootStaticClass(b, t, mergeExpr, bagExpr)
				continue
			}
			if t == staticStyle {
				continue
			}
		case *ast.SpreadAttr:
			// The bag spread at the split position is consumed here. A non-bag spread
			// (handled by the caller's detection) never reaches this helper at splitIdx;
			// a stray SpreadAttr at any other index is emitted inline (unchanged).
			if i == splitIdx {
				continue
			}
			spreadExpr, ok := spreadAttrExpr(t, table, imports, bag)
			if !ok {
				return false
			}
			fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", spreadExpr)
			continue
		}
		if i < splitIdx {
			if !emitScalar(a, true) {
				return false
			}
			continue
		}
		// Post-spread: forced. Buffer it (emitted after the spread) but collect its
		// name now via emitScalar against forcedBuf.
		saved := b
		b = &forcedBuf
		okScalar := emitScalar(a, false)
		b = saved
		if !okScalar {
			return false
		}
	}
	// Phase 3: the spread (now that forcedNames is fully populated), then flush the
	// buffered forced scalars after it.
	if !emitSpread() {
		return false
	}
	b.Write(forcedBuf.Bytes())
	return true
}

// emitManualSpreadElement emits a non-component element that carries the author's
// `{...attrs}` bag spread (MANUAL fallthrough), applying positional precedence:
// root attrs before the spread are caller-overridable, attrs after are forced
// (root wins). splitIdx is the bag SpreadAttr's index in el.Attrs (guaranteed
// unique by the caller).
func emitManualSpreadElement(b *bytes.Buffer, el *ast.Element, splitIdx int, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	emitS(b, "<"+el.Tag)
	if !emitFallthroughAttrs(b, el.Tag, el.Attrs, splitIdx, resolved, table, imports, interpTemp, cls, bag, mergeExpr, "attrs") {
		return false
	}
	if el.Void {
		emitS(b, "/>")
		return true
	}
	emitS(b, ">")
	if strings.EqualFold(el.Tag, "style") {
		for _, c := range el.Children {
			if !genStyleChild(b, c, resolved, table, imports, interpTemp, bag) {
				return false
			}
		}
	} else if strings.EqualFold(el.Tag, "script") {
		for _, c := range el.Children {
			if !genScriptChild(b, c, resolved, table, imports, interpTemp, bag) {
				return false
			}
		}
	} else {
		for _, c := range el.Children {
			if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
	}
	emitS(b, "</"+el.Tag+">")
	return true
}

// bagSpreadIndex returns the index of the author's `{...attrs}` bag spread in
// attrs (a SpreadAttr whose trimmed Expr is the bound bag name "attrs"), whether
// one was found, and an error if more than one is present (precedence ambiguous).
// A non-bag spread (`{...someOtherExpr}`) is ignored — it keeps its inline emit.
func bagSpreadIndex(attrs []ast.Attr) (int, bool, error) {
	idx, found := -1, false
	for i, a := range attrs {
		s, ok := a.(*ast.SpreadAttr)
		if !ok || strings.TrimSpace(s.Expr) != "attrs" {
			continue
		}
		if found {
			return 0, false, fmt.Errorf("codegen: more than one { attrs... } spread on an element; precedence is ambiguous")
		}
		idx, found = i, true
	}
	return idx, found, nil
}

// emitRootComposedClass emits a composed `class={ … }` merged with the bag's
// class: ` class="` + gw.Class(<existing parts…>, gsx.Class(attrs.Class()))
// + `"`. Mirrors emitClassAttr's part lowering, appending the bag class as a
// final unconditional part so it merges/dedupes through the merge func.
func emitRootComposedClass(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag, mergeExpr, bagExpr string, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, interpTemp, bag, resolved, false)
	if !ok {
		return false
	}
	parts = append(parts, fmt.Sprintf("gsx.Class(%s.Class())", bagExpr))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s)\n", mergeExpr, strings.Join(parts, ", "))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// emitRootStaticClass emits a static `class="x"` merged with the bag's class:
// ` class="` + gw.Class(gsx.Class("x"), gsx.Class(attrs.Class())) + `"`.
func emitRootStaticClass(b *bytes.Buffer, a *ast.StaticAttr, mergeExpr, bagExpr string) {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, gsx.Class(%s), gsx.Class(%s.Class()))\n", mergeExpr, strconv.Quote(a.Value), bagExpr)
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
}

// rootAttrName returns the single static attribute name of a non-class/style root
// attr that the caller-wins guard brackets. Static/bool/expr/embedded attrs each have
// one name; Spread/Cond attrs have none (ok=false), so they emit unguarded.
func rootAttrName(a ast.Attr) (string, bool) {
	switch t := a.(type) {
	case *ast.StaticAttr:
		return t.Name, true
	case *ast.BoolAttr:
		return t.Name, true
	case *ast.ExprAttr:
		return t.Name, true
	case *ast.EmbeddedAttr:
		return t.Name, true
	}
	return "", false
}

// rootStyleString returns the Go expression for the root element's own style
// declarations, passed as StyleMerged's first arg (the bag's Style() is second,
// caller-wins). For a static `style="x"` it is the quoted literal; for a composed
// `style={ … }` (a CtxCSS ClassAttr) it is a gsx.StyleString(parts…) call mirroring
// emitStyleAttr's part lowering (string-literal parts trusted, dynamic parts
// CSS-value-filtered). With no style attr it is the empty string literal.
// b and interpTemp are needed when any part is a value-form CF (if/switch): the
// hoisted var+switch statements are written to b before the StyleMerged call.
// resolved maps each *ast.ValueArm to its harvest type for (T, error) unwrap.
func rootStyleString(b *bytes.Buffer, styleAttr *ast.ClassAttr, staticStyle *ast.StaticAttr, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type) (string, []string, bool) {
	switch {
	case staticStyle != nil:
		return strconv.Quote(staticStyle.Value), nil, true
	case styleAttr != nil:
		parts, ok := composedParts(b, styleAttr, table, imports, interpTemp, bag, resolved, true)
		if !ok {
			return "", nil, false
		}
		return "gsx.StyleString(" + strings.Join(parts, ", ") + ")", parts, true
	default:
		return `""`, nil, true
	}
}

// genNode emits one markup node. recvVar/recvTypeName are the enclosing
// component's receiver var + type name (empty for a function component); they
// thread down to genChildComponent for the method-vs-package disambiguation of a
// dotted child-component tag.
func genNode(b *bytes.Buffer, n ast.Markup, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	switch t := n.(type) {
	case *ast.Text:
		emitS(b, t.Value)
	case *ast.Doctype:
		// Renders verbatim — Text holds the full `<!DOCTYPE …>` source.
		emitS(b, t.Text)
	case *ast.HTMLComment:
		// <!-- text --> renders verbatim (spec: HTML comments pass through). Text is
		// the literal between the delimiters and carries no interpolation, so it needs
		// no escaping. But it MUST NOT contain a comment-close sequence or it would
		// break out of the comment in a browser: `-->` (comment-end state) and `--!>`
		// (HTML5 comment-end-bang state, §13.2.5.51) both close a comment. The parser
		// stops at the first `-->` so that can only arrive via a hand-built AST, but
		// `--!>` DOES survive the parser (it isn't `-->`), so this guard is load-bearing.
		if strings.Contains(t.Text, "-->") || strings.Contains(t.Text, "--!>") {
			bag.Errorf(t.Pos(), t.End(), "invalid-comment", "HTML comment text contains a comment-close sequence (`-->` or `--!>`); it would break out of the comment")
			return false
		}
		emitS(b, "<!--"+t.Text+"-->")
	case *ast.Element:
		emitLine(b, fset, t.Pos())
		if isComponentTag(t.Tag) {
			return genChildComponent(b, t, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		}
		// MANUAL fallthrough: an element carrying the author's `{...attrs}` bag spread
		// gets position-aware precedence (pre-spread overridable, post-spread forced).
		// Detection is the trimmed SpreadAttr Expr == bound bag name "attrs"; a non-bag
		// spread keeps its inline emit via the normal attr loop below.
		if splitIdx, found, err := bagSpreadIndex(t.Attrs); err != nil {
			bag.Errorf(t.Pos(), t.End(), "attr-fallthrough", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		} else if found {
			return emitManualSpreadElement(b, t, splitIdx, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		}
		emitS(b, "<"+t.Tag)
		for _, a := range t.Attrs {
			if !emitAttr(b, t.Tag, t.Attrs, a, resolved, table, imports, interpTemp, cls, bag, mergeExpr) {
				return false
			}
		}
		if t.Void {
			emitS(b, "/>")
			return true
		}
		emitS(b, ">")
		if strings.EqualFold(t.Tag, "style") {
			for _, c := range t.Children {
				if !genStyleChild(b, c, resolved, table, imports, interpTemp, bag) {
					return false
				}
			}
		} else if strings.EqualFold(t.Tag, "script") {
			for _, c := range t.Children {
				if !genScriptChild(b, c, resolved, table, imports, interpTemp, bag) {
					return false
				}
			}
		} else {
			for _, c := range t.Children {
				if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
					return false
				}
			}
		}
		emitS(b, "</"+t.Tag+">")
	case *ast.Interp:
		return genInterp(b, t, resolved, table, imports, interpTemp, fset, bag)
	case *ast.Fragment:
		for _, c := range t.Children {
			if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
	case *ast.ForMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "for %s {\n", t.Clause)
		for _, c := range t.Body {
			if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
		b.WriteString("}\n")
	case *ast.IfMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "if %s {\n", t.Cond)
		for _, c := range t.Then {
			if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
		b.WriteString("}")
		if t.Else != nil {
			b.WriteString(" else {\n")
			for _, c := range t.Else {
				if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
					return false
				}
			}
			b.WriteString("}")
		}
		b.WriteString("\n")
	case *ast.SwitchMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "switch %s {\n", t.Tag)
		for _, cc := range t.Cases {
			if cc.Default {
				b.WriteString("default:\n")
			} else {
				fmt.Fprintf(b, "case %s:\n", cc.List)
			}
			for _, c := range cc.Body {
				if !genNode(b, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
					return false
				}
			}
		}
		b.WriteString("}\n")
	case *ast.GoBlock:
		emitLine(b, fset, t.Pos())
		b.WriteString(t.Code)
		b.WriteString("\n")
	default:
		bag.Errorf(n.Pos(), n.End(), "unsupported-node", "unsupported markup node %T", n)
		return false
	}
	return true
}

// genInterp emits the type-aware writer call for an interpolation. The type comes
// from the go/types resolution pass; the expression is emitted verbatim (params
// are in scope as locals).
func genInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, fset *token.FileSet, bag *diag.Bag) bool {
	emitLine(b, fset, n.Pos())
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		// Lower the pipeline to nested filter calls — the SAME lowerPipe output the
		// probe used, so resolved[n] is already the pipeline's RESULT type. Record
		// each used filter package path so writeImports emits it under its alias.
		// The lowered expr then falls through to the SAME (T, error) auto-unwrap as
		// a bare interpolation, so a seed-first filter returning (R, error) unwraps
		// exactly like any other error-returning value.
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table)
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, path := range usedPkgs {
			imports[path] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		// v, err := expr; if err != nil { return err }; then render v by its type.
		tmp := hoistTuple(b, expr, interpTemp)
		return emitRender(b, tmp, elemT, imports, n, bag)
	}
	return emitRender(b, expr, t, imports, n, bag)
}

// tupleUnwrapType reports whether t is a (T, error) tuple, returning T. Any other
// tuple shape is not unwrappable (callers emit the "only (T, error)" diagnostic).
func tupleUnwrapType(t types.Type) (types.Type, bool) {
	tup, ok := t.(*types.Tuple)
	if !ok || tup.Len() != 2 || tup.At(1).Type().String() != "error" {
		return nil, false
	}
	return tup.At(0).Type(), true
}

// hoistTuple emits `tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`
// and returns the temp name. interpTemp is the shared per-component counter, so
// temps are unique across all unwrap sites and `return _gsxerr` binds to the
// enclosing func/closure.
func hoistTuple(b *bytes.Buffer, expr string, interpTemp *int) string {
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n\t\tif _gsxerr != nil {\n\t\t\treturn _gsxerr\n\t\t}\n", tmp, expr)
	return tmp
}

// hoistValueCF emits `var _gsxvN string; <if|switch> { … _gsxvN = <arm> … }`
// before the class/style part list and returns the temp name. style=true wraps
// each arm value with styleDeclExpr (CSS-value filtering for dynamic arms).
// resolved maps each *ast.ValueArm to its harvest type; when an arm's type is
// a (T, error) tuple, armExpr calls hoistTuple to emit the unwrap inline.
func hoistValueCF(b *bytes.Buffer, cf *ast.ValueCF, table filterTable, imports map[string]bool, interpTemp *int, style bool, bag *diag.Bag, resolved map[ast.Node]types.Type) (string, bool) {
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\tvar %s string\n", tmp)
	armExpr := func(a *ast.ValueArm) (string, bool) {
		expr, used, err := lowerClassPartSeed(ast.ClassPart{Expr: a.Expr, Stages: a.Stages}, table)
		if err != nil {
			bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return "", false
		}
		for _, path := range used {
			imports[path] = true
		}
		// If the arm's harvest type is a (T, error) tuple, unwrap it inline.
		// hoistTuple writes `_gsxvN, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`
		// into b at this point — which is AFTER the if/case label and BEFORE the
		// _gsxvN = ... assignment, so the hoist lands inside the correct block.
		if t := resolved[a]; t != nil {
			if _, isTuple := t.(*types.Tuple); isTuple {
				if _, ok := tupleUnwrapType(t); !ok {
					kind := "class"
					if style {
						kind = "style"
					}
					bag.Errorf(a.Pos(), a.End(), "invalid-tuple", "%s value-form arm %q returns %s; only (T, error) is supported", kind, a.Expr, t)
					return "", false
				}
				expr = hoistTuple(b, expr, interpTemp)
			}
		}
		if style {
			expr = styleDeclExpr(expr, len(a.Stages) > 0)
		}
		return expr, true
	}
	if cf.If != nil {
		return tmp, emitValueIf(b, cf.If, tmp, armExpr)
	}
	return tmp, emitValueSwitch(b, cf.Switch, tmp, armExpr)
}

// emitValueIf emits an `if … { _gsxvN = arm } [else if … | else { … }]` block.
// armExpr is called AFTER writing the if/else-if header so any hoist statements
// it writes (e.g. for tuple unwrap) land inside the block, before the assignment.
func emitValueIf(b *bytes.Buffer, vi *ast.ValueIf, tmp string, armExpr func(*ast.ValueArm) (string, bool)) bool {
	fmt.Fprintf(b, "\t\tif %s {\n", vi.Cond)
	e, ok := armExpr(vi.Then)
	if !ok {
		return false
	}
	fmt.Fprintf(b, "\t\t\t%s = %s\n\t\t}", tmp, e)
	switch {
	case vi.ElseIf != nil:
		b.WriteString(" else ")
		if !emitValueIf(b, vi.ElseIf, tmp, armExpr) {
			return false
		}
	case vi.Else != nil:
		b.WriteString(" else {\n")
		ee, ok := armExpr(vi.Else)
		if !ok {
			return false
		}
		fmt.Fprintf(b, "\t\t\t%s = %s\n\t\t}", tmp, ee)
	}
	b.WriteString("\n")
	return true
}

func emitValueSwitch(b *bytes.Buffer, vs *ast.ValueSwitch, tmp string, armExpr func(*ast.ValueArm) (string, bool)) bool {
	fmt.Fprintf(b, "\t\tswitch %s {\n", vs.Tag)
	for _, c := range vs.Cases {
		if c.Default {
			b.WriteString("\t\tdefault:\n")
		} else {
			fmt.Fprintf(b, "\t\tcase %s:\n", c.List)
		}
		e, ok := armExpr(c.Value)
		if !ok {
			return false
		}
		fmt.Fprintf(b, "\t\t\t%s = %s\n", tmp, e)
	}
	b.WriteString("\t\t}\n")
	return true
}

// emitLine writes a //line directive mapping subsequent output to the gsx node's
// source position, so Go compiler errors point at the .gsx file. The directive
// must begin at column 1; it is written at the start of a line (the preceding
// writes always end in "\n") with no leading indentation. go/format special-cases
// //line comments and preserves them verbatim.
func emitLine(b *bytes.Buffer, fset *token.FileSet, pos token.Pos) {
	p := fset.Position(pos)
	fmt.Fprintf(b, "//line %s:%d:%d\n", filepath.Base(p.Filename), p.Line, p.Column)
}

// emitRender writes the type-aware writer call for a single renderable value
// named by expr (a verbatim expression or a temp identifier) and its type t.
// n is the AST node for positioning any error diagnostic.
func emitRender(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool, n ast.Node, bag *diag.Bag) bool {
	switch classify(t) {
	case catString:
		fmt.Fprintf(b, "\t\t_gsxgw.Text(string(%s))\n", expr)
	case catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.Text(string(%s))\n", expr)
	case catInt:
		// Format into the per-render scratch buffer and write the digit bytes
		// directly — no string allocation, no escaping (digits are always safe).
		fmt.Fprintf(b, "\t\t_gsxgw.IntInto(_gsxnum[:], int64(%s))\n", expr)
	case catUint:
		fmt.Fprintf(b, "\t\t_gsxgw.UintInto(_gsxnum[:], uint64(%s))\n", expr)
	case catFloat:
		fmt.Fprintf(b, "\t\t_gsxgw.FloatInto(_gsxnum[:], float64(%s))\n", expr)
	case catBool:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.Text(strconv.FormatBool(bool(%s)))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.Text((%s).String())\n", expr)
	case catNode:
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s)\n", expr)
	case catNodeSlice:
		fmt.Fprintf(b, "\t\tfor _, _gsxn := range %s {\n\t\t\t_gsxgw.Node(ctx, _gsxn)\n\t\t}\n", expr)
	default:
		bag.Errorf(n.Pos(), n.End(), "unrenderable", "interpolation %q has type %s; not a renderable type", expr, t)
		return false
	}
	return true
}

// emitNumScratch declares the per-render numeric scratch buffer (var _gsxnum) at
// the head of a render body, but only when this scope directly interpolates an
// integer/uint/float (the cases that emit gw.IntInto/UintInto/FloatInto). A
// component with no numeric interpolation gets no declaration.
func emitNumScratch(b *bytes.Buffer, nodes []ast.Markup, resolved map[ast.Node]types.Type) {
	if scopeUsesNumeric(nodes, resolved) {
		b.WriteString("\t\tvar _gsxnum [32]byte\n")
	}
}

// scopeUsesNumeric reports whether any text interpolation rendered directly in
// THIS scope has an integer/uint/float type. It recurses through same-scope
// constructs (non-component elements, fragments, control flow) but stops at:
//   - child components — their slots render in their own closure scope, which
//     declares its own scratch buffer (see emitSlotClosure);
//   - <style>/<script> — those interpolate via the CSS/JS writers, not the
//     numeric text path.
//
// Mirrors genNode's traversal and genInterp's render-type resolution, so it
// agrees exactly with where emitRender emits _gsxnum. A mismatch would surface
// immediately as a compile error (an unused or undefined _gsxnum) in the corpus.
func scopeUsesNumeric(nodes []ast.Markup, resolved map[ast.Node]types.Type) bool {
	for _, n := range nodes {
		switch t := n.(type) {
		case *ast.Interp:
			if interpIsNumeric(t, resolved) {
				return true
			}
		case *ast.Element:
			if isComponentTag(t.Tag) || strings.EqualFold(t.Tag, "style") || strings.EqualFold(t.Tag, "script") {
				continue
			}
			if scopeUsesNumeric(t.Children, resolved) {
				return true
			}
		case *ast.Fragment:
			if scopeUsesNumeric(t.Children, resolved) {
				return true
			}
		case *ast.ForMarkup:
			if scopeUsesNumeric(t.Body, resolved) {
				return true
			}
		case *ast.IfMarkup:
			if scopeUsesNumeric(t.Then, resolved) || scopeUsesNumeric(t.Else, resolved) {
				return true
			}
		case *ast.SwitchMarkup:
			for _, cc := range t.Cases {
				if scopeUsesNumeric(cc.Body, resolved) {
					return true
				}
			}
		}
	}
	return false
}

// interpIsNumeric reports whether interp n renders as an int/uint/float (the same
// classification emitRender uses to pick gw.IntInto/UintInto/FloatInto),
// unwrapping a (T, error) tuple like genInterp does.
func interpIsNumeric(n *ast.Interp, resolved map[ast.Node]types.Type) bool {
	t, ok := resolved[n]
	if !ok || t == nil {
		return false
	}
	if tup, ok := t.(*types.Tuple); ok {
		// Mirror genInterp's (T, error) unwrap exactly; any other tuple shape is
		// rejected there with a diagnostic, so it never reaches numeric emission.
		if tup.Len() != 2 || tup.At(1).Type().String() != "error" {
			return false
		}
		t = tup.At(0).Type()
	}
	switch classify(t) {
	case catInt, catUint, catFloat:
		return true
	}
	return false
}

func emitS(b *bytes.Buffer, s string) {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(s))
}

// genStyleChild emits one child of a <style> element. Text is raw CSS (verbatim);
// an Interp is rendered in CSS context (auto-sanitized). <style> bodies contain
// only Text and @{ } interps (parser guarantee).
func genStyleChild(b *bytes.Buffer, n ast.Markup, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	switch t := n.(type) {
	case *ast.Text:
		emitS(b, t.Value)
		return true
	case *ast.Interp:
		return emitCSSInterp(b, t, resolved, table, imports, interpTemp, bag)
	default:
		bag.Errorf(n.Pos(), n.End(), "unsupported-style-node", "<style> body may contain only text and @{ } interpolations, got %T", n)
		return false
	}
}

// emitCSSInterp renders a <style> interpolation value in CSS block context.
func emitCSSInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table)
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of <style> interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "<style> interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		tmp := hoistTuple(b, expr, interpTemp)
		return emitRenderCSS(b, tmp, elemT, imports, n, bag)
	}
	return emitRenderCSS(b, expr, t, imports, n, bag)
}

// emitRenderCSS writes a value in CSS block context (inside <style>): RawCSS and
// numbers are emitted raw (safe by construction); strings/Stringers go through
// gw.CSS (the value-filter). n is the AST node for positioning any error diagnostic.
func emitRenderCSS(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool, n ast.Node, bag *diag.Bag) bool {
	if isRawCSS(t) {
		fmt.Fprintf(b, "\t\t_gsxgw.S(string(%s))\n", expr)
		return true
	}
	switch classify(t) {
	case catInt:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.S(strconv.FormatInt(int64(%s), 10))\n", expr)
	case catUint:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.S(strconv.FormatUint(uint64(%s), 10))\n", expr)
	case catFloat:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.S(strconv.FormatFloat(float64(%s), 'g', -1, 64))\n", expr)
	case catString, catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.CSS(string(%s))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.CSS((%s).String())\n", expr)
	default:
		bag.Errorf(n.Pos(), n.End(), "unrenderable-css", "value of type %s not renderable in CSS context (need string/number/Stringer or gsx.RawCSS)", t)
		return false
	}
	return true
}

// genScriptChild emits one child of a <script> element. Text is raw JS
// (verbatim); an Interp is rendered through the JS escaper selected by its
// JSCtx (set by internal/jsx). Comment-context holes were already un-split to
// Text by jsx.ResolveScripts, so they arrive here as Text.
func genScriptChild(b *bytes.Buffer, n ast.Markup, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	switch t := n.(type) {
	case *ast.Text:
		emitS(b, t.Value)
		return true
	case *ast.Interp:
		return emitJSInterp(b, t, resolved, table, imports, interpTemp, bag)
	default:
		bag.Errorf(n.Pos(), n.End(), "unsupported-script-node", "<script> body may contain only text and @{ } interpolations, got %T", n)
		return false
	}
}

// emitJSInterp renders a <script> interpolation value through the runtime JS
// escaper chosen by its JSCtx. It mirrors emitCSSInterp's pipeline-stage handling
// and (T, error) tuple auto-unwrap.
func emitJSInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table)
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of <script> interpolation %q", n.Expr)
		return false
	}
	// Unwrap (T, error) exactly like emitCSSInterp.
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "<script> interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		tmp := hoistTuple(b, expr, interpTemp)
		return emitJSValue(b, n.JSCtx, tmp, elemT, n, bag)
	}
	return emitJSValue(b, n.JSCtx, expr, t, n, bag)
}

// emitJSValue selects the runtime JS escaper by JS context. Value context goes
// through JSVal(any) (JSON-encode; gsx.RawJS passthrough is handled at runtime);
// string/template/regexp contexts go through the string-taking escapers.
// n is the AST node for positioning any error diagnostic.
func emitJSValue(b *bytes.Buffer, ctx ast.JSCtx, expr string, t types.Type, n ast.Node, bag *diag.Bag) bool {
	switch ctx {
	case ast.JSCtxValue:
		// JSVal accepts any (JSON-encode); gsx.RawJS passthrough handled at runtime.
		// No type constraint — numbers, structs, slices, maps all JSON-encode.
		fmt.Fprintf(b, "\t\t_gsxgw.JSVal(%s)\n", expr)
		return true
	case ast.JSCtxString:
		return emitJSString(b, "JSStr", expr, t, n, bag)
	case ast.JSCtxTemplate:
		return emitJSString(b, "JSTmpl", expr, t, n, bag)
	case ast.JSCtxRegexp:
		return emitJSString(b, "JSRegexp", expr, t, n, bag)
	default:
		bag.Errorf(n.Pos(), n.End(), "unsafe-js-context", "<script> interpolation %q has no JS context (internal error: ResolveScripts not run?)", expr)
		return false
	}
}

// emitJSString emits a string-context JS escaper call (JSStr/JSTmpl/JSRegexp),
// which take a Go string. The value must be string-like.
// n is the AST node for positioning any error diagnostic.
func emitJSString(b *bytes.Buffer, method, expr string, t types.Type, n ast.Node, bag *diag.Bag) bool {
	switch classify(t) {
	case catString, catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.%s(string(%s))\n", method, expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.%s((%s).String())\n", method, expr)
	default:
		bag.Errorf(n.Pos(), n.End(), "unrenderable-js", "value of type %s not renderable in a JS string/template/regex context (need string or Stringer)", t)
		return false
	}
	return true
}

// spreadAttrExpr returns the lowered Go expression for a `{ expr... }` spread/
// splat subject: its trimmed seed when no `|>` pipeline is present, or the nested
// filter-call lowering (the SAME lowerPipe every other value context uses) when
// Stages is present. Lowered filter packages are folded into the caller's import
// set. A lowering failure (unknown filter) is positioned at the SpreadAttr via
// the bag with code "unresolved-pipeline" and ok=false.
func spreadAttrExpr(a *ast.SpreadAttr, table filterTable, imports map[string]bool, bag *diag.Bag) (string, bool) {
	expr := strings.TrimSpace(a.Expr)
	if len(a.Stages) == 0 {
		return expr, true
	}
	lowered, usedPkgs, err := lowerPipe(a.Expr, a.Stages, table)
	if err != nil {
		bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return "", false
	}
	for _, path := range usedPkgs {
		imports[path] = true
	}
	return lowered, true
}

// emitAttr emits one element attribute. Static values are escaped at codegen and
// always double-quoted; bool attrs use gw.BoolAttr. Expr attrs are handled in a
// later task; the deferred attr kinds error clearly.
func emitAttr(b *bytes.Buffer, tag string, attrs []ast.Attr, a ast.Attr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string) bool {
	switch t := a.(type) {
	case *ast.StaticAttr:
		fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+t.Name+`="`+htmlAttrEscape(t.Value)+`"`))
	case *ast.BoolAttr:
		fmt.Fprintf(b, "\t\t_gsxgw.BoolAttr(%s, true)\n", strconv.Quote(t.Name))
	case *ast.ExprAttr:
		return emitExprAttr(b, tag, attrs, t, resolved, table, imports, interpTemp, cls, bag)
	case *ast.EmbeddedAttr:
		switch t.Lang {
		case ast.EmbeddedJS:
			return emitEmbeddedJSAttr(b, t, resolved, table, imports, interpTemp, bag)
		case ast.EmbeddedCSS:
			return emitEmbeddedCSSAttr(b, t, resolved, table, imports, interpTemp, bag)
		default:
			bag.Errorf(a.Pos(), a.End(), "unsupported-attr", "unknown embedded attribute language %d", t.Lang)
			return false
		}
	case *ast.ClassAttr:
		// class -> token merge (gw.Class); style -> '; '-joined declarations
		// (gw.Style) with dynamic parts CSS-value-filtered.
		if cls.Context(t.Name) == attrclass.CtxCSS {
			if !emitStyleAttr(b, t, table, imports, interpTemp, bag, resolved) {
				return false
			}
		} else {
			if !emitClassAttr(b, t, table, imports, interpTemp, bag, mergeExpr, resolved) {
				return false
			}
		}
	case *ast.SpreadAttr:
		// emitAttr runs only for non-component elements (genNode routes component
		// tags to genChildComponent before the attr loop), so a SpreadAttr here is
		// always an element spread. Spread entity-escapes values and drops invalid
		// attr names, but (per the gsx.Attrs trust contract) does NOT URL/CSS-sanitize
		// or reject JS-context keys — a bag's keys/values are trusted developer input.
		// This is deliberately distinct from the composable-style/expr-attr paths,
		// which fail closed on CSS/JS contexts because their values may be untrusted.
		spreadExpr, ok := spreadAttrExpr(t, table, imports, bag)
		if !ok {
			return false
		}
		fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", spreadExpr)
	case *ast.CondAttr:
		// Attr emission is a sequence of writer calls between `<tag` and `>`, so
		// wrapping the branch's attr emits in a real Go `if`/`else` is valid. An
		// else-if is a *CondAttr in Else, handled by the recursive emitAttr below.
		// (No //line for the cond: emitAttr has no fset, the wrapper is a pure
		// control construct, and each nested attr emit carries its own line map.)
		fmt.Fprintf(b, "\t\tif %s {\n", t.Cond)
		for _, inner := range t.Then {
			if !emitAttr(b, tag, attrs, inner, resolved, table, imports, interpTemp, cls, bag, mergeExpr) {
				return false
			}
		}
		if len(t.Else) > 0 {
			b.WriteString("\t\t} else {\n")
			for _, inner := range t.Else {
				if !emitAttr(b, tag, attrs, inner, resolved, table, imports, interpTemp, cls, bag, mergeExpr) {
					return false
				}
			}
		}
		b.WriteString("\t\t}\n")
		return true
	case *ast.OrderedAttrsAttr:
		bag.Errorf(a.Pos(), a.End(), "unsupported-attr",
			"ordered-attrs {{ }} is only valid as the value of a declared gsx.Attrs component prop, not plain-element attribute %q; declare a gsx.Attrs prop and spread it with { prop... }",
			t.Name)
		return false
	default:
		bag.Errorf(a.Pos(), a.End(), "unsupported-attr", "unknown attribute %T", a)
		return false
	}
	return true
}

func embeddedLangName(lang ast.EmbeddedLang) string {
	switch lang {
	case ast.EmbeddedJS:
		return "js"
	case ast.EmbeddedCSS:
		return "css"
	default:
		return fmt.Sprintf("unknown(%d)", lang)
	}
}

// emitEmbeddedJSAttr emits an explicit JS attribute literal whose quoted value
// is literal JS with @{ } holes. Static JS text is
// HTML-attr-escaped at codegen so <,>,& survive the attribute; each hole is
// escaped by its JSCtx and then HTML-attr-escaped (the *Attr escapers do both).
func emitEmbeddedJSAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	for _, seg := range a.Segments {
		switch s := seg.(type) {
		case *ast.Text:
			fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(htmlAttrEscape(s.Value)))
		case *ast.Interp:
			if !emitJSAttrInterp(b, s, resolved, table, imports, interpTemp, bag) {
				return false
			}
		default:
			bag.Errorf(seg.Pos(), seg.End(), "unsupported-attr", "JS attribute %q value may contain only text and @{ } interpolations, got %T", a.Name, seg)
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// emitEmbeddedCSSAttr emits an explicit CSS attribute literal whose quoted value
// is literal CSS with @{ } holes. Static CSS text is HTML-attr-escaped at
// codegen; each hole is CSS-value-filtered with gsx.StyleValue and then
// HTML-attr-escaped.
func emitEmbeddedCSSAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	for _, seg := range a.Segments {
		switch s := seg.(type) {
		case *ast.Text:
			fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(htmlAttrEscape(s.Value)))
		case *ast.Interp:
			if !emitCSSAttrInterp(b, s, resolved, table, imports, interpTemp, bag) {
				return false
			}
		default:
			bag.Errorf(seg.Pos(), seg.End(), "unsupported-attr", "CSS attribute %q value may contain only text and @{ } interpolations, got %T", a.Name, seg)
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// emitJSAttrInterp renders one @{ } hole in an explicit JS attribute literal
// through the runtime *Attr escaper chosen by its JSCtx. It mirrors emitJSInterp's
// pipeline-stage handling and (T, error) tuple auto-unwrap, but routes to the
// JS*Attr methods (which additionally HTML-attr-escape).
func emitJSAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table)
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of JS attribute interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "JS attribute interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		tmp := hoistTuple(b, expr, interpTemp)
		return emitJSAttrValue(b, n.JSCtx, tmp, elemT, n, bag)
	}
	return emitJSAttrValue(b, n.JSCtx, expr, t, n, bag)
}

// emitCSSAttrInterp renders one @{ } hole in an explicit CSS attribute literal.
// It mirrors emitCSSInterp's pipeline-stage handling and (T, error) tuple
// auto-unwrap, but routes through gsx.StyleValue followed by AttrValue because
// the result is inside a quoted HTML attribute.
func emitCSSAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table)
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of CSS attribute interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "CSS attribute interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		tmp := hoistTuple(b, expr, interpTemp)
		return emitRenderCSSAttr(b, tmp, elemT, imports, n, bag)
	}
	return emitRenderCSSAttr(b, expr, t, imports, n, bag)
}

// emitRenderCSSAttr writes a value in an explicit CSS attribute literal. The
// value is first reduced to a CSS-safe string with gsx.StyleValue, then escaped
// for the surrounding HTML attribute with gw.AttrValue.
func emitRenderCSSAttr(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool, n ast.Node, bag *diag.Bag) bool {
	styleExpr := ""
	if isRawCSS(t) {
		styleExpr = expr
	} else {
		switch classify(t) {
		case catInt:
			imports["strconv"] = true
			styleExpr = "strconv.FormatInt(int64(" + expr + "), 10)"
		case catUint:
			imports["strconv"] = true
			styleExpr = "strconv.FormatUint(uint64(" + expr + "), 10)"
		case catFloat:
			imports["strconv"] = true
			styleExpr = "strconv.FormatFloat(float64(" + expr + "), 'g', -1, 64)"
		case catString, catBytes:
			styleExpr = "string(" + expr + ")"
		case catStringer:
			styleExpr = "(" + expr + ").String()"
		default:
			bag.Errorf(n.Pos(), n.End(), "unrenderable-css", "value of type %s not renderable in CSS context (need string/number/Stringer or gsx.RawCSS)", t)
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(gsx.StyleValue(%s))\n", styleExpr)
	return true
}

// emitJSAttrValue selects the runtime JS *Attr escaper by JS context, mirroring
// emitJSValue but calling the attribute-escaping variants.
// n is the AST node for positioning any error diagnostic.
func emitJSAttrValue(b *bytes.Buffer, ctx ast.JSCtx, expr string, t types.Type, n ast.Node, bag *diag.Bag) bool {
	switch ctx {
	case ast.JSCtxValue:
		// JSValAttr accepts any (JSON-encode); gsx.RawJS passthrough at runtime.
		fmt.Fprintf(b, "\t\t_gsxgw.JSValAttr(%s)\n", expr)
		return true
	case ast.JSCtxString:
		return emitJSString(b, "JSStrAttr", expr, t, n, bag)
	case ast.JSCtxTemplate:
		return emitJSString(b, "JSTmplAttr", expr, t, n, bag)
	case ast.JSCtxRegexp:
		return emitJSString(b, "JSRegexpAttr", expr, t, n, bag)
	default:
		bag.Errorf(n.Pos(), n.End(), "unsafe-js-context", "JS attribute interpolation %q has no JS context (internal error: ResolveJSAttr not run?)", expr)
		return false
	}
}

// classPartExpr returns the lowered Go expression for one composable class/style
// part's value: its trimmed seed when the part carries no `|>` pipeline, or the
// nested filter-call lowering (the SAME lowerPipe the text/attr/prop paths use)
// when Stages is present. usedPkgs (the lowered filter packages) are folded into
// the caller's import set so the SAME packages are imported as the probe records.
// A lowering failure (an unknown filter) is positioned at the owning ClassAttr a
// via the bag with code "unresolved-pipeline" and ok=false. The `: cond` guard is
// never piped, so callers lower only the part's Expr/Stages, not its Cond.
func classPartExpr(p ast.ClassPart, a *ast.ClassAttr, table filterTable, imports map[string]bool, bag *diag.Bag) (string, bool) {
	lowered, usedPkgs, err := lowerClassPartSeed(p, table)
	if err != nil {
		bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return "", false
	}
	for _, path := range usedPkgs {
		imports[path] = true
	}
	return lowered, true
}

// lowerClassPartSeed returns one composable class/style part's value expression:
// its trimmed seed when no `|>` pipeline is present, or the nested filter-call
// lowering (the SAME lowerPipe every other value context uses) plus the used
// filter packages (alias→pkgPath) when Stages is present. It is the bag-free core
// shared by the root/element emitters (classPartExpr, which folds usedPkgs into
// imports) and the component-class path (classEntryExpr, which threads usedPkgs
// up as an *attrError-positioned diagnostic instead). The `: cond` guard is never
// piped, so only the part's Expr/Stages are lowered.
func lowerClassPartSeed(p ast.ClassPart, table filterTable) (string, map[string]string, error) {
	if len(p.Stages) == 0 {
		return strings.TrimSpace(p.Expr), nil, nil
	}
	return lowerPipe(p.Expr, p.Stages, table)
}

// emitClassAttr lowers a composable `class={ … }` to the open ` class="`, a
// gw.Class call composing each part (gsx.Class for an unconditional Expr,
// gsx.ClassIf for a conditional one), and the closing `"`. gw.Class runs the
// tokens through the passed merge func and writes the attr-escaped value.
// resolved maps each *ast.ValueArm to its harvest type for (T, error) unwrap.
func emitClassAttr(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag, mergeExpr string, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, interpTemp, bag, resolved, false)
	if !ok {
		return false
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s)\n", mergeExpr, strings.Join(parts, ", "))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// emitStyleAttr lowers a composable `style={ … }` to ` style="` + a gw.Style call
// composing each part as a CSS declaration, then `"`. A string-literal part is
// trusted and emitted raw; any dynamic part value is wrapped in gsx.FilterCSS so
// untrusted data cannot inject declarations or break out. gw.Style joins the
// included parts with "; " and attr-escapes the result.
// resolved maps each *ast.ValueArm to its harvest type for (T, error) unwrap.
func emitStyleAttr(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, interpTemp, bag, resolved, true)
	if !ok {
		return false
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Style(%s)\n", strings.Join(parts, ", "))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

func composedPartsOrdered(a *ast.ClassAttr, resolved map[ast.Node]types.Type) bool {
	for i := range a.Parts {
		p := &a.Parts[i]
		if p.CF != nil {
			return true
		}
		if _, ok := resolved[p].(*types.Tuple); ok {
			return true
		}
	}
	return false
}

func composedParts(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type, style bool) ([]string, bool) {
	parts := make([]string, 0, len(a.Parts))
	ordered := composedPartsOrdered(a, resolved)
	for i := range a.Parts {
		p := &a.Parts[i]
		if p.CF != nil {
			tmp, ok := hoistValueCF(b, p.CF, table, imports, interpTemp, style, bag, resolved)
			if !ok {
				return nil, false
			}
			parts = append(parts, fmt.Sprintf("gsx.Class(%s)", tmp))
			continue
		}
		if p.CSSSegments != nil {
			if !style {
				bag.Errorf(p.Pos(), p.End(), "unsupported-class-part", "css literal parts are only valid in style={...}")
				return nil, false
			}
			val, ok := cssLiteralStylePartExpr(b, p.CSSSegments, resolved, table, imports, interpTemp, bag)
			if !ok {
				return nil, false
			}
			if p.Cond == "" {
				parts = append(parts, fmt.Sprintf("gsx.Class(%s)", val))
				continue
			}
			cond := strings.TrimSpace(p.Cond)
			if ordered {
				tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				fmt.Fprintf(b, "\t\t%s := %s\n", tmp, cond)
				cond = tmp
			}
			parts = append(parts, fmt.Sprintf("gsx.ClassIf(%s, %s)", val, cond))
			continue
		}
		expr, ok := classPartExpr(*p, a, table, imports, bag)
		if !ok {
			return nil, false
		}
		if t, isTuple := resolved[p].(*types.Tuple); isTuple {
			if _, ok := tupleUnwrapType(t); !ok {
				kind := "class"
				if style {
					kind = "style"
				}
				bag.Errorf(p.Pos(), p.End(), "invalid-tuple", "%s part %q returns %s; only (T, error) is supported", kind, p.Expr, t)
				return nil, false
			}
			expr = hoistTuple(b, expr, interpTemp)
		} else if ordered {
			tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
			*interpTemp++
			fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
			expr = tmp
		}
		val := expr
		if style && (len(p.Stages) > 0 || !isStringLiteralExpr(strings.TrimSpace(p.Expr))) {
			val = "gsx.StyleValue(" + expr + ")"
		}
		if p.Cond == "" {
			parts = append(parts, fmt.Sprintf("gsx.Class(%s)", val))
			continue
		}
		cond := strings.TrimSpace(p.Cond)
		if ordered {
			tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
			*interpTemp++
			fmt.Fprintf(b, "\t\t%s := %s\n", tmp, cond)
			cond = tmp
		}
		parts = append(parts, fmt.Sprintf("gsx.ClassIf(%s, %s)", val, cond))
	}
	return parts, true
}

func cssLiteralStylePartExpr(b *bytes.Buffer, segments []ast.Markup, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) (string, bool) {
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch s := seg.(type) {
		case *ast.Text:
			if s.Value != "" {
				parts = append(parts, strconv.Quote(s.Value))
			}
		case *ast.Interp:
			expr := strings.TrimSpace(s.Expr)
			if len(s.Stages) > 0 {
				lowered, usedPkgs, err := lowerPipe(s.Expr, s.Stages, table)
				if err != nil {
					bag.Errorf(s.Pos(), s.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
					return "", false
				}
				for _, p := range usedPkgs {
					imports[p] = true
				}
				expr = lowered
			}
			if t, ok := resolved[s].(*types.Tuple); ok {
				if _, ok := tupleUnwrapType(t); !ok {
					bag.Errorf(s.Pos(), s.End(), "invalid-tuple", "style css literal interpolation %q returns %s; only (T, error) is supported", expr, t)
					return "", false
				}
				expr = hoistTuple(b, expr, interpTemp)
			}
			parts = append(parts, "gsx.StyleValue("+expr+")")
		default:
			bag.Errorf(seg.Pos(), seg.End(), "unsupported-style-part", "css literal style parts may contain only text and @{ } interpolations, got %T", seg)
			return "", false
		}
	}
	if len(parts) == 0 {
		return `""`, true
	}
	return strings.Join(parts, " + "), true
}

// styleDeclExpr returns the Go expression for a composed-style part's value: a
// pure string-literal part is trusted (returned verbatim); any other (dynamic)
// part is wrapped in gsx.FilterCSS so its runtime value is CSS-value-filtered. A
// piped part is ALWAYS dynamic (the lowered call result is not a string literal),
// so piped=true forces the CSS-value filter regardless of the lowered text shape.
func styleDeclExpr(expr string, piped bool) string {
	e := strings.TrimSpace(expr)
	if !piped && isStringLiteralExpr(e) {
		return e
	}
	return "gsx.StyleValue(" + e + ")"
}

// isStringLiteralExpr reports whether expr is exactly a Go string literal.
func isStringLiteralExpr(expr string) bool {
	node, err := goparser.ParseExpr(expr)
	if err != nil {
		return false
	}
	lit, ok := node.(*goast.BasicLit)
	return ok && lit.Kind == token.STRING
}

// htmlAttrEscape escapes a static attribute value for a double-quoted context at
// codegen time (matches the runtime gw.AttrValue entity set).
func htmlAttrEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}

// emitExprAttr emits an expr attribute value. URL attrs keep URL sanitization;
// all other expr attrs use ordinary attribute rendering. Explicit js`...` and
// css`...` literals opt into JS/CSS contextual rendering instead.
func emitExprAttr(b *bytes.Buffer, tag string, attrs []ast.Attr, a *ast.ExprAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag) bool {
	// (1) value expression: lower a pipeline to nested std calls (same lowerPipe
	// the probe used, so resolved[a] is already the pipeline's RESULT type), else
	// the bare trimmed expr.
	expr := strings.TrimSpace(a.Expr)
	if len(a.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(a.Expr, a.Stages, table)
		if err != nil {
			bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, path := range usedPkgs {
			imports[path] = true
		}
		expr = lowered
	}

	t, ok := resolved[a]
	if !ok || t == nil {
		bag.Errorf(a.Pos(), a.End(), "unresolved-attr", "could not resolve type of attribute %q value %q", a.Name, a.Expr)
		return false
	}

	// (2) auto-unwrap a (T, error) value — v, err := expr; if err != nil { return err } —
	// then use v by its type T, exactly as text/<style>/<script> interpolation do.
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(a.Pos(), a.End(), "invalid-tuple", "attribute %q value %q returns %s; only (T, error) is supported", a.Name, a.Expr, t)
			return false
		}
		expr = hoistTuple(b, expr, interpTemp)
		t = elemT
	}

	if classify(t) == catBool {
		fmt.Fprintf(b, "\t\t_gsxgw.BoolAttr(%s, bool(%s))\n", strconv.Quote(a.Name), expr)
		return true
	}

	isMetaRefreshContent := strings.EqualFold(a.Name, "content") && isStaticMetaRefreshAttr(tag, attrs)

	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	if isMetaRefreshContent {
		fmt.Fprintf(b, "\t\t_gsxgw.RefreshContent(%s)\n", urlStringExpr(expr, t))
	} else if cls.Context(a.Name) == attrclass.CtxURL && !isRawURL(t) {
		// URL context: value must be string-like; sanitize + escape. A gsx.RawURL
		// value (isRawURL) is the author's vouch — fall through to gw.AttrValue,
		// which entity-escapes but skips the scheme allow-list.
		fmt.Fprintf(b, "\t\t_gsxgw.URL(%s)\n", urlStringExpr(expr, t))
	} else {
		if !emitAttrValue(b, expr, t, imports, a, bag) {
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

func isStaticMetaRefreshAttr(tag string, attrs []ast.Attr) bool {
	if !strings.EqualFold(tag, "meta") {
		return false
	}
	for _, a := range attrs {
		s, ok := a.(*ast.StaticAttr)
		if !ok {
			continue
		}
		if strings.EqualFold(s.Name, "http-equiv") && strings.EqualFold(strings.TrimSpace(s.Value), "refresh") {
			return true
		}
	}
	return false
}

// isRawURL reports whether t is the gsx.RawURL named type — the author's opt-out
// from URL scheme sanitizing. Such a value is routed through gw.AttrValue
// (entity-escaped, scheme unchecked) instead of gw.URL.
func isRawURL(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Name() == "RawURL" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
}

// urlStringExpr renders a URL-context value as a string expression for gw.URL.
func urlStringExpr(expr string, t types.Type) string {
	if classify(t) == catString {
		return "string(" + expr + ")"
	}
	return expr // non-string URL values are unusual; let the Go compiler check gw.URL's arg
}

// emitAttrValue writes a non-URL attribute value via gw.AttrValue, §5 type-aware.
// n is the AST node for positioning any error diagnostic.
func emitAttrValue(b *bytes.Buffer, expr string, t types.Type, imports map[string]bool, n ast.Node, bag *diag.Bag) bool {
	switch classify(t) {
	case catString:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(string(%s))\n", expr)
	case catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(string(%s))\n", expr)
	case catInt:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(strconv.FormatInt(int64(%s), 10))\n", expr)
	case catUint:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(strconv.FormatUint(uint64(%s), 10))\n", expr)
	case catFloat:
		imports["strconv"] = true
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(strconv.FormatFloat(float64(%s), 'g', -1, 64))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue((%s).String())\n", expr)
	default:
		bag.Errorf(n.Pos(), n.End(), "unsupported-attr-type", "attribute value type %s not supported (string/number/bool/Stringer only)", t)
		return false
	}
	return true
}

// isComponentTag reports whether a tag names a component (uppercase first letter
// or dotted, e.g. ui.Button) rather than an HTML element.
func isComponentTag(tag string) bool {
	if tag == "" {
		return false
	}
	if strings.Contains(tag, ".") {
		return true
	}
	return tag[0] >= 'A' && tag[0] <= 'Z'
}

// singleRoot reports the component body's single root element when the body has
// EXACTLY one meaningful top-level node and that node is a NON-component
// `*ast.Element`. Pure-whitespace `*ast.Text` (Value is all spaces/newlines) and
// HTML comments are insignificant and skipped; any other node kind alongside (or
// as) the candidate — a Doctype, a fragment, multiple elements, a control-flow
// node, an interp, or a component-tag element — yields (nil, false). It drives
// child auto-fallthrough eligibility: only a single HTML-element root can receive
// the synthesized Attrs bag (class-merge + spread at that element).
func singleRoot(body []ast.Markup) (*ast.Element, bool) {
	var root *ast.Element
	for _, n := range body {
		switch t := n.(type) {
		case *ast.Text:
			if strings.TrimSpace(t.Value) == "" {
				continue // insignificant whitespace
			}
			return nil, false // non-whitespace text alongside → not single-root
		case *ast.HTMLComment:
			continue // comments are insignificant
		case *ast.Element:
			if root != nil || isComponentTag(t.Tag) {
				return nil, false // a second element, or a component-tag root
			}
			root = t
		default:
			return nil, false // fragment / control-flow / interp / doctype → not single-root
		}
	}
	if root == nil {
		return nil, false
	}
	// The root is fallthrough-eligible only when ALL its own attributes have
	// STATICALLY-KNOWN names: caller-wins emit guards each root attr with
	// `if !_gsxp.Attrs.Has(name)` and merges the bag's class/style — both need the
	// static name. A *CondAttr (`{ if … { id=… } }`) or *SpreadAttr (`<div {...x}>`) sets attrs
	// whose names/values are only known at runtime, so neither the drop set nor the
	// class merge can account for them — a colliding fallthrough would emit a
	// duplicate attribute (and bypass class-merge). Such a root is NOT auto-eligible;
	// a fallthrough onto it then fails closed (no Attrs field → Go unknown-field).
	for _, a := range root.Attrs {
		switch a.(type) {
		case *ast.CondAttr, *ast.SpreadAttr:
			return nil, false
		}
	}
	return root, true
}

// usesChildren reports whether any interpolation in the markup body is a bare
// `{children}` reference. It mirrors the markup walks (recursing control flow,
// non-component element children, and fragments); a child component's OWN attrs
// are props (not slot content), and a child component's children render in THIS
// parent scope, so those are recursed too — a `{children}` inside a nested
// element or another component's slot still counts. The result drives whether the
// component synthesizes a `Children gsx.Node` field + `children` local binding.
func usesChildren(body []ast.Markup) bool {
	for _, n := range body {
		switch t := n.(type) {
		case *ast.Interp:
			if strings.TrimSpace(t.Expr) == "children" {
				return true
			}
		case *ast.Element:
			// Recurse children for BOTH plain elements and child components: a
			// component's slot content renders in this scope, so a `{children}` there
			// references THIS component's slot. (We do not walk a component's attrs —
			// those are props.)
			if usesChildren(t.Children) {
				return true
			}
		case *ast.Fragment:
			if usesChildren(t.Children) {
				return true
			}
		case *ast.ForMarkup:
			if usesChildren(t.Body) {
				return true
			}
		case *ast.IfMarkup:
			if usesChildren(t.Then) || usesChildren(t.Else) {
				return true
			}
		case *ast.SwitchMarkup:
			for _, cc := range t.Cases {
				if usesChildren(cc.Body) {
					return true
				}
			}
		}
	}
	return false
}

// usesAttrs reports whether the markup body references the EXACT identifier
// `attrs` in any value-position Go fragment — the MANUAL-mode trigger. It mirrors
// usesChildren's recursion (control flow, fragments, non-component and component
// element children incl. named-slot values) but detects via valueIdents, which is
// token-based: it matches the bare ident `attrs` (e.g. `{...attrs}` SpreadAttr,
// `{...attrs.Without("id")}`, `{ attrs.Class() }` interp, an `attrs`-referencing
// control-flow clause), and crucially does NOT match a longer ident like
// `attrsList` (a different token) nor a selector field after a `.` (e.g.
// `x.attrs`). A component's SIMPLE attrs (props) are NOT walked — those are the
// caller's prop exprs, not this component's body — but its named-slot values and
// slot children render in THIS scope and CAN reference this component's `attrs`,
// so they are recursed.
func usesAttrs(body []ast.Markup) bool {
	refsAttrs := func(src string) bool { return valueIdents(src)["attrs"] }
	for _, n := range body {
		switch t := n.(type) {
		case *ast.Interp:
			if refsAttrs(t.Expr) {
				return true
			}
			for _, st := range t.Stages {
				if st.Args != "" && refsAttrs(st.Args) {
					return true
				}
			}
		case *ast.Element:
			// A non-component element: walk its attrs (spread/expr/class/cond) for an
			// `attrs` reference. A component element: skip its SIMPLE attrs (props) but
			// recurse named-slot values, which render in this scope. Both recurse
			// children.
			if !isComponentTag(t.Tag) {
				if attrsRefAttrs(t.Attrs) {
					return true
				}
			} else {
				found := false
				walkMarkupAttrs(t.Attrs, func(value []ast.Markup) {
					if usesAttrs(value) {
						found = true
					}
				})
				if found {
					return true
				}
			}
			if usesAttrs(t.Children) {
				return true
			}
		case *ast.Fragment:
			if usesAttrs(t.Children) {
				return true
			}
		case *ast.ForMarkup:
			if refsAttrs(t.Clause) || usesAttrs(t.Body) {
				return true
			}
		case *ast.IfMarkup:
			if refsAttrs(t.Cond) || usesAttrs(t.Then) || usesAttrs(t.Else) {
				return true
			}
		case *ast.SwitchMarkup:
			if refsAttrs(t.Tag) {
				return true
			}
			for _, cc := range t.Cases {
				if refsAttrs(cc.List) || usesAttrs(cc.Body) {
					return true
				}
			}
		case *ast.GoBlock:
			if refsAttrs(t.Code) {
				return true
			}
		}
	}
	return false
}

// attrsRefAttrs reports whether any verbatim-emitted Go fragment in a (non-
// component) element's attr list references the identifier `attrs`: a
// `{...attrs}` SpreadAttr's Expr, a composable-class part Expr/Cond, an ExprAttr
// Expr or its pipeline args, or — recursing — a CondAttr's Cond and branches.
// These are exactly the fragments collectAttrSrc feeds to the ident walks, so a
// manual `attrs` reference anywhere in them is detected.
func attrsRefAttrs(attrs []ast.Attr) bool {
	refsAttrs := func(src string) bool { return valueIdents(src)["attrs"] }
	for _, a := range attrs {
		switch at := a.(type) {
		case *ast.SpreadAttr:
			if refsAttrs(at.Expr) {
				return true
			}
		case *ast.ClassAttr:
			for _, p := range at.Parts {
				if refsAttrs(p.Expr) || (p.Cond != "" && refsAttrs(p.Cond)) {
					return true
				}
			}
		case *ast.ExprAttr:
			if refsAttrs(at.Expr) {
				return true
			}
			for _, st := range at.Stages {
				if st.Args != "" && refsAttrs(st.Args) {
					return true
				}
			}
		case *ast.CondAttr:
			if refsAttrs(at.Cond) || attrsRefAttrs(at.Then) || attrsRefAttrs(at.Else) {
				return true
			}
		case *ast.EmbeddedAttr:
			// An embedded attribute value's @{ } interps render in this scope, so an
			// `attrs` reference there needs the local bound.
			if usesAttrs(at.Segments) {
				return true
			}
		}
	}
	return false
}

// childInvocation resolves a child-component tag to its CALL shape, applying the
// method-vs-package disambiguation. It is the SINGLE source of the call target +
// props-type name shared by genChildComponent (emit) and emitProbes (probe), so
// the two never drift on which call/props-struct a `<X.Y/>` invokes.
//
// Disambiguation (syntactic, deterministic): split el.Tag on the first `.` into
// (left, method). If the enclosing component is a method component (recvVar != "")
// AND left == recvVar → METHOD invocation: callTarget = `<recvVar>.<method>`,
// propsType = `<recvTypeName><method>Props`. Otherwise → existing PACKAGE-function
// path: callTarget = el.Tag, propsType = el.Tag + "Props" (e.g. `ui.AppShell` and
// `ui.AppShellProps`, or a same-file uppercase `Card` / `CardProps`).
//
// A NULLARY method invocation — a method whose call has NO props struct — happens
// only when isMethod AND the element has no attrs AND no children (a method that
// places `{children}` has a Children field, so children imply a props literal).
// The caller decides nullary via isNullaryCall below; this helper only reports
// the disambiguation result.
func childInvocation(el *ast.Element, byo *byoData, recvVar, recvTypeName string) (callTarget, propsType string, isMethod bool) {
	if recvVar != "" {
		if i := strings.IndexByte(el.Tag, '.'); i >= 0 {
			left, method := el.Tag[:i], el.Tag[i+1:]
			if left == recvVar {
				// BYO method: the method takes its author struct directly, so the
				// props type is that struct's name (not <RecvType><Method>Props).
				if st, ok := byo.structTypeName(recvTypeName + "." + method); ok {
					return recvVar + "." + method, st, true
				}
				return recvVar + "." + method, recvTypeName + method + "Props", true
			}
		}
	}
	// BYO function component: the tag invokes a same-package component whose sole
	// param is an author struct, so the props type is that struct's name.
	if st, ok := byo.structTypeName("." + el.Tag); ok {
		return el.Tag, st, false
	}
	return el.Tag, el.Tag + "Props", false
}

// genChildComponent lowers a child-component element to a gw.Node render call,
// building the props struct literal from the element's attributes. When the
// element has children, the slot markup is passed as a `Children gsx.Node`
// field — a gsx.Func render closure mirroring genComponent's, so the slot renders
// in THIS (parent) scope, where its interps' params/loop vars are bound.
//
// recvVar/recvTypeName are the ENCLOSING component's receiver var + type name
// (empty for a function component); they drive the method-vs-package
// disambiguation via childInvocation.
func genChildComponent(b *bytes.Buffer, el *ast.Element, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	// A bare-call candidate tag (isBareCallCandidate — a hand-written same-package
	// func, or a .gsx no-props component) was probed via _gsxcompsig, so harvest
	// stored its real signature in resolved[el]. A nullary func is a bare call
	// `<F/>` (like a void element); passing attributes or children is an error (a
	// zero-arg func has nowhere to put them). An arity ≥ 1 func falls through to
	// the XxxProps convention below.
	if sig, ok := resolved[el].(*types.Signature); ok && sig.Params().Len() == 0 {
		if len(el.Attrs) > 0 || len(el.Children) > 0 {
			bag.Errorf(el.Pos(), el.End(), "noarg-component-args",
				"no-argument component %s accepts no attributes or children", el.Tag)
			return false
		}
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s())\n", el.Tag)
		return true
	}
	callTarget, propsType, isMethod := childInvocation(el, byo, recvVar, recvTypeName)
	// A nullary call — no attrs, no children — emits a bare call with no props
	// literal. This applies to:
	//   - a nullary METHOD invocation (isMethod, no props struct by method-nullary
	//     contract), and
	//   - a nullary FUNCTION component that has no props struct (isNoPropsComponent:
	//     same-package, propFields entry present with nil value).
	// A BYO child (method OR function) ALWAYS takes its author struct, so even an
	// attr/child-free call passes the zero struct (Comp(T{})) — it is never the
	// bare-call nullary case.
	_, isByoChild := byo.isByoStruct(propsType)
	isNullaryCall := ((isMethod && !isByoChild) || isNoPropsComponent(structFields, propsType)) && len(el.Attrs) == 0 && len(el.Children) == 0
	if isNullaryCall {
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s())\n", callTarget)
		return true
	}

	// Build the FULL props literal (simple attr fields + markup-attr slot fields +
	// Children) through the shared childPropsLiteral. The slot VALUE for both
	// markup attributes and the {children} slot is a real gsx.Func render closure
	// (emitSlotClosure), rendered in THIS (parent) scope so its interps' params/
	// loop vars are bound. emitProbes drives the same builder with a typed-nil
	// slotValue, so emit and probe agree on WHICH fields exist — only the VALUE
	// differs, and they cannot drift.
	// When splatExpr is non-empty, the call is a whole-struct splat: emit
	// callTarget(splatExpr) directly, bypassing the Props{…} literal.
	fieldEntries, splatExpr, usedPkgs, err := childPropsLiteral(el, propsType, "gsx", mergeExpr, table, structFields, nodeProps[propsType], byo, fm, func(nodes []ast.Markup) (string, error) {
		s, ok := emitSlotClosure(nodes, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		if !ok {
			return "", fmt.Errorf("slot closure failed")
		}
		return s, nil
	}, false, resolved, b, interpTemp)
	if err != nil {
		// Convert the childPropsLiteral error to a positioned diagnostic.
		// An attrError carries the specific attr's position; fall back to the element.
		if ae, ok := errors.AsType[*attrError](err); ok {
			bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
		} else {
			bag.Errorf(el.Pos(), el.End(), childPropsErrorCode(err), "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		}
		return false
	}
	// Record any filter packages referenced by a lowered prop/fallthrough pipeline
	// so writeImports emits them under their reserved aliases — mirroring genInterp.
	for _, path := range usedPkgs {
		imports[path] = true
	}
	if splatExpr != "" {
		// Whole-struct splat: `<Card { d... }/>` → `Card(d)` (no Props{…} literal).
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(%s))\n", callTarget, splatExpr)
		return true
	}

	// Reject a child-prop value whose RAW type is a tuple that is NOT (T, error):
	// the auto-unwrap only supports (T, error). This mirrors the identical
	// rejection at the interpolation / markup-attr / style / script / JS-attr sites
	// (same "invalid-tuple" code and "only (T, error) is supported" wording). The
	// skeleton's _gsxunwrap(...) probe no longer rejects such tuples (its trailing
	// param is `...any`), so this is the single source of the clean diagnostic.
	for _, fe := range fieldEntries {
		if fe.ea == nil {
			continue
		}
		if t, ok := resolved[fe.ea].(*types.Tuple); ok {
			if _, unwrappable := tupleUnwrapType(t); !unwrappable {
				bag.Errorf(fe.ea.Pos(), fe.ea.End(), "invalid-tuple", "child prop %q value %q returns %s; only (T, error) is supported", fe.ea.Name, fe.ea.Expr, t)
				return false
			}
		}
	}
	// Same rejection for ordered-attrs pair values: if any pair value is a tuple
	// that is NOT (T, error), emit the same clean diagnostic (wording adapted to
	// the ordered-attrs context). The skeleton's _gsxunwrap(...) on each pair
	// value keeps the skeleton from erroring, so this is the sole diagnostic site.
	for _, fe := range fieldEntries {
		if fe.oa == nil {
			continue
		}
		for j := range fe.oaPairs {
			pairType := resolved[&fe.oa.Pairs[j]]
			if t, ok := pairType.(*types.Tuple); ok {
				if _, unwrappable := tupleUnwrapType(t); !unwrappable {
					bag.Errorf(fe.oa.Pairs[j].Pos(), fe.oa.Pairs[j].End(), "invalid-tuple",
						"ordered-attrs pair %q value %q returns %s; only (T, error) is supported",
						fe.oaPairs[j].key, fe.oaPairs[j].rawVal, t)
					return false
				}
			}
		}
	}

	// Unified hoist-all-when-any: if ANY value across ALL props (ExprAttr slots OR
	// OrderedAttrsAttr pair values) is a (T, error) tuple, hoist every CALL value in
	// source order before the Node call. This single pass over fieldEntries
	// preserves left-to-right evaluation order even when ExprAttr and
	// OrderedAttrsAttr slots are interleaved (e.g. a={f()} bag={{"k":g()}} b={h()}).
	// Non-tuple CALL values get a plain `tmp := expr`; tuple CALL values get
	// `tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`. Non-call values
	// (literals/idents) have no side effects and stay INLINE, preserving their
	// untyped-constant assignability.
	anyTuple := false
outer:
	for _, fe := range fieldEntries {
		if fe.ea != nil {
			if _, ok := tupleUnwrapType(resolved[fe.ea]); ok {
				anyTuple = true
				break outer
			}
		}
		if fe.oa != nil {
			for j := range fe.oaPairs {
				if _, ok := tupleUnwrapType(resolved[&fe.oa.Pairs[j]]); ok {
					anyTuple = true
					break outer
				}
			}
		}
	}
	if anyTuple {
		for i, fe := range fieldEntries {
			switch {
			case fe.ea != nil:
				// Only hoist CALL expressions: a tuple call via hoistTuple, a
				// non-tuple call via `_gsxv := call` (preserving its left-to-right
				// side-effect order relative to the tuple calls). A NON-call value
				// (untyped constant, ident, selector) has no side effects, so its
				// source order is immaterial AND hoisting it as `_gsxv := 100`
				// would fix its untyped type and break assignment to a
				// non-default-typed field — leave it INLINE in the literal.
				_, isTup := tupleUnwrapType(resolved[fe.ea])
				if !isTup && !isCallExpr(fe.rawVal) {
					continue // keep the inline str built by childPropsLiteral
				}
				var tmp string
				if isTup {
					tmp = hoistTuple(b, fe.rawVal, interpTemp)
				} else {
					tmp = fmt.Sprintf("_gsxv%d", *interpTemp)
					*interpTemp++
					fmt.Fprintf(b, "\t\t%s := %s\n", tmp, fe.rawVal)
				}
				if fe.isNodeField {
					fieldEntries[i].str = fmt.Sprintf("%s: gsx.Val(%s)", fe.fieldName, tmp)
				} else {
					fieldEntries[i].str = fmt.Sprintf("%s: %s", fe.fieldName, tmp)
				}
			case fe.oa != nil:
				// Hoist tuple/call pairs and rebuild the gsx.Attrs{…}
				// literal; non-call pairs stay inline (see the ExprAttr note).
				var sb strings.Builder
				fmt.Fprintf(&sb, "%s: gsx.Attrs{", fe.fieldName)
				for j, pr := range fe.oaPairs {
					pairType := resolved[&fe.oa.Pairs[j]]
					_, isTup := tupleUnwrapType(pairType)
					var valueStr string
					switch {
					case isTup:
						valueStr = hoistTuple(b, pr.rawVal, interpTemp)
					case isCallExpr(pr.rawVal):
						tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
						*interpTemp++
						fmt.Fprintf(b, "\t\t%s := %s\n", tmp, pr.rawVal)
						valueStr = tmp
					default:
						valueStr = pr.rawVal // inline non-call value
					}
					fmt.Fprintf(&sb, "{Key: %s, Value: %s}, ", strconv.Quote(pr.key), valueStr)
				}
				sb.WriteString("}")
				fieldEntries[i].str = sb.String()
			}
		}
	}
	strs := make([]string, len(fieldEntries))
	for i, fe := range fieldEntries {
		strs[i] = fe.str
	}
	fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(%s{%s}))\n", callTarget, propsType, strings.Join(strs, ", "))
	return true
}

// attrError is an error from childPropsLiteral that carries the offending attr
// node's position so callers can emit a positioned diagnostic. The msg field has
// the "codegen: " prefix stripped; code is already resolved via childPropsErrorCode.
type attrError struct {
	pos  token.Pos
	end  token.Pos
	code string
	msg  string
}

func (e *attrError) Error() string { return e.msg }

// childPropsErrorCode returns the diagnostic error code for a childPropsLiteral
// error, based on the error message content.
func childPropsErrorCode(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "class attribute"):
		return "unsupported-component-attr"
	case strings.Contains(msg, "spread attribute"):
		return "unsupported-component-attr"
	case strings.Contains(msg, "conditional attribute"):
		return "unsupported-component-attr"
	case strings.Contains(msg, "pipeline/`?`"):
		return "unsupported-child-pipeline"
	case strings.Contains(msg, "non-identifier attribute"):
		return "unsupported-attr-name"
	case strings.Contains(msg, "unknown attribute"):
		return "unsupported-attr"
	case strings.Contains(msg, "slot closure"):
		return "unsupported-node"
	default:
		return "unsupported-component-attr"
	}
}

// emitSlotClosure renders a slot (the {children} markup or a named markup-attr
// value) as a gsx.Func render closure string. The closure mirrors genComponent
// EXACTLY — same reserved idents (_gsxw/_gsxgw), gsx.W/gsx.Func, and trailing
// Err() — so the slot streams to the same output, in THIS (parent) scope. It is
// shared by the Children slot and every named markup slot so they cannot drift.
func emitSlotClosure(nodes []ast.Markup, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) (string, bool) {
	var slot bytes.Buffer
	slot.WriteString("gsx.Func(func(ctx context.Context, _gsxw io.Writer) error {\n")
	slot.WriteString("\t\t_gsxgw := gsx.W(_gsxw)\n")
	emitNumScratch(&slot, nodes, resolved)
	for _, c := range nodes {
		if !genNode(&slot, c, resolved, table, structFields, nodeProps, attrsProps, byo, imports, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
			return "", false
		}
	}
	slot.WriteString("\t\treturn _gsxgw.Err()\n")
	slot.WriteString("\t})")
	return slot.String(), true
}

// propFieldEntry is one field in a child component's props literal. The ea,
// rawVal, fieldName, and isNodeField fields are populated only for a prop-matched
// *ExprAttr; they are used by genChildComponent to detect and hoist (T, error)
// tuple-valued props before the _gsxgw.Node call.
// For an *OrderedAttrsAttr field, oa is non-nil and oaPairs holds per-pair info
// (key + raw value expression); genChildComponent uses oa to look up resolved
// types for each pair and rebuild the gsx.Attrs{…} literal after hoisting.
type propFieldEntry struct {
	str         string        // fully-computed field string, e.g. "Title: lookup(t)"
	ea          *ast.ExprAttr // non-nil iff this entry came from a prop-matched ExprAttr
	rawVal      string        // lowered expression (no _gsxunwrap wrapping); used for hoisting
	fieldName   string        // Go field name; used to rebuild the string after hoisting
	isNodeField bool          // whether the field expects gsx.Node (needs gsx.Val wrapping)
	// For OrderedAttrsAttr fields:
	oa      *ast.OrderedAttrsAttr // non-nil iff this entry came from an ordered-attrs attr
	oaPairs []oaPairEntry         // per-pair info when oa != nil
}

// oaPairEntry holds the key and raw value expression for one pair inside an
// ordered-attrs field. genChildComponent uses it to rebuild the gsx.Attrs
// literal after hoisting tuple-valued pairs.
type oaPairEntry struct {
	key    string // unquoted attribute key (for {Key: …} literal)
	rawVal string // raw Go expression (no _gsxunwrap wrapping)
}

// isCallExpr reports whether rawVal parses as a Go function-call expression
// (after unwrapping any surrounding parens). Only a CallExpr can yield a
// (T, error) tuple when used in single-value position; literals, identifiers,
// selectors, etc. are always single-valued. The (T, error) auto-unwrap therefore
// gates BOTH the skeleton's _gsxunwrap(...) tolerance wrap AND the emit-time
// hoist on this predicate: wrapping/hoisting a non-call value is needless and, for
// an untyped constant, actively wrong (it fixes the constant to its default type,
// breaking assignability to a non-default-typed field). Pipelines already lower to
// calls, so they still satisfy this and remain wrapped/hoisted.
func isCallExpr(rawVal string) bool {
	expr, err := goparser.ParseExpr(rawVal)
	if err != nil {
		return false
	}
	for {
		paren, ok := expr.(*goast.ParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	_, ok := expr.(*goast.CallExpr)
	return ok
}

// childPropsLiteral builds the per-field list for a child component's props
// struct literal (e.g. `Title: "Hi", Featured: true, Header: <slot>`) from
// the element's attributes and children. It is the SINGLE source of the props
// literal so the render emission (genChildComponent) and the type-check probe
// (emitProbes) cannot drift — they pass the SAME field set, differing only in the
// slot VALUE supplied by slotValue (a real gsx.Func closure for emission, a
// typed-nil for the probe).
//
// Whole-struct splat (byo only): when the element's sole attr is a SpreadAttr on
// a byo component — written `<Card { d... }/>` — childPropsLiteral returns a
// non-empty splatExpr ("d") and the callers emit `callTarget(splatExpr)` directly
// instead of `callTarget(propsType{fieldsStr})`. This is all-or-nothing: a splat
// combined with any other attr or with children is a clear error. On a
// non-byo (generated/nullary) component a SpreadAttr in the tag attrs is a
// fallthrough Attrs-bag merge (.Merge), not a whole-struct splat — the byo check
// is load-bearing.
//
// It SPLITS each Static/Expr/Bool attr via matchField(propFields[propsType], …):
//   - a MATCHED field name (identifier-capitalize or kebab→Camel, or custom
//     FieldMatcher hit) → a props-struct field (static→quoted, expr→trimmed
//     expr rejecting pipeline Stages, bool→true), exactly as before; and
//   - anything else (a kebab/non-identifier name that the matcher does not match,
//     or an undeclared identifier on a known same-package child) → a FALLTHROUGH
//     bag entry keyed by the attr's RAW name (static→quoted value, bool→true,
//     expr→trimmed expr).
//
// A markup attribute is always a named slot (rendered via slotValue). Composed
// class/spread/conditional fallthrough attrs build the Attrs bag as a chained
// EXPRESSION in source order:
//
//	<rtPkg>.Attrs{<static/expr/bool + composable-class entries>}
//	    .Merge(<spreadExpr>)
//	    .Merge(<rtPkg>.AttrsCond(<cond>, func() <rtPkg>.Attrs { return <rtPkg>.Attrs{<then>} }, <else-thunk|nil>))…
//
// keeping this a single string keeps emit≡probe trivial (no statement preamble).
// When the bag is a bare static literal with no entries, NO Attrs field is
// emitted (so a pure-props invocation is byte-identical to before); the .Merge
// chain / class entry is only added when those features are present.
//
// rtPkg is the package qualifier for the gsx runtime (`gsx` in emitted code,
// `_gsxrt` in a type-check skeleton), used to name the bag type `<rtPkg>.Attrs`,
// the `<rtPkg>.ClassJoin`/`Class`/`ClassIf` class helpers, `<rtPkg>.AttrsCond`,
// and node-prop `<rtPkg>.Val`/`Text` promotion, so emit and probe reference the
// SAME runtime symbols under their respective aliases.
//
// table lowers any `|>` pipeline on a prop or fallthrough ExprAttr (the SAME
// lowerPipe the text/attr paths use). usedPkgs (alias→pkgPath) reports every
// filter package the lowered exprs reference so BOTH callers import them: the
// emitter into its imports map, the probe into its usedFilters set — without
// this the skeleton would not import the std filter package and prop pipelines
// would fail to resolve.
//
// nodeFields is the child props type's set of declared gsx.Node fields
// (nodeProps[propsType]); a non-node value bound to one of these is promoted via
// rtPkg.Val/rtPkg.Text so a renderable value fills a gsx.Node prop.
// probeWrap=true wraps each prop-matched ExprAttr value with _gsxunwrap(...) so the
// skeleton tolerates (T, error) tuples while still checking field types. Pass false
// for the real code emitter; pass true for the type-check probe (analyze.go).
// b and interpTemp are threaded to classEntryExpr so value-form CF (if/switch) parts
// in a composed class attr on the child element can hoist their var+if/switch
// statements into b before the Node call. Pass nil for both in contexts that do not
// support hoisting (e.g. the analyze path passes a local scratch buffer).
// resolved maps *ClassPart nodes to their harvest type so classEntryExpr can detect
// and hoist (T, error) tuple-returning unconditional plain parts. Pass nil in the
// probe path (skeleton does not need resolved; probeWrap wraps call exprs instead).
func childPropsLiteral(el *ast.Element, propsType, rtPkg, mergeExpr string, table filterTable, propFields map[string]map[string]bool, nodeFields map[string]bool, byo *byoData, fm FieldMatcher, slotValue func(nodes []ast.Markup) (string, error), probeWrap bool, resolved map[ast.Node]types.Type, b *bytes.Buffer, interpTemp *int) (fields []propFieldEntry, splatExpr string, usedPkgs map[string]string, err error) {
	fm = fieldMatcherOrDefault(fm)    // normalize nil → default matcher
	declared := propFields[propsType] // nil for cross-package / unknown → graceful
	// BYO struct facts: when the child is byo, an unmatched attr (→ Attrs bag) or
	// {children} (→ Children field) is a CLEAR ERROR if the author struct lacks the
	// corresponding field, rather than silently auto-synthesizing one (the byo path
	// is explicit — spec §6).
	byoStr, isByoChild := byo.isByoStruct(propsType)

	// Whole-struct splat (byo only): `<Card { d... }/>` → `Card(d)`.
	// A SpreadAttr on a byo component is the whole-prop splat, not an Attrs merge.
	// Must be all-or-nothing: the sole attr, no children. Error otherwise.
	if isByoChild {
		for _, a := range el.Attrs {
			if s, ok := a.(*ast.SpreadAttr); ok {
				// Found a splat on a byo component. Validate all-or-nothing.
				if len(el.Attrs) != 1 || len(el.Children) > 0 {
					msg := fmt.Sprintf("{ x... } splat on <%s> passes the whole prop value; remove the other attrs or children", el.Tag)
					return nil, "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "byo-splat-mixed", msg: msg}
				}
				expr := strings.TrimSpace(s.Expr)
				if expr == "" {
					msg := fmt.Sprintf("empty { x... } splat on <%s>; provide the struct expression to splat", el.Tag)
					return nil, "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "empty-splat", msg: msg}
				}
				splatPkgs := map[string]string{}
				if len(s.Stages) > 0 {
					lowered, used, perr := lowerPipe(s.Expr, s.Stages, table)
					if perr != nil {
						msg := strings.TrimPrefix(perr.Error(), "codegen: ")
						return nil, "", nil, &attrError{pos: s.Pos(), end: s.End(), code: "unresolved-pipeline", msg: msg}
					}
					splatPkgs = used
					expr = lowered
				}
				return nil, expr, splatPkgs, nil
			}
		}
	}

	usedPkgs = map[string]string{}
	var bag []string        // fallthrough base-literal entries: `"<rawName>": <value>`
	var mergeChain []string // `.Merge(<spread>)` / `.Merge(<rtPkg>.AttrsCond(...))` in source order
	// recordPkgs merges a lowerPipe usedPkgs result into the shared set.
	recordPkgs := func(used map[string]string) {
		maps.Copy(usedPkgs, used)
	}
	for _, a := range el.Attrs {
		switch t := a.(type) {
		case *ast.StaticAttr:
			if fn, isProp := matchField(declared, t.Name, fm); isProp {
				if verr := validateMatchedField(fn, t.Name, propsType, declared); verr != nil {
					return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "bad-field-match", msg: verr.Error()}
				}
				if nodeFields[fn] {
					fields = append(fields, propFieldEntry{str: fmt.Sprintf("%s: %s.Text(%s)", fn, rtPkg, strconv.Quote(t.Value))})
				} else {
					fields = append(fields, propFieldEntry{str: fmt.Sprintf("%s: %s", fn, strconv.Quote(t.Value))})
				}
			} else {
				bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(t.Value)))
			}
		case *ast.ExprAttr:
			if fn, isProp := matchField(declared, t.Name, fm); isProp {
				if verr := validateMatchedField(fn, t.Name, propsType, declared); verr != nil {
					return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "bad-field-match", msg: verr.Error()}
				}
				// Compute the lowered expression (pipeline → final expr string).
				rawVal := strings.TrimSpace(t.Expr)
				if len(t.Stages) > 0 {
					lowered, used, perr := lowerPipe(t.Expr, t.Stages, table)
					if perr != nil {
						msg := strings.TrimPrefix(perr.Error(), "codegen: ")
						return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
					}
					recordPkgs(used)
					rawVal = lowered
				}
				// When probeWrap=true (analyze skeleton), wrap in _gsxunwrap so the
				// skeleton tolerates (T, error) tuples while still checking field types.
				// rawVal is stored unwrapped so genChildComponent can hoist it.
				// ONLY a function-call expression can yield a (T, error) tuple in
				// single-value position; literals/idents/selectors are always
				// single-valued. Wrapping a NON-call value is harmful: Go infers
				// _gsxunwrap's T from an untyped constant using its DEFAULT type
				// (_gsxunwrap(100) → int), which then fails to assign to a
				// non-default-typed field (e.g. float64). So only wrap CALL exprs;
				// non-call values are emitted inline, preserving untyped assignability.
				// (nil is not a call, so it is also left as-is.)
				fieldVal := rawVal
				if probeWrap && isCallExpr(rawVal) {
					fieldVal = fmt.Sprintf("_gsxunwrap(%s)", rawVal)
				}
				isNF := nodeFields[fn]
				var str string
				if isNF {
					str = fmt.Sprintf("%s: %s.Val(%s)", fn, rtPkg, fieldVal)
				} else {
					str = fmt.Sprintf("%s: %s", fn, fieldVal)
				}
				fields = append(fields, propFieldEntry{
					str:         str,
					ea:          t,
					rawVal:      rawVal,
					fieldName:   fn,
					isNodeField: isNF,
				})
			} else {
				val := strings.TrimSpace(t.Expr)
				if len(t.Stages) > 0 {
					lowered, used, perr := lowerPipe(t.Expr, t.Stages, table)
					if perr != nil {
						msg := strings.TrimPrefix(perr.Error(), "codegen: ")
						return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
					}
					recordPkgs(used)
					val = lowered
				}
				bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), val))
			}
		case *ast.BoolAttr:
			if fn, isProp := matchField(declared, t.Name, fm); isProp {
				if verr := validateMatchedField(fn, t.Name, propsType, declared); verr != nil {
					return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "bad-field-match", msg: verr.Error()}
				}
				if nodeFields[fn] {
					fields = append(fields, propFieldEntry{str: fmt.Sprintf("%s: %s.Val(true)", fn, rtPkg)})
				} else {
					fields = append(fields, propFieldEntry{str: fmt.Sprintf("%s: true", fn)})
				}
			} else {
				bag = append(bag, fmt.Sprintf("{Key: %s, Value: true}", strconv.Quote(t.Name)))
			}
		case *ast.MarkupAttr:
			// A markup attribute (`header={ <h1/> }`) is a NAMED slot bound to a
			// declared `gsx.Node` prop. Its name must be a valid Go identifier because
			// it maps directly to a field via fieldName (capitalize-first only — no
			// kebab→Camel). Non-identifier names (e.g. "data-x") are a clear error.
			// This is distinct from StaticAttr/ExprAttr/BoolAttr where the FieldMatcher
			// can map kebab names to CamelCase fields.
			if !token.IsIdentifier(t.Name) {
				msg := fmt.Sprintf("non-identifier attribute %q on component <%s> (slot names must be valid Go identifiers)", t.Name, el.Tag)
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "non-identifier-slot", msg: msg}
			}
			val, verr := slotValue(t.Value)
			if verr != nil {
				return nil, "", nil, verr
			}
			fields = append(fields, propFieldEntry{str: fmt.Sprintf("%s: %s", fieldName(t.Name), val)})
		case *ast.ClassAttr:
			// Only a composable class={…} is in scope; a composable style={…} stays
			// unsupported.
			if t.Name != "class" {
				msg := fmt.Sprintf("%s attribute on a component (<%s>) not supported yet", t.Name, el.Tag)
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			entry, used, eerr := classEntryExpr(b, interpTemp, t, rtPkg, mergeExpr, table, resolved, probeWrap)
			if eerr != nil {
				return nil, "", nil, eerr
			}
			recordPkgs(used)
			bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), entry))
		case *ast.SpreadAttr:
			spreadExpr := strings.TrimSpace(t.Expr)
			if len(t.Stages) > 0 {
				lowered, used, perr := lowerPipe(t.Expr, t.Stages, table)
				if perr != nil {
					msg := strings.TrimPrefix(perr.Error(), "codegen: ")
					return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
				}
				recordPkgs(used)
				spreadExpr = lowered
			}
			mergeChain = append(mergeChain, fmt.Sprintf(".Merge(%s)", spreadExpr))
		case *ast.CondAttr:
			condExpr, used, cerr := condAttrsExpr(t, rtPkg, el.Tag, mergeExpr, table)
			if cerr != nil {
				return nil, "", nil, cerr
			}
			recordPkgs(used)
			mergeChain = append(mergeChain, fmt.Sprintf(".Merge(%s)", condExpr))
		case *ast.OrderedAttrsAttr:
			fn, ok := matchField(declared, t.Name, fm)
			if !ok {
				msg := fmt.Sprintf("ordered-attrs literal {{ }} on <%s> attribute %q matches no field of %s and cannot fall through to the Attrs bag; declare a gsx.Attrs field to receive it", el.Tag, t.Name, propsType)
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "ordered-attrs-no-field", msg: msg}
			}
			if verr := validateMatchedField(fn, t.Name, propsType, declared); verr != nil {
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "bad-field-match", msg: verr.Error()}
			}
			// Collect per-pair info for genChildComponent's tuple-hoist pass.
			pairEntries := make([]oaPairEntry, len(t.Pairs))
			for i, pr := range t.Pairs {
				pairEntries[i] = oaPairEntry{key: pr.Key, rawVal: pr.Value}
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%s: %s.Attrs{", fn, rtPkg)
			for _, pr := range t.Pairs {
				// When probeWrap=true (skeleton path), wrap each CALL pair value
				// with _gsxunwrap(...) so the skeleton tolerates (T, error) tuples
				// while still type-checking the value as the first return (any).
				// Only calls can be tuples; non-call values stay inline (the pair
				// field is `any`, so wrapping is unnecessary and we keep it
				// consistent with the ExprAttr path). When probeWrap=false (emit
				// path), inline the raw value; tuple pairs are hoisted by
				// genChildComponent before this literal is built.
				val := pr.Value
				if probeWrap && isCallExpr(val) {
					val = fmt.Sprintf("_gsxunwrap(%s)", val)
				}
				fmt.Fprintf(&sb, "{Key: %s, Value: %s}, ", strconv.Quote(pr.Key), val)
			}
			sb.WriteString("}")
			fields = append(fields, propFieldEntry{
				str:       sb.String(),
				fieldName: fn,
				oa:        t,
				oaPairs:   pairEntries,
			})
		case *ast.EmbeddedAttr:
			msg := fmt.Sprintf("embedded %s attribute literal %q cannot be used as a component prop on <%s>; pass an ordinary prop value or move the literal to an element inside the component", embeddedLangName(t.Lang), t.Name, el.Tag)
			return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
		default:
			msg := fmt.Sprintf("unknown attribute %T on component (<%s>)", a, el.Tag)
			return nil, "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unsupported-component-attr", msg: msg}
		}
	}
	if len(el.Children) > 0 {
		// BYO: {children} requires an explicit `Children gsx.Node` field. Missing →
		// a clear error (the author adds it; gsx auto-adds only for a .gsx-declared
		// struct, which fieldsFromGsxStruct already reported via hasChildren).
		if isByoChild && !byoStr.hasChildren {
			msg := fmt.Sprintf("component <%s> is passed children but its Props type %s has no `Children gsx.Node` field", el.Tag, propsType)
			return nil, "", nil, &attrError{pos: el.Pos(), end: el.End(), code: "byo-missing-children", msg: msg}
		}
		val, verr := slotValue(el.Children)
		if verr != nil {
			return nil, "", nil, verr
		}
		fields = append(fields, propFieldEntry{str: "Children: " + val})
	}
	if len(bag) > 0 || len(mergeChain) > 0 {
		// BYO: unmatched attrs route to an explicit `Attrs gsx.Attrs` field. Missing
		// → a clear error (the author adds it and spreads it in the markup).
		if isByoChild && !byoStr.hasAttrs {
			msg := fmt.Sprintf("attribute on <%s> matches no field of its Props type %s and %s has no `Attrs gsx.Attrs` field", el.Tag, propsType, propsType)
			return nil, "", nil, &attrError{pos: el.Pos(), end: el.End(), code: "byo-missing-attrs", msg: msg}
		}
		attrsExpr := fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(bag, ", "))
		attrsExpr += strings.Join(mergeChain, "")
		fields = append(fields, propFieldEntry{str: "Attrs: " + attrsExpr})
	}
	return fields, "", usedPkgs, nil
}

// classEntryExpr lowers a composable class={…} ClassAttr to a runtime
// ClassJoin(...) call producing the RAW (un-merged) class string for the Attrs
// bag's "class" entry — the bag is consumed via Attrs.Class() and merged exactly
// once at the consuming root, so merging here would be redundant (and, with a
// Tailwind-style merger, double the per-element cost). It mirrors
// emitRootComposedClass's part lowering (an
// unconditional part → <rtPkg>.Class(<Expr>); a conditional part →
// <rtPkg>.ClassIf(<Expr>, <Cond>)) so the value the child root merges is built
// the same way regardless of whether the class sits on the root or a child.
// usedPkgs (alias→pkgPath) reports the filter packages any lowered part references
// so the caller imports them; an unknown filter surfaces as an *attrError
// positioned at the ClassAttr.
// b and interpTemp are needed to hoist value-form CF (if/switch) parts: the
// hoisted var+if/switch statements are written to b before the containing call.
// When b is nil (conditional-attr branch context), CF parts are unsupported.
func classEntryExpr(b *bytes.Buffer, interpTemp *int, a *ast.ClassAttr, rtPkg string, mergeExpr string, table filterTable, resolved map[ast.Node]types.Type, probeWrap bool) (string, map[string]string, error) {
	parts := make([]string, 0, len(a.Parts))
	usedPkgs := map[string]string{}
	ordered := !probeWrap && composedPartsOrdered(a, resolved)
	for i := range a.Parts {
		p := &a.Parts[i]
		if p.CF != nil {
			if b == nil || interpTemp == nil {
				return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unsupported-component-attr", msg: "value-form if/switch in class on a component conditional-attr branch not supported yet"}
			}
			tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
			*interpTemp++
			fmt.Fprintf(b, "\t\tvar %s string\n", tmp)
			var lowerErr error
			armExpr := func(arm *ast.ValueArm) (string, bool) {
				expr, used, err := lowerClassPartSeed(ast.ClassPart{Expr: arm.Expr, Stages: arm.Stages}, table)
				if err != nil {
					lowerErr = &attrError{pos: a.Pos(), end: a.End(), code: "unresolved-pipeline", msg: err.Error()}
					return "", false
				}
				maps.Copy(usedPkgs, used)
				if probeWrap && isCallExpr(expr) {
					// Skeleton mode: wrap call exprs with _gsxunwrap so the
					// assignment _gsxvN = _gsxunwrap(cls(v)) compiles even when
					// cls returns (T, error). b and interpTemp are non-nil here
					// (guarded above). resolved is nil in skeleton mode, so the
					// emit-mode check below is skipped.
					expr = fmt.Sprintf("_gsxunwrap(%s)", expr)
				} else if !probeWrap {
					// Emit mode: consult resolved to detect and hoist (T, error)
					// tuples. hoistTuple writes the unwrap into b at this point —
					// after the if/case label and before the _gsxvN = assignment —
					// so the hoist lands inside the correct block.
					if t := resolved[arm]; t != nil {
						if _, isTuple := t.(*types.Tuple); isTuple {
							if _, ok := tupleUnwrapType(t); !ok {
								lowerErr = &attrError{pos: arm.Pos(), end: arm.End(), code: "invalid-tuple", msg: fmt.Sprintf("class value-form arm %q returns %s; only (T, error) is supported", arm.Expr, t)}
								return "", false
							}
							expr = hoistTuple(b, expr, interpTemp)
						}
					}
				}
				return expr, true
			}
			var cfOK bool
			if p.CF.If != nil {
				cfOK = emitValueIf(b, p.CF.If, tmp, armExpr)
			} else {
				cfOK = emitValueSwitch(b, p.CF.Switch, tmp, armExpr)
			}
			if !cfOK {
				msg := "value-form CF arm lowering failed"
				if lowerErr != nil {
					msg = strings.TrimPrefix(lowerErr.Error(), "codegen: ")
				}
				return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unresolved-pipeline", msg: msg}
			}
			parts = append(parts, fmt.Sprintf("%s.Class(%s)", rtPkg, tmp))
			continue
		}
		expr, used, err := lowerClassPartSeed(*p, table)
		if err != nil {
			msg := strings.TrimPrefix(err.Error(), "codegen: ")
			return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unresolved-pipeline", msg: msg}
		}
		maps.Copy(usedPkgs, used)
		if p.Cond == "" {
			// Unconditional plain part: in probe mode stub call exprs with "" so
			// the skeleton's gsx.Class("") compiles regardless of the call's return
			// type. The string constraint IS re-imposed by gsx.Class in the emitted
			// code, so a wrong type still fails to compile — the stub only defers
			// that check out of the skeleton so the clean emit-time "invalid-tuple"
			// diagnostic can fire first for ALL non-(T,error) tuples.
			// _gsxuseq probe (emitProbes) handles both liveness and type harvest.
			//
			// In emit mode, check resolved for a tuple and hoist it.
			if probeWrap && isCallExpr(expr) {
				expr = `""`
			} else if !probeWrap {
				t := resolved[p]
				if tup, isAny := t.(*types.Tuple); isAny {
					if _, ok2 := tupleUnwrapType(tup); !ok2 {
						return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "invalid-tuple", msg: fmt.Sprintf("class part %q returns %s; only (T, error) is supported", p.Expr, t)}
					}
					if b == nil || interpTemp == nil {
						return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "unsupported-component-attr", msg: "tuple-returning class part in a conditional-attr branch not supported yet"}
					}
					expr = hoistTuple(b, expr, interpTemp)
				} else if ordered {
					if b == nil || interpTemp == nil {
						return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "unsupported-component-attr", msg: "ordered class part in a conditional-attr branch not supported yet"}
					}
					tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
					*interpTemp++
					fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
					expr = tmp
				}
			}
			parts = append(parts, fmt.Sprintf("%s.Class(%s)", rtPkg, expr))
		} else {
			if !probeWrap && ordered {
				if b == nil || interpTemp == nil {
					return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "unsupported-component-attr", msg: "ordered class part in a conditional-attr branch not supported yet"}
				}
				if t, isTuple := resolved[p].(*types.Tuple); isTuple {
					if _, ok := tupleUnwrapType(t); !ok {
						return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "invalid-tuple", msg: fmt.Sprintf("class part %q returns %s; only (T, error) is supported", p.Expr, t)}
					}
					expr = hoistTuple(b, expr, interpTemp)
				} else {
					tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
					*interpTemp++
					fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
					expr = tmp
				}
				condTmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				fmt.Fprintf(b, "\t\t%s := %s\n", condTmp, strings.TrimSpace(p.Cond))
				parts = append(parts, fmt.Sprintf("%s.ClassIf(%s, %s)", rtPkg, expr, condTmp))
			} else {
				parts = append(parts, fmt.Sprintf("%s.ClassIf(%s, %s)", rtPkg, expr, strings.TrimSpace(p.Cond)))
			}
		}
	}
	// Raw join (no merger): the bag's class is consumed via Attrs.Class() and
	// merged exactly once at the consuming root, so merging here would double the
	// work. mergeExpr is intentionally unused.
	_ = mergeExpr
	return fmt.Sprintf("%s.ClassJoin(%s)", rtPkg, strings.Join(parts, ", ")), usedPkgs, nil
}

// condAttrsExpr lowers a conditional attribute { if cond { … } else { … } } on a
// component to a
//
//	<rtPkg>.AttrsCond(<cond>, func() <rtPkg>.Attrs { return <rtPkg>.Attrs{<then>} }, <else>)
//
// call: the branches are emitted as THUNKS so only the taken branch's attrs are
// evaluated at runtime — matching real Go `if/else`, where the untaken branch's
// expressions (e.g. `u.Name` when `u == nil`) never run. then/else entries are
// built from the branch's static/expr/bool attrs (and a nested composable class
// via classEntryExpr); the else argument is the bare literal `nil` (not a thunk)
// when there is no else branch. Nesting stays shallow — a CondAttr nested inside
// a branch is unsupported.
func condAttrsExpr(t *ast.CondAttr, rtPkg, tag string, mergeExpr string, table filterTable) (string, map[string]string, error) {
	usedPkgs := map[string]string{}
	thenLit, thenUsed, err := condBranchAttrs(t.Then, rtPkg, tag, mergeExpr, table)
	if err != nil {
		return "", nil, err
	}
	maps.Copy(usedPkgs, thenUsed)
	thenThunk := fmt.Sprintf("func() %s.Attrs { return %s }", rtPkg, thenLit)
	elseArg := "nil"
	if len(t.Else) > 0 {
		elseLit, elseUsed, err := condBranchAttrs(t.Else, rtPkg, tag, mergeExpr, table)
		if err != nil {
			return "", nil, err
		}
		maps.Copy(usedPkgs, elseUsed)
		elseArg = fmt.Sprintf("func() %s.Attrs { return %s }", rtPkg, elseLit)
	}
	return fmt.Sprintf("%s.AttrsCond(%s, %s, %s)", rtPkg, strings.TrimSpace(t.Cond), thenThunk, elseArg), usedPkgs, nil
}

// condBranchAttrs builds a <rtPkg>.Attrs{…} literal from one conditional-attr
// branch's attrs. Static/expr/bool attrs become bag entries keyed by raw name; a
// composable class={…} becomes a ClassJoin entry. A spread or nested
// conditional inside a branch is unsupported (kept shallow).
func condBranchAttrs(attrs []ast.Attr, rtPkg, tag string, mergeExpr string, table filterTable) (string, map[string]string, error) {
	var entries []string
	usedPkgs := map[string]string{}
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.StaticAttr:
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(t.Value)))
		case *ast.ExprAttr:
			if len(t.Stages) > 0 {
				msg := fmt.Sprintf("pipeline in a conditional attribute branch (<%s>) not supported yet", tag)
				return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strings.TrimSpace(t.Expr)))
		case *ast.BoolAttr:
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: true}", strconv.Quote(t.Name)))
		case *ast.ClassAttr:
			if t.Name != "class" {
				msg := fmt.Sprintf("%s attribute in a conditional branch (<%s>) not supported yet", t.Name, tag)
				return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			// nil, nil, nil, false: CF parts in conditional-attr class branches are
			// unsupported (they require statement-level hoisting into the outer body,
			// not inside an inline closure); classEntryExpr returns an error if CF is
			// present. Tuple parts are also unsupported (b=nil prevents hoisting).
			entry, used, eerr := classEntryExpr(nil, nil, t, rtPkg, mergeExpr, table, nil, false)
			if eerr != nil {
				return "", nil, eerr
			}
			maps.Copy(usedPkgs, used)
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), entry))
		case *ast.EmbeddedAttr:
			msg := fmt.Sprintf("embedded %s attribute literal %q cannot be used as a component prop on <%s>; pass an ordinary prop value or move the literal to an element inside the component", embeddedLangName(t.Lang), t.Name, tag)
			return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
		default:
			msg := fmt.Sprintf("unsupported attribute %T in a conditional branch (<%s>)", a, tag)
			return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unsupported-component-attr", msg: msg}
		}
	}
	return fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(entries, ", ")), usedPkgs, nil
}
