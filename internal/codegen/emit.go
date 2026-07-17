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
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/cssmin"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/goexprshape"
	"github.com/gsxhq/gsx/internal/jsmin"
)

// generateFile emits the .x.go for a parsed gsx file given already-resolved
// interpolation types. It returns (nil, false) if any component failed; all
// component errors are recorded in bag (component-boundary recovery continues
// to the next component on failure, so multiple errors are always reported).
func generateFile(file *ast.File, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, fset *token.FileSet, cls *attrclass.Classifier, bag *diag.Bag, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool, merger *ClassMergerRef, positionalPlan componentPositionalPackagePlan) ([]byte, bool) {
	if cls == nil {
		cls = attrclass.Builtin()
	}
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
	// For a language that is NOT minified, re-base its embedded bodies so they do
	// not ship indented to their source markup depth: strip the common leading
	// indentation, keep the author's relative structure.
	rebaseEmbedded(file, !jsMinify, !cssMinify)
	// imports holds the USER's Go-chunk imports plus the filter / type-arg /
	// class-merger packages. It starts empty: nothing is needed until something
	// is emitted. The generator's own imports live in `rt` and are recorded at
	// their emission sites (see rtimports.go) — the discipline `strconv` already
	// followed, now applied to the runtime, context and io as well.
	imports := map[string]bool{}
	rt := rtImports{}
	// Each GoChunk is split: its imports are folded into the import region (they
	// must precede all other declarations) and its non-import remainder flows
	// into the body. A single chunk may carry both — e.g. an import followed by
	// type/func decls. Aliased / dot / blank imports are kept verbatim; plain
	// imports merge into the import set (the generator's own imports are disjoint,
	// carried under reserved aliases in `rt`, so nothing here can collide).
	var aliased []importSpec
	importAliases := map[string]string{}
	// userPlainImports records the paths the user plain-imported in a GoChunk
	// (referenced by their own package name in user Go code). When such a path is
	// ALSO a filter package, the filter calls qualify it under its reserved alias
	// (_gsxf<i>) while user code still says `<pkg>.X` — so writeImports must emit
	// BOTH the reserved-alias line and the plain line (Go allows the same path
	// under different names). This mirrors the probe skeleton (analyze.go), keeping
	// emit ≡ probe.
	userPlainImports := map[string]bool{}
	// typeArgAliases records, path→alias, every inferred cross-package type
	// argument (childTypeArgUse) whose package NAME collided with a name
	// already bound in this file — see boundNames below. Each colliding path
	// gets a fresh reserved "_gsxti<N>" alias (mirroring the skeleton-side
	// aliasAllocator's naming, infer.go), minted once and reused for every
	// later reference to the SAME path within this file.
	typeArgAliases := map[string]string{}
	// boundNames is the live name→path table childTypeArgUse's qf consults to
	// decide whether an inferred type argument's package can print as plain
	// pkg.Name(): printing it verbatim when that NAME is already bound to a
	// DIFFERENT path would emit two import lines under the same identifier —
	// "name redeclared in this block" at `go build`, even though generate
	// exited 0 (the hard invariant: generate must never emit non-compiling
	// output). Seeded here with every name this file's import region ALREADY
	// binds — the reserved filter/class-merger alias family (see writeImports)
	// and the user's own GoChunk imports (below, resolved to their ACTUAL
	// declared package name via currentPkg.Imports() — the skeleton's own
	// direct-import list, so this is exact, never a last-path-segment
	// heuristic) — then kept live by qf itself as it plain-imports or aliases
	// further paths, so a LATER same-name collision within the same file is
	// still caught (see childTypeArgUse's qf).
	//
	// The generator binds no plain package names any more — every generator
	// reference goes through a reserved `_gsx` alias (rtimports.go), and an
	// inferred type argument's package name can never begin with `_gsx` — so
	// nothing is pre-bound here.
	boundNames := map[string]string{}
	// Build the path→reserved-alias map for the FILTER and RENDERER packages,
	// harvested from the table (every entry in EITHER map records its owning
	// package's alias + path) — computed up front (table doesn't depend on the
	// components below) so boundNames can be seeded with the reserved alias
	// family before any component generates. A path in `imports` that is a
	// filter or renderer package is emitted under its reserved alias; std
	// keeps _gsxstd so std-only output is byte-identical to before. The class
	// merger's reserved alias is ALWAYS reserved here (whether or not the merger
	// ends up used — see below), so an inferred type argument can never collide
	// with it either. Renderer packages join filterAlias/boundNames here so a
	// renderer-only package that a later render-boundary
	// task references can be import-emitted the same way a filter package is.
	filterAlias := map[string]string{}
	for _, e := range table.filters {
		filterAlias[e.pkgPath] = e.alias
		boundNames[e.alias] = e.pkgPath
	}
	for _, e := range table.renderers {
		if e.local {
			continue
		}
		filterAlias[e.pkgPath] = e.alias
		boundNames[e.alias] = e.pkgPath
	}
	boundNames[classMergerAlias] = ""
	// Empty means "the runtime's default merger", resolved at each use site by
	// classMergeExpr. It is NOT spelled out here: naming gsx.DefaultClassMerge
	// eagerly would record a runtime import need in every file, including files
	// that emit nothing at all.
	mergeExpr := ""
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
					// Record the ACTUAL declared package name this plain import
					// binds (resolved via currentPkg's own direct-import list,
					// populated by the skeleton type-check — exact, never a
					// last-path-segment heuristic), so a same-named different-
					// path inferred type argument is caught as a collision by
					// childTypeArgUse's qf. A miss (name == "") can't happen on
					// any path that reaches generateFile: a broken/unresolved
					// import would have left type errors, and generateFile only
					// runs once the package's type-check is clean.
					if name := importedPkgName(currentPkg, s.path); name != "" {
						boundNames[name] = s.path
					}
				} else {
					aliased = append(aliased, s)
					if s.name != "." && s.name != "_" {
						importAliases[s.path] = s.name
						boundNames[s.name] = s.path
					}
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
			if genComponent(&cbuf, v, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, &interpTemp, fset, cls, bag, mergeExpr, positionalPlan) {
				body.Write(cbuf.Bytes())
			} else {
				ok = false
			}
		case *ast.GoWithElements:
			// A top-level Go region that has one or more gsx elements embedded
			// directly in expression position (e.g. `var help = <a href={u}>{
			// label }</a>`). Each GoText part is verbatim Go source (identical to
			// a GoChunk's own body text); each *Element part is replaced inline by
			// its lowered gsx.Node VALUE — concatenating the parts reproduces the
			// original expression with the element replaced by
			// `gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { … })`,
			// so whatever Go construct expected an expression there (a var
			// initializer, a call argument, …) still sees one. Recovery boundary,
			// like *ast.Component above: on failure the diagnostic is already in
			// bag, so this decl's (partial) output is dropped and codegen
			// continues to the next top-level decl.
			//
			// Unlike GoChunk, this does NOT run splitChunk to hoist any `import`
			// spec ahead of the file's own import block: a GoText part is emitted
			// verbatim in its original textual position. It does not need to — the
			// parser peels a leading run of import declarations off the region into
			// its own GoChunk before it becomes a GoWithElements (parser/goexpr.go
			// leadingImportEnd), and that GoChunk's imports ARE hoisted here (the
			// case above). Only a stray `import` placed AFTER some non-import
			// declaration in the region reaches this verbatim path — that is invalid
			// Go, not specially diagnosed here; it surfaces as a gofmt parse failure
			// from generateFile's closing format.Source call (fail-safe: never
			// silently emits broken output).
			// gsx fmt may have wrapped a bare-operand element/fragment (an
			// assignment RHS, return operand, or keyed composite-literal field —
			// never a call argument or bare composite-literal element, where the
			// parens are real call/list syntax) in a decorative "(" ")" purely for
			// source readability (see internal/printer's parenWrapDoc). Those bytes
			// must never reach this closure splice: a newline before the closure's
			// own trailing "}"/")" trips Go's automatic semicolon insertion (see
			// emitSkeletonBlockLine's doc comment for the same hazard elsewhere).
			// goWithElementsParenShapes classifies each element/fragment the same
			// way the printer does; parenStrip{Trailing,Leading} below drop the
			// matching decorative paren (plus its surrounding whitespace) from the
			// adjacent GoText before it's spliced in — the closure text itself is
			// unaffected either way.
			shapes := goWithElementsParenShapes(v)
			var wbuf bytes.Buffer
			partsOK := true
			for i, part := range v.Parts {
				switch p := part.(type) {
				case ast.GoText:
					src := p.Src
					if i > 0 && parenWrappable(v.Parts[i-1], shapes, i-1) {
						src = goexprshape.StripLeadingParen(src)
					}
					if i < len(v.Parts)-1 && parenWrappable(v.Parts[i+1], shapes, i+1) {
						src = goexprshape.StripTrailingParen(src)
					}
					wbuf.WriteString(src)
				case *ast.Element:
					// A top-level Go-expression element literal has NO enclosing gsx
					// component — there is no synthesized `attrs` local anywhere in
					// scope, so enclosingAttrsBound is always false here.
					if !emitElementValue(&wbuf, p, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, &interpTemp, fset, cls, bag, mergeExpr, false, positionalPlan) {
						partsOK = false
					}
				case *ast.Fragment:
					if !emitFragmentValue(&wbuf, p, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, &interpTemp, fset, cls, bag, mergeExpr, false, positionalPlan) {
						partsOK = false
					}
				case *ast.EmbeddedInterp:
					// A prefixed literal in Go-expression position. f`…` lowers to a
					// plain Go string concat; js`…`/css`…` lower to _gsxrt.RawJS/RawCSS
					// wrapping the same concat with per-hole contextual escaping
					// (JSCtx-selected escaper / StyleValue-FilterCSS semantics).
					// Expression positions have no statement context: exprPos forbids
					// hoists, so error-carrying holes are rejected (see embeddedHoleExpr)
					// and concat order is source order.
					if len(p.Stages) > 0 {
						bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
						partsOK = false
					} else if !emitGoExprEmbeddedInterp(&wbuf, &wbuf, p, resolved, table, imports, rt, &interpTemp, bag, false, false) {
						partsOK = false
					}
				default:
					bag.Errorf(part.Pos(), part.End(), "unsupported-node", "unsupported Go-expression part %T", part)
					partsOK = false
				}
				if !partsOK {
					break
				}
			}
			if partsOK {
				body.Write(wbuf.Bytes())
			} else {
				ok = false
			}
		}
	}

	if !ok {
		return nil, false
	}

	// The class merger package (if configured) is registered here under its
	// reserved alias _gsxcm so writeImports emits `_gsxcm "<pkgPath>"` — but
	// only if the body actually contains at least one merge-site reference to
	// avoid emitting an unused import in files with no class attributes.
	// filterAlias itself (and the classMergerAlias reservation in boundNames)
	// were already built above, before any component generated — see there.
	if merger != nil && bytes.Contains(body.Bytes(), []byte(classMergerAlias+".")) {
		imports[merger.PkgPath] = true
		filterAlias[merger.PkgPath] = classMergerAlias
	}
	if sourcePath := fset.Position(file.Pos()).Filename; sourcePath != "" {
		if allocator := positionalPlan.imports[sourcePath]; allocator != nil {
			aliased = append(aliased, allocator.specs()...)
		}
	}

	// A filter package's reserved-alias import line (`_gsxf<i> "<path>"`) is
	// emitted ONLY when the generated body actually references that alias —
	// mirroring the class-merger gate just above. A path can land in `imports`
	// as a filter package because the pipeline lowering recorded it (genInterp
	// et al. → imports[path]=true), OR because the user PLAIN-imported it in a
	// GoChunk for their own Go code (a configured filter package they never use
	// as a |> filter). Only the former puts `<alias>.` in the body; without this
	// gate the latter emits a spurious unused alias next to the user's own plain
	// import ("imported as _gsxf<i> and not used" — one-learning ui/once.gsx,
	// which imports structpages for OnceScopeMiddleware). The alias is _gsx-
	// prefixed and collision-safe, so a body reference is an exact usage signal.
	usedFilterPkg := map[string]bool{}
	for path, alias := range filterAlias {
		if bytes.Contains(body.Bytes(), []byte(alias+".")) {
			usedFilterPkg[path] = true
		}
	}

	var b bytes.Buffer
	b.WriteString("// Code generated by gsx; DO NOT EDIT.\n")
	if dirs := goDirectiveLines(file.Doc); len(dirs) > 0 {
		// Program-significant comments pass through verbatim. Placement rule:
		// after the generated-code marker, blank-line-separated from the
		// package clause, satisfying both the marker convention and the
		// //go:build placement rules.
		b.WriteString("\n")
		for _, d := range dirs {
			b.WriteString(d)
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\npackage %s\n\n", file.Package)
	writeImports(&b, imports, rt, aliased, filterAlias, usedFilterPkg, userPlainImports, typeArgAliases)
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

// importedPkgName resolves path's ACTUAL declared package name via
// currentPkg's own direct-import list (currentPkg.Imports(), populated by the
// skeleton type-check) — the same information the compiler itself used to
// bind a plain `import "path"` to its package-clause name, so this is exact,
// never a last-path-segment heuristic (a package's declared name need not
// match its import path's final element). Returns "" if currentPkg is nil or
// path isn't one of its direct imports.
func importedPkgName(currentPkg *types.Package, path string) string {
	if currentPkg == nil {
		return ""
	}
	for _, imp := range currentPkg.Imports() {
		if imp.Path() == path {
			return imp.Name()
		}
	}
	return ""
}

// goDirectiveLines extracts the program-significant comment lines from a
// file's pre-package doc block: `//go:<directive>` (no space after `//` —
// the toolchain's own directive rule) and the legacy `// +build` constraint
// spelling. Prose comments stay .gsx-only, and `//line` is deliberately
// excluded — it would corrupt the //line mapping this generator emits.
func goDirectiveLines(doc string) []string {
	var out []string
	for line := range strings.SplitSeq(doc, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "//go:") && len(l) > len("//go:") && l[len("//go:")] != ' ':
			out = append(out, l)
		case strings.HasPrefix(l, "// +build") || strings.HasPrefix(l, "//+build"):
			out = append(out, l)
		}
	}
	return out
}

// classMergeExpr resolves the class-merger expression at an emission site. An
// empty mergeExpr means the configured merger is absent and the runtime default
// applies — recorded as a runtime need only here, where a class attribute is
// genuinely being emitted. Resolving it eagerly in generateFile (the old
// `mergeExpr := "gsx.DefaultClassMerge"` default) would be harmless as a string,
// but recording the runtime import need there would mark EVERY file as needing
// the runtime, including files that emit no gsx at all.
func classMergeExpr(mergeExpr string, rt rtImports) string {
	if mergeExpr != "" {
		return mergeExpr
	}
	return rt.rt() + ".DefaultClassMerge"
}

// writeImports emits the generated file's import region. imports is the set of
// plain import paths the USER's Go chunks need, plus the filter / class-merger
// packages; rt is the disjoint set of paths the GENERATOR emitted references to,
// each printed under its reserved `_gsx` alias (see rtimports.go) so it can never
// be shadowed by, or collide with, whatever the user bound those names to.
// aliased carries verbatim user GoChunk imports; filterAlias (path→reserved
// alias) names any FILTER package among
// `imports` so its import line uses the reserved alias the lowered calls
// reference (e.g. `_gsxstd "<std>"`, `_gsxf0 "<user>"`). typeArgAliases
// (path→reserved alias) names every inferred cross-package type argument
// (childTypeArgUse) whose package name collided with a name already bound in
// this file — see generateFile's boundNames doc — under its own fresh
// "_gsxti<N>" alias (never plain-imported: they're emitted from this map, not
// from `imports`). Import-region order: plain std, blank line, plain
// external, generator aliases, filter aliases (sorted by alias), type-arg
// aliases (sorted by alias), user-aliased (sorted by path) — the final
// intra-group ordering is gofmt's (generateFile runs format.Source over the
// result, and gofmt sorts each blank-line-delimited group by path).
//
// When every set is empty — a .gsx carrying only plain Go, or nothing at all —
// NO import block is emitted: an empty `import ()` is legal but noisy, and the
// old unconditional `context`/`io`/gsx seed made `go build` fail with three
// "imported and not used" errors while `gsx generate` exited 0.
func writeImports(b *bytes.Buffer, imports map[string]bool, rt rtImports, aliased []importSpec, filterAlias map[string]string, usedFilterPkg map[string]bool, userPlainImports map[string]bool, typeArgAliases map[string]string) {
	// The generator's own imports, always under their reserved aliases. Kept
	// separate from `imports` so a user's plain import of the SAME path (e.g.
	// they wrote `gsx.Node` in a Go chunk) emits its own line — Go permits one
	// path under two names, and the generator must never depend on, or be
	// satisfied by, whatever the user bound that name to. This is the same
	// one-path-two-names pattern userPlainImports already uses below for filter
	// packages.
	type rtImp struct{ alias, path string }
	var rts []rtImp
	for _, e := range []rtImp{
		{ctxAlias, "context"},
		{ioAlias, "io"},
		{scAlias, "strconv"},
		{stAlias, "strings"},
		{rtAlias, gsxRuntimePath},
	} {
		if rt[e.path] {
			rts = append(rts, e)
		}
	}

	if len(imports) == 0 && len(rts) == 0 && len(aliased) == 0 && len(typeArgAliases) == 0 {
		return // nothing to import — emit no import block at all
	}

	var std, ext []string
	type filterImp struct{ alias, path string }
	var filters []filterImp
	for imp := range imports {
		switch {
		case filterAlias[imp] != "" && usedFilterPkg[imp]:
			// A filter package whose reserved alias is ACTUALLY referenced in the
			// body: the lowered calls reference <alias>.<Func>; the import MUST use
			// exactly that reserved alias (collision-safe — no user symbol can start
			// with _gsx). Emit it separately, not in the plain ext loop. A configured
			// filter package the user merely plain-imported (usedFilterPkg false)
			// falls through to the plain ext/std branch — no spurious unused alias.
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
	type typeArgImp struct{ alias, path string }
	typeArgImps := make([]typeArgImp, 0, len(typeArgAliases))
	for path, alias := range typeArgAliases {
		typeArgImps = append(typeArgImps, typeArgImp{alias: alias, path: path})
	}
	sort.Slice(typeArgImps, func(i, j int) bool { return typeArgImps[i].alias < typeArgImps[j].alias })
	b.WriteString("import (\n")
	for _, imp := range std {
		fmt.Fprintf(b, "\t%q\n", imp)
	}
	if len(std) > 0 && (len(ext) > 0 || len(aliased) > 0 || len(filters) > 0 || len(typeArgImps) > 0 || len(rts) > 0) {
		b.WriteString("\n")
	}
	for _, imp := range ext {
		fmt.Fprintf(b, "\t%q\n", imp)
	}
	for _, r := range rts {
		fmt.Fprintf(b, "\t%s %q\n", r.alias, r.path)
	}
	for _, f := range filters {
		fmt.Fprintf(b, "\t%s %q\n", f.alias, f.path)
	}
	for _, f := range typeArgImps {
		fmt.Fprintf(b, "\t%s %q\n", f.alias, f.path)
	}
	for _, imp := range aliased {
		fmt.Fprintf(b, "\t%s %q\n", imp.name, imp.path)
	}
	b.WriteString(")\n\n")
}

func genComponent(b *bytes.Buffer, c *ast.Component, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, positionalPlan componentPositionalPackagePlan) bool {
	if _, err := normalizedTypeParams(c.TypeParams); err != nil {
		// Validate type parameters before ordinary/reserved parameters and the
		// receiver. Their types may refer to these declarations, so a malformed
		// type-parameter list is the primary signature error and must never be
		// copied through to a later, unpositioned gofmt failure.
		bag.Errorf(c.Pos(), c.End(), "invalid-syntax", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}
	declarationParams, err := parseComponentParamDecls(c.Params)
	if err != nil {
		bag.Errorf(c.Pos(), c.End(), "invalid-syntax", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}
	for _, parameter := range declarationParams {
		if parameter.name == "ctx" {
			bag.Errorf(c.Pos(), c.End(), "reserved-param", "param name %q is reserved (ambient context)", parameter.name)
			return false
		}
		if strings.HasPrefix(parameter.name, reservedPrefix) {
			bag.Errorf(c.Pos(), c.End(), "reserved-param", "param name %q uses the reserved _gsx prefix", parameter.name)
			return false
		}
	}
	typeParamsDecl := typeParamDecl(c.TypeParams)
	hasAttrs := false
	for _, parameter := range declarationParams {
		if parameter.role == declarationParamAttrs {
			hasAttrs = true
			break
		}
	}

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
	}
	if c.Recv != "" && strings.TrimSpace(c.TypeParams) != "" && !toolchainHasGenericMethods() {
		// A generic METHOD component needs a toolchain whose go/parser accepts
		// methods with type parameters (go1.27+); older toolchains reject the
		// emitted skeleton outright, which would otherwise hard-abort the whole
		// run (module_importer). Skip this component with a positioned
		// diagnostic instead — MIRRORS emitComponentSkeleton's guard
		// (analyze.go), which sits in the same relative position (after the
		// recv-parsing block, before declaration emission below).
		bag.Errorf(c.Pos(), c.End(), "unsupported-toolchain",
			"generic method components require a Go toolchain with generic methods (go1.27+); active toolchain: %s", runtime.Version())
		return false
	}

	// Emit exactly the authored declaration. Parameters are already in lexical
	// scope for the nested render closure, so no props type, adapter, or binding
	// locals are required.
	emitLine(b, fset, c.Pos())
	if c.Recv != "" {
		fmt.Fprintf(b, "func %s %s%s(%s) %s.Node {\n", c.Recv, c.Name, typeParamsDecl, strings.TrimSpace(c.Params), rt.rt())
	} else {
		fmt.Fprintf(b, "func %s%s(%s) %s.Node {\n", c.Name, typeParamsDecl, strings.TrimSpace(c.Params), rt.rt())
	}
	fmt.Fprintf(b, "\treturn %s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
	if !emitNodeFuncBody(b, c.Body, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, hasAttrs, positionalPlan) {
		return false
	}
	b.WriteString("\t})\n}\n\n")
	return true
}

// emitNodeFuncBody emits the common body of a gsx.Func render closure: bind
// _gsxgw, declare the numeric scratch buffer if needed, emit each markup node
// via genNode (the shared element/markup lowering — the SAME path a
// component's child elements and an embedded Go-expression element both use),
// then the trailing `return _gsxgw.Err()`. Shared by genComponent's two render
// closures (byo and generated) and emitNodeValue's element/fragment-value
// lowering so there is exactly one place that assembles this scaffolding.
func emitNodeFuncBody(b *bytes.Buffer, nodes []ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
	fmt.Fprintf(b, "\t\t_gsxgw := %s.W(_gsxw)\n", rt.rt())
	emitNumScratch(b, nodes, resolved, table, cls)
	for _, m := range nodes {
		if !genNode(b, m, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
			return false
		}
	}
	b.WriteString("\t\treturn _gsxgw.Err()\n")
	return true
}

// emitNodeValue wraps a markup list as a self-contained gsx.Node VALUE —
// gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { … }) — spliced
// inline in place of the original element/fragment's own source span, e.g.
// `var help = <a href={u}>{ label }</a>` becomes `var help =
// gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { … })`. Shared
// by emitElementValue (one *ast.Element Part of a GoWithElements) and
// emitFragmentValue (one *ast.Fragment Part) — the SAME closure shape either
// way, keyed only on the markup list being lowered. An empty list (an empty
// `<></>` fragment's Children) still produces this same closure shape;
// emitNodeFuncBody simply writes nothing before `return _gsxgw.Err()`, so
// the fragment lowers to a uniform no-op gsx.Func rather than a special case.
//
// No emitLine here (unlike genComponent's declaration line): this wrapper
// carries no user-authored token of its own — it's pure generator
// boilerplate, same as a GoChunk's own raw body text, which also emits no
// //line of its own (see generateFile's *ast.GoChunk case). genNode's OWN
// emitLine call fires immediately below and maps the actual markup/
// interpolation content, which is what matters.
//
// Unlike a component's render closure, this one has NO surrounding component:
// recvVar/recvTypeName are passed "" (no method-receiver dotted-tag
// resolution applies here — there is no receiver in scope) and there is no
// props/attrs/children binding of any kind. Any interpolation inside the
// nodes (`{u}`, `{label}`) is emitted verbatim by genNode/genInterp exactly
// as it is for a component body, so it resolves by ordinary Go closure
// capture against whatever the ENCLOSING Go scope binds — the same as a
// hand-written `gsx.Func(func(...) error { … u … })` literal would. Element
// spread subjects that need gsx.Attrs normalization are converted at their
// semantic spread boundary in emitManualSpreadElement, without changing the
// meaning or scope of unrelated captured identifiers.
//
// Reuses genNode (via emitNodeFuncBody) — the SAME element/markup lowering a
// component body's child elements use — so there is exactly one path from
// markup to emission code; this function only supplies the closure
// scaffolding around it.
func emitNodeValue(b *bytes.Buffer, nodes []ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
	fmt.Fprintf(b, "%s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
	if !emitNodeFuncBody(b, nodes, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, "", "", cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
		return false
	}
	b.WriteString("\t})")
	return true
}

// emitElementValue lowers a gsx element embedded directly in Go-expression
// position (one *ast.Element Part of a GoWithElements) via emitNodeValue.
func emitElementValue(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
	return emitNodeValue(b, []ast.Markup{el}, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan)
}

// emitFragmentValue lowers a gsx fragment embedded directly in Go-expression
// position (one *ast.Fragment Part of a GoWithElements) via emitNodeValue.
// Empty `<></>` → fr.Children is empty → emitNodeFuncBody writes nothing →
// the closure is the uniform no-op gsx.Func (renders nothing) — see
// emitNodeValue's doc comment.
func emitFragmentValue(b *bytes.Buffer, fr *ast.Fragment, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
	return emitNodeValue(b, fr.Children, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan)
}

// emitFallthroughAttrs emits the caller-wins attribute section (between `<tag`
// and the closing `>` / `/>`) for MANUAL mode (emitManualSpreadElement). splitIdx
// is the position of the author's `{ attrs... }` — scalar attrs BEFORE it are
// overridable (guarded `if !Has(name)`, caller-wins); scalar attrs AFTER it are
// FORCED (emitted UNGUARDED so the root always wins) and their names are excluded
// from the bag spread so a same-named bag entry can never emit (root wins).
//
// Cond-attrs follow the same positional rule: a pre-spread `{ if … }` emits
// each branch leaf under a `!Has(name)` guard inside its branch; a post-spread
// one is evaluated exactly ONCE before the spread — branch bodies record the
// taken branch in a bool temp and append their leaf names to a dynamic drop
// slice the spread excludes — and its leaves render after the spread under
// the recorded bool. A class/style leaf inside a branch never reaches this
// function: elementFolds/hasCondClassStyle routes such an element through
// foldElementSpreads instead, which bakes the conditional contribution into
// the bag (spec 2026-07-12: the D3 rejection this used to require is lifted).
//
// class/style are positional-exempt: wherever they appear they MERGE caller-last
// (ClassMerged / StyleMerged), emitted once at the spread position. The author's
// `{ attrs... }` SpreadAttr itself (when present at splitIdx) is consumed here, not
// emitted via emitAttr.
func emitFallthroughAttrs(b *bytes.Buffer, attrs []ast.Attr, splitIdx int, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag, mergeExpr, bagExpr string, nonce *nonceInjection) bool {
	// Find a composed/static class attr to merge the bag's class into, and a
	// composed/static style attr whose declarations the bag's style merges over.
	var classAttr *ast.ClassAttr     // composed class={ … }
	var staticClass *ast.StaticAttr  // static class="x"
	var styleAttr *ast.ClassAttr     // composed style={ … } (CtxCSS ClassAttr)
	var staticStyle *ast.StaticAttr  // static style="x"
	var embedClass *ast.EmbeddedAttr // class=`…@{…}…` backtick literal
	var embedStyle *ast.EmbeddedAttr // style=`…@{…}…` backtick literal
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
		case *ast.EmbeddedAttr:
			if t.Lang == ast.EmbeddedText {
				switch t.Name {
				case "class":
					embedClass = t
				case "style":
					embedStyle = t
				}
			}
		}
	}
	// forcedNames are the statically-named post-spread scalar attrs (idx >
	// splitIdx): the bag spread drops them, so the unguarded root emit wins.
	// They are pure AST facts, collected up front — emission never feeds back
	// into this list. class/style are excluded: they are positional-exempt and
	// merge at the spread site instead.
	var forcedNames []string
	for i, a := range attrs {
		if i <= splitIdx {
			continue
		}
		switch t := a.(type) {
		case *ast.ClassAttr:
			if t == classAttr || t == styleAttr {
				continue
			}
		case *ast.StaticAttr:
			if t == staticClass || t == staticStyle {
				continue
			}
		case *ast.EmbeddedAttr:
			if t == embedClass || t == embedStyle {
				continue
			}
		}
		if name, ok := rootAttrName(a); ok {
			forcedNames = append(forcedNames, name)
		}
	}

	// emitScalar emits one non-class/style attr, either guarded (overridable) or
	// unguarded (forced).
	emitScalar := func(a ast.Attr, guarded bool) bool {
		name, ok := rootAttrName(a)
		if !ok || !guarded {
			// Unguarded: forced (post-spread), or no single static name (Cond) — no
			// caller-wins shadow target. (A SpreadAttr at the split position is
			// consumed below, not here.)
			return emitAttr(b, attrs, a, resolved, table, imports, rt, interpTemp, cls, tag, bag, mergeExpr, nonce)
		}
		fmt.Fprintf(b, "\t\tif !%s.Has(%s) {\n", bagExpr, strconv.Quote(name))
		if !emitAttr(b, attrs, a, resolved, table, imports, rt, interpTemp, cls, tag, bag, mergeExpr, nonce) {
			return false
		}
		b.WriteString("\t\t}\n")
		return true
	}

	// emitCondGuarded emits a PRE-spread cond-attr with every branch leaf
	// caller-overridable: the branch structure is preserved (conditions evaluate
	// once, in place, with else-if short-circuit), each leaf wrapped in the same
	// `!Has(name)` guard as a plain pre-spread scalar.
	var emitCondGuarded func(t *ast.CondAttr) bool
	emitCondGuarded = func(t *ast.CondAttr) bool {
		emitBranch := func(as []ast.Attr) bool {
			for _, inner := range as {
				if nested, ok := inner.(*ast.CondAttr); ok {
					if !emitCondGuarded(nested) {
						return false
					}
					continue
				}
				if !emitScalar(inner, true) {
					return false
				}
			}
			return true
		}
		fmt.Fprintf(b, "\t\tif %s {\n", t.Cond)
		if !emitBranch(t.Then) {
			return false
		}
		if len(t.Else) > 0 {
			b.WriteString("\t\t} else {\n")
			if !emitBranch(t.Else) {
				return false
			}
		}
		b.WriteString("\t\t}\n")
		return true
	}

	// POST-spread cond-attrs force their taken branch's leaves. Planning walks
	// each one once: every branch with direct leaves gets a bool temp, and the
	// leaves are collected into source-ordered runs (a nested cond-attr splits
	// its parent's run so output order matches the original emission order).
	// The selector — emitted just before the spread — evaluates the branch
	// structure exactly once, setting the bools and appending the taken
	// branch's names to the dynamic drop slice.
	postRuns := map[*ast.CondAttr][]condRun{}
	dropVar := ""
	emitPostCondSelectors := func() {
		var post []*ast.CondAttr
		for i, a := range attrs {
			if i <= splitIdx {
				continue
			}
			if t, ok := a.(*ast.CondAttr); ok {
				post = append(post, t)
			}
		}
		if len(post) == 0 {
			return
		}
		dropVar = fmt.Sprintf("_gsxv%d", *interpTemp)
		*interpTemp++
		base := append([]string{"class", "style"}, forcedNames...)
		quotedBase := make([]string, len(base))
		for i, n := range base {
			quotedBase[i] = strconv.Quote(n)
		}
		fmt.Fprintf(b, "\t\t%s := []string{%s}\n", dropVar, strings.Join(quotedBase, ", "))
		for _, t := range post {
			var runs []condRun
			var bools []string
			root := planPostCond(t, &runs, &bools, interpTemp)
			postRuns[t] = runs
			if len(bools) > 0 {
				fmt.Fprintf(b, "\t\tvar %s bool\n", strings.Join(bools, ", "))
			}
			emitPostCondSelector(b, root, dropVar)
		}
	}

	// emitSpread emits the post-cond selectors, the class merge, style merge,
	// then the bag spread (dropping class/style + any forced names — via the
	// dynamic drop slice when post-spread cond-attrs exist). Called once, at
	// the split position.
	emitSpread := func() bool {
		emitPostCondSelectors()
		// If the root had NO class attr at all, emit a merged class in attr position —
		// writes class only when the bag contributes a non-empty token set. An
		// EmbeddedText class literal (embedClass) is emitted in place by Walk 1
		// (emitRootEmbeddedClass), so it is excluded here the same as
		// classAttr/staticClass.
		if classAttr == nil && staticClass == nil && embedClass == nil {
			fmt.Fprintf(b, "\t\t_gsxgw.ClassMerged(%s, %s.Class())\n", classMergeExpr(mergeExpr, rt), bagExpr)
		}
		// Style: when the caller set a `style`, merge it OVER the root's style
		// property-last-wins (StyleMerged, caller-wins). When the caller did NOT set a
		// style, emit the root's own style via its normal context path UNCHANGED — this
		// preserves the composable-style CSS sanitizer (gw.Style → the ZgotmplZ failsafe
		// for an injection, and intra-style duplicate properties) which StyleMerged's
		// `prop: value` parser would lose (it drops colon-less fragments and dedupes
		// properties). The empty-bag case takes the else branch → byte-identical output.
		switch {
		case styleAttr != nil || staticStyle != nil:
			styleStr, styleParts, ok := rootStyleString(b, styleAttr, staticStyle, table, imports, rt, interpTemp, bag, resolved)
			if !ok {
				return false
			}
			fmt.Fprintf(b, "\t\tif %s.Has(\"style\") {\n", bagExpr)
			fmt.Fprintf(b, "\t\t\t_gsxgw.StyleMerged(%s, %s.Style())\n", styleStr, bagExpr)
			b.WriteString("\t\t} else {\n")
			if staticStyle != nil {
				if !emitAttr(b, attrs, staticStyle, resolved, table, imports, rt, interpTemp, cls, tag, bag, mergeExpr, nonce) {
					return false
				}
			} else {
				fmt.Fprintf(b, "\t\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+styleAttr.Name+`="`))
				fmt.Fprintf(b, "\t\t\t_gsxgw.Style(%s)\n", strings.Join(styleParts, ", "))
				fmt.Fprintf(b, "\t\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
			}
			b.WriteString("\t\t}\n")
		case embedStyle != nil:
			// Same byte-identical-when-bag-empty principle as static/composed style
			// above: StyleMerged always splits+dedupes by property (unlike Class's
			// merge, which passes a lone source through verbatim), so an author's
			// literal with an intra-duplicate declaration would lose one when the bag
			// doesn't touch style. The else branch re-emits the literal via its normal
			// standalone path (emitEmbeddedTextAttr), byte-identical to Task 4's
			// no-fallthrough rendering.
			ownStyle, ok := embeddedTextValueExpr(b, embedStyle, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr")
			if !ok {
				return false
			}
			fmt.Fprintf(b, "\t\tif %s.Has(\"style\") {\n", bagExpr)
			fmt.Fprintf(b, "\t\t\t_gsxgw.StyleMerged(%s, %s.Style())\n", ownStyle, bagExpr)
			b.WriteString("\t\t} else {\n")
			if !emitEmbeddedTextAttr(b, embedStyle, resolved, table, imports, rt, interpTemp, cls, tag, bag) {
				return false
			}
			b.WriteString("\t\t}\n")
		default:
			// No root style: emit StyleMerged so a caller-only style still appears (it is
			// a no-op when the bag has no style either).
			fmt.Fprintf(b, "\t\t_gsxgw.StyleMerged(\"\", %s.Style())\n", bagExpr)
		}
		// URL-classified bag attributes sanitize at the leaf (bag hardening Part A).
		// This forwarding element is where the caller's bag becomes real HTML, so a
		// single Spread call writes the residual bag AND routes every
		// URL-classified name through the same tag-aware sink a static attr uses
		// (URLVal for navigational, URLImageVal for image resources; a dangerous
		// scheme → about:invalid#gsx) in ONE ordered pass, matching case-insensitively
		// so a smuggled `HREF`/`SRC` cannot slip an unsanitized value past the leaf.
		// The exact URL names, known statically (the tag is fixed), split into the
		// strict-nav and image-resource sets via urlWriterMethod; prefix URL rules
		// (e.g. `prefix = "data-url-"`) always take the strict nav sink (user rules
		// never get the image-sink allowance). URL keys render IN their bag position,
		// so the caller's authored attribute order is preserved.
		//
		// excluded is the names a forced root attr owns, which Spread skips
		// (the class/style merge above and the unguarded forced emit in Walk 2 own
		// those). It is the dynamic drop slice when post-spread cond-attrs exist (built
		// by the selectors above: class/style + static forced + each taken branch's
		// names, appended at runtime), else the static class/style+forced set. A
		// post-spread conditional that FORCES a URL name thus suppresses the bag's copy
		// exactly when its branch is taken — the runtime membership in the drop slice
		// does it, no `!<bool>` guard — and otherwise the bag's sanitized value renders.
		excludedExpr := dropVar
		if excludedExpr == "" {
			excl := append([]string{"class", "style"}, forcedNames...)
			excludedExpr = goStringSliceLit(excl)
		}
		emitSpreadCall(b, bagExpr, tag, cls, excludedExpr)
		return true
	}

	// Emission order in the output: overridable scalars, then the spread section,
	// then the forced scalars — matching `<div a { attrs... } b/>` (a, bag, b).
	// Walk 1 emits everything EXCEPT post-spread scalars: guarded pre-spread
	// scalars, the class merge site in place (emitRoot{Composed,Static}Class),
	// and inline non-bag spreads at their positions; style is skipped here and
	// merged by emitSpread. class/style handling ignores spread position
	// (positional-exempt).
	for i, a := range attrs {
		switch t := a.(type) {
		case *ast.ClassAttr:
			if t == classAttr {
				if !emitRootComposedClass(b, t, table, imports, rt, interpTemp, bag, mergeExpr, bagExpr, resolved) {
					return false
				}
				continue
			}
			if t == styleAttr {
				continue
			}
		case *ast.StaticAttr:
			if t == staticClass {
				emitRootStaticClass(b, t, rt, mergeExpr, bagExpr)
				continue
			}
			if t == staticStyle {
				continue
			}
		case *ast.EmbeddedAttr:
			if t == embedClass {
				if !emitRootEmbeddedClass(b, t, mergeExpr, bagExpr, resolved, table, imports, rt, interpTemp, bag) {
					return false
				}
				continue
			}
			if t == embedStyle {
				continue
			}
		case *ast.SpreadAttr:
			// The bag spread at the split position is consumed here. A non-bag spread
			// (handled by the caller's detection) never reaches this helper at splitIdx;
			// a stray SpreadAttr at any other index is still a leaf URL sink — it routes
			// through Spread (excluded=nil) so its URL keys sanitize too.
			if i == splitIdx {
				continue
			}
			spreadExpr, ok := spreadAttrExpr(t, table, imports, b, interpTemp, bag)
			if !ok {
				return false
			}
			if tmp, hoisted := nonce.tempFor(t); hoisted {
				fmt.Fprintf(b, "\t\t%s = %s\n", tmp, spreadExpr)
				emitSpreadCall(b, tmp, tag, cls, "nil")
			} else {
				emitSpreadCall(b, spreadExpr, tag, cls, "nil")
			}
			continue
		}
		if i < splitIdx {
			if ca, ok := a.(*ast.CondAttr); ok {
				if !emitCondGuarded(ca) {
					return false
				}
				continue
			}
			if !emitScalar(a, true) {
				return false
			}
		}
	}
	// The spread section (post-cond selectors + class/style merge + bag minus
	// forced names).
	if !emitSpread() {
		return false
	}
	// Walk 2: forced post-spread scalars, unguarded, in source order. A
	// cond-attr renders its planned runs under the branch bools its selector
	// recorded before the spread (conditions are NOT re-evaluated).
	for i, a := range attrs {
		if i <= splitIdx {
			continue
		}
		switch t := a.(type) {
		case *ast.ClassAttr:
			if t == classAttr || t == styleAttr {
				continue
			}
		case *ast.StaticAttr:
			if t == staticClass || t == staticStyle {
				continue
			}
		case *ast.EmbeddedAttr:
			if t == embedClass || t == embedStyle {
				continue
			}
		case *ast.SpreadAttr:
			continue
		case *ast.CondAttr:
			for _, run := range postRuns[t] {
				fmt.Fprintf(b, "\t\tif %s {\n", run.boolVar)
				for _, leaf := range run.leaves {
					if !emitAttr(b, attrs, leaf, resolved, table, imports, rt, interpTemp, cls, tag, bag, mergeExpr, nonce) {
						return false
					}
				}
				b.WriteString("\t\t}\n")
			}
			continue
		}
		if !emitScalar(a, false) {
			return false
		}
	}
	return true
}

// condRun is one source-ordered segment of leaf attrs from a post-spread
// cond-attr, tagged with the bool temp recording whether its branch was taken.
// The leaves render after the bag spread under `if <boolVar>`.
type condRun struct {
	boolVar string
	leaves  []ast.Attr
}

// condBranchPlan is one branch (Then or Else list) of a planned post-spread
// cond-attr: the bool temp set when the branch is taken ("" when the branch
// has no direct leaves), the direct leaf names it forces out of the spread,
// and any nested cond-attrs (planned recursively, in source order).
type condBranchPlan struct {
	boolVar string
	names   []string
	nested  []*condSelNode
}

// condSelNode mirrors one *ast.CondAttr for selector emission.
type condSelNode struct {
	cond      string
	then, els condBranchPlan
}

// planPostCond walks one post-spread cond-attr allocating branch bool temps
// (appended to bools) and collecting its leaves into source-ordered runs
// (appended to runs; a nested cond-attr splits the enclosing run so the
// post-spread emission order matches the original branch-body order).
// Validation has already rejected class/style and spread leaves, so every
// non-cond leaf has a static rootAttrName.
func planPostCond(t *ast.CondAttr, runs *[]condRun, bools *[]string, interpTemp *int) *condSelNode {
	planBranch := func(as []ast.Attr) condBranchPlan {
		var bp condBranchPlan
		curIdx := -1 // index into *runs of the open run, -1 = none
		for _, a := range as {
			if nested, ok := a.(*ast.CondAttr); ok {
				curIdx = -1
				bp.nested = append(bp.nested, planPostCond(nested, runs, bools, interpTemp))
				continue
			}
			name, ok := rootAttrName(a)
			if !ok {
				continue // unreachable after validation; defensive
			}
			if bp.boolVar == "" {
				bp.boolVar = fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				*bools = append(*bools, bp.boolVar)
			}
			bp.names = append(bp.names, name)
			if curIdx < 0 {
				*runs = append(*runs, condRun{boolVar: bp.boolVar})
				curIdx = len(*runs) - 1
			}
			(*runs)[curIdx].leaves = append((*runs)[curIdx].leaves, a)
		}
		return bp
	}
	n := &condSelNode{cond: t.Cond}
	n.then = planBranch(t.Then)
	n.els = planBranch(t.Else)
	return n
}

// emitPostCondSelector writes the run-once branch selector for a planned
// post-spread cond-attr: the original if/else structure evaluates each
// condition exactly once; a taken branch sets its bool temp and appends its
// direct leaf names to the dynamic drop slice.
func emitPostCondSelector(b *bytes.Buffer, n *condSelNode, dropVar string) {
	emitBranch := func(bp condBranchPlan) {
		if bp.boolVar != "" {
			fmt.Fprintf(b, "\t\t%s = true\n", bp.boolVar)
			quoted := make([]string, len(bp.names))
			for i, name := range bp.names {
				quoted[i] = strconv.Quote(name)
			}
			fmt.Fprintf(b, "\t\t%s = append(%s, %s)\n", dropVar, dropVar, strings.Join(quoted, ", "))
		}
		for _, nested := range bp.nested {
			emitPostCondSelector(b, nested, dropVar)
		}
	}
	fmt.Fprintf(b, "\t\tif %s {\n", n.cond)
	emitBranch(n.then)
	if n.els.boolVar != "" || len(n.els.nested) > 0 {
		b.WriteString("\t\t} else {\n")
		emitBranch(n.els)
	}
	b.WriteString("\t\t}\n")
}

// hasAttrsMethodSet reports whether t already supports the method-bearing bag
// operations used by the forwarding leaf. The analyzer separately validates
// every element spread against gsx.Attrs, so a valid methodless type here is an
// assignable slice form such as a variadic []gsx.Attr parameter; it needs an
// explicit conversion before the leaf emits Has/Class/Style calls.
func hasAttrsMethodSet(t types.Type) bool {
	return lookupMethod(t, "Has") != nil &&
		lookupMethod(t, "Class") != nil &&
		lookupMethod(t, "Style") != nil
}

// emitManualSpreadElement emits a non-component element that carries the author's
// `{ attrs... }` bag spread (MANUAL fallthrough), applying positional precedence:
// root attrs before the spread are caller-overridable, attrs after are forced
// (root wins). splitIdx is the bag SpreadAttr's index in el.Attrs (guaranteed
// unique by the caller).
func emitManualSpreadElement(b *bytes.Buffer, el *ast.Element, splitIdx int, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
	// The bag expression: the bare `attrs` local is used directly; a DERIVED bag
	// (`attrs.Without(…)`, `attrs.Merge(…)`, a pipeline) is evaluated exactly
	// once into a hoisted temp so the caller-wins guards (.Has), the class/style
	// merges (.Class()/.Style()) and the spread all read the same value and side
	// effects don't repeat. A spread subject accepted by the analyzer as
	// assignable to gsx.Attrs but lacking that method set (notably a variadic
	// []gsx.Attr parameter) is converted into the same temp at this semantic
	// boundary. Already-method-bearing gsx.Attrs values retain the direct fast path.
	spread := el.Attrs[splitIdx].(*ast.SpreadAttr)
	bagExpr := strings.TrimSpace(spread.Expr)
	needsHoist := bagExpr != "attrs" || len(spread.Stages) > 0
	if needsHoist {
		expr, ok := spreadAttrExpr(spread, table, imports, b, interpTemp, bag)
		if !ok {
			return false
		}
		bagExpr = expr
	}
	if spreadType := resolved[spread]; spreadType != nil && !hasAttrsMethodSet(spreadType) {
		bagExpr = fmt.Sprintf("%s.Attrs(%s)", rt.rt(), bagExpr)
		needsHoist = true
	}
	if needsHoist {
		expr := bagExpr
		bagExpr = fmt.Sprintf("_gsxv%d", *interpTemp)
		*interpTemp++
		fmt.Fprintf(b, "\t\t%s := %s\n", bagExpr, expr)
	}
	emitS(b, "<"+el.Tag)
	ni := newNonceInjection(b, el.Tag, el.Attrs, rt, interpTemp, el.Attrs[splitIdx])
	if ni != nil {
		ni.extra = []string{bagExpr}
	}
	if !emitFallthroughAttrs(b, el.Attrs, splitIdx, resolved, table, imports, rt, interpTemp, cls, el.Tag, bag, mergeExpr, bagExpr, ni) {
		return false
	}
	ni.emitGuard(b)
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
			if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
				return false
			}
		}
	}
	emitS(b, "</"+el.Tag+">")
	return true
}

// foldElementSpreads renders a non-component element carrying ≥2 spreads
// (counting cond-nested), OR exactly one spread that is cond-nested when a
// root class/style attribute requires aggregation, by
// folding ALL its attributes into one source-ordered ConcatAttrs(...) bag and
// rendering that through the single-bag leaf. This is the reference
// full-fold: last writer wins per key, class/style aggregate. The lone-cond
// case is included because the alternative (inline emitAttr's bare
// `if cond { Spread(bag, excluded=nil) }`) never learns about a sibling root
// class/style attr and would emit a second raw class/style instead of
// aggregating (O1, issue #75).
//
// It builds the fold via composeBag (which emits any AttrsCond/pipe hoists into
// b in order), then temporarily swaps el.Attrs for a single synthetic spread
// carrying the fold expression and renders through emitManualSpreadElement with
// splitIdx=0 — keeping el's real span/Pos()/Void/Children and node identity for
// nonce / emitFallthroughAttrs (they compare against el.Attrs[splitIdx]).
func foldElementSpreads(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
	// A hole-bearing js/css literal on a URL-sink attribute cannot fold: its
	// bag value reaches the leaf contextually escaped for JS/CSS, and Spread's
	// URL-key sanitization would then rewrite it (the inline path emits the
	// escaped value verbatim). Reject rather than silently diverge from the
	// inline rendering of the same attribute. Cond-attr branches fold into the
	// same bag (they inherit bagElementFold through condAttrsExpr), so the
	// scan descends into them.
	var rejectURLSinkLiterals func(attrs []ast.Attr) bool
	rejectURLSinkLiterals = func(attrs []ast.Attr) bool {
		for _, a := range attrs {
			switch t := a.(type) {
			case *ast.CondAttr:
				if !rejectURLSinkLiterals(t.Then) || !rejectURLSinkLiterals(t.Else) {
					return false
				}
			case *ast.EmbeddedAttr:
				if t.Lang == ast.EmbeddedText {
					continue
				}
				if _, static := embeddedStaticText(t); static {
					continue
				}
				if cls.Context(t.Name) == attrclass.CtxURL {
					bag.Errorf(t.Pos(), t.End(), "url-sink-fold",
						"embedded %s attribute literal %q with @{ } interpolation is a URL attribute on <%s>; this element's attributes must be merged through a shared bag, whose URL sanitization would rewrite the %s-escaped value, so the contextual literal cannot be used on this URL-sink key — use an ordinary URL expression or string for %q, or avoid the contextual %s literal on that key",
						embeddedLangName(t.Lang), t.Name, el.Tag, embeddedLangName(t.Lang), t.Name, embeddedLangName(t.Lang))
					return false
				}
			}
		}
		return true
	}
	if !rejectURLSinkLiterals(el.Attrs) {
		return false
	}
	expr, used, err := composeBag(b, interpTemp, emitPipeWrap(b, interpTemp), false, el.Attrs, rt.rt(), el.Tag, classMergeExpr(mergeExpr, rt), table, resolved, imports, rt, bag, "return _gsxerr", bagElementFold)
	if err != nil {
		if errors.Is(err, errBagDiagReported) {
			return false // embeddedTextValueExpr already reported it
		}
		if ae, ok := errors.AsType[*attrError](err); ok {
			bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
		} else {
			bag.Errorf(el.Pos(), el.End(), "spread-fold", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		}
		return false
	}
	for _, path := range used {
		imports[path] = true
	}
	orig := el.Attrs
	el.Attrs = []ast.Attr{&ast.SpreadAttr{Expr: expr}}
	defer func() { el.Attrs = orig }()
	return emitManualSpreadElement(b, el, 0, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan)
}

// bagSpreadIndex returns the index of the first top-level element spread and
// whether one is present. The full-fold dispatch handles multiple spreads
// before this single-bag leaf path is consulted.
func bagSpreadIndex(attrs []ast.Attr) (idx int, found bool) {
	for i, a := range attrs {
		if _, ok := a.(*ast.SpreadAttr); ok {
			return i, true
		}
	}
	return -1, false
}

// firstTwoSpreadAttrs returns the first two spread attrs on an element in
// depth-first source order, descending into cond-attr branches, plus the
// enclosing cond-attr condition of the FIRST spread (empty when it is
// top-level). It is the fold trigger's probe: `second` being non-nil tells the
// caller (elementFolds) to use the full-fold path, while
// firstCond lets elementFolds detect a lone cond-NESTED spread (empty firstCond
// = top-level spread; non-empty = nested in a `{ if … { { x... } } }`).
//
// Named distinctly from analyze.go's walkSpreadAttrs (a callback-style visitor
// over every spread, used by probe/collect passes) to avoid redeclaring that
// existing function in the same package.
func firstTwoSpreadAttrs(attrs []ast.Attr) (first, second *ast.SpreadAttr, firstCond string) {
	var visit func(list []ast.Attr, cond string)
	visit = func(list []ast.Attr, cond string) {
		for _, a := range list {
			if second != nil {
				return
			}
			switch t := a.(type) {
			case *ast.SpreadAttr:
				if first == nil {
					first, firstCond = t, cond
				} else {
					second = t
				}
			case *ast.CondAttr:
				visit(t.Then, t.Cond)
				visit(t.Else, "!("+t.Cond+")")
			}
		}
	}
	visit(attrs, "")
	return first, second, firstCond
}

// classStyleContributorCounts counts, per name, the maximum number of
// class/style leaves that can contribute to an element's final composable value
// in a single render. A CondAttr's branches are mutually exclusive, so it
// contributes the larger of its branch counts, not their sum — a lone
// `{ if c { class="a" } else { class="b" } }` yields at most one class and must
// not trigger the fold. This is a shape analysis only; conditions are never
// evaluated. A bare bool `class`/`style` is not a contributor: the bag's
// Class()/Style() aggregation is string-valued, so it stays on the inline
// emitter (where it renders as a boolean attribute) rather than folding.
func classStyleContributorCounts(attrs []ast.Attr) (class, style int) {
	var walk func([]ast.Attr) (int, int)
	walk = func(list []ast.Attr) (class, style int) {
		for _, a := range list {
			var name string
			switch t := a.(type) {
			case *ast.StaticAttr:
				name = t.Name
			case *ast.ClassAttr:
				name = t.Name
			case *ast.EmbeddedAttr:
				name = t.Name
			case *ast.CondAttr:
				thenClass, thenStyle := walk(t.Then)
				elseClass, elseStyle := walk(t.Else)
				class += max(thenClass, elseClass)
				style += max(thenStyle, elseStyle)
			}
			switch name {
			case "class":
				class++
			case "style":
				style++
			}
		}
		return class, style
	}
	return walk(attrs)
}

// elementFolds reports whether genNode's *ast.Element case routes attrs
// through foldElementSpreads. It is true when:
//   - multiple leaves contribute to the same class or style value; OR
//   - the element carries ≥2 spreads (counting cond-nested); OR
//   - exactly one spread that is itself cond-nested AND a top-level class/style
//     attr (O1: the inline `if cond { Spread(bag, excluded=nil) }` path cannot
//     aggregate the bag's class/style with the sibling root class/style — #75); OR
//   - a spread AND a class/style inside a cond-attr branch (D3 lift: the inline
//     path once rejected such a conditional class/style as unable to join the
//     static merge; the fold merges it via an AttrsCond bag entry aggregated at
//     the leaf — see hasCondClassStyle).
//
// A forwarding element with NONE of these (e.g. a lone cond-nested spread with no
// root or cond-attr class/style) stays on the inline emitAttr path, which
// supports the full element attr subset (incl. js/css embedded holes composeBag
// does not).
//
// This is the SINGLE source of truth for the fold trigger, shared with
// scopeUsesNumeric/attrsUseNumericScratch so the numeric-scratch prescan agrees
// with where composeBag actually emits — composeBag never writes through
// _gsxnum (a numeric ExprAttr lowers to a plain `{Key, Value: <expr>}` bag
// entry), so a folded element's attrs must never be scanned as needing the
// scratch buffer, while a non-folded lone-cond element (inline path) may.
func elementFolds(attrs []ast.Attr) bool {
	first, second, firstCond := firstTwoSpreadAttrs(attrs)
	class, style := classStyleContributorCounts(attrs)
	return class > 1 || style > 1 || second != nil ||
		(first != nil && firstCond != "" && hasRootClassStyle(attrs)) ||
		(first != nil && hasCondClassStyle(attrs)) // D3 lift: spread + cond-attr class/style
}

// hasCondClassStyle reports whether attrs carries a class/style leaf inside a
// cond-attr branch (any depth, incl. else-if). Such a shape is what D3 used to
// reject on a forwarding element; routing it through the fold merges it via an
// AttrsCond bag entry aggregated at the leaf.
func hasCondClassStyle(attrs []ast.Attr) bool {
	var walk func(as []ast.Attr) bool
	walk = func(as []ast.Attr) bool {
		for _, a := range as {
			switch t := a.(type) {
			case *ast.CondAttr:
				if walk(t.Then) || walk(t.Else) {
					return true
				}
			case *ast.ClassAttr:
				if t.Name == "class" || t.Name == "style" {
					return true
				}
			case *ast.StaticAttr:
				if t.Name == "class" || t.Name == "style" {
					return true
				}
			case *ast.EmbeddedAttr:
				// Lang-agnostic: a class/style written as an embedded css/js literal
				// (e.g. style=css"…@{}…") must fold too, so the cond-attr routes
				// through composeBag (merge, or a positioned fail-closed diagnostic)
				// rather than the inline emitFallthroughAttrs path — which would emit
				// the conditional style as a SECOND, duplicate attribute. Mirrors the
				// breadth of D3's old rootAttrName check (name only, any Lang).
				if t.Name == "class" || t.Name == "style" {
					return true
				}
			}
		}
		return false
	}
	// Only cond-attr-nested class/style counts (a top-level class/style is not a
	// D3 case); so walk begins one level down, at each top-level CondAttr.
	for _, a := range attrs {
		if c, ok := a.(*ast.CondAttr); ok {
			if walk(c.Then) || walk(c.Else) {
				return true
			}
		}
	}
	return false
}

// hasRootClassStyle reports whether attrs carries a TOP-LEVEL class or style
// attribute — a composable class={…}/style={…} (*ast.ClassAttr), a static
// class="…"/style="…" (*ast.StaticAttr), or a hole-bearing backtick literal
// (*ast.EmbeddedAttr) — mirroring emitFallthroughAttrs' root class/style scan.
// A cond-NESTED class/style is not root (it folds separately via
// hasCondClassStyle — the D3 lift), so this does not recurse into cond-attr
// branches. It gates the
// lone-cond-nested-spread fold: that fold exists only to aggregate a root
// class/style with the conditional bag, so with none present the element takes
// the inline path instead.
func hasRootClassStyle(attrs []ast.Attr) bool {
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.ClassAttr:
			if t.Name == "class" || t.Name == "style" {
				return true
			}
		case *ast.StaticAttr:
			if t.Name == "class" || t.Name == "style" {
				return true
			}
		case *ast.EmbeddedAttr:
			// Lang-agnostic (mirrors hasCondClassStyle): a top-level style=css"…" /
			// class=js"…" must gate the lone-cond fold too, otherwise the inline path
			// emits it as a SEPARATE attribute alongside the folded bag's — a silent
			// duplicate. Folding routes both through the shared leaf bag.
			if t.Name == "class" || t.Name == "style" {
				return true
			}
		}
	}
	return false
}

// emitRootComposedClass emits a composed `class={ … }` merged with the bag's
// class: ` class="` + gw.Class(<existing parts…>, gsx.Class(attrs.Class()))
// + `"`. Mirrors emitClassAttr's part lowering, appending the bag class as a
// final unconditional part so it merges/dedupes through the merge func.
func emitRootComposedClass(b *bytes.Buffer, a *ast.ClassAttr, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, mergeExpr, bagExpr string, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, rt, interpTemp, bag, resolved, false, emitPipeWrap(b, interpTemp), "return _gsxerr")
	if !ok {
		return false
	}
	parts = append(parts, fmt.Sprintf("%s.Class(%s.Class())", rt.rt(), bagExpr))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s)\n", classMergeExpr(mergeExpr, rt), strings.Join(parts, ", "))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// emitRootStaticClass emits a static `class="x"` merged with the bag's class:
// ` class="` + gw.Class(gsx.Class("x"), gsx.Class(attrs.Class())) + `"`.
func emitRootStaticClass(b *bytes.Buffer, a *ast.StaticAttr, rt rtImports, mergeExpr, bagExpr string) {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s.Class(%s), %s.Class(%s.Class()))\n", classMergeExpr(mergeExpr, rt), rt.rt(), strconv.Quote(a.Value), rt.rt(), bagExpr)
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
}

// emitRootEmbeddedClass emits an EmbeddedText class/style backtick literal
// (name=`…@{expr}…`) merged with the bag's class: ` class="` +
// gw.Class(gsx.Class(<own interpolated value>), gsx.Class(attrs.Class())) +
// `"`. Mirrors emitRootStaticClass, but the single static token string is
// replaced by embeddedTextValueExpr's assembled segment expression — the own
// value is one gsx.Class(...) part alongside the bag's, so a single-source
// literal (no bag class) still passes through DefaultClassMerge's
// len(classes)==1 verbatim shortcut, matching the static-class case exactly.
func emitRootEmbeddedClass(b *bytes.Buffer, a *ast.EmbeddedAttr, mergeExpr, bagExpr string, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
	val, ok := embeddedTextValueExpr(b, a, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr")
	if !ok {
		return false
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s.Class(%s), %s.Class(%s.Class()))\n", classMergeExpr(mergeExpr, rt), rt.rt(), val, rt.rt(), bagExpr)
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
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

// nonceEligibleTag reports whether tag is one gsx auto-decorates with the
// context CSP nonce (script/style; HTML tag names are case-insensitive).
func nonceEligibleTag(tag string) bool {
	return strings.EqualFold(tag, "script") || strings.EqualFold(tag, "style")
}

func hasConditionalExplicitNonce(attrs []ast.Attr) bool {
	for _, a := range attrs {
		if c, ok := a.(*ast.CondAttr); ok {
			if attrsContainExplicitNonce(c.Then) || attrsContainExplicitNonce(c.Else) {
				return true
			}
		}
	}
	return false
}

func attrsContainExplicitNonce(attrs []ast.Attr) bool {
	for _, a := range attrs {
		if attrIsExplicitNonce(a) {
			return true
		}
		if c, ok := a.(*ast.CondAttr); ok && (attrsContainExplicitNonce(c.Then) || attrsContainExplicitNonce(c.Else)) {
			return true
		}
	}
	return false
}

func attrIsExplicitNonce(a ast.Attr) bool {
	if name, ok := rootAttrName(a); ok {
		return strings.EqualFold(name, "nonce")
	}
	return false
}

// nonceInjection carries the state for auto-injecting the context CSP nonce
// into a <script>/<style> open tag: one hoisted gsx.Attrs temp per spread
// attr (at any depth, including cond-attr branches) so the post-attr guard
// can ask each spread whether it already carried a "nonce" key. A nil
// *nonceInjection means "not eligible" (not script/style, or the author
// wrote an explicit nonce) and every method is a nil-safe no-op.
type nonceInjection struct {
	temps    map[*ast.SpreadAttr]string
	order    []string // temp names in declaration order
	extra    []string // extra guard bag exprs (MANUAL fallthrough bag)
	explicit string   // bool temp set true when a conditional explicit nonce emits
}

// newNonceInjection decides eligibility and, for an eligible element, writes
// the hoisted `var _gsxvN gsx.Attrs` declarations to b (they must precede the
// attr emits: a spread inside an untaken cond branch leaves its temp nil, and
// a nil Attrs.Has is false, so the guard stays correct). skip excludes one
// attr from the spread walk — the MANUAL `{ attrs... }` bag spread, which is
// consumed by emitFallthroughAttrs and guarded via extra instead.
func newNonceInjection(b *bytes.Buffer, tag string, attrs []ast.Attr, rt rtImports, interpTemp *int, skip ast.Attr) *nonceInjection {
	if !nonceEligibleTag(tag) {
		return nil
	}
	// Top-level author-written nonce owns the attribute, so no automatic nonce is
	// needed. Conditional nonce attrs are tracked at runtime below so the context
	// nonce still appears when the branch is untaken.
	if slices.ContainsFunc(attrs, attrIsExplicitNonce) {
		return nil
	}
	ni := &nonceInjection{temps: map[*ast.SpreadAttr]string{}}
	if hasConditionalExplicitNonce(attrs) {
		ni.explicit = fmt.Sprintf("_gsxv%d", *interpTemp)
		*interpTemp++
		fmt.Fprintf(b, "\t\tvar %s bool\n", ni.explicit)
	}
	var walk func([]ast.Attr)
	walk = func(as []ast.Attr) {
		for _, a := range as {
			if a == skip {
				continue
			}
			switch t := a.(type) {
			case *ast.SpreadAttr:
				tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				ni.temps[t] = tmp
				ni.order = append(ni.order, tmp)
			case *ast.CondAttr:
				walk(t.Then)
				walk(t.Else)
			}
		}
	}
	walk(attrs)
	for _, tmp := range ni.order {
		fmt.Fprintf(b, "\t\tvar %s %s.Attrs\n", tmp, rt.rt())
	}
	return ni
}

// tempFor returns the hoisted temp for spread s (nil-safe).
func (ni *nonceInjection) tempFor(s *ast.SpreadAttr) (string, bool) {
	if ni == nil {
		return "", false
	}
	tmp, ok := ni.temps[s]
	return tmp, ok
}

func (ni *nonceInjection) markExplicit(b *bytes.Buffer, name string) {
	if ni == nil || ni.explicit == "" || !strings.EqualFold(name, "nonce") {
		return
	}
	fmt.Fprintf(b, "\t\t%s = true\n", ni.explicit)
}

// emitGuard writes the nonce injection at the end of the open tag's attrs:
// unconditional when no spread/bag could have carried a nonce, otherwise
// guarded on every bag having no "nonce" key.
func (ni *nonceInjection) emitGuard(b *bytes.Buffer) {
	if ni == nil {
		return
	}
	guards := make([]string, 0, len(ni.extra)+len(ni.order))
	if ni.explicit != "" {
		guards = append(guards, "!"+ni.explicit)
	}
	for _, e := range ni.extra {
		guards = append(guards, "!"+e+".Has(\"nonce\")")
	}
	for _, tmp := range ni.order {
		guards = append(guards, "!"+tmp+".Has(\"nonce\")")
	}
	if len(guards) == 0 {
		b.WriteString("\t\t_gsxgw.Nonce(ctx)\n")
		return
	}
	fmt.Fprintf(b, "\t\tif %s {\n\t\t\t_gsxgw.Nonce(ctx)\n\t\t}\n", strings.Join(guards, " && "))
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
func rootStyleString(b *bytes.Buffer, styleAttr *ast.ClassAttr, staticStyle *ast.StaticAttr, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type) (string, []string, bool) {
	switch {
	case staticStyle != nil:
		return strconv.Quote(staticStyle.Value), nil, true
	case styleAttr != nil:
		parts, ok := composedParts(b, styleAttr, table, imports, rt, interpTemp, bag, resolved, true, emitPipeWrap(b, interpTemp), "return _gsxerr")
		if !ok {
			return "", nil, false
		}
		return rt.rt() + ".StyleString(" + strings.Join(parts, ", ") + ")", parts, true
	default:
		return `""`, nil, true
	}
}

// genNode emits one markup node. recvVar/recvTypeName are the enclosing
// component's receiver var + type name (empty for a function component); they
// thread down to genChildComponent for the method-vs-package disambiguation of a
// dotted child-component tag.
func genNode(b *bytes.Buffer, n ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
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
		if t.IsComponent {
			return genChildComponent(b, t, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan)
		}
		// MANUAL fallthrough: EVERY element spread `{ x... }` is a leaf sink — it
		// routes through emitManualSpreadElement's URL-sanitizing / class-merge
		// machinery regardless of the bag's provenance (declared forwarding param,
		// local `:=` bag, func result, byo field, arbitrary expr). An element
		// carrying ≥2 spreads (counting cond-nested) folds ALL its attributes into
		// one source-ordered ConcatAttrs(...) bag (last writer wins per key,
		// class/style aggregate) and renders that through the single-bag leaf. A
		// LONE cond-nested spread (exactly one spread total, but nested in a
		// `{ if c { { x... } } }` cond-attr) also folds: its inline emitAttr path
		// (a bare `if cond { Spread(bag, excluded=nil) }`) never learns about a
		// sibling root class/style attr, so a root `class="root"` next to it would
		// emit a SECOND raw `class="…"` from the bag instead of aggregating (O1,
		// issue #75) — folding routes it through the same merge site as everything
		// else. Only a top-level-only single spread routes straight to the leaf.
		if elementFolds(t.Attrs) {
			return foldElementSpreads(b, t, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan)
		} else if splitIdx, found := bagSpreadIndex(t.Attrs); found {
			return emitManualSpreadElement(b, t, splitIdx, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan)
		}
		emitS(b, "<"+t.Tag)
		ni := newNonceInjection(b, t.Tag, t.Attrs, rt, interpTemp, nil)
		for _, a := range t.Attrs {
			if !emitAttr(b, t.Attrs, a, resolved, table, imports, rt, interpTemp, cls, t.Tag, bag, mergeExpr, ni) {
				return false
			}
		}
		ni.emitGuard(b)
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
				if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
					return false
				}
			}
		}
		emitS(b, "</"+t.Tag+">")
	case *ast.Interp:
		ec := interpEmitCtx{
			currentPkg:          currentPkg,
			importAliases:       importAliases,
			boundNames:          boundNames,
			typeArgAliases:      typeArgAliases,
			cls:                 cls,
			mergeExpr:           mergeExpr,
			enclosingAttrsBound: enclosingAttrsBound,
			positionalPlan:      positionalPlan,
		}
		return genInterp(b, t, resolved, table, imports, rt, interpTemp, fset, bag, ec)
	case *ast.EmbeddedInterp:
		ec := interpEmitCtx{
			currentPkg:          currentPkg,
			importAliases:       importAliases,
			boundNames:          boundNames,
			typeArgAliases:      typeArgAliases,
			cls:                 cls,
			mergeExpr:           mergeExpr,
			enclosingAttrsBound: enclosingAttrsBound,
			positionalPlan:      positionalPlan,
		}
		return emitEmbeddedInterp(b, t, resolved, table, imports, rt, interpTemp, fset, bag, ec)
	case *ast.Fragment:
		for _, c := range t.Children {
			if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
				return false
			}
		}
	case *ast.ForMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "for %s {\n", t.Clause)
		for _, c := range t.Body {
			if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
				return false
			}
		}
		b.WriteString("}\n")
	case *ast.IfMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "if %s {\n", t.Cond)
		for _, c := range t.Then {
			if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
				return false
			}
		}
		b.WriteString("}")
		if t.Else != nil {
			b.WriteString(" else {\n")
			for _, c := range t.Else {
				if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
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
				if !genNode(b, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
					return false
				}
			}
		}
		b.WriteString("}\n")
	case *ast.GoBlock:
		emitLine(b, fset, t.Pos())
		if t.UnsupportedMarkup != nil {
			// The package preprocessor records and diagnoses this unsupported
			// region once. Refuse to emit it; reconstructing only a prefix would
			// silently produce invalid or incomplete Go.
			return false
		}
		if t.Embedded == nil {
			b.WriteString(t.Code)
			b.WriteString("\n")
			break
		}
		// The block carries embedded f`/js`/css` literals (analyze's split; see
		// preprocessComponentCallSites). Reconstruct it from its parts: GoText runs are
		// verbatim, and each *ast.EmbeddedInterp lowers to its Go value via the
		// SAME emitGoExprEmbeddedInterp the GoWithElements and Interp.Embedded
		// sites use (one lowering, three container sites — they can never
		// diverge). At THIS site — unlike the in-closure Interp.Embedded and
		// component-value sites, which hoist — error-carrying holes are
		// rejected for f`/js`/css` alike (canHoist=false → rejectErr/exprPos,
		// see below); any statement hoist a hole does emit goes to b, before
		// the reconstructed statement (the same pre-existing
		// unconditional-errReturn caveat the other two sites carry).
		for _, part := range t.Embedded {
			switch p := part.(type) {
			case ast.GoText:
				b.WriteString(p.Src)
			case *ast.EmbeddedInterp:
				if len(p.Stages) > 0 {
					bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
					return false
				}
				var vb bytes.Buffer
				// GoBlock: ctx IS in scope (the render closure), but the
				// reconstruction writes GoText runs straight to b, so a hole's
				// statement hoist would land mid-statement — no clean hoist channel
				// (canHoist=false rejects error-carrying f` holes here, instead of
				// splicing invalid Go). ctx-taking holes stay allowed (hasCtx=true).
				if !emitGoExprEmbeddedInterp(b, &vb, p, resolved, table, imports, rt, interpTemp, bag, true, false) {
					return false
				}
				b.WriteString(vb.String())
			case *ast.Element, *ast.Fragment:
				return false
			}
		}
		b.WriteString("\n")
	case *ast.Comment:
		// Source-only content comment ({/* */} / {// }); never rendered.
	default:
		bag.Errorf(n.Pos(), n.End(), "unsupported-node", "unsupported markup node %T", n)
		return false
	}
	return true
}

// interpEmitCtx bundles the emit-time context an interpolation needs ONLY when
// it carries embedded <tag>/<> element literals (n.Embedded != nil): lowering
// each embedded *Element/*Fragment via emitElementValue/emitFragmentValue
// requires the full component emit environment (package, prop/field maps, byo
// data, alias maps, classifier, field matcher, merge expr). It is threaded
// through genInterp (and emitEmbeddedInterp) so the bare, non-embedded interp
// path stays untouched while the embedded path has everything emitNodeValue
// needs. Constructed at the genNode call site where all of these are in scope.
type interpEmitCtx struct {
	currentPkg     *types.Package
	importAliases  map[string]string
	boundNames     map[string]string
	typeArgAliases map[string]string
	cls            *attrclass.Classifier
	mergeExpr      string
	positionalPlan componentPositionalPackagePlan
	// enclosingAttrsBound mirrors genNode's own param: whether the component
	// whose body is being emitted binds a synthesized `attrs` local. Threaded
	// to an embedded <tag>/<> literal's emitElementValue/emitFragmentValue
	// call so a nested `{ attrs... }` forwarding attempt inside an
	// interpolation's embedded element is diagnosed identically to one in
	// ordinary child-element position.
	enclosingAttrsBound bool
}

// soleGoExprLiteral reports whether parts — an Interp.Embedded split — is
// EXACTLY one *ast.EmbeddedInterp (optionally surrounded by whitespace-only
// ast.GoText runs) with nothing else: i.e. the `{ }` body's entire content is
// a single f`/js`/css` backtick literal and no other Go code
// (`{ js`alert(1)` }`, not `{ consume(js`alert(1)`) }`, which has a non-empty
// GoText "consume(" run and so is a genuine sub-expression). Used to detect a
// js`/css` literal written directly as body text, which is never sensible
// (JS/CSS source is not the intended visible content) — contrast the f`
// case, which IS a markup template in this exact position and is deliberately
// excluded (Lang == EmbeddedText) from the rejection this feeds.
func soleGoExprLiteral(parts []ast.GoPart) (*ast.EmbeddedInterp, bool) {
	var lit *ast.EmbeddedInterp
	for _, part := range parts {
		switch p := part.(type) {
		case ast.GoText:
			if strings.TrimSpace(p.Src) != "" {
				return nil, false
			}
		case *ast.EmbeddedInterp:
			if lit != nil {
				return nil, false
			}
			lit = p
		default:
			return nil, false
		}
	}
	if lit == nil {
		return nil, false
	}
	return lit, true
}

// genInterp emits the type-aware writer call for an interpolation. The type comes
// from the go/types resolution pass; the expression is emitted verbatim (params
// are in scope as locals).
//
// When n.Embedded != nil the interp's seed carried operand-position <tag>/<>
// literals (e.g. `wrap(<b/>)`): expr is rebuilt by splicing each embedded
// element/fragment's inline gsx.Func(...) value (emitElementValue /
// emitFragmentValue) between the verbatim GoText runs, then rendered by the
// interp's resolved type exactly as any other value — so `{ wrap(<b/>) }`
// lowers to `wrap(gsx.Func(func(ctx, w){…}))` rendered as wrap's return type.
//
// Between the tuple-unwrap and emitRender, applyRenderer rewrites both expr
// and t when the value's type has a registered [renderers] entry: the
// renderer call replaces expr, and t becomes the renderer's result type, so
// emitRender classifies and renders the REWRITTEN type, not the original one.
func genInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, fset *token.FileSet, bag *diag.Bag, ec interpEmitCtx) bool {
	emitLine(b, fset, n.Pos())
	var expr string
	if n.Embedded != nil {
		if lit, ok := soleGoExprLiteral(n.Embedded); ok && lit.Lang != ast.EmbeddedText {
			bag.Errorf(lit.Pos(), lit.End(), "goexpr-literal-text", "a js`/css` literal renders as visible text here; write the JavaScript/CSS in an attribute (e.g. @click=js`…`) or pass the value to something that consumes gsx.RawJS/gsx.RawCSS")
			return false
		}
		var eb bytes.Buffer
		for _, part := range n.Embedded {
			switch p := part.(type) {
			case ast.GoText:
				eb.WriteString(p.Src)
			case *ast.Element:
				if !emitElementValue(&eb, p, ec.currentPkg, resolved, table, imports, rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, interpTemp, fset, ec.cls, bag, ec.mergeExpr, ec.enclosingAttrsBound, ec.positionalPlan) {
					return false
				}
			case *ast.Fragment:
				if !emitFragmentValue(&eb, p, ec.currentPkg, resolved, table, imports, rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, interpTemp, fset, ec.cls, bag, ec.mergeExpr, ec.enclosingAttrsBound, ec.positionalPlan) {
					return false
				}
			case *ast.EmbeddedInterp:
				// A prefixed literal embedded in this interp's seed → a Go value.
				// f`…` assembles a plain Go string concat; js`…`/css`…` wrap the
				// escaped concat in _gsxrt.RawJS/RawCSS. The value is spliced into
				// the seed (eb) exactly like an element's gsx.Func value; a hole's
				// tuple-unwrap/error hoisting (f` and, since canHoist=true here,
				// js`/css` too) lands in b before the consuming stmt.
				if len(p.Stages) > 0 {
					bag.Errorf(n.Pos(), n.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
					return false
				}
				// Interp.Embedded: inside the render closure — ctx binds and b is a
				// clean pre-statement hoist channel (GoText goes to eb, not b).
				if !emitGoExprEmbeddedInterp(b, &eb, p, resolved, table, imports, rt, interpTemp, bag, true, true) {
					return false
				}
			default:
				bag.Errorf(n.Pos(), n.End(), "unsupported-node", "unsupported embedded interpolation part %T", part)
				return false
			}
		}
		expr = eb.String()
	} else {
		expr = strings.TrimSpace(n.Expr)
	}
	if len(n.Stages) > 0 {
		// Lower the pipeline to nested filter calls — the SAME lowerPipe output the
		// probe used, so resolved[n] is already the pipeline's RESULT type. Record
		// each used filter package path so writeImports emits it under its alias.
		// The lowered expr then falls through to the SAME (T, error) auto-unwrap as
		// a bare interpolation, so a seed-first filter returning (R, error) unwraps
		// exactly like any other error-returning value.
		//
		// For an embedded-literal seed (n.Embedded != nil) the pipeline seed is the
		// already-spliced expr (with each <tag>/<> lowered to its gsx.Func value),
		// not the raw n.Expr (which still holds the un-lowered `<tag>` source).
		seed := n.Expr
		if n.Embedded != nil {
			seed = expr
		}
		lowered, usedPkgs, err := lowerPipe(seed, n.Stages, table, emitPipeWrap(b, interpTemp))
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
		// v, err := expr; if err != nil { return err }; then render v by its
		// type — or, if that type has a registered renderer, by applyRenderer's
		// rewritten call and result type below.
		expr = hoistTuple(b, expr, interpTemp)
		t = elemT
	}
	// A registered [renderers] entry for t rewrites expr into the renderer
	// call and t into the renderer's result type; emitRender then classifies
	// and renders THAT type, not the original.
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
	return emitRender(b, expr, t, rt, n, bag)
}

// emitEmbeddedInterp emits a body/child interpolating backtick literal
// {`…@{expr}…`} [ |> f ]. It is always plain-text — the holes and any piped
// result are Text-context escaped, while trusted static source text is emitted
// verbatim (gsx's body-text model: statics are raw, holes are escaped). No
// js/css lang is valid in body position (parser guarantee).
//
// No stages (len(n.Stages)==0): the literal renders per-segment, preserving the
// exact zero-alloc form a hand-written mix of static text + {expr} holes would
// have — NO materialized concat string. Each *ast.Text segment is emitted
// verbatim via emitS, identically to genNode's *ast.Text case (body static text
// is trusted source content, not runtime-escaped — see genNode); each *ast.Interp
// hole is emitted via genInterp, the SAME Text-context renderer a bare {expr}
// uses (zero-alloc IntInto/UintInto/FloatInto for numeric holes, HTML-escaped
// Text for string holes).
//
// With stages (len(n.Stages)>0): the segments are assembled into ONE Go string
// expression (embeddedValueExpr — the same assembly embeddedTextValueExpr does
// for a braced attr literal) and piped through n.Stages via the SAME lowerPipe
// call analyze.go's probe used to populate resolved[n], so the emitted type
// matches exactly (emit ≡ probe). After the tuple-unwrap, applyRenderer rewrites
// the piped result and its type when a registered [renderers] entry matches, and
// the (possibly rewritten) value is then rendered for Text context via
// emitRender, unwrapping a trailing (T, error) tuple exactly like genInterp.
func emitEmbeddedInterp(b *bytes.Buffer, n *ast.EmbeddedInterp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, fset *token.FileSet, bag *diag.Bag, ec interpEmitCtx) bool {
	if len(n.Stages) == 0 {
		emitLine(b, fset, n.Pos())
		for _, seg := range n.Segments {
			switch s := seg.(type) {
			case *ast.Text:
				emitS(b, s.Value)
			case *ast.Interp:
				if !genInterp(b, s, resolved, table, imports, rt, interpTemp, fset, bag, ec) {
					return false
				}
			default:
				bag.Errorf(seg.Pos(), seg.End(), "unsupported-node", "body interpolation literal may contain only text and @{ } interpolations, got %T", seg)
				return false
			}
		}
		return true
	}
	emitLine(b, fset, n.Pos())
	concat, ok := embeddedValueExpr(b, n.Segments, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr", false, false, "unsupported-node", "body interpolation literal")
	if !ok {
		return false
	}
	lowered, usedPkgs, err := lowerPipe(concat, n.Stages, table, emitPipeWrap(b, interpTemp))
	if err != nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}
	for _, path := range usedPkgs {
		imports[path] = true
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of body interpolation pipeline")
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "body interpolation pipeline returns %s; only (T, error) is supported", t)
			return false
		}
		lowered = hoistTuple(b, lowered, interpTemp)
		t = elemT
	}
	lowered, t = applyRenderer(b, lowered, t, table, imports, interpTemp, "return _gsxerr")
	return emitRender(b, lowered, t, rt, n, bag)
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

// hoistTupleReturning is hoistTuple with a caller-chosen error-return statement:
// the render closure returns `return _gsxerr`; an (Attrs, error) cond-attr thunk
// returns `return nil, _gsxerr`.
func hoistTupleReturning(b *bytes.Buffer, expr string, interpTemp *int, errReturn string) string {
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\t%s, _gsxerr := %s\n\t\tif _gsxerr != nil {\n\t\t\t%s\n\t\t}\n", tmp, expr, errReturn)
	return tmp
}

// hoistTuple emits `tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`
// and returns the temp name. interpTemp is the shared per-component counter, so
// temps are unique across all unwrap sites and `return _gsxerr` binds to the
// enclosing func/closure.
func hoistTuple(b *bytes.Buffer, expr string, interpTemp *int) string {
	return hoistTupleReturning(b, expr, interpTemp, "return _gsxerr")
}

// emitPipeWrap returns the lowerPipe wrap for emit contexts: each error-returning
// non-final stage hoists through hoistTuple (temp + `if _gsxerr != nil { return
// _gsxerr }`), halting the chain at the failing stage. Statements land in b
// before the statement that consumes the pipeline's final expression.
func emitPipeWrap(b *bytes.Buffer, interpTemp *int) func(string) string {
	return pipeWrapReturning(b, interpTemp, "return _gsxerr")
}

// thunkPipeWrap is emitPipeWrap for statement positions INSIDE an (Attrs, error)
// cond-attr thunk: same hoist, two-value error return.
func thunkPipeWrap(b *bytes.Buffer, interpTemp *int) func(string) string {
	return pipeWrapReturning(b, interpTemp, "return nil, _gsxerr")
}

// pipeWrapReturning is the shared implementation of emitPipeWrap/thunkPipeWrap,
// generalized over the enclosing function's error-return statement. Hole
// lowering shared by the render closure and (Attrs, error) cond-attr thunks
// (embeddedValueExpr/holeStringExpr/embeddedHoleExpr) builds its wrap from the
// caller's errReturn so a pipeline hoist always matches the enclosing shape.
func pipeWrapReturning(b *bytes.Buffer, interpTemp *int, errReturn string) func(string) string {
	return func(call string) string {
		return hoistTupleReturning(b, call, interpTemp, errReturn)
	}
}

// probePipeWrap is the lowerPipe wrap for skeleton probes: _gsxunwrap keeps the
// probe a single expression while preserving the stage's result type and any
// positioned go/types errors inside the user's args (emit ≡ probe).
func probePipeWrap(call string) string { return "_gsxunwrap(" + call + ")" }

// hoistValueCF emits `var _gsxvN string; <if|switch> { … _gsxvN = <arm> … }`
// before the class/style part list and returns the temp name. style=true wraps
// each arm value with styleDeclExpr (CSS-value filtering for dynamic arms).
// resolved maps each *ast.ValueArm to its harvest type; when an arm's type is
// a (T, error) tuple, armExpr calls hoistTuple to emit the unwrap inline.
func hoistValueCF(b *bytes.Buffer, cf *ast.ValueCF, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, style bool, bag *diag.Bag, resolved map[ast.Node]types.Type) (string, bool) {
	tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
	*interpTemp++
	fmt.Fprintf(b, "\t\tvar %s string\n", tmp)
	armExpr := func(a *ast.ValueArm) (string, bool) {
		expr, used, err := lowerClassPartSeed(ast.ClassPart{Expr: a.Expr, Stages: a.Stages}, table, emitPipeWrap(b, interpTemp))
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
			if tup, isTuple := t.(*types.Tuple); isTuple {
				elemT, ok := tupleUnwrapType(tup)
				if !ok {
					kind := "class"
					if style {
						kind = "style"
					}
					bag.Errorf(a.Pos(), a.End(), "invalid-tuple", "%s value-form arm %q returns %s; only (T, error) is supported", kind, a.Expr, t)
					return "", false
				}
				expr = hoistTuple(b, expr, interpTemp)
				t = elemT
			}
			// The arm's value is assigned directly to the CF's own temp var
			// inside this if/switch branch (see emitValueIf/emitValueSwitch), so
			// no extra position-preserving capture is needed here — unlike
			// composedParts' plain-part list, which joins several parts into ONE
			// final call.
			expr, _ = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
		}
		if style {
			expr = styleDeclExpr(expr, rt, len(a.Stages) > 0)
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
func emitRender(b *bytes.Buffer, expr string, t types.Type, rt rtImports, n ast.Node, bag *diag.Bag) bool {
	switch classify(t) {
	case catString:
		fmt.Fprintf(b, "\t\t_gsxgw.Text(string(%s))\n", expr)
	case catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.Text(string(%s))\n", expr)
	case catStringSlice:
		fmt.Fprintf(b, "\t\t_gsxgw.Text(%s.Join(%s, \" \"))\n", rt.st(), expr)
	case catInt:
		// Format into the per-render scratch buffer and write the digit bytes
		// directly — no string allocation, no escaping (digits are always safe).
		fmt.Fprintf(b, "\t\t_gsxgw.IntInto(_gsxnum[:], int64(%s))\n", expr)
	case catUint:
		fmt.Fprintf(b, "\t\t_gsxgw.UintInto(_gsxnum[:], uint64(%s))\n", expr)
	case catFloat:
		fmt.Fprintf(b, "\t\t_gsxgw.FloatInto(_gsxnum[:], float64(%s))\n", expr)
	case catBool:
		// S, not Text: FormatBool yields only "true"/"false", neither of which
		// carries a byte htmlReplacer rewrites, so escaping is a no-op on every
		// possible value (same reasoning as the IntInto/FloatInto arms above).
		fmt.Fprintf(b, "\t\t_gsxgw.S(%s.FormatBool(bool(%s)))\n", rt.sc(), expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.Text((%s).String())\n", expr)
	case catNode:
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s)\n", expr)
	case catNodeSlice:
		fmt.Fprintf(b, "\t\tfor _, _gsxn := range %s {\n\t\t\t_gsxgw.Node(ctx, _gsxn)\n\t\t}\n", expr)
	case catAnyMixed:
		fmt.Fprintf(b, "\t\t_gsxgw.TextAny(%s)\n", expr)
	default:
		if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
			bag.Errorf(n.Pos(), n.End(), "unrenderable",
				"interpolation %q has type parameter %s (constraint %s): only same-kind or all-non-tilde renderable constraints render directly — convert explicitly in the expression", expr, t, tp.Constraint())
			return false
		}
		bag.Errorf(n.Pos(), n.End(), "unrenderable", "interpolation %q has type %s; not a renderable type", expr, t)
		return false
	}
	return true
}

// emitNumScratch declares the per-render numeric scratch buffer (var _gsxnum) at
// the head of a render body, but only when this scope directly emits an
// integer/uint/float via the IntInto/UintInto/FloatInto primitives. A component
// with no such numeric emission gets no declaration.
func emitNumScratch(b *bytes.Buffer, nodes []ast.Markup, resolved map[ast.Node]types.Type, table funcTables, cls *attrclass.Classifier) {
	if scopeUsesNumeric(nodes, resolved, table, cls) {
		b.WriteString("\t\tvar _gsxnum [32]byte\n")
	}
}

// scopeUsesNumeric reports whether anything rendered directly in THIS scope emits
// via the numeric scratch buffer (_gsxnum) — an int/uint/float value written by
// IntInto/UintInto/FloatInto. Those primitives are emitted in three positions:
//   - text interpolation (emitRender);
//   - <style>-block interpolation (emitRenderCSS);
//   - attribute value — both plain attr={n} (emitAttrValue via emitExprAttr) and
//     a numeric @{ } hole in a backtick literal attr=`…@{n}…` (emitAttrValue via
//     emitTextAttrInterp); see attrsUseNumericScratch.
//
// It recurses through same-scope constructs (non-component elements, fragments,
// control flow) but stops at:
//   - child components — their props become struct fields and their slots render
//     in their own closure scope, which declares its own scratch (see emitSlotClosure);
//   - <script> — interpolates via the JS writers, not the numeric path.
//
// It descends into <style> (block-context numerics DO use _gsxnum). Mirrors
// genNode's traversal and the emit paths' render-type resolution, so it agrees
// exactly with where _gsxnum is emitted. A mismatch would surface immediately as
// a compile error (an unused or undefined _gsxnum) in the corpus.
func scopeUsesNumeric(nodes []ast.Markup, resolved map[ast.Node]types.Type, table funcTables, cls *attrclass.Classifier) bool {
	for _, n := range nodes {
		switch t := n.(type) {
		case *ast.Interp:
			if interpIsNumeric(t, resolved, table) {
				return true
			}
		case *ast.EmbeddedInterp:
			// Stages>0: the node renders as ONE piped value (emitEmbeddedInterp's
			// with-stages path) — check resolved[t] itself. Stages==0: it renders
			// per-segment (each *ast.Interp hole via genInterp), so recurse into
			// Segments the same way as any other scope.
			if len(t.Stages) > 0 {
				if resolvedTypeIsNumeric(t, resolved, table) {
					return true
				}
			} else if scopeUsesNumeric(t.Segments, resolved, table, cls) {
				return true
			}
		case *ast.Element:
			if t.IsComponent {
				// Child component: numeric attrs are props (struct fields), and its
				// slots render in their own scope — neither uses this scope's _gsxnum.
				continue
			}
			// A folded element (elementFolds) bakes every attr into one
			// composeBag expression, which never routes a numeric ExprAttr
			// through _gsxnum (see elementFolds' doc) — skip the scan entirely
			// so a numeric attr on such an element doesn't fabricate an unused
			// _gsxnum declaration.
			if !elementFolds(t.Attrs) && attrsUseNumericScratch(t.Attrs, resolved, table, cls) {
				return true
			}
			if strings.EqualFold(t.Tag, "script") {
				continue
			}
			if scopeUsesNumeric(t.Children, resolved, table, cls) {
				return true
			}
		case *ast.Fragment:
			if scopeUsesNumeric(t.Children, resolved, table, cls) {
				return true
			}
		case *ast.ForMarkup:
			if scopeUsesNumeric(t.Body, resolved, table, cls) {
				return true
			}
		case *ast.IfMarkup:
			if scopeUsesNumeric(t.Then, resolved, table, cls) || scopeUsesNumeric(t.Else, resolved, table, cls) {
				return true
			}
		case *ast.SwitchMarkup:
			for _, cc := range t.Cases {
				if scopeUsesNumeric(cc.Body, resolved, table, cls) {
					return true
				}
			}
		}
	}
	return false
}

// attrsUseNumericScratch reports whether any of an element's attrs (recursing into
// { if … } cond-attr branches) emits a numeric value through emitAttrValue — the
// only attribute path that writes via _gsxnum. It mirrors emitExprAttr /
// emitEmbeddedTextAttr routing:
//   - a plain attr={n} with numeric value, UNLESS in URL context (routed to
//     gw.URL, which is string-only — a numeric there would not compile anyway);
//   - a numeric @{ } hole in an EmbeddedText backtick literal, again excluding URL
//     context (there the whole value concatenates to one string for gw.URL via
//     holeStringExpr/FormatInt, never emitAttrValue); and a whole-literal pipe on
//     an EmbeddedText literal whose piped result is numeric (rendered via
//     emitAttrValue). js`/css` EmbeddedAttrs (EmbeddedJS/EmbeddedCSS) and
//     style={…}/class={…} ClassAttrs route through the JS/CSS/merge writers, not
//     emitAttrValue, and are not matched here.
func attrsUseNumericScratch(attrs []ast.Attr, resolved map[ast.Node]types.Type, table funcTables, cls *attrclass.Classifier) bool {
	for _, a := range attrs {
		switch at := a.(type) {
		case *ast.ExprAttr:
			if cls.Context(at.Name) != attrclass.CtxURL && resolvedTypeIsNumeric(at, resolved, table) {
				return true
			}
		case *ast.EmbeddedAttr:
			if at.Lang != ast.EmbeddedText || cls.Context(at.Name) == attrclass.CtxURL {
				continue
			}
			if len(at.Stages) > 0 {
				// Whole-literal pipe: renders one piped value via emitAttrValue.
				if resolvedTypeIsNumeric(at, resolved, table) {
					return true
				}
			} else {
				for _, seg := range at.Segments {
					if in, ok := seg.(*ast.Interp); ok && resolvedTypeIsNumeric(in, resolved, table) {
						return true
					}
				}
			}
		case *ast.CondAttr:
			if attrsUseNumericScratch(at.Then, resolved, table, cls) || attrsUseNumericScratch(at.Else, resolved, table, cls) {
				return true
			}
		}
	}
	return false
}

// interpIsNumeric reports whether interp n renders as an int/uint/float (the same
// classification emitRender uses to pick gw.IntInto/UintInto/FloatInto).
func interpIsNumeric(n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables) bool {
	return resolvedTypeIsNumeric(n, resolved, table)
}

// resolvedTypeIsNumeric reports whether node n's resolved type renders as an
// int/uint/float, unwrapping a (T, error) tuple exactly as genInterp/emitExprAttr
// do, then rewriting through a registered [renderers] entry exactly as
// applyRenderer does (effectiveRenderType — a renderer returning int/uint/float
// makes the emit path take the IntInto/UintInto/FloatInto arm on the
// renderer's RESULT type, so the prescan must classify that same type or the
// scratch declaration would be skipped and the generated code would not
// compile). Shared by the text (interpIsNumeric) and attribute
// (attrsUseNumericScratch) scans in scopeUsesNumeric, plus the
// *ast.EmbeddedInterp whole-literal-pipe node, so all agree with the emit
// paths that write through _gsxnum.
func resolvedTypeIsNumeric(n ast.Node, resolved map[ast.Node]types.Type, table funcTables) bool {
	t, ok := resolved[n]
	if !ok || t == nil {
		return false
	}
	if tup, ok := t.(*types.Tuple); ok {
		// Mirror the (T, error) unwrap exactly; any other tuple shape is rejected
		// at emit with a diagnostic, so it never reaches numeric emission.
		if tup.Len() != 2 || tup.At(1).Type().String() != "error" {
			return false
		}
		t = tup.At(0).Type()
	}
	// Same order as every emit boundary: tuple-unwrap FIRST, renderer AFTER
	// (the renderer is registered for T, not (T, error)).
	t = effectiveRenderType(t, table)
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
func genStyleChild(b *bytes.Buffer, n ast.Markup, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
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
func emitCSSInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table, emitPipeWrap(b, interpTemp))
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
		expr, t = applyRenderer(b, tmp, elemT, table, imports, interpTemp, "return _gsxerr")
		return emitRenderCSS(b, expr, t, n, bag)
	}
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
	return emitRenderCSS(b, expr, t, n, bag)
}

// emitRenderCSS writes a value in CSS block context (inside <style>): RawCSS and
// numbers are emitted raw (safe by construction); strings/Stringers go through
// gw.CSS (the value-filter). n is the AST node for positioning any error diagnostic.
func emitRenderCSS(b *bytes.Buffer, expr string, t types.Type, n ast.Node, bag *diag.Bag) bool {
	if isRawCSS(t) {
		fmt.Fprintf(b, "\t\t_gsxgw.S(string(%s))\n", expr)
		return true
	}
	switch classify(t) {
	case catInt:
		// Digits (and a leading '-') are safe verbatim in a CSS value exactly as
		// in text/attr context, so write them straight from the per-render scratch
		// buffer — no string allocation, no filter pass. Matches the `S` (verbatim)
		// output the FormatInt form produced. See scopeUsesNumeric for _gsxnum's scope.
		fmt.Fprintf(b, "\t\t_gsxgw.IntInto(_gsxnum[:], int64(%s))\n", expr)
	case catUint:
		fmt.Fprintf(b, "\t\t_gsxgw.UintInto(_gsxnum[:], uint64(%s))\n", expr)
	case catFloat:
		fmt.Fprintf(b, "\t\t_gsxgw.FloatInto(_gsxnum[:], float64(%s))\n", expr)
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
func genScriptChild(b *bytes.Buffer, n ast.Markup, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
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
func emitJSInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table, emitPipeWrap(b, interpTemp))
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
		expr, t = applyRenderer(b, tmp, elemT, table, imports, interpTemp, "return _gsxerr")
		return emitJSValue(b, n.JSCtx, expr, t, n, bag)
	}
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
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
	case ast.JSCtxBinding:
		// Binding/lvalue position: only gsx.RawJS may splice here. JSVal emits a
		// RawJS value verbatim at runtime, so the static type gate is the guard.
		if !isRawJS(t) {
			bindingPositionDiag(bag, n.Pos(), n.End())
			return false
		}
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
// the bag with code "unresolved-pipeline" and ok=false. b and interpTemp hoist a
// mid-stage (R, error) filter via emitPipeWrap (all callers are emit-only element
// contexts; no probe variant of this path exists).
func spreadAttrExpr(a *ast.SpreadAttr, table funcTables, imports map[string]bool, b *bytes.Buffer, interpTemp *int, bag *diag.Bag) (string, bool) {
	expr := strings.TrimSpace(a.Expr)
	if len(a.Stages) == 0 {
		return expr, true
	}
	lowered, usedPkgs, err := lowerPipe(a.Expr, a.Stages, table, emitPipeWrap(b, interpTemp))
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
func emitAttr(b *bytes.Buffer, attrs []ast.Attr, a ast.Attr, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag, mergeExpr string, nonce *nonceInjection) bool {
	switch t := a.(type) {
	case *ast.StaticAttr:
		fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+t.Name+`="`+htmlAttrEscape(t.Value)+`"`))
		nonce.markExplicit(b, t.Name)
	case *ast.BoolAttr:
		fmt.Fprintf(b, "\t\t_gsxgw.BoolAttr(%s, true)\n", strconv.Quote(t.Name))
		nonce.markExplicit(b, t.Name)
	case *ast.ExprAttr:
		if !emitExprAttr(b, attrs, t, resolved, table, imports, rt, interpTemp, cls, tag, bag) {
			return false
		}
		nonce.markExplicit(b, t.Name)
		return true
	case *ast.EmbeddedAttr:
		switch t.Lang {
		case ast.EmbeddedJS:
			if !emitEmbeddedJSAttr(b, t, resolved, table, imports, rt, interpTemp, bag) {
				return false
			}
		case ast.EmbeddedCSS:
			if !emitEmbeddedCSSAttr(b, t, resolved, table, imports, rt, interpTemp, bag) {
				return false
			}
		case ast.EmbeddedText:
			if !emitEmbeddedTextAttr(b, t, resolved, table, imports, rt, interpTemp, cls, tag, bag) {
				return false
			}
		default:
			bag.Errorf(a.Pos(), a.End(), "unsupported-attr", "unknown embedded attribute language %d", t.Lang)
			return false
		}
		nonce.markExplicit(b, t.Name)
	case *ast.ClassAttr:
		// class -> token merge (gw.Class); style -> '; '-joined declarations
		// (gw.Style) with dynamic parts CSS-value-filtered.
		if cls.Context(t.Name) == attrclass.CtxCSS {
			if !emitStyleAttr(b, t, table, imports, rt, interpTemp, bag, resolved) {
				return false
			}
		} else {
			if !emitClassAttr(b, t, table, imports, rt, interpTemp, bag, mergeExpr, resolved) {
				return false
			}
		}
	case *ast.SpreadAttr:
		// emitAttr runs only for non-component elements (genNode routes component
		// tags to genChildComponent before the attr loop), so a SpreadAttr here is
		// always an element spread — e.g. one nested inside a `{ if c { { x... } } }`
		// cond-attr, which never reaches the top-level bagSpreadIndex dispatch. It is
		// still a leaf URL sink: it routes through Spread (excluded=nil, so
		// nothing is force-owned) so URL-classified keys sanitize regardless of
		// provenance/nesting, exactly like a top-level element spread.
		spreadExpr, ok := spreadAttrExpr(t, table, imports, b, interpTemp, bag)
		if !ok {
			return false
		}
		if tmp, hoisted := nonce.tempFor(t); hoisted {
			// Nonce-eligible element: assign the hoisted temp once (single
			// evaluation) and spread from it; the post-attr guard reads it.
			fmt.Fprintf(b, "\t\t%s = %s\n", tmp, spreadExpr)
			emitSpreadCall(b, tmp, tag, cls, "nil")
		} else {
			emitSpreadCall(b, spreadExpr, tag, cls, "nil")
		}
	case *ast.CondAttr:
		// Attr emission is a sequence of writer calls between `<tag` and `>`, so
		// wrapping the branch's attr emits in a real Go `if`/`else` is valid. An
		// else-if is a *CondAttr in Else, handled by the recursive emitAttr below.
		// (No //line for the cond: emitAttr has no fset, the wrapper is a pure
		// control construct, and each nested attr emit carries its own line map.)
		fmt.Fprintf(b, "\t\tif %s {\n", t.Cond)
		for _, inner := range t.Then {
			if !emitAttr(b, attrs, inner, resolved, table, imports, rt, interpTemp, cls, tag, bag, mergeExpr, nonce) {
				return false
			}
		}
		if len(t.Else) > 0 {
			b.WriteString("\t\t} else {\n")
			for _, inner := range t.Else {
				if !emitAttr(b, attrs, inner, resolved, table, imports, rt, interpTemp, cls, tag, bag, mergeExpr, nonce) {
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
	case *ast.CommentAttr:
		// Source-only comment (// /* */ or braced {/* */}); never rendered.
		return true
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
	case ast.EmbeddedText:
		return "f"
	default:
		return fmt.Sprintf("unknown(%d)", lang)
	}
}

// emitEmbeddedJSAttr emits an explicit JS attribute literal whose quoted value
// is literal JS with @{ } holes. Static JS text is
// HTML-attr-escaped at codegen so <,>,& survive the attribute; each hole is
// escaped by its JSCtx and then HTML-attr-escaped (the *Attr escapers do both).
// embeddedStaticText returns the concatenated literal text of a's segments and
// true when EVERY segment is static (*ast.Text) — i.e. the js`…`/css`…`/`…`
// literal has no @{ } interpolation. A hole-free embedded literal is
// byte-identical to a plain string attribute: the element emitters
// (emitEmbeddedJSAttr/CSS/Text) all HTML-attr-escape the same raw text and
// stream it, so in a component-prop or conditional bag it lowers to the SAME
// {Key, Value: rawtext} entry a plain StaticAttr fallthrough produces
// (emit.go StaticAttr case), and the runtime HTML-attr-escapes it once on
// spread. An interpolated literal returns ("", false): forwarding a hole into a
// bag needs JS/CSS-context-correct per-hole escaping, a separate feature.
func embeddedStaticText(a *ast.EmbeddedAttr) (string, bool) {
	var sb strings.Builder
	for _, seg := range a.Segments {
		t, ok := seg.(*ast.Text)
		if !ok {
			return "", false
		}
		sb.WriteString(t.Value)
	}
	return sb.String(), true
}

func emitEmbeddedJSAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	for _, seg := range a.Segments {
		switch s := seg.(type) {
		case *ast.Text:
			fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(htmlAttrEscape(s.Value)))
		case *ast.Interp:
			if !emitJSAttrInterp(b, s, resolved, table, imports, rt, interpTemp, bag) {
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

// urlWriterMethod returns the generated Writer method for a URL-context
// attribute: "Srcset" for srcset/imagesrcset (a comma-separated image-
// candidate list, sanitized per candidate), "URLImage" for an image resource
// sink (data:image/* allowed), "URL" otherwise. Callers must have established
// CtxURL for name.
func urlWriterMethod(tag, name string) string {
	switch strings.ToLower(name) {
	case "srcset", "imagesrcset":
		return "Srcset"
	}
	if attrclass.URLSink(tag, name) == attrclass.SinkImage {
		return "URLImage"
	}
	return "URL"
}

// goStringSliceLit renders names as a Go `[]string{…}` literal, or "nil" when
// empty — the argument form Spread's name-set params expect.
func goStringSliceLit(names []string) string {
	if len(names) == 0 {
		return "nil"
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return fmt.Sprintf("[]string{%s}", strings.Join(q, ", "))
}

// emitSpreadCall emits `_gsxgw.Spread(ctx, expr, …)` for bag
// expression expr on element tag: the classifier's URL-exact names split into
// the nav vs image vs srcset sinks via urlWriterMethod, prefix URL rules pass
// through, and excludedExpr is the names a forced site owns ("nil" when
// nothing is forced — the standalone / nested-cond-attr spread case). Every
// element spread routes through here so URL-classified keys sanitize at the
// leaf regardless of the bag's provenance or nesting.
func emitSpreadCall(b *bytes.Buffer, expr, tag string, cls *attrclass.Classifier, excludedExpr string) {
	var navNames, imageNames, srcsetNames []string
	for _, name := range cls.URLExactNames() {
		switch urlWriterMethod(tag, name) {
		case "URLImage":
			imageNames = append(imageNames, name)
		case "Srcset":
			srcsetNames = append(srcsetNames, name)
		default:
			navNames = append(navNames, name)
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s, %s, %s, %s, %s, %s)\n",
		expr, goStringSliceLit(navNames), goStringSliceLit(imageNames),
		goStringSliceLit(srcsetNames), goStringSliceLit(cls.URLPrefixes()), excludedExpr)
}

// firstSegIsDataURL reports whether the literal's first segment is static text
// whose value begins (case-insensitively) with the data: scheme — the compile-
// time signal that an author wrote a data: URL literal.
func firstSegIsDataURL(segs []ast.Markup) bool {
	if len(segs) == 0 {
		return false
	}
	txt, ok := segs[0].(*ast.Text)
	if !ok {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(txt.Value)), "data:")
}

// emitEmbeddedTextAttr emits a plain backtick attribute literal name=`…@{expr}…`
// [ |> f ].
//
// A whole-literal pipeline (len(a.Stages) > 0) ALWAYS assembles first: the
// segments are joined into one Go string expression (embeddedTextValueExpr) and
// piped through a.Stages via the SAME lowerPipe call analyze.go's probe used
// (so resolved[a] is already the pipeline's result type — emit ≡ probe),
// unwrapping a trailing (T, error) tuple exactly like genInterp/
// emitEmbeddedInterp. The piped result then renders for URL or plain-attr
// context depending on cls.Context(a.Name), below.
//
// A URL-context attribute (cls.Context(a.Name) == attrclass.CtxURL) is
// sanitized as a WHOLE value: every segment (static text and each hole) is
// assembled into one Go string expression and passed through a single
// _gsxgw.URL(...) call — the SAME urlSanitize fail-closed allow-list +
// entity-escape that href={ expr } uses. This is deliberate: an earlier
// per-hole classifier had FIVE browser-confirmed XSS bypasses (a dangerous
// scheme can be split across hole boundaries, e.g. href=`@{a}@{b}` with
// a="javascript" b=":alert(1)"). There is no per-hole scheme/seam detection
// here — assembling first and sanitizing once closes that class of bypass
// entirely. This invariant EXTENDS to the piped case: URL() sanitizes the
// PIPE'S OUTPUT, so a filter that returns a dangerous scheme is still blocked
// (sanitize-after-pipe, never before). When the pipeline's result type isn't
// already string, it is converted via stringifyExpr (the same string dispatch
// holeStringExpr uses) before entering URL(), which requires a string arg.
//
// Non-URL attributes keep the per-segment path (Text -> S(htmlAttrEscape),
// Interp -> emitTextAttrInterp) when Stages is empty; with Stages, the piped
// result renders via emitAttrValue (string -> AttrValue(string(x)), etc.).
func emitEmbeddedTextAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag) bool {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	isURL := cls.Context(a.Name) == attrclass.CtxURL
	// A literal that OPENS with data: on a strict sink is author error
	// regardless of any downstream pipe: reject at compile time (this
	// inspects the pre-pipe first segment deliberately — unlike Form B's
	// sanitize-after-pipe runtime path, a static data: prefix here is never
	// a safe navigational/script value). gsx.RawURL is the vouching opt-out.
	if isURL && urlWriterMethod(tag, a.Name) == "URL" && firstSegIsDataURL(a.Segments) {
		bag.Errorf(a.Pos(), a.End(), "data-url-strict-sink",
			"data: URL literal in attribute %q on <%s> is a navigational/script sink where data: is blocked; use an image sink (<img src>, <video poster>, background) or gsx.RawURL if you have validated it",
			a.Name, tag)
		return false
	}
	switch {
	case len(a.Stages) > 0:
		concat, ok := embeddedTextValueExpr(b, a, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr")
		if !ok {
			return false
		}
		lowered, usedPkgs, err := lowerPipe(concat, a.Stages, table, emitPipeWrap(b, interpTemp))
		if err != nil {
			bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return false
		}
		for _, path := range usedPkgs {
			imports[path] = true
		}
		t, ok := resolved[a]
		if !ok || t == nil {
			bag.Errorf(a.Pos(), a.End(), "unresolved-interp", "could not resolve type of attribute %q pipeline", a.Name)
			return false
		}
		if _, isTuple := t.(*types.Tuple); isTuple {
			elemT, ok := tupleUnwrapType(t)
			if !ok {
				bag.Errorf(a.Pos(), a.End(), "invalid-tuple", "attribute %q pipeline returns %s; only (T, error) is supported", a.Name, t)
				return false
			}
			lowered = hoistTuple(b, lowered, interpTemp)
			t = elemT
		}
		// Renderer FIRST, then whichever classification/sanitization follows
		// (URL sink or plain emitAttrValue) — same order as emitExprAttr and
		// emitTextAttrInterp: the URL sanitizer below always runs on the
		// renderer's OUTPUT, never on the pre-renderer registered type.
		lowered, t = applyRenderer(b, lowered, t, table, imports, interpTemp, "return _gsxerr")
		if isURL {
			// Sanitize AFTER the pipe: URL() runs on the pipeline's OUTPUT, so a
			// filter returning a dangerous scheme is still blocked, never trusted.
			strExpr, ok := stringifyExpr(lowered, t, rt, a, bag, fmt.Sprintf("attribute %q pipeline result", a.Name))
			if !ok {
				return false
			}
			fmt.Fprintf(b, "\t\t_gsxgw.%s(%s)\n", urlWriterMethod(tag, a.Name), strExpr)
		} else if !emitAttrValue(b, lowered, t, rt, a, bag) {
			return false
		}
	case isURL:
		concat, ok := embeddedTextValueExpr(b, a, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr")
		if !ok {
			return false
		}
		fmt.Fprintf(b, "\t\t_gsxgw.%s(%s)\n", urlWriterMethod(tag, a.Name), concat)
	default:
		for _, seg := range a.Segments {
			switch s := seg.(type) {
			case *ast.Text:
				fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(htmlAttrEscape(s.Value)))
			case *ast.Interp:
				if !emitTextAttrInterp(b, s, resolved, table, imports, rt, interpTemp, bag) {
					return false
				}
			default:
				bag.Errorf(seg.Pos(), seg.End(), "unsupported-attr", "attribute %q value may contain only text and @{ } interpolations, got %T", a.Name, seg)
				return false
			}
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// embeddedTextValueExpr assembles a's segments (static text + @{ } holes) into
// ONE Go string expression, joining each segment with " + ": static text is a
// RAW quoted string literal (NOT htmlAttrEscape'd) and each hole is lowered by
// holeStringExpr to a same-type-routed string conversion (string/[]byte,
// int/uint/float via strconv, Stringer via .String()). It never runs the whole
// merged value through any escaper itself — that is the caller's job, done
// ONCE over the fully assembled string: emitEmbeddedTextAttr's CtxURL branch
// passes it to _gsxgw.URL (entity-escape + scheme sanitize), and the
// class/style merge-target emitters (emitRootEmbeddedClass, the embedStyle
// branch of emitFallthroughAttrs' emitSpread) pass it to gsx.Class(...) /
// StyleMerged, whose single gw.AttrValue call HTML-attr-escapes the merged
// result. Escaping per segment here would be wrong in both cases: it would
// double-escape the static text and leave interpolated values unescaped
// (URL) or bypass the merge machinery's raw-string contract (class/style,
// which operates on unescaped tokens/declarations before its one final
// escape).
func embeddedTextValueExpr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, errReturn string) (string, bool) {
	return embeddedValueExpr(b, a.Segments, resolved, table, imports, rt, interpTemp, bag, errReturn, false, false, "unsupported-attr", fmt.Sprintf("attribute %q value", a.Name))
}

// componentEmbeddedTextValueExpr materializes an f-literal as an unescaped Go
// string expression at a component boundary. Unlike emitEmbeddedTextAttr, this
// path does not write an element attribute: the returned string is assigned to
// a declared prop (or wrapped in Text for a Node prop) or stored as the value of
// an unmatched Attrs entry. A whole-literal pipeline therefore runs before the
// final supported result is stringified, including renderer dispatch and
// (T, error) unwrapping.
func componentEmbeddedTextValueExpr(
	b *bytes.Buffer,
	a *ast.EmbeddedAttr,
	resolved map[ast.Node]types.Type,
	table funcTables,
	imports map[string]bool,
	rt rtImports,
	interpTemp *int,
	bag *diag.Bag,
	errReturn string,
) (string, bool) {
	// Lower each hole into its own buffer first. holeStringExpr may need to emit
	// statements for a tuple/error pipeline, renderer, or AttrString conversion.
	// If those statements were written directly to b, a later hole's hoist would
	// run before an earlier ordinary call left inline in the final concat.
	type part struct {
		expr  string
		hole  bool
		hoist bytes.Buffer
	}
	parts := make([]part, 0, len(a.Segments))
	ordered := false
	for _, seg := range a.Segments {
		switch s := seg.(type) {
		case *ast.Text:
			if s.Value != "" {
				parts = append(parts, part{expr: strconv.Quote(s.Value)})
			}
		case *ast.Interp:
			var hoist bytes.Buffer
			expr, ok := holeStringExpr(&hoist, s, resolved, table, imports, rt, interpTemp, bag, errReturn, false, false)
			if !ok {
				return "", false
			}
			if hoist.Len() > 0 {
				ordered = true
			}
			parts = append(parts, part{expr: expr, hole: true, hoist: hoist})
		default:
			bag.Errorf(seg.Pos(), seg.End(), "unsupported-attr", "attribute %q value may contain only text and @{ } interpolations, got %T", a.Name, seg)
			return "", false
		}
	}

	valueParts := make([]string, 0, len(parts))
	for i := range parts {
		p := &parts[i]
		if ordered && p.hole {
			// Emit every hole's own hoists at its authored segment position. Pin
			// call-shaped string expressions too, so an earlier ordinary call is
			// complete before a later fallible hole and a failure prevents only
			// subsequent holes. isCallExpr parses Go AST; this is not a source-text
			// pattern heuristic.
			b.Write(p.hoist.Bytes())
			if isCallExpr(p.expr) {
				tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				fmt.Fprintf(b, "\t\t%s := %s\n", tmp, p.expr)
				p.expr = tmp
			}
		}
		valueParts = append(valueParts, p.expr)
	}
	value := `""`
	if len(valueParts) > 0 {
		value = strings.Join(valueParts, " + ")
	}
	if len(a.Stages) == 0 {
		return value, true
	}

	lowered, usedPkgs, err := lowerPipe(value, a.Stages, table, pipeWrapReturning(b, interpTemp, errReturn))
	if err != nil {
		bag.Errorf(a.Pos(), a.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return "", false
	}
	for _, path := range usedPkgs {
		imports[path] = true
	}

	t, ok := resolved[a]
	if !ok || t == nil {
		bag.Errorf(a.Pos(), a.End(), "unresolved-interp", "could not resolve type of component attribute %q pipeline", a.Name)
		return "", false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(a.Pos(), a.End(), "invalid-tuple", "component attribute %q pipeline returns %s; only (T, error) is supported", a.Name, t)
			return "", false
		}
		lowered = hoistTupleReturning(b, lowered, interpTemp, errReturn)
		t = elemT
	}
	lowered, t = applyRenderer(b, lowered, t, table, imports, interpTemp, errReturn)
	return stringifyExpr(lowered, t, rt, a, bag, fmt.Sprintf("component attribute %q pipeline result", a.Name))
}

// embeddedValueExpr is embeddedTextValueExpr generalized over a raw segment
// list, so a body/child *ast.EmbeddedInterp's Segments can be assembled through
// the SAME logic (static text -> raw quoted literal, each hole -> holeStringExpr)
// without an *ast.EmbeddedAttr wrapper. errReturn is the error-return statement
// matching the enclosing function's signature ("return _gsxerr" in the render
// closure, "return nil, _gsxerr" in an (Attrs, error) cond-attr thunk) — every
// hole hoist (pipeline, tuple, renderer, AttrString) uses it. errCode/errDesc
// position and word the "unsupported segment" diagnostic for the caller's
// context (attribute vs. body literal). rejectErr/rejectCtx are forwarded to
// holeStringExpr: they gate the Go-expression-position rejections for the f`
// path (see holeStringExpr) and are false for the render-closure body/attribute
// callers, which keep hoisting.
func embeddedValueExpr(b *bytes.Buffer, segs []ast.Markup, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, errReturn string, rejectErr, rejectCtx bool, errCode, errDesc string) (string, bool) {
	parts := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch s := seg.(type) {
		case *ast.Text:
			if s.Value == "" {
				continue
			}
			parts = append(parts, strconv.Quote(s.Value))
		case *ast.Interp:
			p, ok := holeStringExpr(b, s, resolved, table, imports, rt, interpTemp, bag, errReturn, rejectErr, rejectCtx)
			if !ok {
				return "", false
			}
			parts = append(parts, p)
		default:
			bag.Errorf(seg.Pos(), seg.End(), errCode, "%s may contain only text and @{ } interpolations, got %T", errDesc, seg)
			return "", false
		}
	}
	if len(parts) == 0 {
		return `""`, true
	}
	return strings.Join(parts, " + "), true
}

// emitGoExprEmbeddedInterp lowers a prefixed literal (f`/js`/css`) in a
// Go-expression position — a top-level GoWithElements value, a `{{ }}`
// GoBlock, or an Interp.Embedded part — to one self-contained Go value. f`
// assembles a plain Go string concat (embeddedValueExpr); js`/css` wrap the
// JS/CSS-escaped concat in _gsxrt.RawJS/_gsxrt.RawCSS. exprPos=!canHoist
// (passed to the JS/CSS assemblers, same as the f` path) forbids per-hole
// statement hoists where there is no clean hoist channel, so error-carrying
// holes are rejected in embeddedHoleExpr and the concat stays source-ordered;
// where canHoist is true, the assemblers instead materialize each dynamic
// hole to a source-ordered `_gsxvN` temp in hoistBuf, with error shapes
// hoisting through `return _gsxerr` — the same fold-path lowering an
// attribute-local literal already uses.
//
// Two buffers keep the f` path's routing intact: any statement hoist an f` or
// (now) js`/css` hole emits goes to hoistBuf (the enclosing render-closure
// buffer), while the value expression is written to valBuf. At the top-level
// GoWithElements site the two are the same buffer; at the Interp.Embedded
// site hoistBuf is the render closure (before the consuming statement) and
// valBuf is the spliced-seed builder. The caller rejects a whole-literal
// pipeline (p.Stages) first, since the two sites word/position that
// diagnostic differently.
//
// hasCtx / canHoist describe the CONTAINER's render context, and gate the
// no-render-context rejections uniformly across f`/js`/css`:
//   - hasCtx=false (a top-level GoWithElements value — no ambient `ctx`) rejects
//     ctx-taking filters/renderers (rejectCtx), which would reference an
//     undefined `ctx`.
//   - canHoist=false (no clean pre-statement hoist channel — a top-level value,
//     or a `{{ }}` GoBlock whose reconstruction interleaves GoText into hoistBuf)
//     rejects error-carrying holes (rejectErr / exprPos) for f`/js`/css` alike.
//
// The in-closure Interp.Embedded and component-value sites pass (true, true) and
// keep hoisting/threading ctx exactly as before.
func emitGoExprEmbeddedInterp(hoistBuf, valBuf *bytes.Buffer, p *ast.EmbeddedInterp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, hasCtx, canHoist bool) bool {
	exprPos := !canHoist
	switch p.Lang {
	case ast.EmbeddedJS:
		val, ok := embeddedJSValueExpr(hoistBuf, p.Segments, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr", exprPos, !hasCtx)
		if !ok {
			return false
		}
		valBuf.WriteString(rt.rt())
		valBuf.WriteString(".RawJS(")
		valBuf.WriteString(val)
		valBuf.WriteByte(')')
	case ast.EmbeddedCSS:
		val, ok := embeddedCSSValueExpr(hoistBuf, p.Segments, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr", exprPos, !hasCtx)
		if !ok {
			return false
		}
		valBuf.WriteString(rt.rt())
		valBuf.WriteString(".RawCSS(")
		valBuf.WriteString(val)
		valBuf.WriteByte(')')
	default:
		val, ok := embeddedValueExpr(hoistBuf, p.Segments, resolved, table, imports, rt, interpTemp, bag, "return _gsxerr", !canHoist, !hasCtx, "unsupported-node", "backtick literal value")
		if !ok {
			return false
		}
		valBuf.WriteString(val)
	}
	return true
}

// assembleHoleSeed returns the Go expression for a hole's seed. A plain hole is
// its Expr verbatim. A hole whose Expr carries embedded prefixed literals
// (Interp.Embedded, seated by preprocessComponentCallSites when a nested f`/js`/css`
// literal appears in the hole) is reassembled from its parts: GoText runs
// verbatim, each nested literal lowered to its Go value by
// emitGoExprEmbeddedInterp — the same splice genInterp performs for a body
// interp's seed (emit.go ~2120). Capability flags derive from the containing
// hole's own rejection flags (hasCtx = !rejectCtx, canHoist = !rejectErr) so a
// nested literal's holes obey the same position rules as the hole that contains
// them: an error-carrying hole inside a nested literal at a top-level value
// position still fails with the goexpr-literal-error diagnostic. A whole-literal
// pipeline on a nested literal (p.Stages) and an embedded *Element/*Fragment part
// are both rejected — element values are gsx.Node closures, which no
// attribute-literal hole can render. Any hoist emitted by a nested hole lands in
// hoistBuf BEFORE this returns, so it precedes the statement that consumes the
// assembled expression, exactly as holeStringExpr's own hoists do.
func assembleHoleSeed(hoistBuf *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, rejectErr, rejectCtx bool) (string, bool) {
	if n.Embedded == nil {
		return strings.TrimSpace(n.Expr), true
	}
	var eb bytes.Buffer
	for _, part := range n.Embedded {
		switch p := part.(type) {
		case ast.GoText:
			eb.WriteString(p.Src)
		case *ast.EmbeddedInterp:
			if len(p.Stages) > 0 {
				bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
				return "", false
			}
			if !emitGoExprEmbeddedInterp(hoistBuf, &eb, p, resolved, table, imports, rt, interpTemp, bag, !rejectCtx, !rejectErr) {
				return "", false
			}
		default:
			bag.Errorf(n.Pos(), n.End(), "unsupported-node", "element literals are not supported inside this interpolation position; bind the element to a variable in a {{ }} block or use a { } child position")
			return "", false
		}
	}
	return strings.TrimSpace(eb.String()), true
}

// holeStringExpr lowers one @{ } hole inside a backtick attribute literal to a
// Go STRING expression, for whole-value assembly by embeddedTextValueExpr
// (used for both a URL-context literal's CtxURL branch and a class/style
// literal's merge-target emit). It mirrors emitTextAttrInterp's pipeline
// (lowerPipe/emitPipeWrap) and (T, error) tuple auto-unwrap
// (tupleUnwrapType/hoistTuple) — any hoisting is emitted to b BEFORE this
// returns, so temps precede the _gsxgw.URL(...) call that consumes the
// returned expression. Type routing mirrors emitAttrValue's classify(t)
// categories (emit.go ~2670), but produces an expression instead of a writer
// call: string/[]byte -> string(x), int/uint/float -> strconv.Format*,
// Stringer -> (x).String(), a MIXED non-tilde type parameter (catAnyMixed) ->
// a hoisted rt.AttrString conversion. Any other type (bool, unresolved)
// cannot safely carry a URL fragment and is rejected with a diagnostic.
// errReturn is the error-return statement matching the enclosing function's
// signature; every hoist emitted here (pipeline, tuple, renderer, AttrString)
// uses it, so the same lowering serves the render closure ("return _gsxerr")
// and an (Attrs, error) cond-attr thunk ("return nil, _gsxerr").
// rejectErr / rejectCtx bring the f`-literal (plain-string) path to parity with
// embeddedHoleExpr's js`/css` exprPos rejections: they are set at a Go-expression
// position where a hole cannot safely lower. rejectErr (no clean statement hoist
// channel — a top-level value or a `{{ }}` GoBlock, whose reconstruction
// interleaves GoText into the hoist buffer) rejects every error-carrying shape (a
// pipeline (R, error) stage, a (T, error) seed, an error-returning renderer, or a
// mixed type-parameter value whose rt.AttrString conversion returns
// (string, error)). rejectCtx (no ambient `ctx` — a top-level value only) rejects
// a ctx-taking filter or renderer, whose lowering references an undefined `ctx`.
// Both default to false for the URL/class/style merge callers, whose hoists land
// in a real render-closure statement context.
func holeStringExpr(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, errReturn string, rejectErr, rejectCtx bool) (string, bool) {
	// A hole carrying a nested prefixed literal (n.Embedded != nil) is
	// reassembled from its parts; a plain hole is its Expr verbatim. Nested-hole
	// hoists land in b before this returns, ahead of the consuming stmt.
	expr, sok := assembleHoleSeed(b, n, resolved, table, imports, rt, interpTemp, bag, rejectErr, rejectCtx)
	if !sok {
		return "", false
	}
	if rejectErr && pipelineHasErr(n.Stages, table) {
		bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } uses an error-returning filter (|>); "+goExprLiteralErrorRemedy, pipeSourceText(n.Expr, n.Stages))
		return "", false
	}
	if rejectCtx && pipelineWantsCtx(n.Stages, table) {
		bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } uses a ctx-taking filter (|>); "+goExprCtxRemedy, pipeSourceText(n.Expr, n.Stages))
		return "", false
	}
	if len(n.Stages) > 0 {
		// Seed the pipeline with the assembled expr (a nested literal is already
		// lowered to its Go value), not raw n.Expr — matching genInterp.
		lowered, usedPkgs, err := lowerPipe(expr, n.Stages, table, pipeWrapReturning(b, interpTemp, errReturn))
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return "", false
		}
		for _, p := range usedPkgs {
			imports[p] = true
		}
		expr = lowered
	}
	t, ok := resolved[n]
	if !ok || t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of interpolation %q", n.Expr)
		return "", false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "interpolation %q returns %s; only (T, error) is supported", expr, t)
			return "", false
		}
		if rejectErr {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } returns %s; "+goExprLiteralErrorRemedy, expr, t)
			return "", false
		}
		expr = hoistTupleReturning(b, expr, interpTemp, errReturn)
		t = elemT
	}
	if e, ok := table.renderers[rendererKey(t)]; ok {
		if rejectErr && e.hasErr {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } is rendered by an error-returning renderer; "+goExprLiteralErrorRemedy, n.Expr)
			return "", false
		}
		if rejectCtx && e.wantsCtx {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } is rendered by a ctx-taking renderer; "+goExprCtxRemedy, n.Expr)
			return "", false
		}
	}
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, errReturn)
	switch classify(t) {
	case catString, catBytes:
		return "string(" + expr + ")", true
	case catStringSlice:
		return rt.st() + ".Join(" + expr + ", \" \")", true
	case catAnyMixed:
		if rejectErr {
			// A mixed type-parameter value routes through rt.AttrString, which
			// returns (string, error) and must hoist that error — impossible
			// without a statement context.
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } is a mixed type-parameter value (its runtime conversion can fail); "+goExprLiteralErrorRemedy, n.Expr)
			return "", false
		}
		return hoistTupleReturning(b, rt.rt()+".AttrString("+expr+")", interpTemp, errReturn), true
	default:
		return stringifyExpr(expr, t, rt, n, bag, fmt.Sprintf("attribute interpolation %q", n.Expr))
	}
}

// stringifyExpr converts expr (of resolved type t) to a Go STRING expression,
// routing by classify(t): string/[]byte -> string(x), int/uint/float ->
// strconv.Format*, Stringer -> (x).String(). Any other type (bool, catAnyMixed,
// unresolved) cannot safely carry a URL fragment (or stand in for one) and is
// rejected with a diagnostic positioned at n, worded with the caller-supplied
// errPrefix. Shared by holeStringExpr's per-hole dispatch and
// emitEmbeddedTextAttr's URL-context whole-pipe branch (a piped result whose
// type isn't already string).
func stringifyExpr(expr string, t types.Type, rt rtImports, n ast.Node, bag *diag.Bag, errPrefix string) (string, bool) {
	switch classify(t) {
	case catString, catBytes:
		return "string(" + expr + ")", true
	case catStringSlice:
		return rt.st() + ".Join(" + expr + ", \" \")", true
	case catInt:
		return rt.sc() + ".FormatInt(int64(" + expr + "), 10)", true
	case catUint:
		return rt.sc() + ".FormatUint(uint64(" + expr + "), 10)", true
	case catFloat:
		return rt.sc() + ".FormatFloat(float64(" + expr + "), 'g', -1, 64)", true
	case catStringer:
		return "(" + expr + ").String()", true
	default:
		bag.Errorf(n.Pos(), n.End(), "unsupported-url-type", "%s has unsupported type %s (need string/number/Stringer)", errPrefix, t)
		return "", false
	}
}

// emitEmbeddedCSSAttr emits an explicit CSS attribute literal whose quoted value
// is literal CSS with @{ } holes. Static CSS text is HTML-attr-escaped at
// codegen; each hole is CSS-value-filtered with gsx.StyleValue and then
// HTML-attr-escaped.
func emitEmbeddedCSSAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	for _, seg := range a.Segments {
		switch s := seg.(type) {
		case *ast.Text:
			fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(htmlAttrEscape(s.Value)))
		case *ast.Interp:
			if !emitCSSAttrInterp(b, s, resolved, table, imports, rt, interpTemp, bag) {
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
func emitJSAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
	// An attribute literal is inside the render closure (ctx binds, b is a clean
	// hoist channel), so a nested literal's holes may hoist and take ctx.
	expr, sok := assembleHoleSeed(b, n, resolved, table, imports, rt, interpTemp, bag, false, false)
	if !sok {
		return false
	}
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(expr, n.Stages, table, emitPipeWrap(b, interpTemp))
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
		tmp, elemT = applyRenderer(b, tmp, elemT, table, imports, interpTemp, "return _gsxerr")
		return emitJSAttrValue(b, n.JSCtx, tmp, elemT, n, bag)
	}
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
	return emitJSAttrValue(b, n.JSCtx, expr, t, n, bag)
}

// emitTextAttrInterp renders one @{ } hole in a plain non-URL attribute literal
// (the else branch of emitEmbeddedTextAttr). Mirrors emitJSAttrInterp's
// pipeline + (T,error) auto-unwrap, then routes through the type-aware
// emitAttrValue (string→AttrValue, numbers→strconv, Stringer→.String()).
func emitTextAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
	// An attribute literal is inside the render closure (ctx binds, b is a clean
	// hoist channel), so a nested literal's holes may hoist and take ctx.
	expr, sok := assembleHoleSeed(b, n, resolved, table, imports, rt, interpTemp, bag, false, false)
	if !sok {
		return false
	}
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(expr, n.Stages, table, emitPipeWrap(b, interpTemp))
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
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of attribute interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "attribute interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		expr = hoistTuple(b, expr, interpTemp)
		t = elemT
	}
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
	return emitAttrValue(b, expr, t, rt, n, bag)
}

// emitCSSAttrInterp renders one @{ } hole in an explicit CSS attribute literal.
// It mirrors emitCSSInterp's pipeline-stage handling and (T, error) tuple
// auto-unwrap, but routes through gsx.StyleValue followed by AttrValue because
// the result is inside a quoted HTML attribute.
func emitCSSAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
	// An attribute literal is inside the render closure (ctx binds, b is a clean
	// hoist channel), so a nested literal's holes may hoist and take ctx.
	expr, sok := assembleHoleSeed(b, n, resolved, table, imports, rt, interpTemp, bag, false, false)
	if !sok {
		return false
	}
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(expr, n.Stages, table, emitPipeWrap(b, interpTemp))
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
		tmp, elemT = applyRenderer(b, tmp, elemT, table, imports, interpTemp, "return _gsxerr")
		return emitRenderCSSAttr(b, tmp, elemT, rt, n, bag)
	}
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
	return emitRenderCSSAttr(b, expr, t, rt, n, bag)
}

// emitRenderCSSAttr writes a value in an explicit CSS attribute literal. The
// value is first reduced to a CSS-safe string with gsx.StyleValue, then escaped
// for the surrounding HTML attribute with gw.AttrValue.
func emitRenderCSSAttr(b *bytes.Buffer, expr string, t types.Type, rt rtImports, n ast.Node, bag *diag.Bag) bool {
	styleExpr := ""
	if isRawCSS(t) {
		styleExpr = expr
	} else {
		switch classify(t) {
		case catInt:
			styleExpr = rt.sc() + ".FormatInt(int64(" + expr + "), 10)"
		case catUint:
			styleExpr = rt.sc() + ".FormatUint(uint64(" + expr + "), 10)"
		case catFloat:
			styleExpr = rt.sc() + ".FormatFloat(float64(" + expr + "), 'g', -1, 64)"
		case catString, catBytes:
			styleExpr = "string(" + expr + ")"
		case catStringer:
			styleExpr = "(" + expr + ").String()"
		default:
			bag.Errorf(n.Pos(), n.End(), "unrenderable-css", "value of type %s not renderable in CSS context (need string/number/Stringer or gsx.RawCSS)", t)
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(%s.StyleValue(%s))\n", rt.rt(), styleExpr)
	return true
}

// embeddedHoleExpr lowers the evaluation shared by contextual embedded holes.
// Context-specific escaping is deliberately left to the caller. errReturn is
// the error-return statement matching the enclosing function's signature (see
// holeStringExpr); every hoist emitted here uses it.
//
// exprPos is set where NO statement hoist channel exists — a top-level
// GoWithElements value or a `{{ }}` GoBlock (whose reconstruction interleaves
// GoText into the hoist buffer): there the whole literal must lower to ONE
// self-contained Go expression, so there is no statement context to hoist
// into and no error channel to route a filter/renderer/tuple error through.
// Every hole that WOULD need a statement hoist — a pipeline stage that
// returns (R, error), a (T, error) tuple seed, or a renderer that returns
// (R, error) — is rejected with a positioned "goexpr-literal-error"
// diagnostic instead (goExprLiteralErrorRemedy names both ways out: handle
// the error in Go before the literal, or move the literal to an attribute
// position, where the render closure DOES have an error-returning statement
// context). Pure pipes (no error stage) and pure renderers lower to nested
// calls, which are valid expressions, so they remain allowed. In-closure
// Go-expression sites (an Interp.Embedded part or a braced component-prop
// binding, canHoist=true in emitGoExprEmbeddedInterp) pass exprPos=false and
// hoist error-carrying holes exactly like an f` hole at the same site.
//
// rejectCtx is the ctx-availability counterpart: set ONLY where the literal has
// no ambient render context (a top-level GoWithElements value — no `ctx` binds
// there), it rejects a ctx-taking filter or renderer, whose lowering references
// `ctx` and would otherwise silently poison the .x.go at go build
// (goExprCtxRemedy). Positions that DO bind `ctx` (Interp.Embedded, a `{{ }}`
// GoBlock, a component-value slot) pass false and keep such holes, since
// `<pkg>.<Fn>(ctx, …)` is a valid expression there.
func embeddedHoleExpr(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, errReturn string, exprPos, rejectCtx bool) (string, types.Type, bool) {
	// A hole carrying a nested prefixed literal (n.Embedded != nil) is
	// reassembled from its parts; a plain hole is its Expr verbatim. Nested-hole
	// hoists land in b before this returns, ahead of the consuming stmt. exprPos
	// is the error-rejection flag (rejectErr) at this Go-expression site.
	expr, sok := assembleHoleSeed(b, n, resolved, table, imports, rt, interpTemp, bag, exprPos, rejectCtx)
	if !sok {
		return "", nil, false
	}
	if len(n.Stages) > 0 {
		if exprPos && pipelineHasErr(n.Stages, table) {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } uses an error-returning filter (|>); "+goExprLiteralErrorRemedy, pipeSourceText(n.Expr, n.Stages))
			return "", nil, false
		}
		if rejectCtx && pipelineWantsCtx(n.Stages, table) {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } uses a ctx-taking filter (|>); "+goExprCtxRemedy, pipeSourceText(n.Expr, n.Stages))
			return "", nil, false
		}
		// Seed the pipeline with the assembled expr (a nested literal is already
		// lowered to its Go value), not raw n.Expr — matching genInterp.
		lowered, used, err := lowerPipe(expr, n.Stages, table, pipeWrapReturning(b, interpTemp, errReturn))
		if err != nil {
			bag.Errorf(n.Pos(), n.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
			return "", nil, false
		}
		for _, p := range used {
			imports[p] = true
		}
		expr = lowered
	}
	t := resolved[n]
	if t == nil {
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of contextual attribute interpolation %q", n.Expr)
		return "", nil, false
	}
	if _, tuple := t.(*types.Tuple); tuple {
		elem, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "contextual attribute interpolation %q returns %s; only (T, error) is supported", expr, t)
			return "", nil, false
		}
		if exprPos {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } returns %s; "+goExprLiteralErrorRemedy, expr, t)
			return "", nil, false
		}
		expr = hoistTupleReturning(b, expr, interpTemp, errReturn)
		t = elem
	}
	if e, ok := table.renderers[rendererKey(t)]; ok {
		if exprPos && e.hasErr {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } is rendered by an error-returning renderer; "+goExprLiteralErrorRemedy, n.Expr)
			return "", nil, false
		}
		if rejectCtx && e.wantsCtx {
			bag.Errorf(n.Pos(), n.End(), "goexpr-literal-error", "@{ %s } is rendered by a ctx-taking renderer; "+goExprCtxRemedy, n.Expr)
			return "", nil, false
		}
	}
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, errReturn)
	return expr, t, true
}

// goExprLiteralErrorRemedy is the shared second half of every
// "goexpr-literal-error" diagnostic (embeddedHoleExpr's three exprPos
// rejections): it names the reason (no error channel exists in an arbitrary
// Go-expression position — a var initializer, call argument, or {{ }} block
// is one expression, not a statement sequence to hoist an early-return into)
// and both remedies (handle the error in Go before the value ever reaches the
// literal, or move the literal into an attribute position, where the render
// closure IS a statement sequence with an error return).
const goExprLiteralErrorRemedy = "a js`/css`/f` literal in Go-expression position has no error channel to propagate it through — handle the error in Go, or move the literal to an attribute position"

// pipeSourceText reconstructs a hole's `seed |> stage1 |> stage2(args) …`
// source text for diagnostic messages, so a rejection names the actual
// pipeline rather than just its seed expression.
func pipeSourceText(seed string, stages []ast.PipeStage) string {
	var sb strings.Builder
	sb.WriteString(seed)
	for _, s := range stages {
		sb.WriteString(" |> ")
		sb.WriteString(s.Name)
		if s.HasArgs {
			sb.WriteString("(")
			sb.WriteString(s.Args)
			sb.WriteString(")")
		}
	}
	return sb.String()
}

// pipelineHasErr reports whether any stage of a hole's `|>` pipeline resolves to
// a filter that returns (R, error) — i.e. a stage that would force a statement
// hoist (mid-pipeline via pipeWrapReturning, or a final stage producing a
// (T, error) tuple). Used to reject such holes in an expression position, where
// there is no statement context to hoist into.
func pipelineHasErr(stages []ast.PipeStage, table funcTables) bool {
	for _, s := range stages {
		if e, ok := table.filters.lookup(s.Name); ok && e.hasErr {
			return true
		}
	}
	return false
}

// pipelineWantsCtx reports whether any stage of a hole's `|>` pipeline resolves
// to a ctx-taking filter — one whose lowering is `<pkg>.<Fn>(ctx, subject, …)`
// (lowerPipe prepends pipeCtxIdent). Used to reject such holes in a
// Go-expression position with NO ambient render context (a top-level value): the
// lowered call would reference an undefined `ctx` and silently poison the .x.go
// at `go build`. In-closure positions (Interp.Embedded, GoBlock, component value)
// DO bind `ctx`, so they keep such holes.
func pipelineWantsCtx(stages []ast.PipeStage, table funcTables) bool {
	for _, s := range stages {
		if e, ok := table.filters.lookup(s.Name); ok && e.wantsCtx {
			return true
		}
	}
	return false
}

// goExprCtxRemedy is the shared second half of every ctx-related
// "goexpr-literal-error" diagnostic: a hole whose filter or renderer takes the
// ambient render `ctx` cannot lower in a Go-expression position (a var
// initializer, call argument, or top-level value) because no render closure —
// and thus no `ctx` — exists there. It names both remedies: drop the ctx-taking
// filter/renderer, or move the literal to an attribute position, where the
// render closure binds `ctx`.
const goExprCtxRemedy = "a js`/css`/f` literal in Go-expression position has no ambient render context — drop the ctx-taking filter/renderer, or move the literal to an attribute position"

// embeddedJSValueExpr assembles a js`…` literal's segments (static text + @{ }
// holes) into one Go string-concat expression, each hole JS-escaped by its
// JSCtx. segs is the raw segment list (an EmbeddedAttr's or a Go-expression
// EmbeddedInterp's Segments). exprPos selects the Go-expression lowering: when
// true, no per-hole `_gsxvN :=` temp is materialized (the concat is inlined,
// which is naturally source-ordered because nothing hoists) and error-carrying
// holes are rejected in embeddedHoleExpr; when false (the attribute/bag fold
// path) each dynamic hole is materialized to a temp at its source position, so a
// later hole's tuple/renderer/pipeline hoist cannot reorder its evaluation.
func embeddedJSValueExpr(b *bytes.Buffer, segs []ast.Markup, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, errReturn string, exprPos, rejectCtx bool) (string, bool) {
	parts := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch s := seg.(type) {
		case *ast.Text:
			if s.Value != "" {
				parts = append(parts, strconv.Quote(s.Value))
			}
		case *ast.Interp:
			expr, typ, ok := embeddedHoleExpr(b, s, resolved, table, imports, rt, interpTemp, bag, errReturn, exprPos, rejectCtx)
			if !ok {
				return "", false
			}
			var escaped string
			switch s.JSCtx {
			case ast.JSCtxValue:
				escaped = rt.rt() + ".EscapeJSVal(" + expr + ")"
			case ast.JSCtxBinding:
				// Binding/lvalue position: only gsx.RawJS may splice here.
				// EscapeJSVal emits a RawJS value verbatim; the static gate guards.
				if !isRawJS(typ) {
					bindingPositionDiag(bag, s.Pos(), s.End())
					return "", false
				}
				escaped = rt.rt() + ".EscapeJSVal(" + expr + ")"
			case ast.JSCtxString, ast.JSCtxTemplate, ast.JSCtxRegexp:
				str, ok := stringifyJSExpr(expr, typ, s, bag)
				if !ok {
					return "", false
				}
				fn := "EscapeJSStr"
				if s.JSCtx == ast.JSCtxTemplate {
					fn = "EscapeJSTmpl"
				}
				if s.JSCtx == ast.JSCtxRegexp {
					fn = "EscapeJSRegexp"
				}
				escaped = rt.rt() + "." + fn + "(" + str + ")"
			default:
				bag.Errorf(s.Pos(), s.End(), "unsafe-js-context", "JS attribute interpolation %q has no JS context", s.Expr)
				return "", false
			}
			if exprPos {
				// Expression position: nothing can hoist (error-carrying holes are
				// rejected above), so append the escaped expression inline — the
				// concat is already source-ordered.
				parts = append(parts, escaped)
				break
			}
			// Evaluate every dynamic hole at its source position. A later hole may
			// emit tuple/renderer error-handling statements while it is lowered;
			// retaining this expression inline until final concatenation would move
			// that later evaluation ahead of this one.
			name := fmt.Sprintf("_gsxv%d", *interpTemp)
			*interpTemp++
			fmt.Fprintf(b, "\t\t%s := %s\n", name, escaped)
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 {
		return `""`, true
	}
	return strings.Join(parts, " + "), true
}

func stringifyJSExpr(expr string, t types.Type, n ast.Node, bag *diag.Bag) (string, bool) {
	switch classify(t) {
	case catString, catBytes:
		return "string(" + expr + ")", true
	case catStringer:
		return "(" + expr + ").String()", true
	default:
		bag.Errorf(n.Pos(), n.End(), "unrenderable-js", "value of type %s not renderable in a JS string/template/regex context (need string or Stringer)", t)
		return "", false
	}
}

// embeddedCSSValueExpr assembles a css`…` literal's segments into one Go
// string-concat expression, each hole reduced to a CSS-safe string (gsx.RawCSS
// passthrough, otherwise gsx.FilterCSS). segs / exprPos mirror
// embeddedJSValueExpr: exprPos inlines the concat (no per-hole temp) and rejects
// error-carrying holes; the fold path materializes each dynamic hole to a temp.
func embeddedCSSValueExpr(b *bytes.Buffer, segs []ast.Markup, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, errReturn string, exprPos, rejectCtx bool) (string, bool) {
	parts := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch s := seg.(type) {
		case *ast.Text:
			if s.Value != "" {
				parts = append(parts, strconv.Quote(s.Value))
			}
		case *ast.Interp:
			expr, typ, ok := embeddedHoleExpr(b, s, resolved, table, imports, rt, interpTemp, bag, errReturn, exprPos, rejectCtx)
			if !ok {
				return "", false
			}
			var value string
			if isRawCSS(typ) {
				value = "string(" + expr + ")"
			} else {
				str, ok := stringifyExpr(expr, typ, rt, s, bag, fmt.Sprintf("CSS interpolation %q", s.Expr))
				if !ok {
					return "", false
				}
				value = rt.rt() + ".FilterCSS(" + str + ")"
			}
			if exprPos {
				parts = append(parts, value)
				break
			}
			name := fmt.Sprintf("_gsxv%d", *interpTemp)
			*interpTemp++
			fmt.Fprintf(b, "\t\t%s := %s\n", name, value)
			parts = append(parts, name)
		}
	}
	if len(parts) == 0 {
		return `""`, true
	}
	return strings.Join(parts, " + "), true
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
	case ast.JSCtxBinding:
		// Binding/lvalue position: only gsx.RawJS may splice here (verbatim).
		if !isRawJS(t) {
			bindingPositionDiag(bag, n.Pos(), n.End())
			return false
		}
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
// never piped, so callers lower only the part's Expr/Stages, not its Cond. b and
// interpTemp hoist a mid-stage (R, error) filter via emitPipeWrap (the element
// class/style path is emit-only; the probe path harvests via probeExpr instead).
func classPartExpr(p ast.ClassPart, a *ast.ClassAttr, table funcTables, imports map[string]bool, wrap func(string) string, bag *diag.Bag) (string, bool) {
	lowered, usedPkgs, err := lowerClassPartSeed(p, table, wrap)
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
// piped, so only the part's Expr/Stages are lowered. wrap is the lowerPipe hook
// for a mid-stage (R, error) filter: callers pass emitPipeWrap in emit mode,
// probePipeWrap in skeleton mode, or thunkPipeWrap inside a cond-attr branch
// thunk — always non-nil (the buffer each hoisting wrap closes over is the
// caller's; see classEntryExpr's doc).
func lowerClassPartSeed(p ast.ClassPart, table funcTables, wrap func(string) string) (string, map[string]string, error) {
	if len(p.Stages) == 0 {
		return strings.TrimSpace(p.Expr), nil, nil
	}
	return lowerPipe(p.Expr, p.Stages, table, wrap)
}

// emitClassAttr lowers a composable `class={ … }` to the open ` class="`, a
// gw.Class call composing each part (gsx.Class for an unconditional Expr,
// gsx.ClassIf for a conditional one), and the closing `"`. gw.Class runs the
// tokens through the passed merge func and writes the attr-escaped value.
// resolved maps each *ast.ValueArm to its harvest type for (T, error) unwrap.
func emitClassAttr(b *bytes.Buffer, a *ast.ClassAttr, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, mergeExpr string, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, rt, interpTemp, bag, resolved, false, emitPipeWrap(b, interpTemp), "return _gsxerr")
	if !ok {
		return false
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	fmt.Fprintf(b, "\t\t_gsxgw.Class(%s, %s)\n", classMergeExpr(mergeExpr, rt), strings.Join(parts, ", "))
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// emitStyleAttr lowers a composable `style={ … }` to ` style="` + a gw.Style call
// composing each part as a CSS declaration, then `"`. A string-literal part is
// trusted and emitted raw; any dynamic part value is wrapped in gsx.FilterCSS so
// untrusted data cannot inject declarations or break out. gw.Style joins the
// included parts with "; " and attr-escapes the result.
// resolved maps each *ast.ValueArm to its harvest type for (T, error) unwrap.
func emitStyleAttr(b *bytes.Buffer, a *ast.ClassAttr, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, rt, interpTemp, bag, resolved, true, emitPipeWrap(b, interpTemp), "return _gsxerr")
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

func composedParts(b *bytes.Buffer, a *ast.ClassAttr, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type, style bool, wrap func(string) string, errReturn string) ([]string, bool) {
	parts := make([]string, 0, len(a.Parts))
	ordered := composedPartsOrdered(a, resolved)
	for i := range a.Parts {
		p := &a.Parts[i]
		if p.CF != nil {
			tmp, ok := hoistValueCF(b, p.CF, table, imports, rt, interpTemp, style, bag, resolved)
			if !ok {
				return nil, false
			}
			parts = append(parts, fmt.Sprintf("%s.Class(%s)", rt.rt(), tmp))
			continue
		}
		if p.CSSSegments != nil {
			if !style {
				bag.Errorf(p.Pos(), p.End(), "unsupported-class-part", "css literal parts are only valid in style={...}")
				return nil, false
			}
			val, ok := cssLiteralStylePartExpr(b, p.CSSSegments, resolved, table, imports, rt, interpTemp, bag)
			if !ok {
				return nil, false
			}
			if p.Cond == "" {
				parts = append(parts, fmt.Sprintf("%s.Class(%s)", rt.rt(), val))
				continue
			}
			cond := strings.TrimSpace(p.Cond)
			if ordered {
				tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				fmt.Fprintf(b, "\t\t%s := %s\n", tmp, cond)
				cond = tmp
			}
			parts = append(parts, fmt.Sprintf("%s.ClassIf(%s, %s)", rt.rt(), val, cond))
			continue
		}
		expr, ok := classPartExpr(*p, a, table, imports, wrap, bag)
		if !ok {
			return nil, false
		}
		t := resolved[p]
		if tup, isTuple := t.(*types.Tuple); isTuple {
			elemT, ok := tupleUnwrapType(tup)
			if !ok {
				kind := "class"
				if style {
					kind = "style"
				}
				bag.Errorf(p.Pos(), p.End(), "invalid-tuple", "%s part %q returns %s; only (T, error) is supported", kind, p.Expr, t)
				return nil, false
			}
			expr = hoistTupleReturning(b, expr, interpTemp, errReturn)
			t = elemT
			// The part's own tuple hoist already lands expr in a position-
			// preserving temp; applyRenderer may turn it back into a bare call
			// (a no-error renderer), which — since composedPartsOrdered forces
			// ordered=true whenever ANY part is itself tuple-typed — must be
			// re-captured to stay pinned at this source position, exactly like
			// the ordered branch below does for a non-tuple part. A renderer
			// registry miss, or a hasErr renderer (already a hoisted temp),
			// leaves expr as a bare identifier and skips the extra capture.
			expr, _ = applyRenderer(b, expr, t, table, imports, interpTemp, errReturn)
			if isCallExpr(expr) {
				tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
				expr = tmp
			}
		} else {
			// Renderer FIRST (mirrors every other render boundary), THEN the
			// ordered capture — so a renderer's call (or its own error hoist)
			// evaluates at this part's SOURCE position, not deferred to the
			// final gw.Class/ClassJoin call site.
			expr, _ = applyRenderer(b, expr, t, table, imports, interpTemp, errReturn)
			if ordered {
				tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
				*interpTemp++
				fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
				expr = tmp
			}
		}
		val := expr
		if style && (len(p.Stages) > 0 || !isStringLiteralExpr(strings.TrimSpace(p.Expr))) {
			val = rt.rt() + ".StyleValue(" + expr + ")"
		}
		if p.Cond == "" {
			parts = append(parts, fmt.Sprintf("%s.Class(%s)", rt.rt(), val))
			continue
		}
		cond := strings.TrimSpace(p.Cond)
		if ordered {
			tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
			*interpTemp++
			fmt.Fprintf(b, "\t\t%s := %s\n", tmp, cond)
			cond = tmp
		}
		parts = append(parts, fmt.Sprintf("%s.ClassIf(%s, %s)", rt.rt(), val, cond))
	}
	return parts, true
}

func cssLiteralStylePartExpr(b *bytes.Buffer, segments []ast.Markup, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) (string, bool) {
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
				lowered, usedPkgs, err := lowerPipe(s.Expr, s.Stages, table, emitPipeWrap(b, interpTemp))
				if err != nil {
					bag.Errorf(s.Pos(), s.End(), "unresolved-pipeline", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
					return "", false
				}
				for _, p := range usedPkgs {
					imports[p] = true
				}
				expr = lowered
			}
			t := resolved[s]
			if tup, isTuple := t.(*types.Tuple); isTuple {
				elemT, ok := tupleUnwrapType(tup)
				if !ok {
					bag.Errorf(s.Pos(), s.End(), "invalid-tuple", "style css literal interpolation %q returns %s; only (T, error) is supported", expr, t)
					return "", false
				}
				expr = hoistTuple(b, expr, interpTemp)
				t = elemT
			}
			// Renderer FIRST: StyleValue(any) would otherwise fmt.Sprint the raw
			// registered-type value instead of the author's rendered string.
			expr, _ = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")
			parts = append(parts, rt.rt()+".StyleValue("+expr+")")
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
func styleDeclExpr(expr string, rt rtImports, piped bool) string {
	e := strings.TrimSpace(expr)
	if !piped && isStringLiteralExpr(e) {
		return e
	}
	return rt.rt() + ".StyleValue(" + e + ")"
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
func emitExprAttr(b *bytes.Buffer, attrs []ast.Attr, a *ast.ExprAttr, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag) bool {
	// (1) value expression: lower a pipeline to nested std calls (same lowerPipe
	// the probe used, so resolved[a] is already the pipeline's RESULT type), else
	// the bare trimmed expr.
	expr := strings.TrimSpace(a.Expr)
	if len(a.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(a.Expr, a.Stages, table, emitPipeWrap(b, interpTemp))
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

	// (2b) apply a registered [renderers] entry, if t is one: expr becomes the
	// renderer's call (hoisted through the caller's error-return "return
	// _gsxerr" when the renderer itself returns (R, error)) and t becomes the
	// renderer's result type. A registry miss returns (expr, t) unchanged. This
	// runs BEFORE every classification below (bool/meta-refresh/URL/plain), so
	// a renderer's result — including a URL-context string — is what the URL
	// sanitizer downstream ever sees: renderer FIRST, sanitize AFTER, never the
	// reverse.
	expr, t = applyRenderer(b, expr, t, table, imports, interpTemp, "return _gsxerr")

	if classify(t) == catBool {
		fmt.Fprintf(b, "\t\t_gsxgw.BoolAttr(%s, bool(%s))\n", strconv.Quote(a.Name), expr)
		return true
	}

	// A gsx.RawURL content value is the author's vouch, exactly as in the URL
	// branch below — fall through to gw.AttrValue. Non-string-like values
	// (numbers) cannot carry a redirect URL and keep §5 type-aware rendering.
	isMetaRefreshContent := strings.EqualFold(a.Name, "content") && strings.EqualFold(tag, "meta") && attrsDeclareRefresh(attrs) && !isRawURL(t)

	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	if isMetaRefreshContent && isStringLike(t) {
		fmt.Fprintf(b, "\t\t_gsxgw.RefreshContent(%s)\n", stringLikeExpr(expr, t))
	} else if cls.Context(a.Name) == attrclass.CtxURL && !isRawURL(t) {
		// URL context: value must be string-like; sanitize + escape. A gsx.RawURL
		// value (isRawURL) is the author's vouch — fall through to gw.AttrValue,
		// which entity-escapes but skips the scheme allow-list.
		fmt.Fprintf(b, "\t\t_gsxgw.%s(%s)\n", urlWriterMethod(tag, a.Name), urlStringExpr(expr, t))
	} else {
		if !emitAttrValue(b, expr, t, rt, a, bag) {
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
}

// attrsDeclareRefresh reports whether the sibling attrs statically mark the
// element as a meta refresh: an http-equiv="refresh" as a static attr, a
// constant string-literal expr attr, or either inside a conditional attr — a
// refresh in ANY branch marks the element, which is safe because
// refreshContentSanitize no-ops on values that aren't a refresh directive. A
// runtime-dynamic http-equiv={expr} is deliberately out of scope (pinned in
// corpus security/meta_refresh_dynamic_http_equiv); an http-equiv carried
// inside an `{ attrs... }` element spread is likewise not detected here, so
// its "content" key never gets the refresh-content sanitizer — it renders
// through Spread's ordinary per-key routing instead (URL-sanitized
// if URL-classified, attribute-escaped otherwise).
func attrsDeclareRefresh(attrs []ast.Attr) bool {
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.StaticAttr:
			if strings.EqualFold(t.Name, "http-equiv") && strings.EqualFold(strings.TrimSpace(t.Value), "refresh") {
				return true
			}
		case *ast.ExprAttr:
			if strings.EqualFold(t.Name, "http-equiv") && len(t.Stages) == 0 {
				if v, ok := stringLiteralValue(t.Expr); ok && strings.EqualFold(strings.TrimSpace(v), "refresh") {
					return true
				}
			}
		case *ast.CondAttr:
			if attrsDeclareRefresh(t.Then) || attrsDeclareRefresh(t.Else) {
				return true
			}
		}
	}
	return false
}

// stringLiteralValue reports the constant value of a Go string-literal
// expression (`"refresh"`, “ `refresh` “), and false for anything else.
func stringLiteralValue(expr string) (string, bool) {
	e, err := goparser.ParseExpr(strings.TrimSpace(expr))
	if err != nil {
		return "", false
	}
	lit, ok := e.(*goast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return v, true
}

// isStringLike reports whether t renders as a string (string/[]byte/Stringer) —
// the categories that could carry a redirect URL in refresh content.
func isStringLike(t types.Type) bool {
	switch classify(t) {
	case catString, catBytes, catStringer:
		return true
	}
	return false
}

// stringLikeExpr converts a string-like value expression to a plain string.
func stringLikeExpr(expr string, t types.Type) string {
	if classify(t) == catStringer {
		return "(" + expr + ").String()"
	}
	return "string(" + expr + ")"
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

// isRawJS reports whether t is gsx.RawJS. A JSCtxBinding hole (a JS
// binding/lvalue position) is legal only for this type, which is spliced
// verbatim.
func isRawJS(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Name() == "RawJS" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
}

// bindingPositionDiag records the diagnostic for a non-gsx.RawJS @{ } hole in a
// JS binding/lvalue position. The three JS emit paths share it.
func bindingPositionDiag(bag *diag.Bag, pos, end token.Pos) {
	bag.Errorf(pos, end, "jsx-binding-position",
		"@{ } here is a JavaScript binding/lvalue position (assignment target, declaration or member name); only a gsx.RawJS value may be spliced here — wrap it as gsx.RawJS(...) if the bytes are trusted, or use it where a value is expected")
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
func emitAttrValue(b *bytes.Buffer, expr string, t types.Type, rt rtImports, n ast.Node, bag *diag.Bag) bool {
	switch classify(t) {
	case catString:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(string(%s))\n", expr)
	case catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(string(%s))\n", expr)
	case catStringSlice:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(%s.Join(%s, \" \"))\n", rt.st(), expr)
	case catInt:
		// Format into the per-render scratch buffer and write the digit bytes
		// directly — no string allocation, no escaping. A decimal integer is
		// byte-identical in text and double-quoted attribute contexts (digits and
		// a leading '-' are never HTML-escaped), so the same primitive the body
		// path uses is correct here. See scopeUsesNumeric for _gsxnum's scope.
		fmt.Fprintf(b, "\t\t_gsxgw.IntInto(_gsxnum[:], int64(%s))\n", expr)
	case catUint:
		fmt.Fprintf(b, "\t\t_gsxgw.UintInto(_gsxnum[:], uint64(%s))\n", expr)
	case catFloat:
		fmt.Fprintf(b, "\t\t_gsxgw.FloatInto(_gsxnum[:], float64(%s))\n", expr)
	case catStringer:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue((%s).String())\n", expr)
	case catAnyMixed:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrAny(%s)\n", expr)
	default:
		if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
			bag.Errorf(n.Pos(), n.End(), "unsupported-attr-type",
				"attribute value %q has type parameter %s (constraint %s): only same-kind or all-non-tilde renderable constraints render directly — convert explicitly in the expression", expr, t, tp.Constraint())
			return false
		}
		bag.Errorf(n.Pos(), n.End(), "unsupported-attr-type", "attribute value type %s not supported (string/number/bool/Stringer only)", t)
		return false
	}
	return true
}

// genChildComponent emits the exact positional call proven for el. Slot markup
// becomes a closure in the parent scope, so its interps retain the caller's
// params and block locals.
func genChildComponent(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) bool {
	plan, ok := positionalPlan.siteForElement(el)
	if !ok {
		bag.Errorf(el.Pos(), el.End(), "component-positional-plan", "component call has no completed positional plan")
		return false
	}
	return emitPositionalComponentCall(b, el, plan, positionalEmitContext{
		currentPkg: currentPkg, resolved: resolved, table: table, imports: imports, rt: rt,
		importAliases: importAliases, boundNames: boundNames, typeArgAliases: typeArgAliases,
		interpTemp: interpTemp, fset: fset, recvVar: recvVar, recvTypeName: recvTypeName,
		cls: cls, bag: bag, mergeExpr: mergeExpr, enclosingAttrsBound: enclosingAttrsBound,
		positionalPlan: positionalPlan,
	})
}

type attrError struct {
	pos  token.Pos
	end  token.Pos
	code string
	msg  string
}

func (e *attrError) Error() string { return e.msg }

func emitSlotClosure(nodes []ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table funcTables, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string, enclosingAttrsBound bool, positionalPlan componentPositionalPackagePlan) (string, bool) {
	var slot bytes.Buffer
	fmt.Fprintf(&slot, "%s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
	fmt.Fprintf(&slot, "\t\t_gsxgw := %s.W(_gsxw)\n", rt.rt())
	emitNumScratch(&slot, nodes, resolved, table, cls)
	for _, c := range nodes {
		if !genNode(&slot, c, currentPkg, resolved, table, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, bag, mergeExpr, enclosingAttrsBound, positionalPlan) {
			return "", false
		}
	}
	slot.WriteString("\t\treturn _gsxgw.Err()\n")
	slot.WriteString("\t})")
	return slot.String(), true
}

// isCallExpr reports whether rawVal parses as a Go function-call expression
// (after unwrapping any surrounding parens). Only a CallExpr can yield a
// (T, error) tuple when used in single-value position.
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
// b is always a real buffer: at element level it is the render closure's
// statement buffer; inside a component conditional-attr branch (condBranchAttrs)
// it is that branch's thunk-LOCAL buffer, so a hoisted statement lands inside
// the enclosing `func() (rtPkg.Attrs, error) { ... }` thunk rather than the
// caller's statement stream.
// wrap is the lowerPipe hook for a mid-stage (R, error) filter in a class
// part's pipeline, and is also reused directly to hoist a tuple-returning
// PLAIN part (no pipeline) once resolved confirms it: probePipeWrap in probe
// mode; at element level, emitPipeWrap(b, interpTemp) (single-value
// `return _gsxerr`); inside a cond-attr branch thunk, thunkPipeWrap(b,
// interpTemp) (two-value `return nil, _gsxerr`, since the enclosing thunk's
// signature is (Attrs, error)). The caller selects the variant that matches
// its own enclosing return arity — classEntryExpr itself stays agnostic to
// that choice, hoisting exclusively through wrap.
//
// errReturn enables [renderers] application at this component-class-prop
// boundary, mirroring positional component-call lowering: a part value whose
// type is registered is converted to its renderer's string BEFORE it becomes
// a <rtPkg>.Class(...)/.ClassIf(...) argument — the SAME string constraint
// gsx.Class(s string) imposes at element level (composedParts). Non-empty
// errReturn is the applyRenderer error-return matching the caller's own
// arity ("return _gsxerr" from the element-level call path;
// "return nil, _gsxerr" from condBranchAttrs' thunk). Pass "" to disable: the
// probe (skeleton never dispatches renderers; also gated on probeWrap) and
// genSkippedTagSink's discarded nullary func literal (mirrors
// the discarded-call path's own "" precedent). applyRenderer wants an
// `imports map[string]bool`, but usedPkgs (alias->pkgPath) is this
// function's only import-bookkeeping channel back to the caller — bridged
// through a scratch map whose keys are ignored, same as condBranchAttrs.
func classEntryExpr(b *bytes.Buffer, interpTemp *int, a *ast.ClassAttr, rtPkg string, mergeExpr string, table funcTables, resolved map[ast.Node]types.Type, probeWrap bool, wrap func(string) string, errReturn string) (string, map[string]string, error) {
	parts := make([]string, 0, len(a.Parts))
	usedPkgs := map[string]string{}
	ordered := !probeWrap && composedPartsOrdered(a, resolved)
	// applyClassRenderer applies the registered [renderers] entry for t (if
	// any) to expr, folding any imported renderer package into usedPkgs.
	// A no-op in probe mode, when disabled (errReturn == ""), or when t is
	// unresolved — the same disable conditions component-call lowering uses.
	applyClassRenderer := func(expr string, t types.Type) string {
		if probeWrap || errReturn == "" || t == nil {
			return expr
		}
		scratch := map[string]bool{}
		rendered, _ := applyRenderer(b, expr, t, table, scratch, interpTemp, errReturn)
		for path := range scratch {
			usedPkgs[path] = path
		}
		return rendered
	}
	// captureIfCall re-pins a possibly-rewritten (by applyClassRenderer) call
	// expression at its current source position with a fresh temp — needed
	// only after a tuple part's OWN hoist already produced a bare identifier
	// that applyClassRenderer then turned back into a call (a no-error
	// renderer): composedPartsOrdered forces ordered=true whenever any part is
	// itself tuple-typed, so that call must not float down to the final
	// ClassJoin(...) argument list. A registry miss, or a hasErr renderer
	// (already its own hoisted temp), leaves expr a bare identifier and this
	// is a no-op.
	captureIfCall := func(expr string) string {
		if !isCallExpr(expr) {
			return expr
		}
		tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
		*interpTemp++
		fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
		return tmp
	}
	for i := range a.Parts {
		p := &a.Parts[i]
		if p.CF != nil {
			tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
			*interpTemp++
			fmt.Fprintf(b, "\t\tvar %s string\n", tmp)
			var lowerErr error
			armExpr := func(arm *ast.ValueArm) (string, bool) {
				expr, used, err := lowerClassPartSeed(ast.ClassPart{Expr: arm.Expr, Stages: arm.Stages}, table, wrap)
				if err != nil {
					lowerErr = &attrError{pos: a.Pos(), end: a.End(), code: "unresolved-pipeline", msg: err.Error()}
					return "", false
				}
				maps.Copy(usedPkgs, used)
				if probeWrap && isCallExpr(expr) {
					// Skeleton mode: wrap call exprs with _gsxunwrap so the
					// assignment _gsxvN = _gsxunwrap(cls(v)) compiles even when
					// cls returns (T, error). resolved is nil in skeleton mode, so
					// the emit-mode check below is skipped.
					expr = fmt.Sprintf("_gsxunwrap(%s)", expr)
				} else if probeWrap {
					// Non-call arm expr: stub with "" (#85) — the _gsxvN string
					// assignment must not impose the string constraint in the
					// skeleton.
					expr = `""`
				} else if !probeWrap {
					// Emit mode: consult resolved to detect and hoist (T, error)
					// tuples. wrap writes the hoist into b at this point — after the
					// if/case label and before the _gsxvN = assignment — so it lands
					// inside the correct block, with the errReturn arity (single-
					// value at element level, two-value inside a cond-attr thunk)
					// the caller already baked into wrap.
					if t := resolved[arm]; t != nil {
						if tup, isTuple := t.(*types.Tuple); isTuple {
							elemT, ok := tupleUnwrapType(tup)
							if !ok {
								lowerErr = &attrError{pos: arm.Pos(), end: arm.End(), code: "invalid-tuple", msg: fmt.Sprintf("class value-form arm %q returns %s; only (T, error) is supported", arm.Expr, t)}
								return "", false
							}
							expr = wrap(expr)
							t = elemT
						}
						// The arm's value is assigned directly to the CF's own
						// tmp var inside this if/switch branch, so no extra
						// position-preserving capture is needed here (unlike the
						// plain-part list below, which joins several parts into
						// ONE final ClassJoin(...) call).
						expr = applyClassRenderer(expr, t)
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
		expr, used, err := lowerClassPartSeed(*p, table, wrap)
		if err != nil {
			msg := strings.TrimPrefix(err.Error(), "codegen: ")
			return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unresolved-pipeline", msg: msg}
		}
		maps.Copy(usedPkgs, used)
		if p.Cond == "" {
			// Unconditional plain part: in probe mode stub EVERY part value expr
			// (call or not) with "" so the skeleton never imposes gsx.Class's
			// string constraint — a bare identifier/selector of a non-string
			// (e.g. registered) type must not fail the skeleton's own
			// type-check (#85). Liveness and type harvest ride the counted
			// per-part probes (_gsxuseq, emitProbes); the string constraint IS
			// re-imposed by gsx.Class in the emitted code, so a wrong type
			// still fails to compile there — the stub only defers that check
			// out of the skeleton so the clean emit-time "invalid-tuple"
			// diagnostic can fire first for ALL non-(T,error) tuples.
			//
			// In emit mode, check resolved for a tuple and hoist it.
			if probeWrap {
				expr = `""`
			} else {
				t := resolved[p]
				if tup, isAny := t.(*types.Tuple); isAny {
					elemT, ok2 := tupleUnwrapType(tup)
					if !ok2 {
						return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "invalid-tuple", msg: fmt.Sprintf("class part %q returns %s; only (T, error) is supported", p.Expr, t)}
					}
					expr = wrap(expr)
					expr = captureIfCall(applyClassRenderer(expr, elemT))
				} else {
					expr = applyClassRenderer(expr, t)
					if ordered {
						tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
						*interpTemp++
						fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
						expr = tmp
					}
				}
			}
			parts = append(parts, fmt.Sprintf("%s.Class(%s)", rtPkg, expr))
		} else {
			if !probeWrap && ordered {
				if t, isTuple := resolved[p].(*types.Tuple); isTuple {
					elemT, ok := tupleUnwrapType(t)
					if !ok {
						return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "invalid-tuple", msg: fmt.Sprintf("class part %q returns %s; only (T, error) is supported", p.Expr, t)}
					}
					expr = wrap(expr)
					expr = captureIfCall(applyClassRenderer(expr, elemT))
				} else {
					expr = applyClassRenderer(expr, resolved[p])
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
				if probeWrap {
					// Same #85 stub as the unconditional arm: the value expr
					// must not impose the string constraint in the skeleton.
					// The cond expr is a bool guard and stays as-is.
					expr = `""`
				} else {
					expr = applyClassRenderer(expr, resolved[p])
				}
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

// condAttrsExpr lowers a conditional component attribute (`{ if COND { attrs } }`,
// optionally with an `else { attrs }`) to a single AttrsCond(...) expression:
//
//	<rtPkg>.AttrsCond(<cond>, func() (<rtPkg>.Attrs, error) {
//		<thunk-local hoists for the taken branch's error-returning pipe stages>
//		return <rtPkg>.Attrs{<then>}, nil
//	}, <else>)
//
// The branches are THUNKS so only the taken branch's attrs are evaluated at
// runtime — matching real Go if/else, where the untaken branch's expressions
// (e.g. u.Name when u == nil) never run. Each thunk gets its OWN bytes.Buffer:
// a branch pipeline with an error-returning stage hoists `tmp, _gsxerr :=
// call(); if _gsxerr != nil { return nil, _gsxerr }` INSIDE the thunk body —
// legal because the thunk's own signature is (Attrs, error), unlike the
// enclosing render func/closure — so those statements land before the thunk's
// trailing `return <lit>, nil` and the whole AttrsCond(...) call stays ONE
// expression (function literals are expressions; statements inside them are
// legal in expression contexts, including the skeleton probe). The else
// argument is the bare literal `nil` (not a thunk) when there is no else
// branch. Nesting stays shallow — a CondAttr nested inside a branch is
// unsupported.
//
// probeWrap=true (analyze skeleton) lowers each branch with probePipeWrap so a
// mid/final (R, error) stage stays a single _gsxunwrap(...) expression instead
// of a hoist (the skeleton is compile-only and never executed, so laziness
// doesn't matter there); probeWrap=false (real emit) hoists via thunkPipeWrap.
// Either way this returns ONE expression: the *ast.CondAttr call site hoists it
// with hoistTuple in emit mode, or wraps it in _gsxunwrap(...) in probe mode —
// emit ≡ probe, differing only by that tolerance wrap, never by structure.
func condAttrsExpr(t *ast.CondAttr, rtPkg, tag string, mergeExpr string, table funcTables, probeWrap bool, resolved map[ast.Node]types.Type, imports map[string]bool, rt rtImports, bag *diag.Bag, interpTemp *int, ctx bagContext) (string, map[string]string, error) {
	usedPkgs := map[string]string{}

	// branchThunk builds one branch's `func() (rtPkg.Attrs, error) { ...; return
	// lit, nil }` thunk. tb is thunk-LOCAL: any hoist wrap writes into it, so the
	// hoisted statements land inside this thunk's own body, not the caller's.
	branchThunk := func(attrs []ast.Attr) (string, map[string]string, error) {
		var tb bytes.Buffer
		wrap := probePipeWrap
		if !probeWrap {
			wrap = thunkPipeWrap(&tb, interpTemp)
		}
		lit, used, err := condBranchAttrs(&tb, interpTemp, wrap, probeWrap, attrs, rtPkg, tag, mergeExpr, table, resolved, imports, rt, bag, ctx)
		if err != nil {
			return "", nil, err
		}
		thunk := fmt.Sprintf("func() (%s.Attrs, error) {\n%s\t\treturn %s, nil\n\t}", rtPkg, tb.String(), lit)
		return thunk, used, nil
	}

	thenThunk, thenUsed, err := branchThunk(t.Then)
	if err != nil {
		return "", nil, err
	}
	maps.Copy(usedPkgs, thenUsed)
	elseArg := "nil"
	if len(t.Else) > 0 {
		elseThunk, elseUsed, err := branchThunk(t.Else)
		if err != nil {
			return "", nil, err
		}
		maps.Copy(usedPkgs, elseUsed)
		elseArg = elseThunk
	}
	return fmt.Sprintf("%s.AttrsCond(%s, %s, %s)", rtPkg, strings.TrimSpace(t.Cond), thenThunk, elseArg), usedPkgs, nil
}

// condBranchAttrs builds a <rtPkg>.Attrs expression from one conditional-attr
// branch's attrs (delegating to composeBag). Static/expr/bool attrs become bag
// entries keyed by raw name; a composable class={…} becomes a ClassJoin entry.
// A spread or nested conditional inside a branch composes into the same
// expression (its own ConcatAttrs part), recursing through condAttrsExpr for
// nested conds.
//
// wrap is the lowerPipe hook for an error-returning stage in a branch pipeline —
// always non-nil (probePipeWrap in probe mode, thunkPipeWrap in emit mode): the
// ExprAttr path lowers every pipeline through it, hoisting (emit) or unwrapping
// (probe) any error-returning stage, final or not (lowerPipe only wraps
// non-final stages, so a final error stage is wrapped again here explicitly).
// The same wrap also hoists a PLAIN (no-pipeline) ExprAttr value once resolved
// confirms it is a (T, error) tuple call — mirroring positional call lowering's
// element-level ExprAttr handling, just gated on resolved/wrap instead of the
// later hoist-all-when-any pass (a branch's bag literal is built inline here,
// with no later pass to defer to).
// b/interpTemp are the THUNK-LOCAL buffer/counter from condAttrsExpr, so a
// hoist lands inside the enclosing thunk body, not the caller's statement
// stream. probeWrap distinguishes skeleton (resolved is nil; any CALL expr is
// unconditionally _gsxunwrap'd, generic over arity) from real emit (resolved
// gates the hoist).
//
// The composable-class part of a branch reuses classEntryExpr exactly like
// the element-level positional call path: the same thunk-local b/
// interpTemp/resolved and the same wrap are threaded through, so CF (if/
// switch), plain-tuple, and ordered class parts inside a branch hoist their
// errors into the enclosing thunk precisely like the non-branch case.
func condBranchAttrs(b *bytes.Buffer, interpTemp *int, wrap func(string) string, probeWrap bool, attrs []ast.Attr, rtPkg, tag, mergeExpr string, table funcTables, resolved map[ast.Node]types.Type, imports map[string]bool, rt rtImports, bag *diag.Bag, ctx bagContext) (string, map[string]string, error) {
	return composeBag(b, interpTemp, wrap, probeWrap, attrs, rtPkg, tag, mergeExpr, table, resolved, imports, rt, bag, "return nil, _gsxerr", ctx)
}

// bagContext tells composeBag which caller it is lowering for, so a residual
// rejection is worded for the right surface. The two are genuinely different:
// the component path folds a conditional-attr branch's attrs into a child's
// prop bag (a component prop / conditional branch), whereas the element path
// folds a plain element's attrs (≥2 spreads, or a lone cond-nested spread + a
// root class/style) into one leaf bag — there is no component and no prop.
type bagContext int

const (
	bagComponentCond bagContext = iota // a component conditional-attr branch (condBranchAttrs)
	bagElementFold                     // a multi-spread / lone-cond element fold (foldElementSpreads)
)

// errBagDiagReported is a sentinel composeBag returns when a hole-bearing
// embedded attribute's assembly (embeddedTextValueExpr) has already reported a
// positioned diagnostic to the diag.Bag. Callers that turn a composeBag error
// into a diagnostic MUST treat this as "already reported" and abort silently,
// so one malformed hole yields exactly one diagnostic.
var errBagDiagReported = errors.New("bag diagnostic already reported")

// composeBag lowers attrs into a single Go expression evaluating to
// rtPkg.Attrs: a bare `rtPkg.Attrs{…}` literal when attrs is statics-only (the
// common case — no needless wrapper), a single bare spread/cond expr when
// attrs holds exactly one entry and no statics, or rtPkg.ConcatAttrs(…) over
// each part in source order when mixed. A run of static/expr/bool/class/
// embedded attrs coalesces into one Attrs{…} part; each SpreadAttr or nested
// CondAttr becomes its own part. errReturn is threaded through to
// applyRenderer and classEntryExpr for their error-hoist site; callers pass
// whatever return-statement shape matches their enclosing function's
// signature.
//
// imports/rt/bag are consumed only by the hole-bearing embedded-literal arm,
// which lowers `name=f"…@{expr}…"` (and, in the element-fold context,
// js"…@{}…"/css"…@{}…") into the bag via the embedded*ValueExpr assemblers
// (assembling the segments, hoisting into b, recording strconv/etc. into
// imports, and reporting any positioned diagnostic to bag). In probe mode that
// arm emits a string placeholder instead (the hole's type is harvested by a
// separate _gsxuse probe), so imports/rt/bag go unused there.
func composeBag(b *bytes.Buffer, interpTemp *int, wrap func(string) string, probeWrap bool, attrs []ast.Attr, rtPkg, tag, mergeExpr string, table funcTables, resolved map[ast.Node]types.Type, imports map[string]bool, rt rtImports, bag *diag.Bag, errReturn string, ctx bagContext) (string, map[string]string, error) {
	var entries []string
	usedPkgs := map[string]string{}
	var parts []string
	// flush turns a maximal run of static/expr/bool/class/embedded entries into
	// one rtPkg.Attrs{…} part, so pure-static branches (the overwhelming
	// majority) still emit a single literal — no needless ConcatAttrs wrapper —
	// while a spread or nested cond-attr gets its own part in source order.
	flush := func() {
		if len(entries) > 0 {
			parts = append(parts, fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(entries, ", ")))
			entries = nil
		}
	}
	// Lowering a later conditional or error-returning expression can emit a
	// statement immediately. Materialize contributors already encountered so
	// those statements cannot move the later expression ahead of source order.
	materializePrior := func() {
		flush()
		if probeWrap {
			return
		}
		if len(parts) == 0 {
			return
		}
		expr := parts[0]
		if len(parts) > 1 {
			expr = fmt.Sprintf("%s.ConcatAttrs(%s)", rtPkg, strings.Join(parts, ", "))
		}
		name := fmt.Sprintf("_gsxv%d", *interpTemp)
		*interpTemp = *interpTemp + 1
		fmt.Fprintf(b, "\t\t%s := %s\n", name, expr)
		parts = []string{name}
	}
	orderedWrap := func(expr string) string {
		materializePrior()
		return wrap(expr)
	}
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.SpreadAttr:
			flush()
			expr := strings.TrimSpace(t.Expr)
			if len(t.Stages) > 0 {
				lowered, used, perr := lowerPipe(t.Expr, t.Stages, table, orderedWrap)
				if perr != nil {
					msg := strings.TrimPrefix(perr.Error(), "codegen: ")
					return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
				}
				maps.Copy(usedPkgs, used)
				expr = lowered
			}
			parts = append(parts, expr)
		case *ast.CondAttr:
			materializePrior()
			condExpr, used, cerr := condAttrsExpr(t, rtPkg, tag, mergeExpr, table, probeWrap, resolved, imports, rt, bag, interpTemp, ctx)
			if cerr != nil {
				return "", nil, cerr
			}
			maps.Copy(usedPkgs, used)
			if probeWrap {
				condExpr = fmt.Sprintf("_gsxunwrap(%s)", condExpr)
			} else {
				condExpr = hoistTupleReturning(b, condExpr, interpTemp, errReturn)
			}
			parts = append(parts, condExpr)
		case *ast.StaticAttr:
			value := strconv.Quote(t.Value)
			if ctx == bagElementFold {
				value = fmt.Sprintf("%s.RawURL(%s)", rtPkg, value)
			}
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), value))
		case *ast.ExprAttr:
			val := strings.TrimSpace(t.Expr)
			if len(t.Stages) > 0 {
				lowered, used, perr := lowerPipe(t.Expr, t.Stages, table, orderedWrap)
				if perr != nil {
					msg := strings.TrimPrefix(perr.Error(), "codegen: ")
					return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
				}
				maps.Copy(usedPkgs, used)
				// The final stage is never wrapped by lowerPipe; wrap it here too
				// when it returns (R, error), so a final tuple hoists (emit) /
				// unwraps (probe) instead of sitting raw in the bag literal.
				last := t.Stages[len(t.Stages)-1]
				if e, ok := table.filters.lookup(last.Name); ok && e.hasErr {
					lowered = orderedWrap(lowered)
				}
				val = lowered
			} else if probeWrap {
				// Plain tuple-returning call, no pipeline: mirror positional call lowering's
				// ExprAttr handling — the skeleton unconditionally wraps any CALL
				// expr with _gsxunwrap(...) (generic over T vs (T, error), no type
				// info needed) so the probe compiles regardless of the callee's
				// return arity.
				if isCallExpr(val) {
					val = fmt.Sprintf("_gsxunwrap(%s)", val)
				}
			} else if tup, isTuple := resolved[t].(*types.Tuple); isTuple {
				// Emit mode: resolved says the plain call actually returns
				// (T, error); hoist it the same way a pipeline's final stage would.
				if _, ok := tupleUnwrapType(tup); !ok {
					return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "invalid-tuple", msg: fmt.Sprintf("attribute %q value %q returns %s; only (T, error) is supported", t.Name, t.Expr, tup)}
				}
				val = orderedWrap(val)
			}
			if !probeWrap {
				// Apply a registered [renderers] entry, if the attr's value type is
				// one, BEFORE the value enters the Attrs bag as `any` — this is the
				// last point codegen still knows the value's concrete registered
				// type; once it's `Value: val` in the literal, the runtime Spread
				// only sees `any` and falls back to fmt.Sprint (or the URL sink's
				// own toStr), never a registered renderer. Skipped entirely in
				// probe/skeleton mode: resolved is nil there (any lookup returns the
				// zero types.Type), and the skeleton never dispatches through a
				// renderer call anyway — it only needs to type-check.
				attrType := resolved[t]
				if tup, isTuple := attrType.(*types.Tuple); isTuple {
					if elemT, ok := tupleUnwrapType(tup); ok {
						attrType = elemT
					}
				}
				if _, ok := table.renderers[rendererKey(attrType)]; ok {
					materializePrior()
				}
				// applyRenderer wants an `imports map[string]bool`, but this deep in
				// the bag-literal lowering the only import bookkeeping channel back
				// to the caller is usedPkgs (map[string]string, alias->pkgPath,
				// consumed only by its values — see positional call lowering's
				// `for _, path := range usedPkgs { imports[path] = true }`). Bridge
				// through a scratch map and fold pkgPath in as both key and value;
				// the key is never read downstream, only deduped on.
				scratch := map[string]bool{}
				val, _ = applyRenderer(b, val, attrType, table, scratch, interpTemp, errReturn)
				for path := range scratch {
					usedPkgs[path] = path
				}
			}
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), val))
		case *ast.BoolAttr:
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: true}", strconv.Quote(t.Name)))
		case *ast.ClassAttr:
			// Class/style lowering may emit value-form, tuple, or renderer
			// statements directly rather than through orderedWrap. Pin everything
			// already encountered before entering those lowering paths.
			materializePrior()
			if t.Name == "style" && ctx == bagElementFold {
				// A composable/conditional style={ … } on a folded element: lower it
				// exactly like the inline element path (composedParts with style=true —
				// CSS-filtering each dynamic declaration, trusting string literals)
				// but as the VALUE form (gsx.StyleString, the "; "-join without the
				// attr-escape gw.Style applies) so the leaf's Attrs.Style() aggregates
				// it and the single leaf write escapes once. orderedWrap/errReturn
				// carry the enclosing position's shape — inside an AttrsCond
				// branch thunk that means `return nil, _gsxerr` — so a renderer,
				// tuple, or mid-pipe (R, error) hoist emits the right arity. The
				// component path, with its probe pass, still rejects style below.
				parts, ok := composedParts(b, t, table, imports, rt, interpTemp, bag, resolved, true, orderedWrap, errReturn)
				if !ok {
					// composedParts already reported the positioned diagnostic.
					return "", nil, errBagDiagReported
				}
				entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s.StyleString(%s)}", strconv.Quote(t.Name), rtPkg, strings.Join(parts, ", ")))
				break
			}
			if t.Name != "class" {
				var msg string
				switch ctx {
				case bagElementFold:
					msg = fmt.Sprintf("composable %s={ … } attribute is not yet supported on an element that merges its attributes (<%s>)", t.Name, tag)
				default:
					msg = fmt.Sprintf("%s attribute in a conditional branch (<%s>) not supported yet", t.Name, tag)
				}
				return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			entry, used, eerr := classEntryExpr(b, interpTemp, t, rtPkg, mergeExpr, table, resolved, probeWrap, orderedWrap, errReturn)
			if eerr != nil {
				return "", nil, eerr
			}
			maps.Copy(usedPkgs, used)
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), entry))
		case *ast.CommentAttr:
			// Source-only comment; not a component prop.
		case *ast.EmbeddedAttr:
			// A hole-free embedded literal forwards to the bag as raw text.
			if text, static := embeddedStaticText(t); static {
				entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(text)))
				break
			}
			// A hole-bearing element literal enters the shared bag as an assembled
			// string. Text holes are stringified directly; JS/CSS holes are first
			// escaped for their embedded grammar. The leaf then performs the one
			// surrounding HTML-attribute escape. Component props remain restricted
			// to EmbeddedText because contextual literals are element sinks.
			if t.Lang != ast.EmbeddedText && ctx != bagElementFold {
				// Only the component-prop context can still reach this reject: the
				// element fold (including its cond-attr branches, which inherit
				// bagElementFold through condAttrsExpr) lowers js/css holes above.
				msg := fmt.Sprintf("embedded %s attribute literal %q with @{ } interpolation cannot be used as a component prop on <%s> yet; pass an ordinary prop value or move the literal to an element inside the component", embeddedLangName(t.Lang), t.Name, tag)
				return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			if probeWrap {
				// Skeleton: the hole's own type is harvested by a separate _gsxuse
				// probe (walkMarkupAttrs), and the props/bag literal here is a
				// compile-only `_ =` statement that never runs, so a string
				// placeholder type-checks the bag without needing resolved.
				entries = append(entries, fmt.Sprintf("{Key: %s, Value: \"\"}", strconv.Quote(t.Name)))
				break
			}
			// Hole lowering can emit tuple, pipeline, or renderer hoists directly.
			// Evaluate all earlier bag contributors before those statements.
			materializePrior()
			var val string
			var ok bool
			switch t.Lang {
			case ast.EmbeddedText:
				val, ok = embeddedTextValueExpr(b, t, resolved, table, imports, rt, interpTemp, bag, errReturn)
			case ast.EmbeddedJS:
				val, ok = embeddedJSValueExpr(b, t.Segments, resolved, table, imports, rt, interpTemp, bag, errReturn, false, false)
			case ast.EmbeddedCSS:
				val, ok = embeddedCSSValueExpr(b, t.Segments, resolved, table, imports, rt, interpTemp, bag, errReturn, false, false)
			}
			if !ok {
				// The value assembler has already emitted the positioned
				// diagnostic (unresolved/unusable hole); abort without a second one.
				return "", nil, errBagDiagReported
			}
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), val))
		default:
			var msg string
			switch ctx {
			case bagElementFold:
				msg = fmt.Sprintf("unsupported attribute %T on an element that merges its attributes (<%s>)", a, tag)
			default:
				msg = fmt.Sprintf("unsupported attribute %T in a conditional branch (<%s>)", a, tag)
			}
			return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unsupported-component-attr", msg: msg}
		}
	}
	flush()
	switch len(parts) {
	case 0:
		return fmt.Sprintf("%s.Attrs{}", rtPkg), usedPkgs, nil
	case 1:
		return parts[0], usedPkgs, nil
	default:
		return fmt.Sprintf("%s.ConcatAttrs(%s)", rtPkg, strings.Join(parts, ", ")), usedPkgs, nil
	}
}
