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
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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
//
// nodeProps records which declared params have type exactly gsx.Node; it is
// threaded alongside propFields and consumed by emit/probe to promote renderable
// values into gsx.Node props (gsx.Val/gsx.Text).
func resolveTypesPkg(dir string, files map[string]*gsxast.File, propFields, nodeProps map[string]map[string]bool, byo *byoData, fset *token.FileSet) (map[gsxast.Node]types.Type, filterTable, error) {
	return resolveTypesPkgWithFilters(dir, files, propFields, nodeProps, byo, nil, []string{stdImportPath}, nil, fset, nil)
}

// resolveTypesPkgWithFilters is the multi-package form of resolveTypesPkg: it
// harvests the filter table from filterPkgs (last-wins precedence) and otherwise
// behaves identically. resolveTypesPkg is the std-only wrapper.
//
// resolver is optional; nil uses packagesLoadResolver (the original packages.Load
// behavior, byte-identical to before). When a *CachedResolver is passed, its
// prebuilt filterTable is used instead of calling loadFilterTableMulti again.
func resolveTypesPkgWithFilters(dir string, files map[string]*gsxast.File, propFields, nodeProps map[string]map[string]bool, byo *byoData, fm FieldMatcher, filterPkgs []string, aliases []FilterAlias, fset *token.FileSet, resolver typeResolver) (map[gsxast.Node]types.Type, filterTable, error) {
	if resolver == nil {
		resolver = packagesLoadResolver{}
	}

	// Use the cached resolver's prebuilt filter table when available; otherwise
	// load it fresh (the original behavior).
	var table filterTable
	if cr, ok := resolver.(*CachedResolver); ok {
		table = cr.filters()
	} else {
		var err error
		table, err = loadFilterTableMulti(dir, filterPkgs, aliases)
		if err != nil {
			return nil, nil, err
		}
	}

	overlay := map[string][]byte{}
	skelComps := map[string][]*gsxast.Component{}
	for path, file := range files {
		skel, comps, _, err := buildSkeleton(file, table, propFields, nodeProps, byo, fm, fset)
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
	overlay[sharedPath] = []byte("package " + pkgName + "\n\nfunc _gsxuse(...any) {}\nfunc _gsxcompsig(any) {}\n")

	goFiles, info, err := resolver.check(dir, overlay, fset)
	if err != nil {
		return nil, nil, err
	}

	out := map[gsxast.Node]types.Type{}
	for _, f := range goFiles {
		fname := fset.Position(f.Pos()).Filename
		comps, ok := skelComps[fname]
		if !ok {
			continue // a real .go file, not one of our overlays
		}
		harvest(f, comps, info, out, nil)
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
// nodeProps records which DECLARED params have type exactly gsx.Node (keyed by the
// same props-type name, value fieldName → true). Synthetic Children/Attrs fields
// are NOT included. nodeProps is derived in the same loop as propFields and threaded
// alongside it; it is currently unused after derivation (a later task consumes it).
//
// A receiver parse failure is silently skipped (the component is simply omitted, so
// its call sites take the graceful cross-package path); buildSkeleton re-parses the
// same receiver and surfaces a clean error there.
//
// BYO (author-owns-Props): a component whose SOLE non-receiver parameter is an
// author-declared NAMED STRUCT uses that struct directly — gsx generates no
// <Name>Props wrapper. Such a component is recorded in byo (componentKey →
// struct type name) and the struct's exported field set + node fields are
// published into propFields/nodeProps under the STRUCT's type name (so
// childPropsLiteral keys on it exactly as for a generated props type). A struct
// declared in a .gsx GoChunk is read syntactically (no resolution); an external
// .go struct is enumerated by a preliminary go/packages load of dir. byo is
// nil-safe and always returned non-nil.
func componentPropFieldsFor(dir string, files map[string]*gsxast.File) (propFields, nodeProps map[string]map[string]bool, byo *byoData, err error) {
	out := map[string]map[string]bool{}
	nodeOut := map[string]map[string]bool{}
	byo = newByoData()
	byo.nullaryFuncs = packageNullaryFuncs(dir)

	// Discover author structs: those declared in .gsx GoChunks are read from the
	// AST now; any candidate struct NOT found in the .gsx is enumerated via a
	// preliminary external (.go) type-load below.
	gsxStructs := gsxStructDecls(files)
	externalWanted := map[string]bool{}

	// genProps derives the GENERATED-path prop-field map + node-field map for a
	// component (the historical AST-derived behavior), keyed by propsName/compKey.
	genProps := func(c *gsxast.Component, params []param, propsName string) {
		fields := map[string]bool{}
		nodeFields := map[string]bool{}
		for _, p := range params {
			fields[fieldName(p.name)] = true
			if isGsxNodeType(p.typ) {
				nodeFields[fieldName(p.name)] = true
			}
		}
		hasChildren := usesChildren(c.Body)
		if hasChildren {
			fields["Children"] = true
		}
		// Mirror the Attrs synthesis gate in genComponent/buildSkeleton exactly
		// (hasFallthrough) so the map agrees with the struct that is actually
		// emitted. MANUAL mode (a body referencing `attrs`) forces the Attrs field
		// regardless, so OR it in. A NULLARY component (no params, no children)
		// is auto-eligible only when it already has something in the props struct
		// (params or children) — otherwise auto fallthrough would force a props
		// struct for a component that is truly nullary (no props at all). This
		// mirrors the method-nullary gate in genComponent/emitComponentSkeleton.
		_, hasRoot := singleRoot(c.Body)
		manual := usesAttrs(c.Body)
		if (hasRoot && (len(params) > 0 || hasChildren)) || manual {
			fields["Attrs"] = true
		}
		// A function component whose fields map is empty (no params, no Children,
		// no Attrs) has no props struct — record it with a nil value so callers
		// can distinguish it from a cross-package component (absent key).
		// Method nullary is already handled by isMethod in the call-site logic and
		// keeps its empty map entry here.
		if c.Recv == "" && len(fields) == 0 {
			out[propsName] = nil
		} else {
			out[propsName] = fields
		}
		nodeOut[propsName] = nodeFields
	}

	// deferred holds external-struct candidate components whose byo-vs-generated
	// decision waits on the preliminary load: only after we know the candidate
	// type IS a struct (load result) can we commit to byo; otherwise the component
	// falls back to the generated path.
	type deferredComp struct {
		c          *gsxast.Component
		params     []param
		propsName  string
		compKey    string
		structName string
	}
	var deferred []deferredComp

	for _, file := range files {
		for _, d := range file.Decls {
			c, ok := d.(*gsxast.Component)
			if !ok {
				continue
			}
			params, err := parseParams(c.Params)
			if err != nil {
				return nil, nil, nil, err
			}
			propsName := c.Name + "Props"
			compKey := "." + c.Name
			if c.Recv != "" {
				_, _, recvTypeName, rerr := parseRecv(c.Recv)
				if rerr != nil {
					continue // surfaced cleanly by buildSkeleton
				}
				propsName = recvTypeName + c.Name + "Props"
				compKey = recvTypeName + "." + c.Name
			}
			// BYO classification: a sole non-receiver param whose type is a bare
			// (same-package) struct name → byo. Resolve the field set from the .gsx
			// GoChunk decl when present; otherwise defer pending the external load.
			if structName := soleParamTypeName(params); structName != "" {
				if st, ok := gsxStructs[structName]; ok {
					f, nf, bs := fieldsFromGsxStruct(st)
					out[structName] = f
					nodeOut[structName] = nf
					byo.structs[structName] = bs
					byo.inGsx[structName] = true
					byo.compStruct[compKey] = structName
					continue
				}
				externalWanted[structName] = true
				deferred = append(deferred, deferredComp{c, params, propsName, compKey, structName})
				continue
			}
			genProps(c, params, propsName)
		}
	}

	// Preliminary external load: enumerate any candidate struct declared in a
	// sibling .go file. Then finalize each deferred component: byo iff its
	// candidate type resolved to a struct; otherwise it falls back to the
	// generated path (an inline single-param component whose type happens to be a
	// same-package non-struct named type, or a type we could not resolve).
	if dir != "" && len(externalWanted) > 0 {
		ef, enf, es := loadExternalStructFields(dir, externalWanted)
		for name, f := range ef {
			out[name] = f
			nodeOut[name] = enf[name]
			byo.structs[name] = es[name]
		}
	}
	for _, dc := range deferred {
		if _, ok := byo.structs[dc.structName]; ok {
			byo.compStruct[dc.compKey] = dc.structName
			continue
		}
		// Not a struct (or unresolved) → not byo; take the generated path.
		genProps(dc.c, dc.params, dc.propsName)
	}
	return out, nodeOut, byo, nil
}

// isNoPropsComponent reports whether propsType names a same-package function
// component that has NO props struct (nullary: no params, no children, no
// fallthrough Attrs). The sentinel is a propFields entry whose key is present
// (same-package) but whose value is nil (no props struct). A cross-package
// component has no entry at all (absent key → not no-props). A method component
// is handled by isMethod in the call-site logic and is never recorded with a nil
// value, so this function fires only for function components.
func isNoPropsComponent(propFields map[string]map[string]bool, propsType string) bool {
	fields, ok := propFields[propsType]
	return ok && fields == nil
}

// isBareCallCandidate reports whether a component tag should be resolved by its
// real Go signature rather than the XxxProps convention. It fires for a
// same-package (non-dotted) tag whose backing func is nullary-by-construction:
//   - a hand-written `func F() gsx.Node` (not a .gsx component), or
//   - a .gsx no-props function component (`component F() { … }`, which codegen
//     emits as a bare `func F() gsx.Node`).
//
// For either, a nullary call is a valid bare `<F/>` — like a self-contained void
// element, no props struct — and passing attributes or children is a clean error
// (a zero-arg func has nowhere to put them). byo components and methods keep
// their existing paths. The probe (emitProbes) and emitter (genChildComponent)
// both branch on this so emit ≡ probe: the probe emits _gsxcompsig(F)
// (arity-agnostic) and the emitter reads the harvested *types.Signature from
// `resolved[el]`.
func isBareCallCandidate(el *gsxast.Element, propFields map[string]map[string]bool, byo *byoData, recvVar, recvTypeName string) bool {
	if !isComponentTag(el.Tag) || strings.Contains(el.Tag, ".") {
		return false
	}
	_, propsType, isMethod := childInvocation(el, byo, recvVar, recvTypeName)
	if isMethod {
		return false
	}
	if _, isByo := byo.isByoStruct(propsType); isByo {
		return false
	}
	if _, gsxDeclared := propFields[propsType]; gsxDeclared {
		// A .gsx component: only a no-props function component is bare-callable;
		// a with-props component keeps the XxxProps convention.
		return isNoPropsComponent(propFields, propsType)
	}
	// A hand-written same-package func: bare-callable ONLY when it is nullary.
	// An arity ≥ 1 func keeps the XxxProps convention so its attrs still type-check
	// against the props struct at generate time.
	return byo.isNullaryFunc(el.Tag)
}

// isGsxNodeType reports whether a param's declared type string is exactly
// gsx.Node (ignoring surrounding whitespace).
func isGsxNodeType(typ string) bool {
	return strings.TrimSpace(typ) == "gsx.Node"
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
		exists, err := diskExists(p)
		if err != nil {
			return "", fmt.Errorf("codegen: probing overlay path %s: %w", p, err)
		}
		if !exists {
			return p, nil
		}
		// exists on disk — try the next candidate
	}
}

// buildSkeleton synthesizes a Go file standing in for the gsx file during type
// resolution: the file's GoChunks, plus each component's real props struct and
// func signature, with a probe body (used-param locals, each interpolation as
// `_gsxuse(expr)`, each child component as `_ = Child(ChildProps{})`).
func buildSkeleton(file *gsxast.File, table filterTable, propFields, nodeProps map[string]map[string]bool, byo *byoData, fm FieldMatcher, fset *token.FileSet) (string, []*gsxast.Component, []importSpec, error) {
	var comps []*gsxast.Component
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			comps = append(comps, c)
		}
	}

	// Split every GoChunk into its imports (hoisted ahead of all declarations)
	// and its non-import body. Each body carries the .gsx source position of its
	// first byte so it can be emitted under a //line directive — go-to-definition
	// on a user-declared top-level Go symbol (a helper func/type/var in the .gsx)
	// then resolves back to the .gsx instead of the synthetic overlay.
	var imports []importSpec
	var bodies []goBody
	for _, d := range file.Decls {
		if gc, ok := d.(*gsxast.GoChunk); ok {
			imps, body, bodyOff, err := splitChunk(gc.Src)
			if err != nil {
				return "", nil, nil, err
			}
			// Resolve each import's .gsx position so the skeleton can emit a //line
			// directive ahead of it — gc.Src starts exactly at gc.Pos(), so the
			// chunk's byte offset plus the import's intra-chunk offset is the
			// absolute .gsx offset. Without this, go/types import errors (e.g.
			// "imported and not used") resolve to the overlay .x.go path/line.
			if fset != nil && gc.Pos().IsValid() {
				if tf := fset.File(gc.Pos()); tf != nil {
					base := fset.Position(gc.Pos()).Offset
					for i := range imps {
						imps[i].pos = tf.Pos(base + imps[i].srcOff)
					}
				}
			}
			imports = append(imports, imps...)
			if body != "" {
				bodies = append(bodies, goBody{src: body, pos: gc.Pos() + token.Pos(bodyOff)})
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
		if err := emitComponentSkeleton(&compBuf, c, table, propFields, nodeProps, byo, fm, usedFilters, fset); err != nil {
			if errors.Is(err, errSkipComponent) {
				// Validation failure: skip this component's skeleton; it will fail
				// again (with a positioned diagnostic) during generateFile.
				continue
			}
			return "", nil, nil, err
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
		// Map go/types import errors back to the .gsx source. The skeleton spec
		// starts at column 8 (after "import "), so compensate the //line column by
		// that prefix; when the source column is < 8 (the common indented-import
		// case) the compensated column would be < 1, so fall back to a line-only
		// directive (column 1) rather than emit a misleading offset.
		emitSkeletonLineImport(&sb, fset, imp.pos)
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
	// Component skeletons first, then the user's raw-Go bodies. Bodies follow the
	// components (Go permits forward references between top-level decls, so a probe
	// like `_gsxuse(helper())` still resolves) so each body's //line directive
	// stays scoped to that body — it cannot bleed into a component skeleton's
	// unmapped props-struct / signature lines and shift their overlay positions.
	sb.WriteString(compBuf.String())
	for _, b := range bodies {
		emitSkeletonLine(&sb, fset, b.pos)
		sb.WriteString(b.src)
		sb.WriteByte('\n')
	}
	return sb.String(), comps, imports, nil
}

// goBody is a GoChunk's non-import remainder paired with the .gsx source
// position of its first byte (for the //line directive that maps it back).
type goBody struct {
	src string
	pos token.Pos
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
func emitComponentSkeleton(sb *strings.Builder, c *gsxast.Component, table filterTable, propFields, nodeProps map[string]map[string]bool, byo *byoData, fm FieldMatcher, usedFilters map[string]string, fset *token.FileSet) error {
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
	// BYO (author-owns-Props): the sole non-receiver param is an author-declared
	// struct used DIRECTLY — gsx generates NO props struct. The skeleton emits the
	// real param (name + type verbatim from the .gsx) so the author's `p.Field`
	// references type-check against the real struct (declared in a GoChunk body or
	// external .go), then probes the body. No params/children/attrs magic applies
	// (the author accesses p.Children / p.Attrs explicitly). MIRRORS the byo branch
	// in genComponent so emit ≡ probe.
	if structName, isByo := byo.structTypeName(componentKey(c)); isByo {
		// The skeleton names the props param `_gsxp` (reserved, so it can never
		// collide with a user ident) and binds the author's real param to it via a
		// //line'd local — MIRRORING the generated path's param binding. The local's
		// //line maps go-to-definition on a `p.Field` reference back to the param's
		// .gsx declaration (instead of this synthesized binding or the overlay). The
		// EMITTED signature (genComponent) keeps the real param name verbatim; the
		// skeleton differs only in this reserved-name shape (harvest keys on the func
		// name/recv, not the signature), so resolution + LSP stay correct.
		if c.Recv != "" {
			fmt.Fprintf(sb, "func %s %s(_gsxp %s) _gsxrt.Node {\n", c.Recv, c.Name, structName)
		} else {
			fmt.Fprintf(sb, "func %s(_gsxp %s) _gsxrt.Node {\n", c.Name, structName)
		}
		sb.WriteString("\tvar ctx _gsxctx.Context\n\t_ = ctx\n")
		if c.ParamsPos.IsValid() {
			emitSkeletonLineParam(sb, fset, c.ParamsPos+token.Pos(params[0].nameOff))
		}
		fmt.Fprintf(sb, "\t%s := _gsxp\n\t_ = %s\n", params[0].name, params[0].name)
		// Reset the //line so the probe body's own positions are not shifted by the
		// param binding's mapping.
		emitSkeletonLine(sb, fset, c.Pos())
		if err := emitProbes(sb, c.Body, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
			return err
		}
		sb.WriteString("\treturn nil\n}\n")
		return nil
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
	// MIRROR emit.go: a nullary component (no params, no children) stays nullary
	// (no props struct, bare call) — AUTO fallthrough is gated out of that case so
	// it does not force a props struct; manual forces it. This applies to BOTH
	// function and method components (unifying the no-props path).
	hasFallthrough := (hasRoot && (len(params) > 0 || hasChildren)) || manual
	// A component has a props struct iff it has at least one field (params,
	// Children, or Attrs via fallthrough). A nullary function or method with no
	// fallthrough and no children generates no props struct (bare call).
	hasProps := len(params) > 0 || hasChildren || hasFallthrough
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
			// //line so go-to-definition on a param reference resolves back to the
			// param's .gsx declaration (the line+col of its name in the component
			// signature) instead of this synthesized binding.
			if c.ParamsPos.IsValid() {
				emitSkeletonLineParam(sb, fset, c.ParamsPos+token.Pos(p.nameOff))
			}
			fmt.Fprintf(sb, "\t%s := _gsxp.%s\n\t_ = %s\n", p.name, fieldName(p.name), p.name)
		}
	}
	if hasChildren || manual {
		// Reset the //line so the children/attrs bindings (which are synthesized,
		// not user-declared) don't inherit the last param's source mapping; point
		// them at the component declaration.
		emitSkeletonLine(sb, fset, c.Pos())
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
	if err := emitProbes(sb, c.Body, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
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
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup, table filterTable, propFields, nodeProps map[string]map[string]bool, byo *byoData, fm FieldMatcher, recvVar, recvTypeName string, usedFilters map[string]string, fset *token.FileSet) error {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			probe, err := probeExpr(t.Expr, t.Stages, table, usedFilters)
			if err != nil {
				return err
			}
			const probePrefixLen = len("_gsxuse(") // 8
			if len(t.Stages) == 0 && t.ExprPos.IsValid() {
				ep := fset.Position(t.ExprPos)
				if col := ep.Column - probePrefixLen; col >= 1 {
					// Compensated //line: the probe's first token (at byte offset 8 into
					// "_gsxuse(expr)") will be reported at ep.Column, matching the source.
					fmt.Fprintf(sb, "//line %s:%d:%d\n", ep.Filename, ep.Line, col)
				} else {
					// Expr is too near the line start for //line compensation (col < 1 is
					// invalid). Fall back to the '{'-anchored position — identical to the
					// pre-column-accuracy behavior — so shallow interps are never worse than
					// the base. Making shallow interps exact would need a post-type-check
					// column override keyed to the originating Interp — deferred.
					// TODO(column-accuracy): exact columns for shallow interps (exprCol ≤ 8).
					emitSkeletonLine(sb, fset, t.Pos())
				}
			} else {
				// Staged pipeline or no ExprPos: keep unchanged behavior ('{' pos).
				emitSkeletonLine(sb, fset, t.Pos())
			}
			fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				// Emit the SAME call as genChildComponent (via childInvocation) so the
				// assignment type-checks each prop expr against the child's/method's real
				// field type. The shared childPropsLiteral builder guarantees the attr +
				// slot fields never drift. A nullary invocation (method OR no-props
				// function; no attrs, no children) has no props struct → `_ = X()`.
				// Otherwise build the
				// props literal; named-slot/Children fields use a typed-nil so the
				// literal type-checks WITHOUT building the real slot closure here —
				// the slot content (markup-attr values then t.Children) is probed
				// SEPARATELY below (its interps become _gsxuse in the SAME order
				// collectExprs collected them; the props-literal exprs are NOT _gsxuse,
				// so they don't perturb the k-th alignment).
				callTarget, propsType, isMethod := childInvocation(t, byo, recvVar, recvTypeName)
				_, isByoChild := byo.isByoStruct(propsType)
				if isBareCallCandidate(t, propFields, byo, recvVar, recvTypeName) {
					// A bare-call candidate (a hand-written same-package func, or a .gsx
					// no-props component): resolve it by its REAL signature rather than the
					// XxxProps convention, because a nullary `func F() gsx.Node` has no
					// props struct and the `F(FProps{})` probe would not compile.
					// _gsxcompsig(F) type-checks at any arity; harvest reads the signature
					// back into `resolved[el]` so genChildComponent picks the call shape.
					emitSkeletonLine(sb, fset, t.Pos())
					fmt.Fprintf(sb, "_gsxcompsig(%s)\n", callTarget)
				} else if ((isMethod && !isByoChild) || isNoPropsComponent(propFields, propsType)) && len(t.Attrs) == 0 && len(t.Children) == 0 {
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
					// When splatExpr is non-empty (byo whole-struct splat), emit
					// `_ = callTarget(splatExpr)` mirroring the emitter exactly.
					fields, splatExpr, usedPkgs, err := childPropsLiteral(t, propsType, "_gsxrt", table, propFields, nodeProps[propsType], byo, fm, func(nodes []gsxast.Markup) (string, error) {
						return "_gsxrt.Node(nil)", nil
					})
					if err != nil {
						// childPropsLiteral returns an *attrError with the offending attr's
						// position embedded. Propagate it as-is so the batch.go sink can emit
						// a positioned diagnostic (not positionless).
						return err
					}
					// Record filter packages referenced by a lowered prop/fallthrough
					// pipeline so the skeleton imports them under their reserved aliases
					// — the SAME set the emitter records into its imports map. Without
					// this the skeleton would not import _gsxstdN and a prop pipeline
					// would fail to resolve.
					for alias, path := range usedPkgs {
						usedFilters[alias] = path
					}
					emitSkeletonLine(sb, fset, t.Pos())
					if splatExpr != "" {
						// Whole-struct splat: mirrors genChildComponent exactly.
						fmt.Fprintf(sb, "_ = %s(%s)\n", callTarget, splatExpr)
					} else {
						fmt.Fprintf(sb, "_ = %s(%s{%s})\n", callTarget, propsType, fields)
					}
				}
				// Probe slot content in the SAME canonical order collectExprs walks:
				// each markup-attr value (attr order) then the children.
				var probeErr error
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset)
				})
				if probeErr != nil {
					return probeErr
				}
				if err := emitProbes(sb, t.Children, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
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
				// ClassAttr/SpreadAttr part exprs are emitted verbatim by codegen (no
				// type harvest), so a var used ONLY in `class={ "on": v }` / `{...attrs}`
				// must still be referenced here or it's "declared and not used". Emit a
				// liveness `_ = (expr)` — NOT _gsxuse, so the harvest alignment is intact.
				walkLivenessAttrExprs(t.Attrs, table, usedFilters, func(expr string) {
					fmt.Fprintf(sb, "_ = (%s)\n", expr)
				})
				// Then probe each JS-attribute's @{ } interps, in attr source order —
				// collectExprs walks identically (same walkMarkupAttrs), so the k-th
				// _gsxuse maps to the k-th collected node.
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset)
				})
				if probeErr != nil {
					return probeErr
				}
				if err := emitProbes(sb, t.Children, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
					return err
				}
			}
		case *gsxast.Fragment:
			if err := emitProbes(sb, t.Children, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
				return err
			}
		case *gsxast.ForMarkup:
			fmt.Fprintf(sb, "for %s {\n", t.Clause)
			if err := emitProbes(sb, t.Body, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
				return err
			}
			sb.WriteString("}\n")
		case *gsxast.IfMarkup:
			fmt.Fprintf(sb, "if %s {\n", t.Cond)
			if err := emitProbes(sb, t.Then, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
				return err
			}
			sb.WriteString("}")
			if t.Else != nil {
				sb.WriteString(" else {\n")
				if err := emitProbes(sb, t.Else, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
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
				if err := emitProbes(sb, cc.Body, table, propFields, nodeProps, byo, fm, recvVar, recvTypeName, usedFilters, fset); err != nil {
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
//
// Column accuracy: interpolation expression columns are now exact via
// Interp.ExprPos + compensated //line (col = exprCol - len("_gsxuse(")).
// Component-prop and pipeline-staged probes remain coarse: synthesized
// props-literal fields and rewritten pipeline expressions have no faithful
// source column, so they still emit //line at the node's Pos().
func emitSkeletonLine(sb *strings.Builder, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, p.Column)
}

// emitSkeletonLineParam emits a //line for a param binding (`\t<name> := …`). The
// binding is indented one tab, so the name sits at skeleton column 2 and would
// map one column past the param's .gsx position; point the directive one column
// left to compensate so go-to-definition lands exactly on the param name. The
// column is adjusted (not the byte position), so the line stays correct even for
// a multi-line param list.
func emitSkeletonLineParam(sb *strings.Builder, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	col := p.Column - 1
	if col < 1 {
		col = 1
	}
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}

// emitSkeletonLineImport emits a //line directive ahead of a hoisted user
// import so go/types import errors (notably "imported and not used") resolve to
// the .gsx source instead of the synthesized overlay .x.go. The skeleton spec
// sits at column 8 (after the literal "import "), so the directive column is
// compensated by that 7-char prefix; when the source column is ≤ 7 (the common
// indented-import case) the compensated column would be < 1, so a line-only
// directive (column 1) is emitted rather than a misleading offset.
func emitSkeletonLineImport(sb *strings.Builder, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	const prefixLen = len("import ")
	p := fset.Position(pos)
	col := p.Column - prefixLen
	if col < 1 {
		col = 1
	}
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}

// probeExpr returns the Go expression to probe for an interpolation / expr-attr.
// Without stages it is the trimmed seed; with stages it is the lowered pipeline
// (the SAME lowerPipe output the emitter uses), so the harvested type is the
// pipeline's RESULT type and resolution stays aligned with emission. The
// pipeline's used filter packages are merged into usedFilters so the skeleton
// imports each referenced package under its reserved alias.
// An unknown filter is TOLERATED here: the probe falls back to the bare seed so
// the skeleton still type-checks and generation proceeds to the POSITIONED
// unknown-filter diagnostic emit reports (the probe's bare error must not pre-empt
// the positioned bag.Errorf in generateFile). A valid pipeline lowers exactly as
// emit does, keeping emit ≡ probe.
func probeExpr(seed string, stages []gsxast.PipeStage, table filterTable, usedFilters map[string]string) (string, error) {
	if len(stages) == 0 {
		return strings.TrimSpace(seed), nil
	}
	lowered, used, err := lowerPipe(seed, stages, table)
	if err != nil {
		return strings.TrimSpace(seed), nil
	}
	for alias, path := range used {
		usedFilters[alias] = path
	}
	return lowered, nil
}

// harvest reads each interpolation's resolved type from a type-checked skeleton
// file. An interpolation probe is now an ExprStmt whose call target is the
// identifier `_gsxuse`; harvest the single argument's type.
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr) {
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
				if exprOut != nil {
					exprOut[nodes[k]] = call.Args[0]
				}
				k++
			}
			return true
		})

		// Resolve hand-written same-package component tags by their REAL signature:
		// each `_gsxcompsig(F)` probe carries F's type. Map it back (by tag name — a
		// tag resolves to one func per package, so no k-ordering is needed) onto
		// every <F/> element in the body, so genChildComponent branches on arity.
		sigByName := map[string]types.Type{}
		goast.Inspect(fd.Body, func(node goast.Node) bool {
			call, ok := node.(*goast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || id.Name != "_gsxcompsig" || len(call.Args) != 1 {
				return true
			}
			arg, ok := call.Args[0].(*goast.Ident)
			if !ok {
				return true
			}
			if tv, ok := info.Types[arg]; ok && tv.Type != nil {
				sigByName[arg.Name] = tv.Type
			}
			return true
		})
		if len(sigByName) > 0 {
			forEachComponentTagElement(c.Body, func(el *gsxast.Element) {
				if t, ok := sigByName[el.Tag]; ok {
					out[el] = t
				}
			})
		}
	}
}

// forEachComponentTagElement invokes fn for every component-tag *Element in a
// markup tree (recursing through children, named-slot markup-attr values, and
// control-flow bodies), so harvest can attach a resolved signature to each
// invocation site. Mirrors collectExprs's recursion structure.
func forEachComponentTagElement(nodes []gsxast.Markup, fn func(*gsxast.Element)) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				fn(t)
			}
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				forEachComponentTagElement(value, fn)
			})
			forEachComponentTagElement(t.Children, fn)
		case *gsxast.Fragment:
			forEachComponentTagElement(t.Children, fn)
		case *gsxast.ForMarkup:
			forEachComponentTagElement(t.Body, fn)
		case *gsxast.IfMarkup:
			forEachComponentTagElement(t.Then, fn)
			forEachComponentTagElement(t.Else, fn)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				forEachComponentTagElement(cc.Body, fn)
			}
		}
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

// walkLivenessAttrExprs invokes fn for each Go expression in a ClassAttr/SpreadAttr
// (recursing CondAttr) — the attr exprs that walkAttrExprs does NOT yield. Codegen
// emits these verbatim (gsx.Class/ClassIf/StyleString, gw.Spread) so they need no
// type harvest, but the skeleton must still REFERENCE them or a var used ONLY here
// (e.g. a for-loop var in `class={ "on": v }`) is rejected as "declared and not
// used". emitProbes emits `_ = (expr)` for each — a liveness reference that, unlike
// _gsxuse, is invisible to the k-th-probe→k-th-node type-harvest alignment.
// A ClassPart/SpreadAttr carrying a `|>` pipeline must reference the LOWERED
// expression — the SAME lowerPipe output emit produces — so type resolution and
// import harvest match emit exactly (emit ≡ probe). table lowers each pipeline and
// usedFilters accumulates the referenced filter packages (alias→pkgPath) so the
// skeleton imports them under the same reserved aliases the emitter records. A
// lowering failure (an unknown filter) is tolerated here: the probe falls back to
// referencing the bare seed so type-checking proceeds to the POSITIONED
// unknown-filter diagnostic generateFile reports (the probe's bare error must not
// pre-empt it). The guard Cond is never piped, so it is referenced verbatim.
func walkLivenessAttrExprs(attrs []gsxast.Attr, table filterTable, usedFilters map[string]string, fn func(expr string)) {
	emit := func(seed string, stages []gsxast.PipeStage) {
		if strings.TrimSpace(seed) == "" {
			return
		}
		if len(stages) == 0 {
			fn(strings.TrimSpace(seed))
			return
		}
		lowered, used, err := lowerPipe(seed, stages, table)
		if err != nil {
			// Unknown filter: reference the bare seed so the skeleton still type-checks
			// (and stays "used"); the positioned diagnostic fires in generateFile.
			fn(strings.TrimSpace(seed))
			return
		}
		for alias, path := range used {
			usedFilters[alias] = path
		}
		fn(lowered)
	}
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ClassAttr:
			for _, p := range at.Parts {
				emit(p.Expr, p.Stages)
				if c := strings.TrimSpace(p.Cond); c != "" {
					fn(c)
				}
			}
		case *gsxast.SpreadAttr:
			emit(at.Expr, at.Stages)
		case *gsxast.CondAttr:
			walkLivenessAttrExprs(at.Then, table, usedFilters, fn)
			walkLivenessAttrExprs(at.Else, table, usedFilters, fn)
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
				// Every verbatim-emitted attr fragment childPropsLiteral places into the
				// props literal — prop/fallthrough *ExprAttr exprs (+ pipeline stage
				// args), composable-class part Expr/Cond, spread Expr, conditional-attr
				// Cond and its branch attrs — references parent locals and must be bound.
				// collectAttrSrc walks exactly that set (and recurses CondAttr branches).
				collectAttrSrc(t.Attrs, add)
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
				for _, st := range p.Stages {
					if st.Args != "" {
						add(st.Args)
					}
				}
				if p.Cond != "" {
					add(p.Cond)
				}
			}
		case *gsxast.SpreadAttr:
			add(at.Expr)
			for _, st := range at.Stages {
				if st.Args != "" {
					add(st.Args)
				}
			}
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

// param is one component parameter. nameOff is the byte offset of the param's
// name within the (trimmed) Params source string — added to Component.ParamsPos
// it yields the param name's .gsx position (for go-to-definition).
type param struct {
	name, typ string
	nameOff   int
}

// paramSynthPrefix is the synthetic source prepended in parseParams; the param
// list begins immediately after it, so a name's offset within the param source
// is its offset in the synthetic file minus this length.
const paramSynthPrefix = "package p\nfunc _("

// parseParams parses an inline param list ("name string, user User") into
// (name, Go-type, name-offset) tuples by reusing go/parser on a synthesized
// function.
func parseParams(src string) ([]param, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", paramSynthPrefix+src+") {}", 0)
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
			out = append(out, param{
				name:    nm.Name,
				typ:     typ,
				nameOff: fset.Position(nm.Pos()).Offset - len(paramSynthPrefix),
			})
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
	name   string    // "" for the default import name
	path   string    // import path, unquoted
	srcOff int       // byte offset of the spec's start within the chunk src
	pos    token.Pos // resolved .gsx position of the spec (set by buildSkeleton)
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
//
// bodyOff is the byte offset WITHIN src of the body's first character, so a
// caller holding the chunk's source position can emit a //line directive that
// maps the body back to its .gsx origin. Valid Go requires every import to
// precede all other declarations, so the body is the single contiguous span
// after the last import (any comments before/between imports carry no symbols
// and are dropped); a single //line anchor therefore maps the whole body.
func splitChunk(src string) (imports []importSpec, body string, bodyOff int, err error) {
	const prefix = "package _gsxp\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+src, parser.ParseComments)
	if err != nil {
		return nil, "", 0, fmt.Errorf("codegen: invalid Go in pass-through block: %w", err)
	}
	const shift = len(prefix)
	lastImportEnd := 0
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
			imports = append(imports, importSpec{
				name:   name,
				path:   path,
				srcOff: fset.Position(is.Pos()).Offset - shift,
			})
		}
		if end := fset.Position(gd.End()).Offset - shift; end > lastImportEnd {
			lastImportEnd = end
		}
	}
	tail := src[lastImportEnd:]
	left := strings.TrimLeft(tail, " \t\r\n")
	bodyOff = lastImportEnd + (len(tail) - len(left))
	body = strings.TrimRight(left, " \t\r\n")
	return imports, body, bodyOff, nil
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
