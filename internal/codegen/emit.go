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
// sunkImports (nil-safe) identifies the user import SPECS — by .gsx line +
// path (sunkImportKey) — whose ONLY use in this file was a
// requalification-failed generic tag (see analyzed.sunkImports): the tag's
// call is replaced by a sink that drops the package reference, so each such
// spec is rewritten to a blank `_` import — the emitted file compiles, and
// the import's init side effects are preserved. Position keying means a
// same-path sibling spec on another line is never touched.
func generateFile(file *ast.File, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, cssMin, jsMin func(string) (string, error), cssMinify, jsMinify bool, merger *ClassMergerRef, sunkImports map[sunkImportKey]bool) ([]byte, bool) {
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
	// Build the path→reserved-alias map for the FILTER packages, harvested from
	// the table (every entry records its owning package's alias + path) — computed
	// up front (table doesn't depend on the components below) so boundNames can be
	// seeded with the reserved alias family before any component generates. A path
	// in `imports` that is a filter package is emitted under its reserved alias;
	// std keeps _gsxstd so std-only output is byte-identical to before. The class
	// merger's reserved alias is ALWAYS reserved here (whether or not the merger
	// ends up used — see below), so an inferred type argument can never collide
	// with it either.
	filterAlias := map[string]string{}
	for _, e := range table {
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
			// Resolve each spec's .gsx line (chunk position + intra-chunk
			// offset, the same arithmetic buildSkeleton's //line block uses)
			// so sunk-import lookups key on the exact spec — see the
			// sunkImports param doc. specLine stays nil when positions are
			// unavailable; no rewrite happens then (fail toward keeping the
			// user's import verbatim).
			var specLine func(srcOff int) int
			if len(sunkImports) > 0 && fset != nil && v.Pos().IsValid() {
				if tf := fset.File(v.Pos()); tf != nil {
					base := fset.Position(v.Pos()).Offset
					specLine = func(srcOff int) int { return fset.Position(tf.Pos(base + srcOff)).Line }
				}
			}
			for _, s := range specs {
				// A sunk import spec (its only use was a requalification-
				// failed generic tag whose call was replaced by a sink — see
				// the sunkImports param doc) is emitted as a blank import:
				// the emitted body no longer references it, so a named/plain
				// import would fail `go build` with "imported and not used",
				// while `_` compiles and keeps init side effects. Dot/blank
				// user spellings are left untouched (a dotted tag can only
				// qualify through a named or plain import).
				if specLine != nil && s.name != "." && s.name != "_" &&
					sunkImports[sunkImportKey{line: specLine(s.srcOff), path: s.path}] {
					aliased = append(aliased, importSpec{name: "_", path: s.path})
					continue
				}
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
			if genComponent(&cbuf, v, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, &interpTemp, fset, cls, fm, bag, mergeExpr) {
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
					if !emitElementValue(&wbuf, p, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, &interpTemp, fset, cls, fm, bag, mergeExpr) {
						partsOK = false
					}
				case *ast.Fragment:
					if !emitFragmentValue(&wbuf, p, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, &interpTemp, fset, cls, fm, bag, mergeExpr) {
						partsOK = false
					}
				case *ast.EmbeddedInterp:
					// A prefixed backtick literal in Go-expression position lowers to
					// a Go string value: embeddedValueExpr assembles its statics +
					// @{…} holes into ONE string concat (`"hi " + string(name)`), the
					// SAME assembly the body/attr embedded forms use. Any contextual
					// escaping is applied where this string is consumed, not here.
					if len(p.Stages) > 0 {
						bag.Errorf(p.Pos(), p.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
						partsOK = false
					} else if val, vok := embeddedValueExpr(&wbuf, p.Segments, resolved, table, imports, rt, &interpTemp, bag, "unsupported-node", "backtick literal value"); !vok {
						partsOK = false
					} else {
						wbuf.WriteString(val)
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

func genComponent(b *bytes.Buffer, c *ast.Component, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	params, err := parseParams(c.Params)
	if err != nil {
		bag.Errorf(c.Pos(), c.End(), "invalid-syntax", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}
	// Type-param validation MUST precede reserved-param validation — MIRRORS
	// emitComponentSkeleton's priority: when both defects co-occur
	// (`Box[T](children T)`), the skeleton skips the component on the broken
	// type-param list (a broken list makes every param type suspect), so the
	// diagnostic surfaced here must be the same defect.
	typeParamNames, err := parseTypeParamNames(c.TypeParams)
	if err != nil {
		bag.Errorf(c.Pos(), c.End(), "invalid-syntax", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}
	if err := checkReservedParams(params); err != nil {
		bag.Errorf(c.Pos(), c.End(), "reserved-param", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		return false
	}
	typeParamsDecl := typeParamDecl(c.TypeParams)
	typeParamsUse := typeParamUse(typeParamNames)

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
	if c.Recv != "" && len(typeParamNames) > 0 && !toolchainHasGenericMethods() {
		// A generic METHOD component needs a toolchain whose go/parser accepts
		// methods with type parameters (go1.27+); older toolchains reject the
		// emitted skeleton outright, which would otherwise hard-abort the whole
		// run (module_importer). Skip this component with a positioned
		// diagnostic instead — MIRRORS emitComponentSkeleton's guard
		// (analyze.go), which sits in the same relative position (after the
		// recv-parsing block, before the BYO check below).
		bag.Errorf(c.Pos(), c.End(), "unsupported-toolchain",
			"generic method components require a Go toolchain with generic methods (go1.27+); active toolchain: %s", runtime.Version())
		return false
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
			fmt.Fprintf(b, "func %s %s%s(%s) %s.Node {\n", c.Recv, c.Name, typeParamsDecl, strings.TrimSpace(c.Params), rt.rt())
		} else {
			fmt.Fprintf(b, "func %s%s(%s) %s.Node {\n", c.Name, typeParamsDecl, strings.TrimSpace(c.Params), rt.rt())
		}
		fmt.Fprintf(b, "\treturn %s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
		if !emitNodeFuncBody(b, c.Body, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
			return false
		}
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
	// or forwards attrs (references the `attrs` bag).
	hasChildren := usesChildren(c.Body)
	// MANUAL mode: a component whose body references the identifier `attrs` (a
	// `{ attrs... }` element spread, or `attrs.X()` in an interp/expr/clause)
	// takes over fallthrough placement itself. Explicit forwarding is required:
	// we only synthesize an `Attrs gsx.Attrs` field when the author actually
	// references `attrs`; there is no implicit single-root auto-injection path
	// (removed by the 2026-06-30 explicit-forwarding decision).
	manual := usesAttrs(c.Body)
	hasProps := len(params) > 0 || hasChildren || manual
	if hasProps {
		fmt.Fprintf(b, "type %s%s struct {\n", propsName, typeParamsDecl)
		for _, p := range params {
			fmt.Fprintf(b, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		if hasChildren {
			fmt.Fprintf(b, "\tChildren %s.Node\n", rt.rt())
		}
		if manual {
			fmt.Fprintf(b, "\tAttrs %s.Attrs\n", rt.rt())
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
		fmt.Fprintf(b, "func %s %s%s(", c.Recv, c.Name, typeParamsDecl)
	} else {
		fmt.Fprintf(b, "func %s%s(", c.Name, typeParamsDecl)
	}
	if hasProps {
		fmt.Fprintf(b, "_gsxp %s%s", propsName, typeParamsUse)
	}
	fmt.Fprintf(b, ") %s.Node {\n", rt.rt())

	// Bind each USED param to a same-named local so interpolation expressions can
	// be emitted verbatim. The props param, io.Writer closure param, and
	// gsx.Writer local use the reserved _gsx* namespace so a user param named
	// p/w/gw cannot collide with them. ctx stays ambient (user interpolation exprs
	// may reference it). For a method component the receiver var is already in
	// scope (it's the method receiver) and is NOT bound as a prop local.
	fmt.Fprintf(b, "\treturn %s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
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
		// `{ attrs... }` element spread (emitted via `_gsxgw.SpreadForwarding(ctx,
		// attrs, …)`) and any `attrs.X()` reference resolve. Nil-safe: a nil bag
		// spreads/queries to nothing. usesAttrs guarantees that lowering consumes
		// this binding.
		b.WriteString("\t\tattrs := _gsxp.Attrs\n")
	}
	if !emitNodeFuncBody(b, c.Body, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
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
func emitNodeFuncBody(b *bytes.Buffer, nodes []ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	fmt.Fprintf(b, "\t\t_gsxgw := %s.W(_gsxw)\n", rt.rt())
	emitNumScratch(b, nodes, resolved, cls)
	for _, m := range nodes {
		if !genNode(b, m, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
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
// hand-written `gsx.Func(func(...) error { … u … })` literal would.
//
// Reuses genNode (via emitNodeFuncBody) — the SAME element/markup lowering a
// component body's child elements use — so there is exactly one path from
// markup to emission code; this function only supplies the closure
// scaffolding around it.
func emitNodeValue(b *bytes.Buffer, nodes []ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	fmt.Fprintf(b, "%s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
	if !emitNodeFuncBody(b, nodes, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, "", "", cls, fm, bag, mergeExpr) {
		return false
	}
	b.WriteString("\t})")
	return true
}

// emitElementValue lowers a gsx element embedded directly in Go-expression
// position (one *ast.Element Part of a GoWithElements) via emitNodeValue.
func emitElementValue(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	return emitNodeValue(b, []ast.Markup{el}, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, cls, fm, bag, mergeExpr)
}

// emitFragmentValue lowers a gsx fragment embedded directly in Go-expression
// position (one *ast.Fragment Part of a GoWithElements) via emitNodeValue.
// Empty `<></>` → fr.Children is empty → emitNodeFuncBody writes nothing →
// the closure is the uniform no-op gsx.Func (renders nothing) — see
// emitNodeValue's doc comment.
func emitFragmentValue(b *bytes.Buffer, fr *ast.Fragment, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	return emitNodeValue(b, fr.Children, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, cls, fm, bag, mergeExpr)
}

// emitFallthroughAttrs emits the caller-wins attribute section (between `<tag`
// and the closing `>` / `/>`) for MANUAL mode (emitManualSpreadElement). splitIdx
// is the position of the author's `{ attrs... }` — scalar attrs BEFORE it are
// overridable (guarded `if !Has(name)`, caller-wins); scalar attrs AFTER it are
// FORCED (emitted UNGUARDED so the root always wins) and their names are excluded
// from the bag spread so a same-named bag entry can never emit (root wins).
//
// Cond-attrs follow the same positional rule (spec 2026-07-02 D3): a pre-spread
// `{ if … }` emits each branch leaf under a `!Has(name)` guard inside its
// branch; a post-spread one is evaluated exactly ONCE before the spread —
// branch bodies record the taken branch in a bool temp and append their leaf
// names to a dynamic drop slice the spread excludes — and its leaves render
// after the spread under the recorded bool. class/style or a spread inside a
// branch is rejected (the static merge/guard sites cannot account for them).
//
// class/style are positional-exempt: wherever they appear they MERGE caller-last
// (ClassMerged / StyleMerged), emitted once at the spread position. The author's
// `{ attrs... }` SpreadAttr itself (when present at splitIdx) is consumed here, not
// emitted via emitAttr.
func emitFallthroughAttrs(b *bytes.Buffer, attrs []ast.Attr, splitIdx int, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag, mergeExpr, bagExpr string, nonce *nonceInjection) bool {
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

	// Cond-attr branch leaves must be plain named scalars: the class/style merge
	// and the caller-wins guard/drop machinery are emitted at static sites and
	// cannot account for a conditional class contribution or a spread's unknown
	// keys. Validate every cond-attr on the element (any position) up front.
	var validateCondBranch func(as []ast.Attr, encCond string) bool
	validateCondBranch = func(as []ast.Attr, encCond string) bool {
		for _, a := range as {
			switch t := a.(type) {
			case *ast.CondAttr:
				if !validateCondBranch(t.Then, t.Cond) || !validateCondBranch(t.Else, "!("+t.Cond+")") {
					return false
				}
				continue
			case *ast.SpreadAttr:
				bag.Errorf(t.Pos(), t.End(), "attr-fallthrough",
					"spread inside { if } on an element with attribute forwarding has statically unknown keys; hoist it with a GoBlock instead (e.g. {{ a, err := gsx.AttrsCond(%s, func() (gsx.Attrs, error) { return %s, nil }, nil); if err != nil { return err } }} then { attrs.Merge(a)... })",
					encCond, strings.TrimSpace(t.Expr))
				return false
			}
			name, _ := rootAttrName(a)
			if c, ok := a.(*ast.ClassAttr); ok {
				name = c.Name
			}
			if name == "class" || name == "style" {
				hint := "; use the composable form (style={ … }) with conditional declarations instead"
				if name == "class" {
					if s, ok := a.(*ast.StaticAttr); ok {
						hint = fmt.Sprintf("; use the composable form (class={ %q: %s }) instead", s.Value, encCond)
					} else {
						hint = "; use the composable form (class={ <expr>: <cond> }) instead"
					}
				}
				bag.Errorf(a.Pos(), a.End(), "attr-fallthrough",
					"conditional %s inside { if } on an element with attribute forwarding cannot join the %s merge%s", name, name, hint)
				return false
			}
		}
		return true
	}
	for _, a := range attrs {
		if t, ok := a.(*ast.CondAttr); ok {
			if !validateCondBranch(t.Then, t.Cond) || !validateCondBranch(t.Else, "!("+t.Cond+")") {
				return false
			}
		}
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
			ownStyle, ok := embeddedTextValueExpr(b, embedStyle, resolved, table, imports, rt, interpTemp, bag)
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
		// single SpreadForwarding call writes the residual bag AND routes every
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
		// excluded is the names a forced root attr owns, which SpreadForwarding skips
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
		emitSpreadForwardingCall(b, bagExpr, tag, cls, excludedExpr)
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
			// through SpreadForwarding (excluded=nil) so its URL keys sanitize too.
			if i == splitIdx {
				continue
			}
			spreadExpr, ok := spreadAttrExpr(t, table, imports, b, interpTemp, bag)
			if !ok {
				return false
			}
			if tmp, hoisted := nonce.tempFor(t); hoisted {
				fmt.Fprintf(b, "\t\t%s = %s\n", tmp, spreadExpr)
				emitSpreadForwardingCall(b, tmp, tag, cls, "nil")
			} else {
				emitSpreadForwardingCall(b, spreadExpr, tag, cls, "nil")
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

// emitManualSpreadElement emits a non-component element that carries the author's
// `{ attrs... }` bag spread (MANUAL fallthrough), applying positional precedence:
// root attrs before the spread are caller-overridable, attrs after are forced
// (root wins). splitIdx is the bag SpreadAttr's index in el.Attrs (guaranteed
// unique by the caller).
func emitManualSpreadElement(b *bytes.Buffer, el *ast.Element, splitIdx int, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	// The bag expression: the bare `attrs` local is used directly; a DERIVED bag
	// (`attrs.Without(…)`, `attrs.Merge(…)`, a pipeline) is evaluated exactly
	// once into a hoisted temp so the caller-wins guards (.Has), the class/style
	// merges (.Class()/.Style()) and the spread (.Without(…)) all read the same
	// value and side effects don't repeat.
	spread := el.Attrs[splitIdx].(*ast.SpreadAttr)
	bagExpr := strings.TrimSpace(spread.Expr)
	if bagExpr != "attrs" || len(spread.Stages) > 0 {
		expr, ok := spreadAttrExpr(spread, table, imports, b, interpTemp, bag)
		if !ok {
			return false
		}
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
			if !genNode(b, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
	}
	emitS(b, "</"+el.Tag+">")
	return true
}

// bagSpreadIndex returns the index of THE element spread, and whether one is
// present. Every element spread is a forwarding spread (a sink): it routes
// through emitManualSpreadElement's URL-sanitizing / class-merge machinery
// regardless of what the bag expression is. An element carries at most one
// spread; second is the offending second *ast.SpreadAttr when the element
// carries more than one (nil otherwise), so the caller can position the
// precedence-ambiguous diagnostic at it and name both spread expressions in
// the merge-hint, rather than pointing at the element.
func bagSpreadIndex(attrs []ast.Attr) (idx int, found bool, second *ast.SpreadAttr) {
	idx = -1
	for i, a := range attrs {
		s, ok := a.(*ast.SpreadAttr)
		if !ok {
			continue
		}
		if found {
			return idx, found, s
		}
		idx, found = i, true
	}
	return idx, found, nil
}

// emitRootComposedClass emits a composed `class={ … }` merged with the bag's
// class: ` class="` + gw.Class(<existing parts…>, gsx.Class(attrs.Class()))
// + `"`. Mirrors emitClassAttr's part lowering, appending the bag class as a
// final unconditional part so it merges/dedupes through the merge func.
func emitRootComposedClass(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, mergeExpr, bagExpr string, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, rt, interpTemp, bag, resolved, false)
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
func emitRootEmbeddedClass(b *bytes.Buffer, a *ast.EmbeddedAttr, mergeExpr, bagExpr string, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
	val, ok := embeddedTextValueExpr(b, a, resolved, table, imports, rt, interpTemp, bag)
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
func rootStyleString(b *bytes.Buffer, styleAttr *ast.ClassAttr, staticStyle *ast.StaticAttr, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type) (string, []string, bool) {
	switch {
	case staticStyle != nil:
		return strconv.Quote(staticStyle.Value), nil, true
	case styleAttr != nil:
		parts, ok := composedParts(b, styleAttr, table, imports, rt, interpTemp, bag, resolved, true)
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
func genNode(b *bytes.Buffer, n ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
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
			return genChildComponent(b, t, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		}
		// MANUAL fallthrough: EVERY element spread `{ x... }` is a leaf sink — it
		// routes through emitManualSpreadElement's URL-sanitizing / class-merge
		// machinery regardless of the bag's provenance (declared forwarding param,
		// local `:=` bag, func result, byo field, arbitrary expr). An element
		// carries at most one spread; a second spread has no expressible
		// precedence against the guard machinery, so bagSpreadIndex hands back
		// the offending second spread and the diagnostic is positioned there
		// (not at the element) with a merge-hint naming both spreads.
		if splitIdx, found, second := bagSpreadIndex(t.Attrs); second != nil {
			first := t.Attrs[splitIdx].(*ast.SpreadAttr)
			firstExpr := strings.TrimSpace(first.Expr)
			secondExpr := strings.TrimSpace(second.Expr)
			bag.Errorf(second.Pos(), second.End(), "attr-fallthrough",
				"element with a spread { %s... } cannot carry another spread { %s... }; merge them into one spread ({ %s.Merge(%s)... } or { %s.Merge(%s)... }) so precedence is explicit",
				firstExpr, secondExpr, firstExpr, secondExpr, secondExpr, firstExpr)
			return false
		} else if found {
			return emitManualSpreadElement(b, t, splitIdx, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
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
				if !genNode(b, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
					return false
				}
			}
		}
		emitS(b, "</"+t.Tag+">")
	case *ast.Interp:
		ec := interpEmitCtx{currentPkg, structFields, nodeProps, attrsProps, byo, importAliases, boundNames, typeArgAliases, cls, fm, mergeExpr}
		return genInterp(b, t, resolved, table, imports, rt, interpTemp, fset, bag, ec)
	case *ast.EmbeddedInterp:
		ec := interpEmitCtx{currentPkg, structFields, nodeProps, attrsProps, byo, importAliases, boundNames, typeArgAliases, cls, fm, mergeExpr}
		return emitEmbeddedInterp(b, t, resolved, table, imports, rt, interpTemp, fset, bag, ec)
	case *ast.Fragment:
		for _, c := range t.Children {
			if !genNode(b, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
	case *ast.ForMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "for %s {\n", t.Clause)
		for _, c := range t.Body {
			if !genNode(b, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
		b.WriteString("}\n")
	case *ast.IfMarkup:
		emitLine(b, fset, t.Pos())
		fmt.Fprintf(b, "if %s {\n", t.Cond)
		for _, c := range t.Then {
			if !genNode(b, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
				return false
			}
		}
		b.WriteString("}")
		if t.Else != nil {
			b.WriteString(" else {\n")
			for _, c := range t.Else {
				if !genNode(b, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
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
				if !genNode(b, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
					return false
				}
			}
		}
		b.WriteString("}\n")
	case *ast.GoBlock:
		emitLine(b, fset, t.Pos())
		b.WriteString(t.Code)
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
	structFields   map[string]map[string]bool
	nodeProps      map[string]map[string]bool
	attrsProps     map[string]map[string]bool
	byo            *byoData
	importAliases  map[string]string
	boundNames     map[string]string
	typeArgAliases map[string]string
	cls            *attrclass.Classifier
	fm             FieldMatcher
	mergeExpr      string
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
func genInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, fset *token.FileSet, bag *diag.Bag, ec interpEmitCtx) bool {
	emitLine(b, fset, n.Pos())
	var expr string
	if n.Embedded != nil {
		var eb bytes.Buffer
		for _, part := range n.Embedded {
			switch p := part.(type) {
			case ast.GoText:
				eb.WriteString(p.Src)
			case *ast.Element:
				if !emitElementValue(&eb, p, ec.currentPkg, resolved, table, ec.structFields, ec.nodeProps, ec.attrsProps, ec.byo, imports, rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, interpTemp, fset, ec.cls, ec.fm, bag, ec.mergeExpr) {
					return false
				}
			case *ast.Fragment:
				if !emitFragmentValue(&eb, p, ec.currentPkg, resolved, table, ec.structFields, ec.nodeProps, ec.attrsProps, ec.byo, imports, rt, ec.importAliases, ec.boundNames, ec.typeArgAliases, interpTemp, fset, ec.cls, ec.fm, bag, ec.mergeExpr) {
					return false
				}
			case *ast.EmbeddedInterp:
				// A prefixed backtick literal embedded in this interp's seed → a Go
				// string value. embeddedValueExpr assembles it to one string concat,
				// spliced into the seed exactly like an element's gsx.Func value; any
				// hole tuple-unwrap hoisting lands in b before the consuming stmt.
				if len(p.Stages) > 0 {
					bag.Errorf(n.Pos(), n.End(), "unsupported-node", "whole-literal pipelines on a Go-expression backtick literal are not supported")
					return false
				}
				val, vok := embeddedValueExpr(b, p.Segments, resolved, table, imports, rt, interpTemp, bag, "unsupported-node", "backtick literal value")
				if !vok {
					return false
				}
				eb.WriteString(val)
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
		// v, err := expr; if err != nil { return err }; then render v by its type.
		tmp := hoistTuple(b, expr, interpTemp)
		return emitRender(b, tmp, elemT, rt, n, bag)
	}
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
// matches exactly (emit ≡ probe). The result is then rendered for Text context
// via emitRender, unwrapping a trailing (T, error) tuple exactly like genInterp.
func emitEmbeddedInterp(b *bytes.Buffer, n *ast.EmbeddedInterp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, fset *token.FileSet, bag *diag.Bag, ec interpEmitCtx) bool {
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
	concat, ok := embeddedValueExpr(b, n.Segments, resolved, table, imports, rt, interpTemp, bag, "unsupported-node", "body interpolation literal")
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
		tmp := hoistTuple(b, lowered, interpTemp)
		return emitRender(b, tmp, elemT, rt, n, bag)
	}
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
	return func(call string) string { return hoistTuple(b, call, interpTemp) }
}

// thunkPipeWrap is emitPipeWrap for statement positions INSIDE an (Attrs, error)
// cond-attr thunk: same hoist, two-value error return.
func thunkPipeWrap(b *bytes.Buffer, interpTemp *int) func(string) string {
	return func(call string) string {
		return hoistTupleReturning(b, call, interpTemp, "return nil, _gsxerr")
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
func hoistValueCF(b *bytes.Buffer, cf *ast.ValueCF, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, style bool, bag *diag.Bag, resolved map[ast.Node]types.Type) (string, bool) {
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
	case catInt:
		// Format into the per-render scratch buffer and write the digit bytes
		// directly — no string allocation, no escaping (digits are always safe).
		fmt.Fprintf(b, "\t\t_gsxgw.IntInto(_gsxnum[:], int64(%s))\n", expr)
	case catUint:
		fmt.Fprintf(b, "\t\t_gsxgw.UintInto(_gsxnum[:], uint64(%s))\n", expr)
	case catFloat:
		fmt.Fprintf(b, "\t\t_gsxgw.FloatInto(_gsxnum[:], float64(%s))\n", expr)
	case catBool:
		fmt.Fprintf(b, "\t\t_gsxgw.Text(%s.FormatBool(bool(%s)))\n", rt.sc(), expr)
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
func emitNumScratch(b *bytes.Buffer, nodes []ast.Markup, resolved map[ast.Node]types.Type, cls *attrclass.Classifier) {
	if scopeUsesNumeric(nodes, resolved, cls) {
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
func scopeUsesNumeric(nodes []ast.Markup, resolved map[ast.Node]types.Type, cls *attrclass.Classifier) bool {
	for _, n := range nodes {
		switch t := n.(type) {
		case *ast.Interp:
			if interpIsNumeric(t, resolved) {
				return true
			}
		case *ast.EmbeddedInterp:
			// Stages>0: the node renders as ONE piped value (emitEmbeddedInterp's
			// with-stages path) — check resolved[t] itself. Stages==0: it renders
			// per-segment (each *ast.Interp hole via genInterp), so recurse into
			// Segments the same way as any other scope.
			if len(t.Stages) > 0 {
				if resolvedTypeIsNumeric(t, resolved) {
					return true
				}
			} else if scopeUsesNumeric(t.Segments, resolved, cls) {
				return true
			}
		case *ast.Element:
			if isComponentTag(t.Tag) {
				// Child component: numeric attrs are props (struct fields), and its
				// slots render in their own scope — neither uses this scope's _gsxnum.
				continue
			}
			if attrsUseNumericScratch(t.Attrs, resolved, cls) {
				return true
			}
			if strings.EqualFold(t.Tag, "script") {
				continue
			}
			if scopeUsesNumeric(t.Children, resolved, cls) {
				return true
			}
		case *ast.Fragment:
			if scopeUsesNumeric(t.Children, resolved, cls) {
				return true
			}
		case *ast.ForMarkup:
			if scopeUsesNumeric(t.Body, resolved, cls) {
				return true
			}
		case *ast.IfMarkup:
			if scopeUsesNumeric(t.Then, resolved, cls) || scopeUsesNumeric(t.Else, resolved, cls) {
				return true
			}
		case *ast.SwitchMarkup:
			for _, cc := range t.Cases {
				if scopeUsesNumeric(cc.Body, resolved, cls) {
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
func attrsUseNumericScratch(attrs []ast.Attr, resolved map[ast.Node]types.Type, cls *attrclass.Classifier) bool {
	for _, a := range attrs {
		switch at := a.(type) {
		case *ast.ExprAttr:
			if cls.Context(at.Name) != attrclass.CtxURL && resolvedTypeIsNumeric(at, resolved) {
				return true
			}
		case *ast.EmbeddedAttr:
			if at.Lang != ast.EmbeddedText || cls.Context(at.Name) == attrclass.CtxURL {
				continue
			}
			if len(at.Stages) > 0 {
				// Whole-literal pipe: renders one piped value via emitAttrValue.
				if resolvedTypeIsNumeric(at, resolved) {
					return true
				}
			} else {
				for _, seg := range at.Segments {
					if in, ok := seg.(*ast.Interp); ok && resolvedTypeIsNumeric(in, resolved) {
						return true
					}
				}
			}
		case *ast.CondAttr:
			if attrsUseNumericScratch(at.Then, resolved, cls) || attrsUseNumericScratch(at.Else, resolved, cls) {
				return true
			}
		}
	}
	return false
}

// interpIsNumeric reports whether interp n renders as an int/uint/float (the same
// classification emitRender uses to pick gw.IntInto/UintInto/FloatInto).
func interpIsNumeric(n *ast.Interp, resolved map[ast.Node]types.Type) bool {
	return resolvedTypeIsNumeric(n, resolved)
}

// resolvedTypeIsNumeric reports whether node n's resolved type renders as an
// int/uint/float, unwrapping a (T, error) tuple exactly as genInterp/emitExprAttr
// do. Shared by the text (interpIsNumeric) and attribute (attrsUseNumericScratch)
// scans in scopeUsesNumeric, plus the *ast.EmbeddedInterp whole-literal-pipe node,
// so all agree with the emit paths that write through _gsxnum.
func resolvedTypeIsNumeric(n ast.Node, resolved map[ast.Node]types.Type) bool {
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
		return emitRenderCSS(b, tmp, elemT, n, bag)
	}
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
// the bag with code "unresolved-pipeline" and ok=false. b and interpTemp hoist a
// mid-stage (R, error) filter via emitPipeWrap (all callers are emit-only element
// contexts; no probe variant of this path exists).
func spreadAttrExpr(a *ast.SpreadAttr, table filterTable, imports map[string]bool, b *bytes.Buffer, interpTemp *int, bag *diag.Bag) (string, bool) {
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
func emitAttr(b *bytes.Buffer, attrs []ast.Attr, a ast.Attr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag, mergeExpr string, nonce *nonceInjection) bool {
	switch t := a.(type) {
	case *ast.StaticAttr:
		fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+t.Name+`="`+htmlAttrEscape(t.Value)+`"`))
		nonce.markExplicit(b, t.Name)
	case *ast.BoolAttr:
		fmt.Fprintf(b, "\t\t_gsxgw.BoolAttr(%s, true)\n", strconv.Quote(t.Name))
		nonce.markExplicit(b, t.Name)
	case *ast.ExprAttr:
		if !emitExprAttr(b, attrs, t, resolved, table, imports, interpTemp, cls, tag, bag) {
			return false
		}
		nonce.markExplicit(b, t.Name)
		return true
	case *ast.EmbeddedAttr:
		switch t.Lang {
		case ast.EmbeddedJS:
			if !emitEmbeddedJSAttr(b, t, resolved, table, imports, interpTemp, bag) {
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
		// still a leaf URL sink: it routes through SpreadForwarding (excluded=nil, so
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
			emitSpreadForwardingCall(b, tmp, tag, cls, "nil")
		} else {
			emitSpreadForwardingCall(b, spreadExpr, tag, cls, "nil")
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

// urlWriterMethod returns the generated Writer method for a URL-context
// attribute: "URLImage" for an image resource sink (data:image/* allowed),
// "URL" otherwise. Callers must have established CtxURL for name.
func urlWriterMethod(tag, name string) string {
	if attrclass.URLSink(tag, name) == attrclass.SinkImage {
		return "URLImage"
	}
	return "URL"
}

// goStringSliceLit renders names as a Go `[]string{…}` literal, or "nil" when
// empty — the argument form SpreadForwarding's name-set params expect.
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

// emitSpreadForwardingCall emits `_gsxgw.SpreadForwarding(ctx, expr, …)` for bag
// expression expr on element tag: the classifier's URL-exact names split into
// the nav vs image sinks via urlWriterMethod, prefix URL rules pass through, and
// excludedExpr is the names a forced site owns ("nil" when nothing is forced —
// the standalone / nested-cond-attr spread case). Every element spread routes
// through here so URL-classified keys sanitize at the leaf regardless of the
// bag's provenance or nesting.
func emitSpreadForwardingCall(b *bytes.Buffer, expr, tag string, cls *attrclass.Classifier, excludedExpr string) {
	var navNames, imageNames []string
	for _, name := range cls.URLExactNames() {
		if urlWriterMethod(tag, name) == "URLImage" {
			imageNames = append(imageNames, name)
		} else {
			navNames = append(navNames, name)
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.SpreadForwarding(ctx, %s, %s, %s, %s, %s)\n",
		expr, goStringSliceLit(navNames), goStringSliceLit(imageNames), goStringSliceLit(cls.URLPrefixes()), excludedExpr)
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
func emitEmbeddedTextAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag) bool {
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
		concat, ok := embeddedTextValueExpr(b, a, resolved, table, imports, rt, interpTemp, bag)
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
		if isURL {
			// Sanitize AFTER the pipe: URL() runs on the pipeline's OUTPUT, so a
			// filter returning a dangerous scheme is still blocked, never trusted.
			strExpr, ok := stringifyExpr(lowered, t, rt, a, bag, fmt.Sprintf("attribute %q pipeline result", a.Name))
			if !ok {
				return false
			}
			fmt.Fprintf(b, "\t\t_gsxgw.%s(%s)\n", urlWriterMethod(tag, a.Name), strExpr)
		} else if !emitAttrValue(b, lowered, t, a, bag) {
			return false
		}
	case isURL:
		concat, ok := embeddedTextValueExpr(b, a, resolved, table, imports, rt, interpTemp, bag)
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
				if !emitTextAttrInterp(b, s, resolved, table, imports, interpTemp, bag) {
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
func embeddedTextValueExpr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) (string, bool) {
	return embeddedValueExpr(b, a.Segments, resolved, table, imports, rt, interpTemp, bag, "unsupported-attr", fmt.Sprintf("attribute %q value", a.Name))
}

// embeddedValueExpr is embeddedTextValueExpr generalized over a raw segment
// list, so a body/child *ast.EmbeddedInterp's Segments can be assembled through
// the SAME logic (static text -> raw quoted literal, each hole -> holeStringExpr)
// without an *ast.EmbeddedAttr wrapper. errCode/errDesc position and word the
// "unsupported segment" diagnostic for the caller's context (attribute vs. body
// literal).
func embeddedValueExpr(b *bytes.Buffer, segs []ast.Markup, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, errCode, errDesc string) (string, bool) {
	parts := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch s := seg.(type) {
		case *ast.Text:
			if s.Value == "" {
				continue
			}
			parts = append(parts, strconv.Quote(s.Value))
		case *ast.Interp:
			p, ok := holeStringExpr(b, s, resolved, table, imports, rt, interpTemp, bag)
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
// Stringer -> (x).String(). Any other type (bool, catAnyMixed, unresolved)
// cannot safely carry a URL fragment and is rejected with a diagnostic.
func holeStringExpr(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) (string, bool) {
	expr := strings.TrimSpace(n.Expr)
	if len(n.Stages) > 0 {
		lowered, usedPkgs, err := lowerPipe(n.Expr, n.Stages, table, emitPipeWrap(b, interpTemp))
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
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of URL interpolation %q", n.Expr)
		return "", false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "URL interpolation %q returns %s; only (T, error) is supported", expr, t)
			return "", false
		}
		expr = hoistTuple(b, expr, interpTemp)
		t = elemT
	}
	switch classify(t) {
	case catString, catBytes:
		return "string(" + expr + ")", true
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
func emitEmbeddedCSSAttr(b *bytes.Buffer, a *ast.EmbeddedAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
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
func emitJSAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
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

// emitTextAttrInterp renders one @{ } hole in a plain non-URL attribute literal
// (the else branch of emitEmbeddedTextAttr). Mirrors emitJSAttrInterp's
// pipeline + (T,error) auto-unwrap, then routes through the type-aware
// emitAttrValue (string→AttrValue, numbers→strconv, Stringer→.String()).
func emitTextAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, bag *diag.Bag) bool {
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
		bag.Errorf(n.Pos(), n.End(), "unresolved-interp", "could not resolve type of attribute interpolation %q", n.Expr)
		return false
	}
	if _, isTuple := t.(*types.Tuple); isTuple {
		elemT, ok := tupleUnwrapType(t)
		if !ok {
			bag.Errorf(n.Pos(), n.End(), "invalid-tuple", "attribute interpolation %q returns %s; only (T, error) is supported", expr, t)
			return false
		}
		tmp := hoistTuple(b, expr, interpTemp)
		return emitAttrValue(b, tmp, elemT, n, bag)
	}
	return emitAttrValue(b, expr, t, n, bag)
}

// emitCSSAttrInterp renders one @{ } hole in an explicit CSS attribute literal.
// It mirrors emitCSSInterp's pipeline-stage handling and (T, error) tuple
// auto-unwrap, but routes through gsx.StyleValue followed by AttrValue because
// the result is inside a quoted HTML attribute.
func emitCSSAttrInterp(b *bytes.Buffer, n *ast.Interp, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) bool {
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
		return emitRenderCSSAttr(b, tmp, elemT, rt, n, bag)
	}
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
// never piped, so callers lower only the part's Expr/Stages, not its Cond. b and
// interpTemp hoist a mid-stage (R, error) filter via emitPipeWrap (the element
// class/style path is emit-only; the probe path harvests via probeExpr instead).
func classPartExpr(p ast.ClassPart, a *ast.ClassAttr, table filterTable, imports map[string]bool, b *bytes.Buffer, interpTemp *int, bag *diag.Bag) (string, bool) {
	lowered, usedPkgs, err := lowerClassPartSeed(p, table, emitPipeWrap(b, interpTemp))
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
func lowerClassPartSeed(p ast.ClassPart, table filterTable, wrap func(string) string) (string, map[string]string, error) {
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
func emitClassAttr(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, mergeExpr string, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, rt, interpTemp, bag, resolved, false)
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
func emitStyleAttr(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type) bool {
	parts, ok := composedParts(b, a, table, imports, rt, interpTemp, bag, resolved, true)
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

func composedParts(b *bytes.Buffer, a *ast.ClassAttr, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag, resolved map[ast.Node]types.Type, style bool) ([]string, bool) {
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
		expr, ok := classPartExpr(*p, a, table, imports, b, interpTemp, bag)
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

func cssLiteralStylePartExpr(b *bytes.Buffer, segments []ast.Markup, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, rt rtImports, interpTemp *int, bag *diag.Bag) (string, bool) {
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
			if t, ok := resolved[s].(*types.Tuple); ok {
				if _, ok := tupleUnwrapType(t); !ok {
					bag.Errorf(s.Pos(), s.End(), "invalid-tuple", "style css literal interpolation %q returns %s; only (T, error) is supported", expr, t)
					return "", false
				}
				expr = hoistTuple(b, expr, interpTemp)
			}
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
func emitExprAttr(b *bytes.Buffer, attrs []ast.Attr, a *ast.ExprAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, tag string, bag *diag.Bag) bool {
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
		if !emitAttrValue(b, expr, t, a, bag) {
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
// through SpreadForwarding's ordinary per-key routing instead (URL-sanitized
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

// urlStringExpr renders a URL-context value as a string expression for gw.URL.
func urlStringExpr(expr string, t types.Type) string {
	if classify(t) == catString {
		return "string(" + expr + ")"
	}
	return expr // non-string URL values are unusual; let the Go compiler check gw.URL's arg
}

// emitAttrValue writes a non-URL attribute value via gw.AttrValue, §5 type-aware.
// n is the AST node for positioning any error diagnostic.
func emitAttrValue(b *bytes.Buffer, expr string, t types.Type, n ast.Node, bag *diag.Bag) bool {
	switch classify(t) {
	case catString:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(string(%s))\n", expr)
	case catBytes:
		fmt.Fprintf(b, "\t\t_gsxgw.AttrValue(string(%s))\n", expr)
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

// isComponentTag delegates to ast.IsComponentTag — see that function for the
// rule. Kept as a local alias for the many existing call sites.
func isComponentTag(tag string) bool { return ast.IsComponentTag(tag) }

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
		case *ast.EmbeddedInterp:
			// A body backtick literal's @{ } holes render in this scope (either
			// per-segment or as the pipeline seed), so a bare `children` hole
			// (e.g. {`@{children}`}) counts the same as a top-level {children}.
			if usesChildren(t.Segments) {
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
// token-based: it matches the bare ident `attrs` (e.g. `{ attrs... }` SpreadAttr,
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
		case *ast.EmbeddedInterp:
			// A body backtick literal's @{ } holes render in this scope
			// (per-segment, or as the whole-literal pipeline's seed), so an
			// `attrs` reference inside a hole — or inside a node-level `|> f(attrs)`
			// filter arg — needs the local bound, same as usesAttrs' *ast.Interp case.
			if usesAttrs(t.Segments) {
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
// `{ attrs... }` SpreadAttr's Expr, a composable-class part Expr/Cond, an ExprAttr
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
func genChildComponent(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	// Task 4: a cross-package generic tag whose requalification failed at
	// analyze time has NO probe anywhere in the skeleton (nothing for
	// harvest to have populated resolved[el] from) — module_importer.go's
	// analyze marks it with the types.Invalid sentinel instead, right after
	// harvest runs (see inferRegistry.failed's doc). Emitting a call for it
	// is impossible (uninstantiated, invalid Go — no resolved type arg to
	// use), but emitting NOTHING is just as broken: an enclosing local (a
	// used-param binding like `name := _gsxp.Name`, a loop var, a CF-hoisted
	// class temp) whose ONLY use sits in this tag's value expressions would
	// trip "declared and not used" in the OUTPUT — generate would exit 0
	// having written non-compiling Go. So emit a never-executed SINK that
	// consumes the tag's value expressions instead (genSkippedTagSink). The
	// positioned "inference-unavailable" diagnostic was already recorded at
	// analyze time; the sink returns true (not false — false would propagate
	// up through genNode and abort the WHOLE enclosing component, discarding
	// every sibling node too, which is not what "generation of other tags
	// unaffected" means): the element renders nothing.
	if basic, ok := resolved[el].(*types.Basic); ok && basic.Kind() == types.Invalid {
		return genSkippedTagSink(b, el, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
	}
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
		callTypeArgs, argsOK := childTypeArgUse(el, currentPkg, resolved, table, imports, importAliases, boundNames, typeArgAliases, bag)
		if !argsOK {
			return false
		}
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s%s())\n", el.Tag, callTypeArgs)
		return true
	}
	// Attrs-only component value: a same-package tag with no <Name>Props type,
	// gated onto the _gsxcompsig probe (isAttrsOnlyCandidate). When harvest
	// resolved its type to an attrs-only signature (attrsOnlySig), emit a bag
	// call F(bag) / F(bag...) instead of the (nonexistent) FProps convention —
	// wrapped in a []gsx.Attr conversion (needsConvert) when the param is a
	// user-defined named slice type. emit ≡ probe: emitProbes gates the
	// identical predicate.
	if isAttrsOnlyCandidate(el, structFields, byo, recvVar, recvTypeName) {
		// The name-based gate fired on a dotted tag whose qualifier is a known
		// gsx import alias, but harvest found that alias SHADOWED by a same-named
		// local/param in scope — the probe's selector resolved to that value's
		// field, not the package. Bag-calling the field would silently miscompile
		// (or nil-panic) a region that is a hard build error on main; reject it.
		if resolved[el] == shadowedQualifierType {
			qual := el.Tag
			if dot := strings.IndexByte(el.Tag, '.'); dot >= 0 {
				qual = el.Tag[:dot]
			}
			bag.Errorf(el.Pos(), el.End(), "attrsonly-shadowed-qualifier",
				"<%s> is not tag-callable: %s is shadowed by a local declaration; component values must be package-level",
				el.Tag, qual)
			return false
		}
		if t, probed := resolved[el]; probed {
			if variadic, needsConvert, match := attrsOnlySig(t); match {
				if len(el.Children) > 0 {
					bag.Errorf(el.Pos(), el.End(), "attrsonly-children",
						"component values do not support children — declare a Children slot on a named-struct component instead")
					return false
				}
				expr, usedPkgs, err := attrsOnlyBagExpr(el, rt.rt(), classMergeExpr(mergeExpr, rt), table, byo, fm, false, resolved, b, interpTemp)
				if err != nil {
					if ae, ok := errors.AsType[*attrError](err); ok {
						bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
					} else {
						bag.Errorf(el.Pos(), el.End(), "attrsonly-bag", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
					}
					return false
				}
				for _, path := range usedPkgs {
					imports[path] = true
				}
				// needsConvert (set only for a non-variadic, defined param
				// type other than the named gsx.Attrs — see attrsOnlySig's
				// doc comment) wraps the bag expression in a conversion to
				// the unnamed []gsx.Attr, which assigns to ANY named type
				// sharing that underlying (one side unnamed, same rule Go's
				// assignability check applies) — this is what makes accepting
				// an arbitrary user-defined named slice type sound. nil
				// already assigns to any slice type with no conversion.
				if needsConvert && expr != "" {
					expr = fmt.Sprintf("[]%s.Attr(%s)", rt.rt(), expr)
				}
				switch {
				case expr == "" && variadic:
					fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s())\n", el.Tag)
				case expr == "":
					fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(nil))\n", el.Tag)
				case variadic:
					fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(%s...))\n", el.Tag, expr)
				default:
					fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(%s))\n", el.Tag, expr)
				}
				return true
			}
			// Package-name qualifier (same-package types unqualified): the
			// full-path default prints func(attrs []github.com/gsxhq/gsx.Attr),
			// which buries the shape the message is contrasting against.
			// Bare package names suffice for diagnostic text: unlike emitted code
			// (see qf), a same-name package collision here only cosmetically
			// ambiguates an error message, never miscompiles.
			qual := func(p *types.Package) string {
				if p == currentPkg {
					return ""
				}
				return p.Name()
			}
			bag.Errorf(el.Pos(), el.End(), "attrsonly-bad-type",
				"<%s> is not tag-callable: its type is %s, not a component-value signature (one parameter with underlying type []gsx.Attr, result gsx.Node), and no %sProps struct was found",
				el.Tag, types.TypeString(t, qual), el.Tag)
			return false
		}
		// not harvested: the skeleton already reported the underlying error
		// (e.g. undefined: <Tag>); fall through to the convention emission,
		// which is never reached in a successful generation.
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
		callTypeArgs, argsOK := childTypeArgUse(el, currentPkg, resolved, table, imports, importAliases, boundNames, typeArgAliases, bag)
		if !argsOK {
			return false
		}
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s%s())\n", callTarget, callTypeArgs)
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
	fieldEntries, splatExpr, usedPkgs, err := childPropsLiteral(el, propsType, rt.rt(), classMergeExpr(mergeExpr, rt), table, structFields, nodeProps[propsType], byo, fm, func(nodes []ast.Markup) (string, error) {
		s, ok := emitSlotClosure(nodes, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
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
		callTypeArgs, argsOK := childTypeArgUse(el, currentPkg, resolved, table, imports, importAliases, boundNames, typeArgAliases, bag)
		if !argsOK {
			return false
		}
		fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s%s(%s))\n", callTarget, callTypeArgs, splatExpr)
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
					fieldEntries[i].str = fmt.Sprintf("%s: %s.Val(%s)", fe.fieldName, rt.rt(), tmp)
				} else {
					fieldEntries[i].str = fmt.Sprintf("%s: %s", fe.fieldName, tmp)
				}
			case fe.oa != nil:
				// Hoist tuple/call pairs and rebuild the gsx.Attrs{…}
				// literal; non-call pairs stay inline (see the ExprAttr note).
				var sb strings.Builder
				fmt.Fprintf(&sb, "%s.Attrs{", rt.rt())
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
				if fe.oaMergePrefix != "" {
					fieldEntries[i].str = fmt.Sprintf("Attrs: %s.ConcatAttrs(%s, %s)", rt.rt(), fe.oaMergePrefix, sb.String())
				} else {
					fieldEntries[i].str = fmt.Sprintf("%s: %s", fe.fieldName, sb.String())
				}
			}
		}
	}
	strs := make([]string, len(fieldEntries))
	for i, fe := range fieldEntries {
		strs[i] = fe.str
	}
	callTypeArgs, argsOK := childTypeArgUse(el, currentPkg, resolved, table, imports, importAliases, boundNames, typeArgAliases, bag)
	if !argsOK {
		return false
	}
	fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s%s(%s%s{%s}))\n", callTarget, callTypeArgs, propsType, callTypeArgs, strings.Join(strs, ", "))
	return true
}

// childTypeArgUse renders the `[T1, T2]` type-argument suffix for a generic
// child-component call: an explicit `<Comp[int]>` echoes el.TypeArgs
// verbatim, otherwise the caller-side inference engine already stored the
// instantiated *types.Named in resolved[el] and the type args come from
// there.
//
// An INFERRED type argument can name a type that is unspeakable at the call
// site: an unexported type declared in a package other than currentPkg
// (e.g. a constructor in another package returns an unexported type, and a
// generic component's type parameter gets inferred as that type). Printing
// `otherpkg.secret` verbatim is not valid Go outside otherpkg — the emitted
// .x.go would fail to compile even though generate exited 0, violating the
// hard invariant that generate never emits non-compiling output. So every
// inferred type argument is walked (unspeakableTypeArg) BEFORE printing; if
// any reachable named type is cross-package-unexported, this records a
// positioned "unrenderable-type-arg" diagnostic and returns ("", false) — the
// established emission-failure contract: the caller propagates false, this
// one component fails, siblings continue.
//
// A SPEAKABLE inferred type argument can still collide: its package's own
// NAME may already be bound to a DIFFERENT import path in this file (see
// generateFile's boundNames doc). qf (below) prints pkg.Name() verbatim only
// when that name is free; on a collision it mints a fresh "_gsxti<N>" alias
// (recorded in typeArgAliases, path→alias, and consulted first on any later
// reference to the SAME path) instead of plain-importing the path — the same
// hard-invariant concern as the unexported-type case above, just surfacing
// as a `go build` "redeclared" error instead of an unspeakable identifier.
//
// A SPEAKABLE inferred type argument can ALSO name a type declared in a
// FILTER package (table, harvested from gen.Options.FilterPkgs) that this
// file never plain-imports itself — the file only reaches that package
// through the reserved-alias pipe-filter mechanism (lowerPipe), which sets
// imports[path] = true WITHOUT ever registering a plain import line for it.
// The old qf treated `imports[path]` as proof a plain import line would be
// emitted (see the branch below); for a filter-only path that's false —
// writeImports emits ONLY the reserved-alias line (`_gsxf0 "path"`) for a
// path present in `imports` that's also a filter package, unless the user
// ALSO plain-imported it themselves (userPlainImports). Printing pkg.Name()
// for such a path names an unbound identifier: `go build` failure with gsx
// exiting 0 — the final whole-branch review's Critical-2 finding. So qf
// checks table for the path FIRST (before the plain-imported-path branch)
// and, when it names a filter package, qualifies with that package's
// reserved alias instead — the SAME alias the file's own filter calls use,
// so the reference is always bound regardless of whether this exact path
// was already in `imports` via a filter call, a plain user import, or (this
// case) only the inferred type argument itself; imports[path] is set here
// too so writeImports actually emits the reserved-alias import line for it.
func childTypeArgUse(el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, bag *diag.Bag) (string, bool) {
	if el.TypeArgs != "" {
		return typeArgUse(el.TypeArgs), true
	}
	named, ok := resolved[el].(*types.Named)
	if !ok || named.TypeArgs() == nil || named.TypeArgs().Len() == 0 {
		return "", true
	}
	visited := make(map[types.Type]bool)
	for typ := range named.TypeArgs().Types() {
		if offPkg, offName, found := unspeakableTypeArg(typ, currentPkg, visited); found {
			// "instantiate <tag> explicitly" is dropped here (Task 8's adjudicated
			// wording, per Task 6's report): unlike an ordinary inference
			// failure, this diagnostic fires when inference SUCCEEDED but the
			// winning type argument is unspeakable from the call site — no
			// spelling of <tag>[SomeExportedType] at THIS site can name
			// offPkg's unexported type, so that advice is a dead end. The only
			// real fixes live in the OTHER package: export the type, or change
			// the constructor's return type to something exported.
			bag.Errorf(el.Pos(), el.End(), "unrenderable-type-arg",
				"cannot instantiate %s: inferred type argument %s.%s is unexported outside package %q; pass an exported type, or export the type or change the constructor's return type",
				el.Tag, offPkg.Name(), offName, offPkg.Path())
			return "", false
		}
	}
	args := make([]string, 0, named.TypeArgs().Len())
	qf := func(pkg *types.Package) string {
		if pkg == nil {
			return ""
		}
		path := pkg.Path()
		if currentPkg != nil && path == currentPkg.Path() {
			return ""
		}
		if alias := importAliases[path]; alias != "" {
			return alias
		}
		// A path this call (or an earlier tag in this same file) already
		// aliased away for colliding NAME reuses the SAME alias — never mint
		// a second one for the same path.
		if alias := typeArgAliases[path]; alias != "" {
			return alias
		}
		// A FILTER package (harvested into table) is qualified with its OWN
		// reserved alias, never a plain pkg.Name() — see the doc above. This
		// must run BEFORE the imports[path] branch below: a filter path can
		// already be present in `imports` (set by an earlier lowerPipe call
		// in this file) without ever being plain-imported, so that branch's
		// "already plain-imported" assumption does not hold for it.
		if alias, ok := table.aliasForPath(path); ok {
			if imports != nil {
				imports[path] = true
			}
			return alias
		}
		// A path already plain-imported (the user's own GoChunk import, or an
		// earlier NON-colliding type-arg addition for this SAME path) is
		// already correctly bound under its own name — reuse it verbatim.
		if imports != nil && imports[path] {
			return pkg.Name()
		}
		// A genuinely new path: printing pkg.Name() verbatim is only safe if
		// that name isn't ALREADY bound to some OTHER path in this file (see
		// generateFile's boundNames doc) — two import lines binding the same
		// name is `go build`'s "name redeclared in this block", a hard-
		// invariant violation (generate must never emit non-compiling
		// output). On a genuine collision, mint a fresh reserved "_gsxti<N>"
		// alias (mirroring the skeleton-side aliasAllocator's naming,
		// infer.go) instead of plain-importing the path.
		name := pkg.Name()
		if boundNames != nil && typeArgAliases != nil && typeArgNameCollides(boundNames, name, path) {
			alias := fmt.Sprintf("_gsxti%d", len(typeArgAliases)+1)
			typeArgAliases[path] = alias
			boundNames[alias] = path
			return alias
		}
		if imports != nil {
			imports[path] = true
		}
		if boundNames != nil {
			boundNames[name] = path
		}
		return name
	}
	for typ := range named.TypeArgs().Types() {
		args = append(args, types.TypeString(typ, qf))
	}
	return "[" + strings.Join(args, ", ") + "]", true
}

// typeArgNameCollides reports whether printing an inferred type argument's
// package under name would collide with a binding this file already holds
// for a DIFFERENT import path (see generateFile's boundNames doc). PRESENCE
// in boundNames is what makes a name taken — the comma-ok idiom, never a
// zero-value check — because a reserved name can be seeded with an EMPTY
// path sentinel (`boundNames[classMergerAlias] = ""`: the class-merger alias
// is reserved whether or not a merger is configured) that must collide with
// EVERY candidate path; a zero-value check would treat that deliberate
// sentinel as unset, plain-import a package literally named `_gsxcm` (a
// legal Go identifier), and bind the name twice — `go build`'s "redeclared
// in this block" with gsx exiting 0.
func typeArgNameCollides(boundNames map[string]string, name, path string) bool {
	boundPath, taken := boundNames[name]
	return taken && boundPath != path
}

// unspeakableTypeArg walks t looking for a named type (or materialized
// alias) whose identifier is unexported AND declared in a package other than
// currentPkg — such a type cannot be spelled at all outside its declaring
// package (`pkg.lowerName` is not valid Go syntax for referring to an
// unexported identifier), so printing it as an inferred type argument would
// emit non-compiling Go. Recurses through the composite type kinds a printed
// type argument can be built from (pointer/slice/array/map/chan element
// types, struct fields, signature params/results, interface embeddeds AND
// explicit method signatures, and a nested generic instantiation's own type
// arguments) so an offender buried in e.g. `[]models.secret`,
// `map[string]models.secret`, or `interface{ Get() models.secret }` is still
// caught — not just a bare `models.secret`. The walk mirrors what
// types.TypeString actually PRINTS: a Named/Alias prints only its (possibly
// qualified) name plus type args — never its underlying/RHS — so those are
// not recursed into. visited guards against unbounded recursion through
// self-referential named types (e.g. `type List struct { Next *List }`).
func unspeakableTypeArg(t types.Type, currentPkg *types.Package, visited map[types.Type]bool) (offendingPkg *types.Package, offendingName string, found bool) {
	if t == nil || visited[t] {
		return nil, "", false
	}
	visited[t] = true
	switch tt := t.(type) {
	case *types.Named:
		if obj := tt.Obj(); obj != nil && obj.Pkg() != nil &&
			(currentPkg == nil || obj.Pkg().Path() != currentPkg.Path()) &&
			!token.IsExported(obj.Name()) {
			return obj.Pkg(), obj.Name(), true
		}
		// A nested generic instantiation (e.g. Box[secret]) can bury the
		// offender in ITS OWN type arguments.
		if targs := tt.TypeArgs(); targs != nil {
			for typ := range targs.Types() {
				if pkg, name, ok := unspeakableTypeArg(typ, currentPkg, visited); ok {
					return pkg, name, ok
				}
			}
		}
		return nil, "", false
	case *types.Alias:
		// gotypesalias=1 is unconditional on this repo's Go version, so alias
		// types are materialized *types.Alias nodes — and types.TypeString
		// prints an alias by the ALIAS's OWN object name (typestring.go's
		// `case *Alias: w.typeName(t.obj)`), never its RHS. An unexported
		// cross-package alias name is therefore exactly as unspeakable as an
		// unexported Named; mirror that branch. When the alias name itself is
		// speakable, the printed form is just that name (plus any type args
		// for a generic alias), so only the TypeArgs need recursing — the RHS
		// is never printed.
		if obj := tt.Obj(); obj != nil && obj.Pkg() != nil &&
			(currentPkg == nil || obj.Pkg().Path() != currentPkg.Path()) &&
			!token.IsExported(obj.Name()) {
			return obj.Pkg(), obj.Name(), true
		}
		if targs := tt.TypeArgs(); targs != nil {
			for typ := range targs.Types() {
				if pkg, name, ok := unspeakableTypeArg(typ, currentPkg, visited); ok {
					return pkg, name, ok
				}
			}
		}
		return nil, "", false
	case *types.Pointer:
		return unspeakableTypeArg(tt.Elem(), currentPkg, visited)
	case *types.Slice:
		return unspeakableTypeArg(tt.Elem(), currentPkg, visited)
	case *types.Array:
		return unspeakableTypeArg(tt.Elem(), currentPkg, visited)
	case *types.Chan:
		return unspeakableTypeArg(tt.Elem(), currentPkg, visited)
	case *types.Map:
		if pkg, name, ok := unspeakableTypeArg(tt.Key(), currentPkg, visited); ok {
			return pkg, name, ok
		}
		return unspeakableTypeArg(tt.Elem(), currentPkg, visited)
	case *types.Struct:
		for field := range tt.Fields() {
			if pkg, name, ok := unspeakableTypeArg(field.Type(), currentPkg, visited); ok {
				return pkg, name, ok
			}
		}
		return nil, "", false
	case *types.Tuple:
		if tt == nil {
			return nil, "", false
		}
		for v := range tt.Variables() {
			if pkg, name, ok := unspeakableTypeArg(v.Type(), currentPkg, visited); ok {
				return pkg, name, ok
			}
		}
		return nil, "", false
	case *types.Signature:
		if pkg, name, ok := unspeakableTypeArg(tt.Params(), currentPkg, visited); ok {
			return pkg, name, ok
		}
		return unspeakableTypeArg(tt.Results(), currentPkg, visited)
	case *types.Interface:
		// An ANONYMOUS interface prints its explicit method SIGNATURES inline
		// (typestring.go writes `Name(params) results` per explicit method),
		// so an offender in a method's params or results (e.g. `interface{
		// Get() models.secret }`) is part of the printed representation —
		// walk each method's *types.Signature through the Signature case
		// above, not just the embedded types.
		for m := range tt.ExplicitMethods() {
			if pkg, name, ok := unspeakableTypeArg(m.Type(), currentPkg, visited); ok {
				return pkg, name, ok
			}
		}
		for et := range tt.EmbeddedTypes() {
			if pkg, name, ok := unspeakableTypeArg(et, currentPkg, visited); ok {
				return pkg, name, ok
			}
		}
		return nil, "", false
	default:
		return nil, "", false
	}
}

// genSkippedTagSink emits the fail-safe replacement for a requalification-
// failed cross-package generic tag (the types.Invalid sentinel in
// resolved[el] — see genChildComponent's guard): a NEVER-EXECUTED func
// literal assigned to blank whose body consumes every value expression the
// tag's normal call would have consumed —
//
//	_ = func() {
//		<CF-hoisted class statements>
//		_, _ = tupleValuedProp()                      // per (T, error) value
//		_ = struct{ F0 any; F1 any }{F0: v0, F1: v1}  // everything else
//	}
//
// This mirrors, at emit time, the anonymous-struct sink the SKELETON emits
// for the same tag (analyze.go's emitProbes fail-safe branch), restoring the
// probe ≡ emit reference symmetry: any enclosing local (used-param binding,
// loop var, CF-hoisted temp) whose only use sits in this tag stays used, so
// the emitted file compiles. Wrapping in a discarded func literal (rather
// than executing the struct literal inline) means the expressions are never
// EVALUATED at render time — a skipped tag must not run its prop
// expressions' side effects — while every identifier reference still counts
// as a use for the compiler.
//
// Values come from the same childPropsLiteral walk the real call path uses
// (slot closures included, so children references are consumed too, built by
// the same emitSlotClosure). A (T, error)-tuple-valued prop or ordered-attrs
// pair cannot sit in a single-value struct field, so it is blank-assigned at
// its own arity instead; the tag renders nothing either way, so the
// invalid-tuple diagnostic the normal path raises for non-(T, error) tuples
// is deliberately not repeated here (the tag already carries its positioned
// inference-unavailable warning).
func genSkippedTagSink(b *bytes.Buffer, el *ast.Element, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) bool {
	_, propsType, _ := childInvocation(el, byo, recvVar, recvTypeName)
	// Route CF-hoisted class statements into a temp buffer so they land
	// INSIDE the discarded func literal (never executed) instead of inline
	// in the render body (where they would run — and where their temps would
	// be unused, since the consuming class entry lives in the sink).
	var hoist bytes.Buffer
	fieldEntries, splatExpr, usedPkgs, err := childPropsLiteral(el, propsType, rt.rt(), classMergeExpr(mergeExpr, rt), table, structFields, nodeProps[propsType], byo, fm, func(nodes []ast.Markup) (string, error) {
		s, ok := emitSlotClosure(nodes, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr)
		if !ok {
			return "", fmt.Errorf("slot closure failed")
		}
		return s, nil
	}, false, resolved, &hoist, interpTemp)
	if err != nil {
		if ae, ok := errors.AsType[*attrError](err); ok {
			bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
		} else {
			bag.Errorf(el.Pos(), el.End(), childPropsErrorCode(err), "%s", strings.TrimPrefix(err.Error(), "codegen: "))
		}
		return false
	}
	for _, path := range usedPkgs {
		imports[path] = true
	}

	var typeParts, litParts []string
	sinkValue := func(val string) {
		name := fmt.Sprintf("F%d", len(typeParts))
		typeParts = append(typeParts, name+" any")
		litParts = append(litParts, name+": "+val)
	}
	// blankAssign writes an arity-matched all-blank assignment for a
	// multi-value expression (`_, _ = f()`), the only shape that can consume
	// a tuple-valued call.
	blankAssign := func(w *bytes.Buffer, n int, expr string) {
		blanks := make([]string, n)
		for i := range blanks {
			blanks[i] = "_"
		}
		fmt.Fprintf(w, "\t\t\t%s = %s\n", strings.Join(blanks, ", "), expr)
	}

	var stmts bytes.Buffer
	if splatExpr != "" {
		sinkValue(splatExpr)
	}
	for _, fe := range fieldEntries {
		switch {
		case fe.ea != nil:
			if t, ok := resolved[fe.ea].(*types.Tuple); ok && t.Len() > 1 {
				blankAssign(&stmts, t.Len(), fe.rawVal)
				continue
			}
			if _, val, ok := strings.Cut(fe.str, ":"); ok {
				sinkValue(strings.TrimSpace(val))
			}
		case fe.oa != nil:
			// Sink each ordered-attrs pair VALUE individually (the keys are
			// string constants), tuple-aware; the concat-prefix bag args
			// (composed from fallthrough attr values) are sunk too so their
			// references stay used. oaMergePrefix is the comma-separated arg
			// list, so wrap it back into a single ConcatAttrs call expression.
			if fe.oaMergePrefix != "" {
				sinkValue(fmt.Sprintf("%s.ConcatAttrs(%s)", rt.rt(), fe.oaMergePrefix))
			}
			for j, pr := range fe.oaPairs {
				if t, ok := resolved[&fe.oa.Pairs[j]].(*types.Tuple); ok && t.Len() > 1 {
					blankAssign(&stmts, t.Len(), pr.rawVal)
					continue
				}
				sinkValue(pr.rawVal)
			}
		default:
			// Slot/Children closures, class entries, boolean props, Attrs
			// bags — every entry childPropsLiteral produces is "Field: value".
			if _, val, ok := strings.Cut(fe.str, ":"); ok {
				sinkValue(strings.TrimSpace(val))
			}
		}
	}

	if hoist.Len() == 0 && stmts.Len() == 0 && len(litParts) == 0 {
		return true // nothing to consume; the element simply renders nothing
	}
	b.WriteString("\t\t_ = func() {\n")
	b.Write(hoist.Bytes())
	b.Write(stmts.Bytes())
	if len(litParts) > 0 {
		fmt.Fprintf(b, "\t\t\t_ = struct{ %s }{%s}\n", strings.Join(typeParts, "; "), strings.Join(litParts, ", "))
	}
	b.WriteString("\t\t}\n")
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
func emitSlotClosure(nodes []ast.Markup, currentPkg *types.Package, resolved map[ast.Node]types.Type, table filterTable, structFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, imports map[string]bool, rt rtImports, importAliases map[string]string, boundNames map[string]string, typeArgAliases map[string]string, interpTemp *int, fset *token.FileSet, recvVar, recvTypeName string, cls *attrclass.Classifier, fm FieldMatcher, bag *diag.Bag, mergeExpr string) (string, bool) {
	var slot bytes.Buffer
	fmt.Fprintf(&slot, "%s.Func(func(ctx %s.Context, _gsxw %s.Writer) error {\n", rt.rt(), rt.ctx(), rt.io())
	fmt.Fprintf(&slot, "\t\t_gsxgw := %s.W(_gsxw)\n", rt.rt())
	emitNumScratch(&slot, nodes, resolved, cls)
	for _, c := range nodes {
		if !genNode(&slot, c, currentPkg, resolved, table, structFields, nodeProps, attrsProps, byo, imports, rt, importAliases, boundNames, typeArgAliases, interpTemp, fset, recvVar, recvTypeName, cls, fm, bag, mergeExpr) {
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
	oa            *ast.OrderedAttrsAttr // non-nil iff this entry came from an ordered-attrs attr
	oaPairs       []oaPairEntry         // per-pair info when oa != nil
	oaLit         string                // bare `<rtPkg>.Attrs{…}` literal text for an Attrs-targeted ordered-attrs attr (fieldName == "Attrs")
	oaMergePrefix string                // comma-separated ConcatAttrs prefix args (base literal + spread/cond segments) the literal concatenates after; "" when the literal stands alone. The full field is `<rtPkg>.ConcatAttrs(<oaMergePrefix>, <oaLit>)`.
	inferField    string
	inferArg      string
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

// attrsOnlyPropsKey is a synthetic props-type key for attrsOnlyBagExpr's
// childPropsLiteral call. It contains a '.' so it can never collide with a real
// same-package <Name>Props key, and never escapes into emitted code.
const attrsOnlyPropsKey = "attrsonly.bag"

// attrsOnlyBagExpr builds the single gsx.Attrs expression for an attrs-only
// component-value call site by reusing childPropsLiteral's fallthrough assembly
// with a synthetic declared-field set of {"Attrs"}: every call-site attr is
// fallthrough; spreads become ConcatAttrs(...) segments; an attrs={{ }} ordered
// literal targets the bag and concatenates last — all existing behavior.
// Returns "" when the tag has no attrs at all.
//
// The single returned entry is run through the SAME (T, error) tuple rejection +
// hoist genChildComponent applies to a real props literal, so a pipeline stage
// (or a matched attrs={ } value) that returns a tuple is handled identically —
// hoisted before the call in emit mode, or rejected with a positioned *attrError.
func attrsOnlyBagExpr(el *ast.Element, rtPkg, mergeExpr string, table filterTable, byo *byoData, fm FieldMatcher, probeWrap bool, resolved map[ast.Node]types.Type, b *bytes.Buffer, interpTemp *int) (expr string, usedPkgs map[string]string, err error) {
	synthetic := map[string]map[string]bool{attrsOnlyPropsKey: {"Attrs": true}}
	fields, splat, used, err := childPropsLiteral(el, attrsOnlyPropsKey, rtPkg, mergeExpr, table, synthetic, nil, byo, fm,
		func([]ast.Markup) (string, error) { return "", fmt.Errorf("attrs-only components take no slots") },
		probeWrap, resolved, b, interpTemp)
	if err != nil {
		return "", nil, err
	}
	if splat != "" {
		// cannot happen: the synthetic set has an Attrs bag, so the
		// whole-struct-splat branch is skipped; guard anyway.
		return "", nil, fmt.Errorf("codegen: unexpected splat on attrs-only component <%s>", el.Tag)
	}
	if len(fields) == 0 {
		return "", used, nil
	}
	if len(fields) != 1 || !strings.HasPrefix(fields[0].str, "Attrs: ") {
		return "", nil, fmt.Errorf("codegen: attrs-only bag for <%s> produced unexpected fields %v", el.Tag, fields)
	}
	// Mirror genChildComponent's (T, error) tuple handling for this one entry: a
	// matched attrs={call()} ExprAttr or an attrs={{ … }} ordered-attrs literal
	// can carry a tuple-returning value. In probe mode resolved is nil, so both
	// checks are no-ops (the skeleton uses _gsxunwrap wrapping instead).
	fe := &fields[0]
	if fe.ea != nil {
		if t, ok := resolved[fe.ea].(*types.Tuple); ok {
			if _, unwrappable := tupleUnwrapType(t); !unwrappable {
				return "", nil, &attrError{pos: fe.ea.Pos(), end: fe.ea.End(), code: "invalid-tuple",
					msg: fmt.Sprintf("child prop %q value %q returns %s; only (T, error) is supported", fe.ea.Name, fe.ea.Expr, t)}
			}
			tmp := hoistTuple(b, fe.rawVal, interpTemp)
			fe.str = fmt.Sprintf("%s: %s", fe.fieldName, tmp)
		}
	}
	if fe.oa != nil {
		anyTuple := false
		for j := range fe.oaPairs {
			pairType := resolved[&fe.oa.Pairs[j]]
			if t, ok := pairType.(*types.Tuple); ok {
				if _, unwrappable := tupleUnwrapType(t); !unwrappable {
					return "", nil, &attrError{pos: fe.oa.Pairs[j].Pos(), end: fe.oa.Pairs[j].End(), code: "invalid-tuple",
						msg: fmt.Sprintf("ordered-attrs pair %q value %q returns %s; only (T, error) is supported", fe.oaPairs[j].key, fe.oaPairs[j].rawVal, t)}
				}
				anyTuple = true
			}
		}
		if anyTuple {
			var sb strings.Builder
			fmt.Fprintf(&sb, "%s.Attrs{", rtPkg)
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
					valueStr = pr.rawVal
				}
				fmt.Fprintf(&sb, "{Key: %s, Value: %s}, ", strconv.Quote(pr.key), valueStr)
			}
			sb.WriteString("}")
			if fe.oaMergePrefix != "" {
				fe.str = fmt.Sprintf("Attrs: %s.ConcatAttrs(%s, %s)", rtPkg, fe.oaMergePrefix, sb.String())
			} else {
				fe.str = fmt.Sprintf("%s: %s", fe.fieldName, sb.String())
			}
		}
	}
	return strings.TrimPrefix(fe.str, "Attrs: "), used, nil
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
// statements into b before the Node call, and are also used directly (via
// emitPipeWrap, gated on probeWrap — see pipeWrap below) to hoist a mid-stage
// (R, error) filter in any prop/fallthrough/splat pipeline. Pass nil for both in
// contexts that do not support hoisting (e.g. the analyze path passes a local
// scratch buffer).
// resolved maps *ClassPart nodes to their harvest type so classEntryExpr can detect
// and hoist (T, error) tuple-returning unconditional plain parts. Pass nil in the
// probe path (skeleton does not need resolved; probeWrap wraps call exprs instead).
func childPropsLiteral(el *ast.Element, propsType, rtPkg, mergeExpr string, table filterTable, propFields map[string]map[string]bool, nodeFields map[string]bool, byo *byoData, fm FieldMatcher, slotValue func(nodes []ast.Markup) (string, error), probeWrap bool, resolved map[ast.Node]types.Type, b *bytes.Buffer, interpTemp *int) (fields []propFieldEntry, splatExpr string, usedPkgs map[string]string, err error) {
	fm = fieldMatcherOrDefault(fm)    // normalize nil → default matcher
	declared := propFields[propsType] // nil for cross-package / unknown → graceful
	// pipeWrap is the lowerPipe hook for a mid-stage (R, error) filter in a prop
	// pipeline: probeWrap (analyze skeleton) uses probePipeWrap so the composite
	// literal built below stays a single expression; probeWrap=false (real emit)
	// uses emitPipeWrap, hoisting into b (always non-nil for this function's real
	// callers; a nil b here would only be reached by a test driving a filter with
	// no error-returning stage, so the wrap closure is never invoked).
	pipeWrap := probePipeWrap
	if !probeWrap {
		pipeWrap = emitPipeWrap(b, interpTemp)
	}
	// BYO struct facts: when the child is byo, an unmatched attr (→ Attrs bag) or
	// {children} (→ Children field) is a CLEAR ERROR if the author struct lacks the
	// corresponding field, rather than silently auto-synthesizing one (the byo path
	// is explicit — spec §6).
	byoStr, isByoChild := byo.isByoStruct(propsType)

	// Whole-struct splat: `<Card { d... }/>` → `Card(d)`. A SpreadAttr passes the
	// whole prop value, NOT an Attrs merge, when the target component has no
	// fallthrough `Attrs gsx.Attrs` bag to merge into. Two cases qualify:
	//   - byo components (isByoChild): the author struct is the prop value, and the
	//     Attrs bag — if any — is filled by unmatched NAMED attrs, never by a spread.
	//   - non-byo components whose enumerated Props type has NO Attrs bag field
	//     (templ-interop / convention structs like CheckboxPopupSelectProps, and
	//     generated components that never reference `attrs`). Emitting an Attrs
	//     merge there produces `Props{Attrs: …}` against a struct with no Attrs
	//     field — a hard compile error. `{ f... }` is unambiguously the prop value.
	// A component WITH an Attrs bag keeps the spread as an attrs-merge (it may even
	// mix with field attrs, e.g. `<Card title="Hi" { extra... }/>`), so this branch
	// is skipped for it. Must be all-or-nothing: the sole attr, no children.
	if isByoChild || (isKnownPropsType(propFields, propsType) && !hasAttrsBag(propFields, propsType, byoStr)) {
		for _, a := range el.Attrs {
			if s, ok := a.(*ast.SpreadAttr); ok {
				// Found a splat on a bag-less component. Validate all-or-nothing.
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
					lowered, used, perr := lowerPipe(s.Expr, s.Stages, table, pipeWrap)
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
	var bag []string      // fallthrough base-literal entries: `"<rawName>": <value>`
	var segments []string // bare ConcatAttrs segment exprs (<spread> / <rtPkg>.AttrsCond(...)) in source order
	attrsLitIdx := -1     // index into fields of an Attrs-targeted ordered-attrs literal, or -1
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
					q := strconv.Quote(t.Value)
					fields = append(fields, propFieldEntry{str: fmt.Sprintf("%s: %s", fn, q), inferField: fn, inferArg: q})
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
					lowered, used, perr := lowerPipe(t.Expr, t.Stages, table, pipeWrap)
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
					inferField:  fn,
					inferArg:    fieldVal,
				})
			} else {
				val := strings.TrimSpace(t.Expr)
				if len(t.Stages) > 0 {
					lowered, used, perr := lowerPipe(t.Expr, t.Stages, table, pipeWrap)
					if perr != nil {
						msg := strings.TrimPrefix(perr.Error(), "codegen: ")
						return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
					}
					recordPkgs(used)
					val = lowered
					// lowerPipe leaves a FINAL (R, error) stage unwrapped (it hoists
					// only non-final error stages). A fallthrough attr's value is
					// spliced into the Attrs bag literal in single-value position, so
					// unwrap the final tuple HERE — this is the one attr context that
					// lacks a downstream tuple-hoist pass. Probe mode keeps the
					// skeleton a single expression via _gsxunwrap; emit mode hoists
					// `v, _gsxerr := call; if _gsxerr != nil { return _gsxerr }`,
					// mirroring the inline CondAttr hoist and the declared-prop /
					// ordered-attrs unwrap that genChildComponent defers.
					if finalStageErr(t.Stages, table) {
						if probeWrap {
							val = "_gsxunwrap(" + val + ")"
						} else {
							val = hoistTuple(b, val, interpTemp)
						}
					}
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
					fields = append(fields, propFieldEntry{str: fmt.Sprintf("%s: true", fn), inferField: fn, inferArg: "true"})
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
			entry, used, eerr := classEntryExpr(b, interpTemp, t, rtPkg, mergeExpr, table, resolved, probeWrap, pipeWrap)
			if eerr != nil {
				return nil, "", nil, eerr
			}
			recordPkgs(used)
			bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), entry))
		case *ast.SpreadAttr:
			spreadExpr := strings.TrimSpace(t.Expr)
			if len(t.Stages) > 0 {
				lowered, used, perr := lowerPipe(t.Expr, t.Stages, table, pipeWrap)
				if perr != nil {
					msg := strings.TrimPrefix(perr.Error(), "codegen: ")
					return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
				}
				recordPkgs(used)
				spreadExpr = lowered
			}
			segments = append(segments, spreadExpr)
		case *ast.CondAttr:
			// One uniform lowering: condAttrsExpr always emits an AttrsCond(...) call
			// whose branches are (Attrs, error) thunks with thunk-local hoists for any
			// error-returning pipeline stage. In emit mode the call is hoisted here via
			// hoistTuple (the temp is appended as a `segments` entry); in probe mode it
			// is wrapped in _gsxunwrap(...) to stay a single expression (emit ≡ probe).
			condExpr, used, cerr := condAttrsExpr(t, rtPkg, el.Tag, mergeExpr, table, probeWrap, resolved, interpTemp)
			if cerr != nil {
				return nil, "", nil, cerr
			}
			recordPkgs(used)
			if probeWrap {
				condExpr = "_gsxunwrap(" + condExpr + ")"
			} else {
				condExpr = hoistTuple(b, condExpr, interpTemp)
			}
			segments = append(segments, condExpr)
		case *ast.OrderedAttrsAttr:
			fn, ok := matchOrderedAttrsField(declared, t.Name, fm)
			if !ok {
				msg := fmt.Sprintf("ordered-attrs literal {{ }} on <%s> attribute %q matches no field of %s and cannot fall through to the Attrs bag; declare a gsx.Attrs field to receive it", el.Tag, t.Name, propsType)
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "ordered-attrs-no-field", msg: msg}
			}
			if verr := validateMatchedField(fn, t.Name, propsType, declared); verr != nil {
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "bad-field-match", msg: verr.Error()}
			}
			if fn == "Attrs" && attrsLitIdx >= 0 {
				msg := fmt.Sprintf("duplicate ordered-attrs literal targeting the Attrs bag on <%s>; combine the pairs into one {{ }} literal", el.Tag)
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "ordered-attrs-duplicate", msg: msg}
			}
			// Collect per-pair info for genChildComponent's tuple-hoist pass.
			pairEntries := make([]oaPairEntry, len(t.Pairs))
			for i, pr := range t.Pairs {
				pairEntries[i] = oaPairEntry{key: pr.Key, rawVal: pr.Value}
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "%s.Attrs{", rtPkg)
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
			lit := sb.String()
			if fn == "Attrs" {
				attrsLitIdx = len(fields)
			}
			fields = append(fields, propFieldEntry{
				str:       fn + ": " + lit,
				fieldName: fn,
				oa:        t,
				oaPairs:   pairEntries,
				oaLit:     lit,
			})
		case *ast.CommentAttr:
			// Source-only comment; not a component prop.
		case *ast.EmbeddedAttr:
			// A hole-free js`…`/css`…`/`…` literal forwards to the Attrs bag as
			// raw text — identical to a plain string attribute (JSX-style
			// directive forwarding, e.g. x-model=js`pdcaCategory`). Embedded
			// literals always fall through to the bag; to set a declared prop
			// use a string or { expr }.
			text, static := embeddedStaticText(t)
			if !static {
				msg := fmt.Sprintf("embedded %s attribute literal %q with @{ } interpolation cannot be used as a component prop on <%s> yet; pass an ordinary prop value or move the literal to an element inside the component", embeddedLangName(t.Lang), t.Name, el.Tag)
				return nil, "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(text)))
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
	if len(bag) > 0 || len(segments) > 0 {
		// BYO: unmatched attrs route to an explicit `Attrs gsx.Attrs` field. Missing
		// → a clear error (the author adds it and spreads it in the markup).
		if isByoChild && !byoStr.hasAttrs {
			msg := fmt.Sprintf("attribute on <%s> matches no field of its Props type %s and %s has no `Attrs gsx.Attrs` field", el.Tag, propsType, propsType)
			return nil, "", nil, &attrError{pos: el.Pos(), end: el.End(), code: "byo-missing-attrs", msg: msg}
		}
		// Call-site bags CONCATENATE — last-wins/aggregation is resolved at the
		// leaf, so the composition never eagerly merges (a component may iterate
		// its raw bag). parts = base literal (omitted when the bag is empty) +
		// each spread/cond segment in source order; an attrs={{ }} ordered literal
		// is always concatenated LAST (merge-last rule).
		var parts []string
		if len(bag) > 0 {
			parts = append(parts, fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(bag, ", ")))
		}
		parts = append(parts, segments...) // parts is non-empty (block guard)
		if attrsLitIdx >= 0 {
			// An explicit attrs={{ }} literal coexists with other bag
			// contributors: fold them into ONE Attrs field — the composed bag
			// first, the literal concatenated last — instead of emitting a
			// duplicate struct field. The hoist pass rebuilds this composition
			// when pair values are hoisted, keyed off oaMergePrefix (the
			// comma-separated prefix args before the literal).
			prefix := strings.Join(parts, ", ")
			fields[attrsLitIdx].oaMergePrefix = prefix
			fields[attrsLitIdx].str = fmt.Sprintf("Attrs: %s.ConcatAttrs(%s, %s)", rtPkg, prefix, fields[attrsLitIdx].oaLit)
		} else if len(segments) == 0 {
			// base literal only — keep the plain form (no wrapper), zero churn.
			fields = append(fields, propFieldEntry{str: "Attrs: " + parts[0]})
		} else {
			attrsExpr := fmt.Sprintf("%s.ConcatAttrs(%s)", rtPkg, strings.Join(parts, ", "))
			fields = append(fields, propFieldEntry{str: "Attrs: " + attrsExpr})
		}
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
func classEntryExpr(b *bytes.Buffer, interpTemp *int, a *ast.ClassAttr, rtPkg string, mergeExpr string, table filterTable, resolved map[ast.Node]types.Type, probeWrap bool, wrap func(string) string) (string, map[string]string, error) {
	parts := make([]string, 0, len(a.Parts))
	usedPkgs := map[string]string{}
	ordered := !probeWrap && composedPartsOrdered(a, resolved)
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
				} else if !probeWrap {
					// Emit mode: consult resolved to detect and hoist (T, error)
					// tuples. wrap writes the hoist into b at this point — after the
					// if/case label and before the _gsxvN = assignment — so it lands
					// inside the correct block, with the errReturn arity (single-
					// value at element level, two-value inside a cond-attr thunk)
					// the caller already baked into wrap.
					if t := resolved[arm]; t != nil {
						if _, isTuple := t.(*types.Tuple); isTuple {
							if _, ok := tupleUnwrapType(t); !ok {
								lowerErr = &attrError{pos: arm.Pos(), end: arm.End(), code: "invalid-tuple", msg: fmt.Sprintf("class value-form arm %q returns %s; only (T, error) is supported", arm.Expr, t)}
								return "", false
							}
							expr = wrap(expr)
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
		expr, used, err := lowerClassPartSeed(*p, table, wrap)
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
					expr = wrap(expr)
				} else if ordered {
					tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
					*interpTemp++
					fmt.Fprintf(b, "\t\t%s := %s\n", tmp, expr)
					expr = tmp
				}
			}
			parts = append(parts, fmt.Sprintf("%s.Class(%s)", rtPkg, expr))
		} else {
			if !probeWrap && ordered {
				if t, isTuple := resolved[p].(*types.Tuple); isTuple {
					if _, ok := tupleUnwrapType(t); !ok {
						return "", nil, &attrError{pos: p.Pos(), end: p.End(), code: "invalid-tuple", msg: fmt.Sprintf("class part %q returns %s; only (T, error) is supported", p.Expr, t)}
					}
					expr = wrap(expr)
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
func condAttrsExpr(t *ast.CondAttr, rtPkg, tag string, mergeExpr string, table filterTable, probeWrap bool, resolved map[ast.Node]types.Type, interpTemp *int) (string, map[string]string, error) {
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
		lit, used, err := condBranchAttrs(&tb, interpTemp, wrap, probeWrap, attrs, rtPkg, tag, mergeExpr, table, resolved)
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

// condBranchAttrs builds a <rtPkg>.Attrs{…} literal from one conditional-attr
// branch's attrs. Static/expr/bool attrs become bag entries keyed by raw name; a
// composable class={…} becomes a ClassJoin entry. A spread or nested
// conditional inside a branch is unsupported (kept shallow).
//
// wrap is the lowerPipe hook for an error-returning stage in a branch pipeline —
// always non-nil (probePipeWrap in probe mode, thunkPipeWrap in emit mode): the
// ExprAttr path lowers every pipeline through it, hoisting (emit) or unwrapping
// (probe) any error-returning stage, final or not (lowerPipe only wraps
// non-final stages, so a final error stage is wrapped again here explicitly).
// The same wrap also hoists a PLAIN (no-pipeline) ExprAttr value once resolved
// confirms it is a (T, error) tuple call — mirroring childPropsLiteral's
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
// the element-level path (childPropsLiteral): the same thunk-local b/
// interpTemp/resolved and the same wrap are threaded through, so CF (if/
// switch), plain-tuple, and ordered class parts inside a branch hoist their
// errors into the enclosing thunk precisely like the non-branch case.
func condBranchAttrs(b *bytes.Buffer, interpTemp *int, wrap func(string) string, probeWrap bool, attrs []ast.Attr, rtPkg, tag, mergeExpr string, table filterTable, resolved map[ast.Node]types.Type) (string, map[string]string, error) {
	var entries []string
	usedPkgs := map[string]string{}
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.StaticAttr:
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(t.Value)))
		case *ast.ExprAttr:
			val := strings.TrimSpace(t.Expr)
			if len(t.Stages) > 0 {
				lowered, used, perr := lowerPipe(t.Expr, t.Stages, table, wrap)
				if perr != nil {
					msg := strings.TrimPrefix(perr.Error(), "codegen: ")
					return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unresolved-pipeline", msg: msg}
				}
				maps.Copy(usedPkgs, used)
				// The final stage is never wrapped by lowerPipe; wrap it here too
				// when it returns (R, error), so a final tuple hoists (emit) /
				// unwraps (probe) instead of sitting raw in the bag literal.
				last := t.Stages[len(t.Stages)-1]
				if e, ok := table.lookup(last.Name); ok && e.hasErr {
					lowered = wrap(lowered)
				}
				val = lowered
			} else if probeWrap {
				// Plain tuple-returning call, no pipeline: mirror childPropsLiteral's
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
				val = wrap(val)
			}
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), val))
		case *ast.BoolAttr:
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: true}", strconv.Quote(t.Name)))
		case *ast.ClassAttr:
			if t.Name != "class" {
				msg := fmt.Sprintf("%s attribute in a conditional branch (<%s>) not supported yet", t.Name, tag)
				return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			entry, used, eerr := classEntryExpr(b, interpTemp, t, rtPkg, mergeExpr, table, resolved, probeWrap, wrap)
			if eerr != nil {
				return "", nil, eerr
			}
			maps.Copy(usedPkgs, used)
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), entry))
		case *ast.CommentAttr:
			// Source-only comment; not a component prop.
		case *ast.EmbeddedAttr:
			// A hole-free embedded literal forwards to the conditional Attrs bag
			// as raw text — same lowering as the component-prop path above.
			text, static := embeddedStaticText(t)
			if !static {
				msg := fmt.Sprintf("embedded %s attribute literal %q with @{ } interpolation cannot be used as a component prop on <%s> yet; pass an ordinary prop value or move the literal to an element inside the component", embeddedLangName(t.Lang), t.Name, tag)
				return "", nil, &attrError{pos: t.Pos(), end: t.End(), code: "unsupported-component-attr", msg: msg}
			}
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(text)))
		default:
			msg := fmt.Sprintf("unsupported attribute %T in a conditional branch (<%s>)", a, tag)
			return "", nil, &attrError{pos: a.Pos(), end: a.End(), code: "unsupported-component-attr", msg: msg}
		}
	}
	return fmt.Sprintf("%s.Attrs{%s}", rtPkg, strings.Join(entries, ", ")), usedPkgs, nil
}
