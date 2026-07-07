package codegen

import (
	"bytes"
	"errors"
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"go/types"
	"maps"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/exp/typeparams"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// errSkipComponent is a sentinel returned by emitComponentSkeleton when the
// component fails an early validation check (reserved param/recv, parse error)
// that will also be caught — with a positioned diagnostic — in genComponent at
// emit time. The caller (buildSkeleton) skips this component's skeleton and
// continues; the overall skeleton remains valid Go. Infrastructure errors
// (e.g. unknown filter in emitProbes) are NOT wrapped in this sentinel and
// propagate as fatal errors.
var errSkipComponent = errors.New("skip")

// componentPropFieldsFor builds the call-site split's prop-field map purely from
// the parsed component ASTs — SAME-PACKAGE only, available BEFORE type resolution.
// It is keyed by props-struct TYPE NAME exactly as childInvocation produces it
// (bare <Name>Props for a function component, <RecvType><Name>Props for a method),
// with value the set of field NAMES the skeleton/emitter synthesize for that
// component:
//
//	propFields(c) = { fieldName(param) : param ∈ c.Params }
//	             ∪ { "Children" if usesChildren(c.Body) }
//	             ∪ { "Attrs"    if usesAttrs(c.Body) }
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
// .go struct is enumerated the same way — by syntactically parsing the sibling
// .go files (loadExternalStructFields), NOT a go/packages type-load. byo is
// nil-safe and always returned non-nil.
func componentPropFieldsFor(dir string, files map[string]*gsxast.File) (propFields, nodeProps, attrsProps map[string]map[string]bool, byo *byoData, err error) {
	out := map[string]map[string]bool{}
	nodeOut := map[string]map[string]bool{}
	// attrsOut[propsType] is the set of field names whose declared type is exactly
	// gsx.Attrs (the ordered bag slice). The skeleton uses this classification to
	// enforce that values bound to bag fields are themselves gsx.Attrs.
	attrsOut := map[string]map[string]bool{}
	byo = newByoData()
	byo.nullaryFuncs = packageNullaryFuncs(dir)

	// Discover author structs: those declared in .gsx GoChunks are read from the
	// AST now; any candidate struct NOT found in the .gsx is enumerated via a
	// preliminary external (.go) type-load below.
	gsxStructs := gsxStructDecls(files)
	externalWanted := map[string]bool{}

	// genProps derives the GENERATED-path prop-field map + node-field map +
	// attrs-field map for a component (the historical AST-derived behavior),
	// keyed by propsName/compKey.
	genProps := func(c *gsxast.Component, params []param, propsName string) {
		fields := map[string]bool{}
		nodeFields := map[string]bool{}
		attrsFields := map[string]bool{}
		for _, p := range params {
			fields[fieldName(p.name)] = true
			if isGsxNodeType(p.typ) {
				nodeFields[fieldName(p.name)] = true
			}
			if isGsxAttrsType(p.typ) {
				attrsFields[fieldName(p.name)] = true
			}
		}
		hasChildren := usesChildren(c.Body)
		if hasChildren {
			fields["Children"] = true
		}
		manual := usesAttrs(c.Body)
		if manual {
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
		attrsOut[propsName] = attrsFields
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
				return nil, nil, nil, nil, err
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

	// Interop-splat enumeration: a capitalized tag that carries a whole-struct
	// splat `{ x… }`, is NOT a gsx component, and is NOT byo resolves via the
	// XxxProps convention to a hand-written (templ-interop / same-package)
	// `<Tag>Props` struct. Enumerate ONLY those structs so childPropsLiteral learns
	// whether the struct has an `Attrs gsx.Attrs` bag — the fact that disambiguates
	// the spread between an attrs-merge (has bag) and a whole-struct splat (no bag,
	// so `{ x… }` is the prop value). Scoping to splat-bearing tags keeps every
	// ordinary interop call on the unchanged graceful cross-package path: only a
	// tag the author actually splatted — which today generates a broken
	// `Props{Attrs: …}` against a bag-less struct — is enumerated here.
	gsxCompFuncs := map[string]bool{}
	for _, file := range files {
		for _, d := range file.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" {
				gsxCompFuncs[c.Name] = true
			}
		}
	}
	interopWanted := map[string]bool{}
	for _, file := range files {
		for _, d := range file.Decls {
			c, ok := d.(*gsxast.Component)
			if !ok {
				continue
			}
			forEachComponentTagElement(c.Body, func(el *gsxast.Element) {
				if !elementHasSpread(el.Attrs) {
					return // only splat-bearing tags need the bag/no-bag fact
				}
				if strings.Contains(el.Tag, ".") || gsxCompFuncs[el.Tag] {
					return // dotted (cross-pkg/method) or a gsx component — not interop
				}
				propsName := el.Tag + "Props"
				if _, known := out[propsName]; known {
					return // already gsx-declared / byo / generated / enumerated
				}
				interopWanted[propsName] = true
			})
		}
	}
	if dir != "" && len(interopWanted) > 0 {
		ef, enf, es := loadExternalStructFields(dir, interopWanted)
		for name, f := range ef {
			// Publish the field set (so isKnownPropsType is true and hasAttrsBag can
			// read the "Attrs" member) WITHOUT registering the struct as byo: an
			// interop component keeps the convention call shape and the graceful
			// attr-fallthrough rules, unlike an author-owns-Props byo component.
			out[name] = f
			nodeOut[name] = enf[name]
			if es[name].hasAttrs {
				attrsOut[name] = map[string]bool{"Attrs": true}
			}
		}
	}
	return out, nodeOut, attrsOut, byo, nil
}

// elementHasSpread reports whether an element's attrs include a whole-struct /
// attrs spread `{ x… }` (recursing conditional-attr groups, like walkSpreadAttrs).
func elementHasSpread(attrs []gsxast.Attr) bool {
	found := false
	walkSpreadAttrs(attrs, func(*gsxast.SpreadAttr) { found = true })
	return found
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

// buildSkeleton synthesizes a Go file standing in for the gsx file during type
// resolution: the file's GoChunks, plus each component's real props struct and
// func signature, with a probe body (used-param locals, each interpolation as
// `_gsxuse(expr)`, each child component as `_ = Child(ChildProps{})`).
//
// names is the PACKAGE-WIDE inference-probe-helper name allocator (see
// inferNameAllocator's doc): module_importer.go's analyze constructs ONE
// allocator per package and passes the SAME one to every sibling file's
// buildSkeleton call, so two files that each caller-side-infer against a
// shared component never both mint the literal helper name "_gsxinfer1" —
// every sibling skeleton is type-checked together as one package, and a
// name collision there is a hard `redeclared in this block` failure for the
// whole package. A nil names (some buildSkeleton callers, e.g. unit tests
// exercising a single file's skeleton in isolation) gets a private,
// single-file allocator instead — this file's own names still start at
// "_gsxinfer1" and never collide with anything, since nothing else shares
// that private allocator.
func buildSkeleton(file *gsxast.File, table filterTable, propFields, nodeProps, attrsProps map[string]map[string]bool, genericSigs map[string]*genericSig, importedGenericSigs map[string]*genericSig, byo *byoData, fm FieldMatcher, fset *token.FileSet, bag *diag.Bag, names *inferNameAllocator) (string, []*gsxast.Component, []importSpec, map[gsxast.Node]int, *inferRegistry, [][]gsxast.Markup, error) {
	if names == nil {
		names = newInferNameAllocator()
	}
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
				return "", nil, nil, nil, nil, nil, err
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
	// ctrlOff maps each control-flow node (ForMarkup/IfMarkup/GoBlock, and each
	// value-form if condition's *ValueIf) to the
	// byte offset of its clause/cond/code text within the final skeleton string.
	// Offsets are recorded relative to compBuf during emitComponentSkeleton and
	// adjusted below (after the prefix is assembled into sb) to be file-relative.
	ctrlOff := map[gsxast.Node]int{}
	// genericSigs maps a generic component's props-type name to its
	// genericSig (typeParams/params/imports), so emitProbes can build a
	// caller-side inference probe (emitInferProbe) at each tag that omits
	// its type args — for BOTH a SAME-PACKAGE component (genericSigs,
	// computed PACKAGE-WIDE by the caller from every .gsx file's components,
	// so a tag in one file can infer against a component declared in a
	// sibling file — see module_importer.go's analyze) and an IMPORTED one
	// (importedGenericSigs, this FILE's own view — see fileScopedFacts, since
	// import aliases are file-scoped). Merge into one combined map without
	// mutating the caller's package-wide genericSigs (shared across every
	// file's buildSkeleton call).
	combinedSigs := make(map[string]*genericSig, len(genericSigs)+len(importedGenericSigs))
	maps.Copy(combinedSigs, genericSigs)
	maps.Copy(combinedSigs, importedGenericSigs)
	registry := newInferRegistry(names)
	// Keep only the components whose skeletons succeed. A validation error
	// (errSkipComponent — reserved param/recv, parse failure) means the component
	// is invalid for codegen; skip its skeleton so the overall file stays valid Go.
	// genComponent will re-encounter the same error at emit time and record a
	// positioned diagnostic via the bag. Any OTHER error is a real infrastructure
	// failure and must abort the whole skeleton build.
	var validComps []*gsxast.Component
	for _, c := range comps {
		if err := emitComponentSkeleton(&compBuf, c, table, propFields, nodeProps, attrsProps, combinedSigs, byo, fm, usedFilters, fset, ctrlOff, registry, bag); err != nil {
			if errors.Is(err, errSkipComponent) {
				// Validation failure: skip this component's skeleton; it will fail
				// again (with a positioned diagnostic) during generateFile.
				continue
			}
			return "", nil, nil, nil, nil, nil, err
		}
		validComps = append(validComps, c)
	}
	comps = validComps

	// GoWithElements: a top-level Go region with one or more gsx elements
	// embedded directly in expression position (e.g. `var help = <a
	// href={u}>{ label }</a>`, or `func H(label string) gsx.Node { return
	// <div>{ label }</div> }`) — see ast.GoWithElements's doc and emit.go's
	// *ast.GoWithElements case (Task 4). Mirror emitElementValue's lowering on
	// the probe side, INLINE: the region's Go text is spliced verbatim and
	// each *Element Part is replaced, IN PLACE, by an immediately-invoked
	// func literal (IIFE) of type _gsxrt.Node:
	//
	//	func() _gsxrt.Node { _gsxelem(N); var ctx _gsxctx.Context; _ = ctx; <probes>; return nil }()
	//
	// so the surrounding Go construct (a var initializer, a return statement,
	// a call argument, …) still sees a real _gsxrt.Node-typed expression
	// there and type-checks. The IIFE's OWN body probes the element's
	// interpolations/props via emitProbes — the EXACT same machinery a
	// component body's child elements use — so a wrong-typed interpolation or
	// component-tag prop is caught exactly as inside a component.
	//
	// INLINE (not a hoisted top-level func) is load-bearing for emit≡probe:
	// an embedded interpolation can reference the SURROUNDING lexical scope —
	// a func parameter, a local variable, or a method receiver (`func H(label
	// string) … { return <div>{ label }</div> }`). A separate top-level probe
	// func would NOT see those names → a false `undefined: label` that blocks
	// generation. Because the IIFE is spliced inline inside the region's
	// (verbatim-spliced) surrounding Go, it captures params/locals/receiver by
	// ordinary Go closure capture, exactly as emit.go's real gsx.Func closure
	// does. The inner `var ctx _gsxctx.Context` mirrors the real closure's
	// `ctx context.Context` param (so an interp referencing `ctx` type-checks,
	// and — like the real closure — shadows any outer `ctx`); the OUTER scope
	// remains visible for every other name.
	//
	// Everything (verbatim Go + inline IIFEs) is written into compBuf (the
	// SAME temp builder emitComponentSkeleton just filled), so the single
	// compBufStart adjustment below (already needed for component skeletons)
	// also re-bases every ctrlOff/registry.spans entry emitProbes records
	// while probing an embedded element — no separate adjustment pass needed.
	// Each verbatim GoText Part is preceded by a `/*line*/` BLOCK-form
	// directive to its .gsx position (so a type error in the surrounding Go
	// maps back to source, and the synthetic IIFE lines injected before it
	// don't shift the mapping). The block form — not `//line` — is required:
	// an element can sit mid-expression (`Wrap(<Foo/>)`), where the trailing
	// GoText (`)`) must attach to the IIFE's `}()` on the SAME line; a
	// `//line` newline there would trigger Go's automatic semicolon insertion
	// after `}()` ("missing ',' before newline in argument list"). The block
	// comment carries no newline, so it re-syncs the position without breaking
	// the enclosing call/operand syntax.
	//
	// gwMarkups collects, in source order, the markup list each embedded
	// value's IIFE probes: one *gsxast.Element becomes a single-element
	// []Markup{p}; a *gsxast.Fragment contributes its whole Children list (a
	// fragment probes as one IIFE over all its children, so interps inside it
	// resolve against the enclosing scope exactly like an element's do).
	// Index N is the argument the corresponding IIFE's `_gsxelem(N)` marker
	// carries. The caller hands this slice to harvestEmbeddedElements, which
	// finds each marked IIFE in the type-checked skeleton and harvests its
	// _gsxuse/_gsxuseq/_gsxcompsig/inference-probe results back onto the
	// markup's nodes (via the shared harvestBody). The marker is what lets
	// harvest distinguish an embedded-value probe IIFE from any func literal
	// the user wrote verbatim in the surrounding Go.
	//
	// Unlike GoChunk, this does NOT run splitChunk to hoist an `import` spec out
	// of a GoText Part ahead of the skeleton's own import block — mirrors emit.go's
	// identical choice (see its *ast.GoWithElements case comment): splitChunk
	// cannot parse text containing markup. It does not need to: the parser peels a
	// leading run of import declarations off this region into its own GoChunk
	// before the region becomes a GoWithElements (parser/goexpr.go
	// leadingImportEnd), so a func returning an element whose return type needs a
	// user-imported package is hoisted through the normal GoChunk path. Only a
	// stray `import` placed AFTER a non-import declaration in the region can reach
	// here — that is invalid Go, and surfaces as a skeleton parse/type error,
	// never silently-broken output.
	var gwMarkups [][]gsxast.Markup
	for _, d := range file.Decls {
		we, ok := d.(*gsxast.GoWithElements)
		if !ok {
			continue
		}
		for _, part := range we.Parts {
			switch p := part.(type) {
			case gsxast.GoText:
				// Block-form directive (no newline) so an element mid-expression
				// (`Wrap(<Foo/>)`) keeps its trailing `)` attached to the IIFE's
				// `}()` — a `//line` newline there would trip ASI.
				emitSkeletonBlockLine(&compBuf, fset, p.Pos())
				compBuf.WriteString(p.Src)
			case *gsxast.Element:
				markup := []gsxast.Markup{p}
				compBuf.WriteString("func() _gsxrt.Node {\n")
				fmt.Fprintf(&compBuf, "_gsxelem(%d)\n", len(gwMarkups))
				compBuf.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				if err := emitProbes(&compBuf, markup, table, propFields, nodeProps, attrsProps, combinedSigs, byo, fm, "", "", usedFilters, fset, ctrlOff, registry, bag); err != nil {
					return "", nil, nil, nil, nil, nil, err
				}
				compBuf.WriteString("return nil\n}()")
				gwMarkups = append(gwMarkups, markup)
			case *gsxast.Fragment:
				// A fragment probes its children list as one IIFE (empty <></> →
				// no probes, still a valid _gsxrt.Node-returning IIFE — the nop).
				compBuf.WriteString("func() _gsxrt.Node {\n")
				fmt.Fprintf(&compBuf, "_gsxelem(%d)\n", len(gwMarkups))
				compBuf.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				if err := emitProbes(&compBuf, p.Children, table, propFields, nodeProps, attrsProps, combinedSigs, byo, fm, "", "", usedFilters, fset, ctrlOff, registry, bag); err != nil {
					return "", nil, nil, nil, nil, nil, err
				}
				compBuf.WriteString("return nil\n}()")
				gwMarkups = append(gwMarkups, p.Children)
			default:
				return "", nil, nil, nil, nil, nil, fmt.Errorf("codegen: unsupported Go-expression part %T", part)
			}
		}
		compBuf.WriteString("\n")
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
	// Import each USED filter package under its reserved alias. std keeps its
	// _gsxstd alias and dedicated `import _gsxstd "<std>"` line (in alias order),
	// so std-only skeletons stay byte-identical to before.
	for _, alias := range sortedFilterAliases(usedFilters) {
		fmt.Fprintf(&sb, "import %s %q\n", alias, usedFilters[alias])
	}
	// Import every dependency package a requalified cross-package generic
	// probe (Task 4) had to re-import under a fresh "_gsxtiN" alias — e.g. a
	// constraint like `T fmt.Stringer` on an IMPORTED component. Coalesced by
	// registry.alloc (one line per distinct path; see inferRegistry.alloc's
	// doc), in first-seen (deterministic) order.
	for _, imp := range registry.importAssembly() {
		fmt.Fprintf(&sb, "import %s %q\n", imp.name, imp.path)
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
	//
	// Adjust ctrlOff from compBuf-relative to skeleton-file-relative: each recorded
	// offset was relative to compBuf (the temp builder used by emitComponentSkeleton);
	// add the prefix length (everything written into sb before compBuf) so ctrlOff
	// values index into the final skeleton string returned by buildSkeleton.
	compBufStart := sb.Len()
	sb.WriteString(compBuf.String())
	for k, v := range ctrlOff {
		ctrlOff[k] = v + compBufStart
	}
	// Same adjustment for every recorded probe's span (compBuf-relative to
	// file-relative) — the call/probe statement was written directly into
	// compBuf by emitInferProbe or recordProbeSpan's callers, at whatever
	// inline position the tag's probe occupied. Ranges over registry.spans
	// (not registry.sites): spans is the superset — every sites value PLUS the
	// unnamed probe shapes recordProbeSpan records — so every recorded span is
	// adjusted exactly once regardless of which path recorded it.
	for _, s := range registry.spans {
		s.span.start += compBufStart
		s.span.end += compBufStart
		for i := range s.args {
			s.args[i].span.start += compBufStart
			s.args[i].span.end += compBufStart
		}
	}
	// The probe helpers' package-level func decls (accumulated in registry.funcs
	// by emitInferProbe) are appended AFTER the component skeletons, alongside
	// the user's raw-Go bodies below — Go permits forward references between
	// top-level decls, so their position relative to the components and bodies
	// is immaterial to type-checking. Adjust each site's declSpan the same way
	// (funcs-buffer-relative to file-relative) so siteAt can match a raw offset
	// landing inside a hoisted decl body — see inferSite.declSpan's doc.
	funcsStart := sb.Len()
	sb.WriteString(registry.funcs.String())
	for _, s := range registry.spans {
		if s.declSpan.end > s.declSpan.start {
			s.declSpan.start += funcsStart
			s.declSpan.end += funcsStart
		}
	}
	for _, b := range bodies {
		emitSkeletonLine(&sb, fset, b.pos)
		sb.WriteString(b.src)
		sb.WriteByte('\n')
	}
	return sb.String(), comps, imports, ctrlOff, registry, gwMarkups, nil
}

// genericSigsFor computes the props-type-name -> *genericSig map for every
// generic component declared across files: same-package generics (module_
// importer.go's analyze passes every .gsx file of ONE package) and, with the
// identical filter, a DEPENDENCY package's own components (module_importer.
// go's importedPropFacts passes one dep package's files). One function
// serves both cases — see genericSig's doc on why a same-package entry's
// imports field goes unused.
//
// Filter: TypeParams non-empty, not BYO (an author-owns-Props component's
// sole param is a concrete struct — no type-arg-omitted caller-side probe
// applies), and a parseable param list. Unlike an earlier version of this
// function, a NULLARY generic component (zero declared params, and its own
// body never reads children/attrs — e.g. `component Marker[T any]()`) is NOT
// excluded: such a component can never reach emitInferProbe's call-form probe
// (nothing to infer FROM), so its entry is inert at that call site, but Task
// 8's diagnostic rewrite still needs its arity — a nullary generic tag's own
// caller-side probe (emitProbes' bare-call-candidate or no-props/method
// branch, never emitInferProbe) can perfectly legitimately fail to infer, and
// recordProbeSpan (infer.go) looks up this SAME map to learn the arity for
// that diagnostic.
func genericSigsFor(files map[string]*gsxast.File, byo *byoData) map[string]*genericSig {
	out := map[string]*genericSig{}
	for _, file := range files {
		fileImports := fileImportSpecs(file, nil)
		for _, d := range file.Decls {
			c, ok := d.(*gsxast.Component)
			if !ok || c.TypeParams == "" {
				continue
			}
			if _, isByo := byo.structTypeName(componentKey(c)); isByo {
				continue
			}
			params, err := parseParams(c.Params)
			if err != nil {
				continue
			}
			typeParamNames, err := parseTypeParamNames(c.TypeParams)
			if err != nil {
				continue
			}
			propsName := c.Name + "Props"
			if c.Recv != "" {
				if _, _, recvTypeName, err := parseRecv(c.Recv); err == nil {
					propsName = recvTypeName + c.Name + "Props"
				}
			}
			out[propsName] = &genericSig{
				typeParams: c.TypeParams,
				params:     params,
				arity:      len(typeParamNames),
				imports:    fileImports,
			}
		}
	}
	return out
}

// goBody is a GoChunk's non-import remainder paired with the .gsx source
// position of its first byte (for the //line directive that maps it back).
type goBody struct {
	src string
	pos token.Pos
}

// ctrlRef maps a gsx control-flow node (ForMarkup/IfMarkup/GoBlock) to its
// clause's position in the skeleton (ClauseStart) and the smallest skeleton
// go/ast node that spans the clause region. The LSP uses this to place a
// cursor inside the clause and call innermostIdent for go-to-definition.
type ctrlRef struct {
	ClauseStart token.Pos
	Node        goast.Node
}

// SigTypeRef bridges one navigable identifier region in a component signature
// (a parameter type, method receiver type, type-parameter name, or
// type-parameter constraint) to its type-checked skeleton expression. GSXPos is
// the region's first byte in the .gsx and Len its byte length; SkelTyp is the
// corresponding skeleton expression, whose bytes are identical to the .gsx
// source span — so the LSP bridges a cursor into it by relative offset and
// resolves via go/types.
type SigTypeRef struct {
	GSXPos  token.Pos
	Len     int
	SkelTyp goast.Expr
}

// ctrlClauseText returns the clause/cond/code/tag text a control-flow node
// contributes verbatim to the skeleton at its recorded ctrlOff: ForMarkup →
// Clause, IfMarkup → Cond, GoBlock → Code, SwitchMarkup → Tag, CaseClause →
// List, CondAttr → Cond (in-tag `{ if … }` attribute group), ClassPart → Cond
// (a `: cond` guard in class/style), ValueIf → Cond, ValueSwitch → Tag,
// ValueSwitchCase → List (value-form if/switch inside class/style).
func ctrlClauseText(n gsxast.Node) string {
	switch t := n.(type) {
	case *gsxast.ForMarkup:
		return t.Clause
	case *gsxast.IfMarkup:
		return t.Cond
	case *gsxast.GoBlock:
		return t.Code
	case *gsxast.SwitchMarkup:
		return t.Tag
	case *gsxast.CaseClause:
		return t.List
	case *gsxast.CondAttr:
		return t.Cond
	case *gsxast.ClassPart:
		return t.Cond
	case *gsxast.ValueIf:
		return t.Cond
	case *gsxast.ValueSwitch:
		return t.Tag
	case *gsxast.ValueSwitchCase:
		return t.List
	}
	return ""
}

// buildCtrlMap converts each control-flow node's skeleton byte-offset to a
// token.Pos and finds the smallest skeleton node spanning its clause/cond/code
// region, so the LSP can bridge a cursor into the clause and innermostIdent it.
func buildCtrlMap(f *goast.File, fset *token.FileSet, ctrlOff map[gsxast.Node]int, clauseText map[gsxast.Node]string) map[gsxast.Node]ctrlRef {
	tf := fset.File(f.Pos())
	if tf == nil {
		return nil
	}
	out := map[gsxast.Node]ctrlRef{}
	for node, off := range ctrlOff {
		text := clauseText[node]
		start := tf.Pos(off)
		endOff := min(off+len(text), tf.Size())
		end := tf.Pos(endOff)
		var smallest goast.Node
		goast.Inspect(f, func(n goast.Node) bool {
			if n == nil {
				return false
			}
			if n.Pos() <= start && end <= n.End() {
				smallest = n // tighter container; keep descending
				return true
			}
			return false
		})
		out[node] = ctrlRef{ClauseStart: start, Node: smallest}
	}
	return out
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
func emitComponentSkeleton(sb *strings.Builder, c *gsxast.Component, table filterTable, propFields, nodeProps, attrsProps map[string]map[string]bool, genericSigs map[string]*genericSig, byo *byoData, fm FieldMatcher, usedFilters map[string]string, fset *token.FileSet, ctrlOff map[gsxast.Node]int, registry *inferRegistry, bag *diag.Bag) error {
	// Parse the type-param list ONCE here and thread the result into every
	// emitComponentStub call site below (instead of each stub re-parsing the
	// same string and swallowing its own error) — see typeParamNames/tpErr use
	// a few lines down for the failure shape.
	typeParamNames, tpErr := parseTypeParamNames(c.TypeParams)
	typeParamsDecl := typeParamDecl(c.TypeParams)
	if tpErr != nil {
		typeParamNames, typeParamsDecl = nil, ""
	}
	// Parse the receiver clause ONCE too (results reused by the recv-handling
	// section below). Every early-exit stub emits c.Recv verbatim when
	// withRecv=true, so an unparsable receiver co-occurring with any other
	// defect (`component (p !) Box[T](v T)`) would break the whole skeleton's
	// parse — module_importer hard-errors on that, aborting the ENTIRE run.
	// stubRecv=false makes those stubs fall back to a bare function, exactly
	// like the parseRecv-failure branch below.
	//
	// recvVar/recvTypeName stay "" for a function component; for a method
	// component they are passed to emitProbes so a dotted child tag whose left ==
	// recvVar is probed as a method call (mirroring the emitter's childInvocation).
	var recvVar, recvTypeName string
	var recvErr error
	if c.Recv != "" {
		recvVar, _, recvTypeName, recvErr = parseRecv(c.Recv)
	}
	stubRecv := recvErr == nil
	params, err := parseParams(c.Params)
	if err != nil {
		// Emit a minimal stub so the overall skeleton remains valid Go, keeping
		// any user GoChunk imports used. The parse error will be re-surfaced (with
		// position) by genComponent at emit time.
		emitComponentStub(sb, c, nil, stubRecv, recvTypeName, typeParamNames, typeParamsDecl, false)
		return errSkipComponent
	}
	if tpErr != nil {
		// Unparsable type-param list: emit the stub with NO params (their types
		// may reference the now-undeclared type params — an undefined-T type
		// error here is unmappable and silently kills the whole package's
		// generation). params=nil matches the parseParams-failure shape above;
		// genComponent re-parses at emit time and records the positioned
		// invalid-syntax diagnostic.
		//
		// This MUST precede checkReservedParams: a reserved param name can
		// co-occur with the broken type-param list (`Box[T](children T)`), and
		// the reserved-param stub keeps params — whose types may reference the
		// now-undeclared T — reintroducing the silent collapse. A broken
		// type-param list makes every param type suspect, so it takes priority.
		emitComponentStub(sb, c, nil, stubRecv, recvTypeName, nil, "", false)
		return errSkipComponent
	}
	if err := checkReservedParams(params); err != nil {
		// Emit a stub that INCLUDES the props struct (keeping user-imported types
		// like gsx.Node used in the skeleton) so GoChunk imports don't spuriously
		// trigger "imported and not used". The reserved-param error will be
		// re-surfaced (with position) by genComponent at emit time.
		emitComponentStub(sb, c, params, stubRecv, recvTypeName, typeParamNames, typeParamsDecl, false)
		return errSkipComponent
	}
	typeParamsUse := typeParamUse(typeParamNames)
	// MIRROR genComponent (emit.go): a method component emits a Go method whose
	// receiver var is in scope (so `p.Field` probes type-check against the real
	// receiver type), its props struct is named <RecvTypeName><Name>Props, and a
	// NULLARY method (no params, no children) gets NO props struct + no _gsxp
	// param. The receiver clause + props-struct name + nullary-no-props must be
	// byte-identical in shape to emission, else resolution disagrees.
	propsName := c.Name + "Props"
	if c.Recv != "" {
		if recvErr != nil {
			// Recv parse failed (hoisted parse above) — the receiver clause may be
			// invalid Go; use a bare function stub (no receiver) to keep the
			// skeleton valid.
			emitComponentStub(sb, c, params, false, "", typeParamNames, typeParamsDecl, false)
			return errSkipComponent
		}
		if rerr := checkReservedRecvVar(recvVar); rerr != nil {
			emitComponentStub(sb, c, params, true, recvTypeName, typeParamNames, typeParamsDecl, false)
			return errSkipComponent
		}
		propsName = recvTypeName + c.Name + "Props"
	}
	if c.Recv != "" && len(typeParamNames) > 0 && !toolchainHasGenericMethods() {
		// A generic METHOD skeleton would fail this toolchain's go/parser and
		// abort the whole run (module_importer hard-errors on skeleton parse
		// failures). Emit the props struct only — no func — and let
		// genComponent record the positioned unsupported-toolchain diagnostic.
		// Sibling call sites of this component get positioned type errors at
		// their probes, which is the standard broken-component experience.
		//
		// Placed after the recv-parsing block above (an invalid receiver or a
		// reserved receiver var already returned) so this only fires once every
		// other defect has been ruled out — MIRRORS genComponent's guard
		// (emit.go), which sits in the same relative position.
		emitComponentStub(sb, c, params, true, recvTypeName, typeParamNames, typeParamsDecl, true /*omitFunc*/)
		return errSkipComponent
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
		// Anchor the skeleton func declaration to the component NAME position so
		// go/types (and thus same-package LSP go-to-definition on `{ LocalComp(…) }`)
		// reports the component's name column precisely, not just the keyword column.
		// Mirrors genComponent's emit-side anchor for the line; adds column precision.
		emitSkeletonComponentNameLine(sb, fset, c)
		if c.Recv != "" {
			fmt.Fprintf(sb, "func %s %s%s(_gsxp %s) _gsxrt.Node {\n", c.Recv, c.Name, typeParamsDecl, structName)
		} else {
			fmt.Fprintf(sb, "func %s%s(_gsxp %s) _gsxrt.Node {\n", c.Name, typeParamsDecl, structName)
		}
		sb.WriteString("\tvar ctx _gsxctx.Context\n\t_ = ctx\n")
		if c.ParamsPos.IsValid() {
			emitSkeletonLineParam(sb, fset, c.ParamsPos+token.Pos(params[0].nameOff))
		}
		fmt.Fprintf(sb, "\t%s := _gsxp\n\t_ = %s\n", params[0].name, params[0].name)
		// Reset the //line so the probe body's own positions are not shifted by the
		// param binding's mapping.
		emitSkeletonLine(sb, fset, c.Pos())
		if err := emitProbes(sb, c.Body, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
			return err
		}
		sb.WriteString("\treturn nil\n}\n")
		return nil
	}
	// Synthesize the implicit `Children _gsxrt.Node` slot field + `children`
	// local in lockstep with genComponent (emit.go), so skeleton and emitted
	// code agree on the props shape and the `{children}` interp type-checks.
	hasChildren := usesChildren(c.Body)
	// A body referencing `attrs` explicitly forces an Attrs field, including for
	// a nullary component.
	manual := usesAttrs(c.Body)
	hasProps := len(params) > 0 || hasChildren || manual
	if hasProps {
		fmt.Fprintf(sb, "type %s%s struct {\n", propsName, typeParamsDecl)
		for _, p := range params {
			// Emit the param TYPE verbatim from the .gsx (not the printer-normalized
			// p.typ) so its bytes stay identical to the source — the LSP bridges a
			// cursor on a type identifier into this field's type by relative offset,
			// exactly as it does for interpolation expressions. For gsx-fmt'd source
			// (all generated output) p.typeSrc == p.typ, so codegen is unchanged.
			fmt.Fprintf(sb, "\t%s %s\n", fieldName(p.name), p.typeSrc)
		}
		if hasChildren {
			sb.WriteString("\tChildren _gsxrt.Node\n")
		}
		if manual {
			sb.WriteString("\tAttrs _gsxrt.Attrs\n")
		}
		sb.WriteString("}\n")
	}
	// Use the same reserved props-param name as the emitted code (_gsxp) so a
	// user param named `p` does not collide in the skeleton either. Emit the
	// receiver clause verbatim for a method component (its receiver var is in
	// scope, like the emitted method).
	// Anchor the skeleton func declaration to the component NAME position (see
	// the BYO branch above) so same-package go-to-definition reports the name column.
	emitSkeletonComponentNameLine(sb, fset, c)
	if c.Recv != "" {
		fmt.Fprintf(sb, "func %s %s%s(", c.Recv, c.Name, typeParamsDecl)
	} else {
		fmt.Fprintf(sb, "func %s%s(", c.Name, typeParamsDecl)
	}
	if hasProps {
		fmt.Fprintf(sb, "_gsxp %s%s", propsName, typeParamsUse)
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
	// probe type-checks the author's `{ attrs... }` (probed as `_gsxgw.Spread(ctx,
	// attrs)`) and any `attrs.X()` reference identically to emitted code.
	if manual {
		sb.WriteString("\tattrs := _gsxp.Attrs\n")
	}
	if err := emitProbes(sb, c.Body, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
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
func emitProbes(sb *strings.Builder, nodes []gsxast.Markup, table filterTable, propFields, nodeProps, attrsProps map[string]map[string]bool, genericSigs map[string]*genericSig, byo *byoData, fm FieldMatcher, recvVar, recvTypeName string, usedFilters map[string]string, fset *token.FileSet, ctrlOff map[gsxast.Node]int, registry *inferRegistry, bag *diag.Bag) error {
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
		case *gsxast.EmbeddedInterp:
			// Body backtick literal {`…@{expr}…`} [ |> f ]. Probe each hole
			// first (so every param it references stays live and its own
			// type is harvested — mirrors an EmbeddedAttr's holes), then, ONLY
			// when the whole literal itself carries a pipeline, probe the
			// assembled seed piped through node.Stages — the SAME lowerPipe
			// call codegen's emitEmbeddedInterp will build (via
			// embeddedTextValueExpr + lowerPipe), so resolved[t] ends up the
			// exact type codegen emits (emit ≡ probe).
			if err := emitProbes(sb, t.Segments, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
				return err
			}
			if len(t.Stages) > 0 {
				seed := embeddedProbeSeed(t.Segments, table, usedFilters)
				probe, err := probeExpr(seed, t.Stages, table, usedFilters)
				if err != nil {
					return err
				}
				emitSkeletonLine(sb, fset, t.Pos())
				fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
			}
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
					//
					// A bare-call candidate can itself be a GENERIC component with
					// nothing to supply (e.g. `component Marker[T any]()`, never
					// eligible for emitInferProbe's call-form probe since it has no
					// params to infer from — genericSigsFor still records its arity,
					// see its doc). Passing an uninstantiated generic function VALUE
					// (Marker, with no explicit type args) as this bare `any` argument
					// is itself an implicit-instantiation attempt, so it can fail with
					// its own "cannot infer" here — record the span (Task 8) so that
					// diagnostic is keyed to THIS tag, not blamed on an unrelated one.
					emitSkeletonLine(sb, fset, t.Pos())
					start := sb.Len()
					fmt.Fprintf(sb, "_gsxcompsig(%s%s)\n", callTarget, typeArgUse(t.TypeArgs))
					if t.TypeArgs == "" {
						if sig := genericSigs[propsType]; sig != nil {
							registry.recordProbeSpan(t, propsType, sig.arity, start, sb.Len())
						}
					}
				} else if ((isMethod && !isByoChild) || isNoPropsComponent(propFields, propsType)) && len(t.Attrs) == 0 && len(t.Children) == 0 {
					// Same Task 8 concern as the bare-call-candidate branch above: a
					// generic no-props method/function reaching this nullary `_ = F()`
					// probe (rather than emitInferProbe) can still fail its own
					// implicit instantiation attempt.
					emitSkeletonLine(sb, fset, t.Pos())
					start := sb.Len()
					fmt.Fprintf(sb, "_ = %s%s()\n", callTarget, typeArgUse(t.TypeArgs))
					if t.TypeArgs == "" {
						if sig := genericSigs[propsType]; sig != nil {
							registry.recordProbeSpan(t, propsType, sig.arity, start, sb.Len())
						}
					}
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
					// probeWrap=true: ExprAttr values are wrapped with _gsxunwrap(...) in
					// the skeleton so (T, error) tuples type-check while field-type
					// checking is preserved.
					// cfHoistBuf collects any var+if/switch statements hoisted by
					// classEntryExpr for value-form CF parts in a class attr. They
					// are emitted to the skeleton BEFORE the probe call so the skeleton
					// remains syntactically valid Go. A local interpTemp counter keeps
					// the temp names _gsxv0… unique within this probe context.
					var cfHoistBuf bytes.Buffer
					cfInterpTemp := 0
					fieldEntries, splatExpr, usedPkgs, err := childPropsLiteral(t, propsType, "_gsxrt", "_gsxrt.DefaultClassMerge", table, propFields, nodeProps[propsType], byo, fm, func(nodes []gsxast.Markup) (string, error) {
						return "_gsxrt.Node(nil)", nil
					}, true, nil, &cfHoistBuf, &cfInterpTemp)
					if err != nil {
						// childPropsLiteral returns an *attrError with the offending attr's
						// position embedded. Propagate it as-is so the caller can emit
						// a positioned diagnostic (not positionless).
						return err
					}
					// Record filter packages referenced by a lowered prop/fallthrough
					// pipeline so the skeleton imports them under their reserved aliases
					// — the SAME set the emitter records into its imports map. Without
					// this the skeleton would not import _gsxstdN and a prop pipeline
					// would fail to resolve.
					maps.Copy(usedFilters, usedPkgs)
					// One //line for the WHOLE props literal (the element position).
					// A child-prop FIELD-TYPE error (e.g. `cannot use … as string`)
					// therefore reports at this single position, so the COLUMN of a
					// trailing field can be inaccurate — earlier wrapped fields
					// (_gsxunwrap(…), gsx.Val(…)) shift the offset, and the column can
					// even land past end-of-line for a CALL-valued prop whose wrapped
					// earlier fields shift the offset. This is a known limitation; a future reader
					// should not trust the column of a child-prop field-type error.
					emitSkeletonLine(sb, fset, t.Pos())
					// Emit any CF-hoisted statements before the probe call.
					sb.WriteString(cfHoistBuf.String())
					if splatExpr != "" {
						// Whole-struct splat: mirrors genChildComponent exactly.
						fmt.Fprintf(sb, "_ = %s%s(%s)\n", callTarget, typeArgUse(t.TypeArgs), splatExpr)
					} else if sig := genericSigs[propsType]; t.TypeArgs == "" && sig != nil {
						// Omitted type args on a generic tag: emit a caller-side inference
						// probe (a fresh, uniquely-named generic helper whose params are
						// exactly the props SUPPLIED here) so go/types infers the type args
						// from THIS subset — mirroring how a plain Go generic function call
						// infers from whatever arguments it's given, unlike the old exported
						// declaring-side inference helper which required every declared prop
						// to be supplied. ONE emitter path serves both a SAME-PACKAGE and an
						// IMPORTED component (genericSigs, unified — see genericSig's doc):
						// for an imported (dotted, non-method) tag, the type-param decl and
						// each SUPPLIED param's type are first requalified into the calling
						// file's context (Task 3's engine, via registry's per-file-coalesced
						// aliasMinter). Requalification failure (an unexported dep-local
						// type, a dot-imported dep-local name, or an unresolvable/ambiguous
						// dep qualifier) fails safe: the probe is SKIPPED entirely (neither
						// this inference probe nor the plain composite-literal fallback is
						// emitted, so no confusing raw go/types error accompanies it) and a
						// single positioned "inference-unavailable" diagnostic is recorded.
						emitted := false
						failed := false
						if targetTPNames, tperr := parseTypeParamNames(sig.typeParams); tperr == nil {
							supplied := map[string]string{}
							for _, fe := range fieldEntries {
								if fe.inferField != "" {
									supplied[fe.inferField] = fe.inferArg
								}
							}
							typeParamsSrc := sig.typeParams
							targetParams := sig.params
							if depAlias, imported := importedTagAlias(t.Tag, isMethod); imported {
								declared := make(map[string]bool, len(targetTPNames))
								for _, n := range targetTPNames {
									declared[n] = true
								}
								reqDecl, rerr := registry.requalifyTypeParams(sig.typeParams, depAlias, sig.imports)
								if rerr != nil {
									recordInferenceUnavailable(bag, registry, t, rerr)
									failed = true
								} else {
									typeParamsSrc = reqDecl
									ordered := suppliedInDeclOrder(sig.params, supplied)
									reqOrdered := make([]param, 0, len(ordered))
									for _, p := range ordered {
										rq, perr := registry.requalifyTypeExpr(p.typeSrc, depAlias, sig.imports, declared)
										if perr != nil {
											recordInferenceUnavailable(bag, registry, t, perr)
											failed = true
											break
										}
										p.typeSrc = rq
										reqOrdered = append(reqOrdered, p)
									}
									if !failed {
										targetParams = reqOrdered
									}
								}
							}
							if !failed {
								emitted = registry.emitInferProbe(sb, t, propsType,
									typeParamDecl(typeParamsSrc), typeParamUse(targetTPNames),
									targetParams, supplied, sig.arity)
							}
						}
						switch {
						case emitted:
							// probe already written above.
						case failed:
							// Sink every supplied prop's value expression (and any
							// CF-hoisted var it references — see cfHoistBuf above) into a
							// throwaway anonymous-struct literal, so the skeleton stays
							// valid Go without attempting the (necessarily invalid)
							// generic instantiation that would otherwise surface a
							// second, confusing diagnostic alongside the clean one
							// recordInferenceUnavailable already added. Each entry's
							// VALUE half (fe.str is always "GoFieldName: value-expr",
							// the shape childPropsLiteral produces for splicing into a
							// real props literal) is re-keyed under a synthetic,
							// guaranteed-unique field name typed `any` — sinking the
							// value alone, not the props type, so this never depends on
							// whichever generic instantiation failed to resolve.
							if len(fieldEntries) > 0 {
								typeParts := make([]string, len(fieldEntries))
								litParts := make([]string, len(fieldEntries))
								for i, fe := range fieldEntries {
									val := fe.str
									if idx := strings.Index(fe.str, ":"); idx >= 0 {
										val = strings.TrimSpace(fe.str[idx+1:])
									}
									name := fmt.Sprintf("F%d", i)
									typeParts[i] = name + " any"
									litParts[i] = name + ": " + val
								}
								fmt.Fprintf(sb, "_ = struct{ %s }{%s}\n", strings.Join(typeParts, "; "), strings.Join(litParts, ", "))
							}
						default:
							// No supplied prop matched any declared field (emitInferProbe
							// itself declined for lack of anything to infer FROM), so the
							// tag falls through to a plain, uninstantiated composite-
							// literal probe — go/types' own "cannot infer" against THIS
							// call is exactly as real as emitInferProbe's call-form one;
							// record its span (Task 8) so it resolves back to this tag too.
							strs := make([]string, len(fieldEntries))
							for i, fe := range fieldEntries {
								strs[i] = fe.str
							}
							start := sb.Len()
							fmt.Fprintf(sb, "_ = %s(%s{%s})\n", callTarget, propsType, strings.Join(strs, ", "))
							registry.recordProbeSpan(t, propsType, sig.arity, start, sb.Len())
						}
					} else {
						strs := make([]string, len(fieldEntries))
						for i, fe := range fieldEntries {
							strs[i] = fe.str
						}
						fmt.Fprintf(sb, "_ = %s%s(%s%s{%s})\n", callTarget, typeArgUse(t.TypeArgs), propsType, typeArgUse(t.TypeArgs), strings.Join(strs, ", "))
					}
				}
				// Probe simple ExprAttr values (child-prop values) with _gsxuse so harvest
				// records their RAW types into resolved[ea]. This is emitted for ALL child
				// component branches (bare-call, nullary, props-literal) so the k-th probe
				// always aligns with the k-th node in collectExprs (which also adds ExprAttr
				// nodes for all child components, before slot content).
				for _, a := range t.Attrs {
					ea, ok := a.(*gsxast.ExprAttr)
					if !ok {
						continue
					}
					probe, perr := probeExpr(ea.Expr, ea.Stages, table, usedFilters)
					if perr != nil {
						return perr
					}
					emitSkeletonLine(sb, fset, ea.Pos())
					// _gsxuseq (the QUIET harvest probe), not _gsxuse: an
					// expression-internal error here (undefined ident, bad call, …)
					// is reported by the props-literal _gsxunwrap(...) probe above, so
					// the duplicate from this type-only probe is suppressed when its
					// errors are surfaced (see the quietSpans handling in analyze's
					// type-error loop). harvest reads _gsxuseq exactly like _gsxuse.
					fmt.Fprintf(sb, "_gsxuseq(%s)\n", probe)
				}
				// Probe ordered-attrs pair values AFTER ExprAttr probes, in attr source
				// order then pair order — matching collectExprs's ordering exactly.
				// _gsxuseq harvests the raw type (possibly a tuple) of each pair value;
				// the props-literal _gsxunwrap(...) probe already reports expression-internal
				// errors, so _gsxuseq's quiet suppression avoids duplicates.
				for _, a := range t.Attrs {
					oa, ok := a.(*gsxast.OrderedAttrsAttr)
					if !ok {
						continue
					}
					for i := range oa.Pairs {
						emitSkeletonLine(sb, fset, oa.Pairs[i].Pos())
						fmt.Fprintf(sb, "_gsxuseq(%s)\n", oa.Pairs[i].Value)
					}
				}
				// Probe CF arm exprs and unconditional plain ClassPart exprs AFTER pair
				// probes — matching collectExprs's ClassPart ordering exactly (the
				// shared walkClassAttrs recurses CondAttr on both sides). _gsxuse
				// harvests the raw type so classEntryExpr can detect and hoist (T, error)
				// tuple call parts and CF arms. Unlike ordinary child-prop expressions,
				// call-shaped class parts are stubbed in the props-literal probe to
				// tolerate tuples, so this non-quiet probe is also responsible for
				// surfacing expression errors such as undefined identifiers.
				walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
					for i := range ca.Parts {
						if ca.Parts[i].CF != nil {
							// Value-form CF part: probe each arm so harvest populates
							// resolved[arm] for classEntryExpr's (T, error) unwrap.
							for _, arm := range valueFormArms(ca.Parts[i].CF) {
								probe, perr := probeExpr(arm.Expr, arm.Stages, table, usedFilters)
								if perr != nil {
									probe = strings.TrimSpace(arm.Expr)
								}
								emitSkeletonLine(sb, fset, arm.Pos())
								fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
							}
						} else if ca.Parts[i].Cond == "" && ca.Parts[i].CSSSegments == nil {
							probe, perr := probeExpr(ca.Parts[i].Expr, ca.Parts[i].Stages, table, usedFilters)
							if perr != nil {
								probe = strings.TrimSpace(ca.Parts[i].Expr)
							}
							emitSkeletonLine(sb, fset, ca.Parts[i].Pos())
							fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
						}
					}
				})
				// Probe ExprAttr values nested in a component cond-attr branch
				// (`{ if C { attr={expr} } }`) with _gsxuseq, AFTER the parts probes —
				// matching collectExprs's walkBranchAttrExprs pass exactly (Then→Else,
				// top-level ExprAttrs excluded). childPropsLiteral embeds the whole
				// AttrsCond(...) expression in the props probe without a per-value
				// harvest probe, so these probes are what populate resolved for branch
				// ExprAttrs (Task 3 consumes them for (T, error) tuple detection). These
				// are TOP-LEVEL skeleton statements: the skeleton is compile-only and
				// never executed, so probing the branch value UNCONDITIONALLY (outside
				// any thunk/cond) is safe — laziness is irrelevant here. _gsxuseq (quiet)
				// harvests the raw type (possibly a tuple); an expression-internal error
				// is reported by the props-literal probe above, so the quiet suppression
				// avoids a duplicate. _gsxuseq(...any) also tolerates a multi-value
				// (T, error) argument that `_ = (expr)` liveness could not.
				var branchProbeErr error
				walkBranchAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
					if branchProbeErr != nil {
						return
					}
					probe, perr := probeExpr(ea.Expr, ea.Stages, table, usedFilters)
					if perr != nil {
						branchProbeErr = perr
						return
					}
					emitSkeletonLine(sb, fset, ea.Pos())
					fmt.Fprintf(sb, "_gsxuseq(%s)\n", probe)
				})
				if branchProbeErr != nil {
					return branchProbeErr
				}
				// Probe slot content in the SAME canonical order collectExprs walks:
				// each markup-attr value (attr order) then the children.
				var probeErr error
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag)
				})
				if probeErr != nil {
					return probeErr
				}
				// Probe each braced-attr whole-literal pipeline (`attr={`…` |> f}`)
				// AFTER the markup-attr/hole probes above — matching collectExprs's
				// walkEmbeddedAttrStages ordering exactly.
				walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
					if probeErr != nil {
						return
					}
					seed := embeddedProbeSeed(ea.Segments, table, usedFilters)
					probe, err := probeExpr(seed, ea.Stages, table, usedFilters)
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
				if err := emitProbes(sb, t.Children, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
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
				// Probe each element-spread expr with _gsxuseq. The quiet probe checks
				// the expression and doubles as its liveness reference, so a var used
				// ONLY in a `{ x... }` spread stays "used". collectExprs appends each
				// SpreadAttr AFTER all the element's ExprAttrs, so the k-th probe stays
				// aligned with the k-th node.
				walkSpreadAttrs(t.Attrs, func(sa *gsxast.SpreadAttr) {
					if probeErr != nil {
						return
					}
					probe, err := probeExpr(sa.Expr, sa.Stages, table, usedFilters)
					if err != nil {
						probeErr = err
						return
					}
					emitSkeletonLine(sb, fset, sa.Pos())
					fmt.Fprintf(sb, "_gsxuseq(%s)\n", probe)
					// The _gsxuseq harvest above has its error span SUPPRESSED
					// (module_importer quietSpans). Re-check the expression in a native
					// gsx.Attrs assignment so invalid spread types and expression errors
					// surface exactly once without exposing a synthetic helper name.
					// This declaration is NOT a counted probe, so it is invisible to the
					// k-th-probe→k-th-node harvest alignment.
					emitSkeletonLine(sb, fset, sa.Pos())
					fmt.Fprintf(sb, "var _ _gsxrt.Attrs = (%s)\n", probe)
				})
				if probeErr != nil {
					return probeErr
				}
				// Emit _gsxuse probes for value-form CF arm expressions in the SAME
				// source order collectExprs collects them (attr order → CF-part order
				// → arm order). harvest maps the k-th _gsxuse to the k-th node,
				// populating resolved[arm] so hoistValueCF can detect and unwrap
				// (T, error) return types. _gsxuse(...any) accepts multi-return calls
				// without a syntax error, and harvest reads the tuple type from the
				// argument's resolved type. A probe per CF arm, and also per
				// unconditional plain part, replaces the former liveness-only behavior
				// for those parts; _gsxuse also keeps identifier references live.
				// walkClassAttrs recurses CondAttr Then/Else in lockstep with
				// collectExprs, so arms of a class attr nested in a conditional attr
				// group are probed (liveness + harvest) too.
				walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
					for i := range ca.Parts {
						if ca.Parts[i].CF != nil {
							for _, arm := range valueFormArms(ca.Parts[i].CF) {
								probe, perr := probeExpr(arm.Expr, arm.Stages, table, usedFilters)
								if perr != nil {
									// Unknown filter: fall back to the bare seed so the skeleton
									// type-checks (and identifiers stay live); the positioned
									// unknown-filter diagnostic fires separately.
									probe = strings.TrimSpace(arm.Expr)
								}
								emitSkeletonLine(sb, fset, arm.Pos())
								fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
							}
						} else if ca.Parts[i].Cond == "" && ca.Parts[i].CSSSegments == nil {
							// Unconditional plain part: harvest its type for (T, error) unwrap.
							// _gsxuse also serves as a liveness reference (replaces _ = (expr)).
							probe, perr := probeExpr(ca.Parts[i].Expr, ca.Parts[i].Stages, table, usedFilters)
							if perr != nil {
								probe = strings.TrimSpace(ca.Parts[i].Expr)
							}
							emitSkeletonLine(sb, fset, ca.Parts[i].Pos())
							fmt.Fprintf(sb, "_gsxuse(%s)\n", probe)
						}
					}
				})
				// ClassAttr conditional part exprs, cond guards, and value-form CF
				// control expressions are emitted verbatim by codegen (no type
				// harvest), so a var used ONLY in `class={ "on": v }` or in a
				// value-form if/switch condition must still be referenced here or
				// it's "declared and not used". The walk yields ready-made liveness
				// STATEMENTS — `_ = (expr)`, or (via emitValueCFControl) an
				// empty-bodied if/switch for CF parts (their tags/case lists are
				// only legal in statement position) — NOT _gsxuse, so the harvest
				// alignment is intact. Each value-form if condition also records a
				// ctrlOff entry so the LSP can go-to-definition inside it. CF arms
				// and unconditional plain parts are excluded (they have _gsxuse
				// probes above). Spreads are excluded too because their _gsxuseq
				// probes above also keep them live.
				walkLivenessAttrExprs(t.Attrs, table, usedFilters, func(stmt string) {
					sb.WriteString(stmt)
					sb.WriteByte('\n')
				}, func(cf *gsxast.ValueCF) {
					emitValueCFControl(sb, fset, cf, ctrlOff)
				}, func(node gsxast.Node, cond string, condPos token.Pos) {
					emitCondLiveness(sb, fset, node, cond, condPos, ctrlOff)
				})
				// Then probe each JS-attribute's @{ } interps, in attr source order —
				// collectExprs walks identically (same walkMarkupAttrs), so the k-th
				// _gsxuse maps to the k-th collected node.
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag)
				})
				if probeErr != nil {
					return probeErr
				}
				// Probe each braced-attr whole-literal pipeline AFTER the
				// markup-attr/hole probes above — matching collectExprs's
				// walkEmbeddedAttrStages ordering exactly.
				walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
					if probeErr != nil {
						return
					}
					seed := embeddedProbeSeed(ea.Segments, table, usedFilters)
					probe, err := probeExpr(seed, ea.Stages, table, usedFilters)
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
				if err := emitProbes(sb, t.Children, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
					return err
				}
			}
		case *gsxast.Fragment:
			if err := emitProbes(sb, t.Children, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
				return err
			}
		case *gsxast.ForMarkup:
			emitSkeletonClauseLine(sb, fset, t.ClausePos, len("for ")) // 4
			ctrlOff[t] = sb.Len() + len("for ")
			fmt.Fprintf(sb, "for %s {\n", t.Clause)
			if err := emitProbes(sb, t.Body, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
				return err
			}
			sb.WriteString("}\n")
		case *gsxast.IfMarkup:
			emitSkeletonClauseLine(sb, fset, t.CondPos, len("if ")) // 3
			ctrlOff[t] = sb.Len() + len("if ")
			fmt.Fprintf(sb, "if %s {\n", t.Cond)
			if err := emitProbes(sb, t.Then, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
				return err
			}
			sb.WriteString("}")
			if t.Else != nil {
				sb.WriteString(" else {\n")
				if err := emitProbes(sb, t.Else, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
					return err
				}
				sb.WriteString("}")
			}
			sb.WriteString("\n")
		case *gsxast.SwitchMarkup:
			if strings.TrimSpace(t.Tag) != "" {
				emitSkeletonClauseLine(sb, fset, t.TagPos, len("switch "))
				ctrlOff[t] = sb.Len() + len("switch ")
			}
			fmt.Fprintf(sb, "switch %s {\n", t.Tag)
			for _, cc := range t.Cases {
				if cc.Default {
					sb.WriteString("default:\n")
				} else {
					emitSkeletonClauseLine(sb, fset, cc.ListPos, len("case "))
					ctrlOff[cc] = sb.Len() + len("case ")
					fmt.Fprintf(sb, "case %s:\n", cc.List)
				}
				if err := emitProbes(sb, cc.Body, table, propFields, nodeProps, attrsProps, genericSigs, byo, fm, recvVar, recvTypeName, usedFilters, fset, ctrlOff, registry, bag); err != nil {
					return err
				}
			}
			sb.WriteString("}\n")
		case *gsxast.GoBlock:
			emitSkeletonClauseLine(sb, fset, t.CodePos, 0)
			ctrlOff[t] = sb.Len()
			sb.WriteString(t.Code)
			sb.WriteString("\n")
		}
	}
	return nil
}

// emitSkeletonComponentNameLine anchors the skeleton's `func … Name(` declaration
// so the Name token maps to the component's .gsx NamePos column-precisely, letting
// LSP go-to-definition land on the component name. genNameCol is the name's column
// within the generated func line: 6 for `func <Name>` (after "func "), or
// 7+len(Recv) for `func <Recv> <Name>`. The directive is shifted left by that
// prefix. (Only the skeleton needs this; the emit-side anchor stays at c.Pos().)
func emitSkeletonComponentNameLine(sb *strings.Builder, fset *token.FileSet, c *gsxast.Component) {
	if fset == nil || !c.NamePos.IsValid() {
		return
	}
	genNameCol := 6 // func <Name>
	if c.Recv != "" {
		genNameCol = 7 + len(c.Recv) // func <Recv> <Name>
	}
	p := fset.Position(c.NamePos)
	col := max(p.Column-genNameCol+1, 1)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}

// emitSkeletonClauseLine emits a //line anchored so the clause/cond/code text
// (which the skeleton emits verbatim starting `prefixLen` bytes into the line)
// maps to its .gsx position pos. col = clauseCol - prefixLen.
func emitSkeletonClauseLine(sb *strings.Builder, fset *token.FileSet, pos token.Pos, prefixLen int) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	col := max(p.Column-prefixLen, 1)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
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

// emitSkeletonBlockLine emits a BLOCK-form `/*line file:line:col*/` directive
// (no trailing newline), used to re-sync the position of verbatim GoText
// spliced around a GoWithElements-embedded element's inline IIFE. Unlike the
// `//line` form (which spans to end of line and needs its own line), the block
// form is valid mid-expression, so it does not force a newline that would trip
// Go's automatic semicolon insertion when the GoText attaches to the IIFE's
// `}()` (e.g. the trailing `)` of `Wrap(<Foo/>)`). It sets the position of the
// character immediately following the comment — the GoText's first byte.
func emitSkeletonBlockLine(sb *strings.Builder, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	fmt.Fprintf(sb, "/*line %s:%d:%d*/", p.Filename, p.Line, p.Column)
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
	col := max(p.Column-1, 1)
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
	col := max(p.Column-prefixLen, 1)
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
	lowered, used, err := lowerPipe(seed, stages, table, probePipeWrap)
	if err != nil {
		return strings.TrimSpace(seed), nil
	}
	maps.Copy(usedFilters, used)
	return lowered, nil
}

// embeddedProbeSeed builds the Go source text probed as the SEED for a
// whole-literal pipeline's `lowerPipe(seed, stages)` call — an
// EmbeddedInterp's or EmbeddedAttr's node-level `|> f` — mirroring, at the
// TYPE level, what codegen's embeddedTextValueExpr (emit.go) assembles from
// the SAME segments: static *Text becomes the identical quoted string
// literal, joined with " + ".
//
// Each *Interp hole becomes _gsxstr(holeProbe), where holeProbe is the SAME
// probeExpr the individual-hole probe already uses (so a hole's own
// pipeline/tuple handling is identical, and it stays live/harvested exactly
// as it would be probed on its own). _gsxstr(any, ...any) string is a
// package-level skeleton helper (module_importer.go) that always yields a
// `string` — this is not an approximation: every successful branch of the
// REAL emit-time holeStringExpr (string(x), strconv.Format*, (x).String())
// ALSO always yields a Go expression of exactly the built-in `string` type.
// So this seed and codegen's later, precisely-typed seed differ only in
// WHICH string-producing snippet appears per hole, never in the resulting
// static type — the seed's overall type is string either way, which is all
// lowerPipe's stage lowering (and thus resolved[node]) depends on. This lets
// the probe resolve the node's piped RESULT type without first knowing each
// hole's real type, which is impossible at skeleton-build time (hole types
// are only known once THIS SAME skeleton has been type-checked and
// harvested — a later, one-shot step, not available mid-build).
func embeddedProbeSeed(segments []gsxast.Markup, table filterTable, usedFilters map[string]string) string {
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch s := seg.(type) {
		case *gsxast.Text:
			if s.Value == "" {
				continue
			}
			parts = append(parts, strconv.Quote(s.Value))
		case *gsxast.Interp:
			probe, _ := probeExpr(s.Expr, s.Stages, table, usedFilters)
			parts = append(parts, "_gsxstr("+probe+")")
		}
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " + ")
}

// harvest reads each interpolation's resolved type from a type-checked skeleton
// file. An interpolation probe is now an ExprStmt whose call target is the
// identifier `_gsxuse`; harvest the single argument's type.
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr, registry *inferRegistry) {
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
		harvestBody(fd.Body, c.Body, info, out, exprOut, registry)
	}
}

// harvestBody resolves one skeleton func/closure body's probe calls back onto
// the gsx nodes of the markup it was generated from. bodyMarkup is the markup
// whose emitProbes output produced body — a component's Body, or (for an
// embedded element, via harvestEmbeddedElements) a single-element markup
// slice. Extracted from harvest so BOTH a component's top-level skeleton func
// and a GoWithElements-embedded element's inline IIFE share ONE resolution
// path (emit≡probe: the same probe shapes, harvested the same way).
func harvestBody(body *goast.BlockStmt, bodyMarkup []gsxast.Markup, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr, registry *inferRegistry) {
	var nodes []gsxast.Node
	collectExprs(bodyMarkup, &nodes)
	k := 0
	goast.Inspect(body, func(node goast.Node) bool {
		call, ok := node.(*goast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*goast.Ident)
		// _gsxuseq is the quiet child-prop harvest variant; it carries a node's
		// type identically to _gsxuse and occupies the SAME k-ordering slot
		// (collectExprs adds child-prop ExprAttr nodes in source order), so both
		// must be matched here or the k alignment would drift.
		if !ok || (id.Name != "_gsxuse" && id.Name != "_gsxuseq") || len(call.Args) != 1 {
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
	goast.Inspect(body, func(node goast.Node) bool {
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
		forEachComponentTagElement(bodyMarkup, func(el *gsxast.Element) {
			if t, ok := sigByName[el.Tag]; ok {
				out[el] = t
			}
		})
	}

	// Resolve each caller-side inference probe (emitInferProbe) by its exact,
	// registry-synthesized name — NOT by a user-spellable prefix like the old
	// exported-helper convention, which any same-package func sharing that
	// prefix would also match and silently corrupt this harvest (finding 3). Each
	// probe call's own instantiated return type IS the resolved props type
	// for the tag it was emitted for (site.el) — no k-ordering or
	// genericProps re-check needed, since emitInferProbe already resolved
	// both at emission time and recorded them on the site.
	if registry != nil {
		goast.Inspect(body, func(node goast.Node) bool {
			call, ok := node.(*goast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*goast.Ident)
			if !ok || !isInferProbeName(id.Name) {
				return true
			}
			site, ok := registry.lookup(id.Name)
			if !ok {
				return true
			}
			if tv, ok := info.Types[call]; ok && tv.Type != nil {
				out[site.el] = tv.Type
			}
			return true
		})
	}
}

// harvestEmbeddedElements resolves the probe calls inside each
// GoWithElements-embedded value's inline IIFE (see buildSkeleton) back onto
// that value's markup list. Each such IIFE is a func literal whose FIRST
// statement is the marker call `_gsxelem(N)`, where N indexes `markups` in
// source order; the marker is what distinguishes an embedded-value probe IIFE
// from any func literal the user wrote verbatim in the surrounding Go. For
// each marked IIFE, harvestBody runs over its body with that entry's markup
// list — the SAME resolution a component body gets, so a mistyped
// interpolation/prop inside an embedded element or fragment resolves (and is
// diagnosed) identically.
func harvestEmbeddedElements(f *goast.File, markups [][]gsxast.Markup, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr, registry *inferRegistry) {
	if len(markups) == 0 {
		return
	}
	goast.Inspect(f, func(node goast.Node) bool {
		fl, ok := node.(*goast.FuncLit)
		if !ok || fl.Body == nil || len(fl.Body.List) == 0 {
			return true
		}
		es, ok := fl.Body.List[0].(*goast.ExprStmt)
		if !ok {
			return true
		}
		call, ok := es.X.(*goast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*goast.Ident)
		if !ok || id.Name != "_gsxelem" || len(call.Args) != 1 {
			return true
		}
		lit, ok := call.Args[0].(*goast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return true
		}
		idx, err := strconv.Atoi(lit.Value)
		if err != nil || idx < 0 || idx >= len(markups) {
			return true
		}
		harvestBody(fl.Body, markups[idx], info, out, exprOut, registry)
		return true
	})
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

// buildSigTypeRefs pairs each navigable signature type/name region in component
// c — parameter types, type-parameter names/constraints, and (for a method
// component) its receiver type — with the corresponding type-checked skeleton
// expression, for the LSP's go-to-definition / hover. Parameter types live in
// the generated props struct's fields (in param order), or, for a BYO component,
// in the sole func parameter; the receiver type is the skeleton method's
// receiver. Returns nil when c's skeleton shape cannot be located (a
// skipped/stub component) or it carries no navigable types.
func buildSigTypeRefs(gf *goast.File, c *gsxast.Component, byo *byoData) []SigTypeRef {
	fd := funcDeclForKey(gf, componentKey(c))
	if fd == nil {
		return nil
	}
	var refs []SigTypeRef

	// Type-parameter names and constraints. The skeleton emits the type-param
	// list verbatim, so offsets from a synthetic parse of c.TypeParams align with
	// the matching skeleton TypeParams fields.
	if c.TypeParams != "" && c.TypeParamsPos.IsValid() && fd.Type.TypeParams != nil {
		if tpl, tpFset, err := parseTypeParamFieldList(c.TypeParams); err == nil && tpl != nil {
			off := func(p token.Pos) int { return tpFset.Position(p).Offset - len(typeParamSynthPrefix) }
			for i, field := range tpl.List {
				if i >= len(fd.Type.TypeParams.List) {
					break
				}
				skelField := fd.Type.TypeParams.List[i]
				for j, name := range field.Names {
					if j >= len(skelField.Names) {
						break
					}
					refs = append(refs, SigTypeRef{
						GSXPos:  c.TypeParamsPos + token.Pos(off(name.Pos())),
						Len:     len(name.Name),
						SkelTyp: skelField.Names[j],
					})
				}
				if field.Type != nil && skelField.Type != nil {
					refs = append(refs, SigTypeRef{
						GSXPos:  c.TypeParamsPos + token.Pos(off(field.Type.Pos())),
						Len:     off(field.Type.End()) - off(field.Type.Pos()),
						SkelTyp: skelField.Type,
					})
				}
			}
		}
	}

	// Parameter types.
	if params, err := parseParams(c.Params); err == nil && len(params) > 0 {
		if skel := paramSkelTypes(gf, c, fd, params, byo); skel != nil {
			for i, p := range params {
				refs = append(refs, SigTypeRef{
					GSXPos:  c.ParamsPos + token.Pos(p.typeOff),
					Len:     p.typeLen,
					SkelTyp: skel[i],
				})
			}
		}
	}

	// Method receiver type. The skeleton emits the receiver clause verbatim, so
	// its bytes match the .gsx; bridge into it like a param type. (Go forbids
	// methods on non-local types, so a receiver type is always same-package.)
	if c.Recv != "" && c.RecvPos.IsValid() && fd.Recv != nil && len(fd.Recv.List) == 1 {
		if off, length, ok := recvTypeSpan(c.Recv); ok {
			refs = append(refs, SigTypeRef{
				GSXPos:  c.RecvPos + token.Pos(off),
				Len:     length,
				SkelTyp: fd.Recv.List[0].Type,
			})
		}
	}
	return refs
}

// paramSkelTypes returns the skeleton type expression for each of c's params, in
// declaration order: the BYO component's sole func parameter type, or the
// generated props struct's first len(params) field types. Returns nil when the
// skeleton shape cannot be located.
func paramSkelTypes(gf *goast.File, c *gsxast.Component, fd *goast.FuncDecl, params []param, byo *byoData) []goast.Expr {
	if fd.Type.Params == nil || len(fd.Type.Params.List) != 1 {
		return nil
	}
	if _, isByo := byo.structTypeName(componentKey(c)); isByo {
		// BYO: the sole skeleton parameter type IS the author struct type the user
		// navigates to (e.g. `Form` in `func C(_gsxp Form)`).
		if len(params) != 1 {
			return nil
		}
		return []goast.Expr{fd.Type.Params.List[0].Type}
	}
	// Normal: the skeleton param is `_gsxp <PropsName>`; the props struct's first
	// len(params) fields carry the param types in declaration order.
	typeName := recvTypeIdent(fd.Type.Params.List[0].Type)
	if typeName == "" {
		return nil
	}
	st := structByName(gf, typeName)
	if st == nil || st.Fields == nil || len(st.Fields.List) < len(params) {
		return nil
	}
	skel := make([]goast.Expr, len(params))
	for i := range params {
		skel[i] = st.Fields.List[i].Type
	}
	return skel
}

// recvTypeSpan parses a method-component receiver clause (e.g. "(p *Page)") and
// returns the byte offset and length of the receiver TYPE within that clause
// string (e.g. "*Page"), for locating it in the .gsx. ok is false if the clause
// does not parse as a single receiver.
func recvTypeSpan(recv string) (off, length int, ok bool) {
	src := strings.TrimSpace(recv)
	if src == "" {
		return 0, 0, false
	}
	const prefix = "package _\nfunc "
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+src+" _m() {}", 0)
	if err != nil {
		return 0, 0, false
	}
	fn, isFn := f.Decls[0].(*goast.FuncDecl)
	if !isFn || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return 0, 0, false
	}
	t := fn.Recv.List[0].Type
	tStart := fset.Position(t.Pos()).Offset
	tEnd := fset.Position(t.End()).Offset
	return tStart - len(prefix), tEnd - tStart, true
}

// funcDeclForKey returns the skeleton FuncDecl whose component key matches key,
// or nil. Used to locate a component's generated signature in its skeleton file.
func funcDeclForKey(gf *goast.File, key string) *goast.FuncDecl {
	for _, d := range gf.Decls {
		if fd, ok := d.(*goast.FuncDecl); ok && funcDeclKey(fd) == key {
			return fd
		}
	}
	return nil
}

// structByName returns the *goast.StructType of the top-level `type name struct
// {…}` declaration in gf, or nil. Used to locate a component's generated props
// struct so its field types (the param types) can be navigated.
func structByName(gf *goast.File, name string) *goast.StructType {
	for _, d := range gf.Decls {
		gd, ok := d.(*goast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*goast.TypeSpec)
			if ok && ts.Name.Name == name {
				if st, ok := ts.Type.(*goast.StructType); ok {
					return st
				}
			}
		}
	}
	return nil
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
	catAnyMixed // type param whose non-tilde type set mixes renderable basic kinds → runtime dispatch
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
	if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
		return classifyTypeParam(tp)
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

// classifyTypeParam maps a type parameter to a render category from its
// constraint's normalized term set (typeparams.NormalTerms — real
// normalization of unions/embeddings, not an approximation).
//
//   - every term classifies to the SAME basic-kind category → that category:
//     the static conversions the emitter writes (string(v), int64(v), …)
//     compile for the whole type set, tilde or not.
//   - terms mix renderable categories, are ALL non-tilde, AND every term is
//     runtime-dispatchable (isRuntimeDispatchableTerm: an unnamed predeclared
//     type, an unnamed []byte, or a Stringer) → catAnyMixed: anyRenderString's
//     dynamic type switch matches every term in the set, so the runtime
//     dispatch (Writer.TextAny/AttrAny) is total.
//   - any ~term in a mixed set, or any non-tilde term that is NOT
//     runtime-dispatchable (e.g. a named scalar like `type Slug string`,
//     which classifies via Underlying but has no case in anyRenderString's
//     exact-type switch) → catUnsupported: reject statically rather than
//     fail mid-render.
//   - a term classifying to catNode/catNodeSlice/catStringer contributes no
//     METHOD to T (term ≠ embedded method), so it has no static call path;
//     Stringer terms are still fine in the runtime switch, Node terms are not
//     (rendering needs ctx). Method-BASED constraints (T fmt.Stringer,
//     T gsx.Node) never reach here — classify's method-set checks above run
//     first and see constraint methods via types.NewMethodSet.
func classifyTypeParam(tp *types.TypeParam) category {
	terms, err := typeparams.NormalTerms(tp)
	if err != nil || len(terms) == 0 {
		return catUnsupported // empty/invalid type set, or `any`
	}
	uniform := true
	hasTilde := false
	allDispatchable := true
	var first category
	for i, tm := range terms {
		c := classify(tm.Type())
		switch c {
		case catUnsupported, catNode, catNodeSlice:
			return catUnsupported
		}
		if tm.Tilde() {
			hasTilde = true
		}
		if !isRuntimeDispatchableTerm(tm.Type()) {
			allDispatchable = false
		}
		if i == 0 {
			first = c
		} else if c != first {
			uniform = false
		}
	}
	if uniform && first != catStringer {
		return first
	}
	if hasTilde || !allDispatchable {
		return catUnsupported
	}
	return catAnyMixed
}

// isRuntimeDispatchableTerm reports whether a mixed-constraint term's type
// has a matching case in anyRenderString's dynamic type switch (writer.go):
// either an UNNAMED predeclared type (*types.Basic — string, int, float64,
// …), the unnamed []byte (unnamed slice of unnamed byte), or any type
// implementing fmt.Stringer (matched via anyRenderString's `case
// fmt.Stringer`, which catches named Stringer types too). A named type is
// none of these — anyRenderString only has cases for the exact predeclared
// types, not arbitrary named types with matching Underlying — so it must
// disqualify the term's constraint from the mixed runtime-dispatch path.
// That goes for named types at EITHER level of a slice term: a defined
// `type Bytes []byte` (named slice — Unalias(t) is *types.Named, not
// *types.Slice) and a defined `type MyByte byte` element (`[]MyByte` is a
// distinct type from []byte at runtime) both fail. The element check uses
// Unalias, NOT Underlying: an alias `type B = byte` keeps []B identical to
// []byte (dispatchable), while Underlying would wrongly admit defined types.
func isRuntimeDispatchableTerm(t types.Type) bool {
	if classify(t) == catStringer {
		return true
	}
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		return true
	case *types.Slice:
		b, ok := types.Unalias(u.Elem()).(*types.Basic)
		return ok && b.Kind() == types.Byte
	}
	return false
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
		case *gsxast.EmbeddedInterp:
			// Holes first (matching emitProbes' order), then the node itself
			// ONLY when it carries a whole-literal pipeline — a Stages-less
			// literal renders per-segment and needs no node-level type.
			collectExprs(t.Segments, out)
			if len(t.Stages) > 0 {
				*out = append(*out, t)
			}
		case *gsxast.Element:
			if isComponentTag(t.Tag) {
				// Child component: collect ExprAttr nodes (prop values) first, then
				// OrderedPair nodes (pair values, one per pair per OrderedAttrsAttr),
				// then class-attr CF arms + plain parts (walkClassAttrs, recursing
				// CondAttr), then cond-attr BRANCH ExprAttr values (walkBranchAttrExprs),
				// then slot content (markup-attr values and children). emitProbes emits
				// _gsxuseq/_gsxuse probes in the SAME order — ExprAttrs, pairs, parts,
				// branch ExprAttrs, then slot content — so the k-th probe aligns with
				// the k-th node for ALL child-component branches. The ExprAttr types in
				// resolved let genChildComponent detect and hoist (T, error) tuple-valued
				// props; the OrderedPair types let it detect and hoist tuple pair values;
				// the branch ExprAttr / class-part types let a cond-attr branch hoist its
				// own (T, error) values (Task 3 consumer).
				for _, a := range t.Attrs {
					if ea, ok := a.(*gsxast.ExprAttr); ok {
						*out = append(*out, ea)
					}
				}
				// Collect pair nodes AFTER all ExprAttrs, in attr source order then
				// pair order — matching the emitProbes ordering exactly.
				for _, a := range t.Attrs {
					if oa, ok := a.(*gsxast.OrderedAttrsAttr); ok {
						for i := range oa.Pairs {
							*out = append(*out, &oa.Pairs[i])
						}
					}
				}
				// Collect *ValueArm nodes for CF arms and *ClassPart nodes for
				// unconditional plain parts AFTER all OrderedPair nodes — matching the
				// _gsxuseq probes emitProbes emits after the pair probes (the shared
				// walkClassAttrs recurses CondAttr on both sides).
				// classEntryExpr reads resolved[arm] for (T, error) CF-arm unwrap and
				// resolved[part] for plain-part tuple unwrap.
				walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
					for i := range ca.Parts {
						if ca.Parts[i].CF != nil {
							for _, arm := range valueFormArms(ca.Parts[i].CF) {
								*out = append(*out, arm)
							}
						} else if ca.Parts[i].Cond == "" && ca.Parts[i].CSSSegments == nil {
							*out = append(*out, &ca.Parts[i])
						}
					}
				})
				// Collect ExprAttr nodes nested in a component cond-attr branch
				// (`{ if C { attr={expr} } }`) AFTER the parts pass — the leading
				// ExprAttr pass above is top-level-only, and childPropsLiteral embeds
				// the whole AttrsCond(...) expression in the props probe without a
				// per-value harvest probe, so branch ExprAttr values would otherwise
				// have no resolved entry. emitProbes emits the matching _gsxuseq probes
				// in the SAME position and Then→Else order. (Branch class parts / CF
				// arms are already covered by the walkClassAttrs parts pass above, which
				// recurses CondAttr, so they are NOT re-collected here.)
				walkBranchAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
					*out = append(*out, ea)
				})
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					collectExprs(value, out)
				})
				// Collect each braced-attr whole-literal pipeline node AFTER the
				// markup-attr/hole nodes above — emitProbes emits the matching
				// node-level _gsxuse probe in the SAME position (via
				// walkEmbeddedAttrStages), so the k-th probe stays aligned.
				walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
					*out = append(*out, ea)
				})
				collectExprs(t.Children, out)
				continue
			}
			// Collect each attr-expr (top-level and CondAttr-nested) in canonical
			// order, before the element's children — emitProbes walks identically.
			walkAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
				*out = append(*out, ea)
			})
			// Then each element-spread expr, AFTER all ExprAttrs — emitProbes emits the
			// _gsxuseq spread probes in the SAME position (after the _gsxuse ExprAttr
			// probes), so the k-th spread node maps to the k-th spread probe.
			walkSpreadAttrs(t.Attrs, func(sa *gsxast.SpreadAttr) {
				*out = append(*out, sa)
			})
			// Collect ValueArm nodes for value-form CF parts, and *ClassPart nodes
			// for unconditional plain parts, in source order: for each ClassAttr in
			// attr order, for each part, in arm source order for CF parts. emitProbes
			// emits _gsxuse probes in the SAME order so the k-th probe aligns with
			// the k-th node, populating resolved[arm] for hoistValueCF's unwrap and
			// resolved[part] for (T, error) auto-unwrap on plain unconditional parts.
			// The liveness path (walkLivenessAttrExprs) skips unconditional plain
			// parts (they now get _gsxuse probes which also serve as liveness refs).
			// walkClassAttrs recurses CondAttr Then/Else in lockstep with emitProbes,
			// so class attrs nested in a conditional attr group collect too.
			walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
				for i := range ca.Parts {
					if ca.Parts[i].CF != nil {
						for _, arm := range valueFormArms(ca.Parts[i].CF) {
							*out = append(*out, arm)
						}
					} else if ca.Parts[i].Cond == "" && ca.Parts[i].CSSSegments == nil {
						// Unconditional plain part: harvest its type for (T, error) unwrap.
						*out = append(*out, &ca.Parts[i])
					}
				}
			})
			// Then each explicit JS attribute literal (e.g. x-data=js`…@{x}…`) interp, in
			// attr source order — emitProbes walks identically (same walkMarkupAttrs).
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				collectExprs(value, out)
			})
			// Collect each braced-attr whole-literal pipeline node AFTER the
			// markup-attr/hole nodes above — matching emitProbes' ordering.
			walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
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

// walkBranchAttrExprs invokes fn for each *ExprAttr nested inside a component
// cond-attr group (`{ if C { attr={expr} } else { … } }`) — the Then attr-exprs
// then the Else attr-exprs of every *CondAttr, recursing nested *CondAttrs via
// walkAttrExprs so an else-if chain is visited in order. TOP-LEVEL ExprAttrs are
// deliberately NOT visited (they are collected/probed separately in the
// component case's leading ExprAttr pass). Branch class parts and value-form CF
// arms are ALSO not visited here — the shared walkClassAttrs already recurses
// CondAttr Then/Else, so those branch positions are covered by the component
// case's parts pass; only branch ExprAttr values need this dedicated walk. It is
// the SINGLE walk shared by collectExprs (which appends each branch ExprAttr
// node AFTER the parts pass) and emitProbes (which emits one _gsxuseq harvest
// probe per branch ExprAttr in the SAME position), so the k-th branch-ExprAttr
// probe always maps to the k-th collected branch-ExprAttr node.
func walkBranchAttrExprs(attrs []gsxast.Attr, fn func(*gsxast.ExprAttr)) {
	for _, a := range attrs {
		if ca, ok := a.(*gsxast.CondAttr); ok {
			walkAttrExprs(ca.Then, fn)
			walkAttrExprs(ca.Else, fn)
		}
	}
}

// walkSpreadAttrs invokes fn for each *SpreadAttr in an element's attr list, in
// canonical source order (recursing *CondAttr Then→Else, like walkAttrExprs). It is
// the SINGLE walk shared by collectExprs (which appends each spread node AFTER all
// the element's ExprAttrs) and emitProbes (which emits one _gsxuseq harvest probe
// per spread, AFTER all the element's _gsxuse ExprAttr probes), so the k-th spread
// probe always maps to the k-th collected spread node.
func walkSpreadAttrs(attrs []gsxast.Attr, fn func(*gsxast.SpreadAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.SpreadAttr:
			fn(at)
		case *gsxast.CondAttr:
			walkSpreadAttrs(at.Then, fn)
			walkSpreadAttrs(at.Else, fn)
		}
	}
}

// walkClassAttrs invokes fn for each *ClassAttr in an element's attr list, in
// canonical source order (recursing *CondAttr Then→Else, like walkAttrExprs).
// It is the SINGLE walk shared by collectExprs (which appends each CF-arm /
// unconditional-plain-part node) and emitProbes (which emits one _gsxuse probe
// per such node), so the k-th probe always maps to the k-th collected node —
// including for class/style attrs nested inside a conditional attr group.
func walkClassAttrs(attrs []gsxast.Attr, fn func(*gsxast.ClassAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ClassAttr:
			fn(at)
		case *gsxast.CondAttr:
			walkClassAttrs(at.Then, fn)
			walkClassAttrs(at.Else, fn)
		}
	}
}

// walkLivenessAttrExprs invokes fn with a liveness *statement* for each Go
// fragment in a ClassAttr (recursing CondAttr) — the attr exprs that
// walkAttrExprs does NOT yield, and that carry no type harvest. (SpreadAttr
// exprs ARE harvested now, via walkSpreadAttrs + _gsxuseq, which doubles as
// their liveness reference, so they are no longer handled here.) Codegen emits
// these verbatim (gsx.Class/ClassIf/StyleString) so they need no type harvest,
// but the skeleton must still REFERENCE them or a var used ONLY here (e.g. a
// for-loop var in `class={ "on": v }`) is rejected as "declared and not used".
// Plain exprs become `_ = (expr)` via fn; guard conds (a ClassPart's `: cond`,
// incl. on css literals) and in-tag conditional-attribute conds (*CondAttr) are
// yielded to fnCond with their owning node and source position, so the caller
// can emit an `if cond {\n}` statement and record a ctrlOff entry (the LSP's
// CtrlMap bridge); a value-form CF part is yielded whole to fnCF, which emits
// its empty-bodied control statement(s) (see emitValueCFControl — its tag and
// case lists are only legal in statement position) the same way. All forms are
// invisible to the k-th-probe→k-th-node type-harvest alignment, unlike _gsxuse.
// A ClassPart carrying a `|>` pipeline must reference the LOWERED
// expression — the SAME lowerPipe output emit produces — so type resolution and
// import harvest match emit exactly (emit ≡ probe). table lowers each pipeline and
// usedFilters accumulates the referenced filter packages (alias→pkgPath) so the
// skeleton imports them under the same reserved aliases the emitter records. A
// lowering failure (an unknown filter) is tolerated here: the probe falls back to
// referencing the bare seed so type-checking proceeds to the POSITIONED
// unknown-filter diagnostic generateFile reports (the probe's bare error must not
// pre-empt it). The guard Cond is never piped, so it is referenced verbatim.
func walkLivenessAttrExprs(attrs []gsxast.Attr, table filterTable, usedFilters map[string]string, fn func(stmt string), fnCF func(cf *gsxast.ValueCF), fnCond func(node gsxast.Node, cond string, condPos token.Pos)) {
	ref := func(expr string) {
		fn("_ = (" + expr + ")")
	}
	emit := func(seed string, stages []gsxast.PipeStage) {
		if strings.TrimSpace(seed) == "" {
			return
		}
		if len(stages) == 0 {
			ref(strings.TrimSpace(seed))
			return
		}
		lowered, used, err := lowerPipe(seed, stages, table, probePipeWrap)
		if err != nil {
			// Unknown filter: reference the bare seed so the skeleton still type-checks
			// (and stays "used"); the positioned diagnostic fires in generateFile.
			ref(strings.TrimSpace(seed))
			return
		}
		maps.Copy(usedFilters, used)
		ref(lowered)
	}
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ClassAttr:
			// Index (not range-copy) so fnCond is keyed by the SAME *ClassPart
			// pointer ast.Inspect yields — the identity the LSP looks up in CtrlMap.
			for i := range at.Parts {
				p := &at.Parts[i]
				if p.CSSSegments != nil {
					fnCond(p, p.Cond, p.CondPos)
					continue
				}
				if p.CF == nil && p.Cond == "" {
					// Unconditional plain parts now get _gsxuse probes in emitProbes
					// (which also provide liveness). Skip here to avoid a duplicate
					// `_ = (expr)` that would fail for (T, error) multi-return calls.
					continue
				}
				if p.CF != nil {
					fnCF(p.CF)
					continue
				}
				emit(p.Expr, p.Stages)
				fnCond(p, p.Cond, p.CondPos)
			}
		case *gsxast.CondAttr:
			fnCond(at, at.Cond, at.CondPos)
			walkLivenessAttrExprs(at.Then, table, usedFilters, fn, fnCF, fnCond)
			walkLivenessAttrExprs(at.Else, table, usedFilters, fn, fnCF, fnCond)
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
		case *gsxast.EmbeddedAttr:
			// Explicit embedded-language attribute values carry @{ } interps that
			// need types — yield their Segments so they are collected and probed in
			// the SAME order by collectExprs and emitProbes.
			fn(t.Segments)
		case *gsxast.ClassAttr:
			for i := range t.Parts {
				if t.Parts[i].CSSSegments != nil {
					fn(t.Parts[i].CSSSegments)
				}
			}
		case *gsxast.CondAttr:
			walkMarkupAttrs(t.Then, fn)
			walkMarkupAttrs(t.Else, fn)
		}
	}
}

// walkEmbeddedAttrStages invokes fn for each *EmbeddedAttr in an element's
// attr list whose Stages (whole-literal `|> f` pipeline) is non-empty, in
// canonical source order (recursing *CondAttr Then→Else, like
// walkAttrExprs). A Stages-less EmbeddedAttr is skipped here — its holes are
// already collected/probed via walkMarkupAttrs, and it needs no node-level
// type. It is the SINGLE walk shared by collectExprs (which appends each
// such node, AFTER the element's walkMarkupAttrs pass) and emitProbes
// (which emits one _gsxuse probe per node, assembling+lowering its Segments
// the SAME way via embeddedProbeSeed+probeExpr), so the k-th probe always
// maps to the k-th collected node.
func walkEmbeddedAttrStages(attrs []gsxast.Attr, fn func(*gsxast.EmbeddedAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.EmbeddedAttr:
			if len(at.Stages) > 0 {
				fn(at)
			}
		case *gsxast.CondAttr:
			walkEmbeddedAttrStages(at.Then, fn)
			walkEmbeddedAttrStages(at.Else, fn)
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
		case *gsxast.EmbeddedInterp:
			// A body backtick literal has no single seed expr (its Segments'
			// own holes are separately collected as *Interp nodes, handled by
			// the case above); only its whole-literal pipeline's filter args
			// need this pass — e.g. a param used only as `|> join(sep)`.
			stages = v.Stages
		case *gsxast.EmbeddedAttr:
			// Same reasoning as EmbeddedInterp: only the whole-literal
			// pipeline's filter args need collecting here.
			stages = v.Stages
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
// composable-class part Expr+Cond (and value-form CF switch-tag/if-cond + arm
// exprs), element-spread Expr, conditional-attr Cond, and — recursing into a
// *CondAttr's Then/Else — the same for nested attrs. (Nested *ExprAttr exprs are
// bound via the componentExprs path in usedParams, but a param used ONLY inside a
// CondAttr branch's expr-attr value is still bound because componentExprs/
// collectExprs now also recurse CondAttr; the Cond and nested class/spread
// fragments are bound here.)
func collectAttrSrc(attrs []gsxast.Attr, add func(string)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ClassAttr:
			for _, p := range at.Parts {
				if p.CSSSegments != nil {
					if p.Cond != "" {
						add(p.Cond)
					}
					continue
				}
				if p.CF != nil {
					addValueCFSrc(p.CF, add)
					continue
				}
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
		case *gsxast.OrderedAttrsAttr:
			for _, pr := range at.Pairs {
				add(pr.Value)
			}
		case *gsxast.CondAttr:
			add(at.Cond)
			collectAttrSrc(at.Then, add)
			collectAttrSrc(at.Else, add)
		}
	}
}

// addValueCFSrc feeds the verbatim-emitted Go fragments from a value-form CF
// (if/switch inside a class/style list) to add. The switch tag, case lists,
// if/else-if conditions, and arm expressions are all emitted verbatim, so any
// identifiers they reference must be in scope as locals.
func addValueCFSrc(cf *gsxast.ValueCF, add func(string)) {
	if cf.If != nil {
		addValueIfSrc(cf.If, add)
		return
	}
	if cf.Switch != nil {
		add(cf.Switch.Tag)
		for _, c := range cf.Switch.Cases {
			if c.List != "" {
				add(c.List)
			}
			addValueArmSrc(c.Value, add)
		}
	}
}

// emitCondLiveness writes the empty-bodied `if <cond> {\n}` statement that
// keeps a guard condition's identifiers live in the skeleton, with a
// compensated //line and a ctrlOff entry keyed by node — the CtrlMap bridge
// the LSP uses for go-to-definition/hover inside the condition. Used for
// in-tag conditional-attribute conds (*CondAttr), class/style `: cond` guards
// (*ClassPart), and value-form if conditions (*ValueIf).
func emitCondLiveness(sb *strings.Builder, fset *token.FileSet, node gsxast.Node, cond string, condPos token.Pos, ctrlOff map[gsxast.Node]int) {
	if strings.TrimSpace(cond) == "" {
		return
	}
	emitSkeletonClauseLine(sb, fset, condPos, len("if "))
	ctrlOff[node] = sb.Len() + len("if ")
	fmt.Fprintf(sb, "if %s {\n}\n", cond)
}

// emitValueCFControl writes the empty-bodied skeleton statement(s) that
// reference a value-form CF part's control expressions in their natural
// grammatical positions. Statement position (not `_ = (expr)`) is required —
// case lists may contain `nil`, type names under a `.(type)` tag, or untyped
// constants that only fit the tag's type, none of which are legal as a bare
// expression. Arm expressions are excluded: emitProbes harvests them with
// _gsxuse so value-form arms preserve (T, error) auto-unwrapping without
// disturbing probe order.
//
// The if-form emits each condition in the chain as its OWN `if <cond> {\n}`
// statement (not an `else if` chain — the conds are independent bool
// expressions, so type-checking is identical) so every condition carries a
// compensated //line and a ctrlOff entry keyed by its *ValueIf. The switch
// form records ctrlOff for the tag (keyed by the *ValueSwitch) and each case
// list (keyed by its *ValueSwitchCase) the same way. That is the same CtrlMap
// bridge IfMarkup uses, making go-to-definition (and positioned type errors)
// work inside value-form control expressions.
func emitValueCFControl(sb *strings.Builder, fset *token.FileSet, cf *gsxast.ValueCF, ctrlOff map[gsxast.Node]int) {
	if cf.If != nil {
		for vi := cf.If; vi != nil; vi = vi.ElseIf {
			emitCondLiveness(sb, fset, vi, vi.Cond, vi.CondPos, ctrlOff)
		}
		return
	}
	if vs := cf.Switch; vs != nil {
		if strings.TrimSpace(vs.Tag) != "" {
			emitSkeletonClauseLine(sb, fset, vs.TagPos, len("switch "))
			ctrlOff[vs] = sb.Len() + len("switch ")
		}
		fmt.Fprintf(sb, "switch %s {\n", strings.TrimSpace(vs.Tag))
		for _, c := range vs.Cases {
			if c.Default {
				sb.WriteString("default:\n")
				continue
			}
			emitSkeletonClauseLine(sb, fset, c.ListPos, len("case "))
			ctrlOff[c] = sb.Len() + len("case ")
			fmt.Fprintf(sb, "case %s:\n", c.List)
		}
		sb.WriteString("}\n")
	}
}

func addValueIfSrc(vi *gsxast.ValueIf, add func(string)) {
	add(vi.Cond)
	addValueArmSrc(vi.Then, add)
	if vi.ElseIf != nil {
		addValueIfSrc(vi.ElseIf, add)
	}
	if vi.Else != nil {
		addValueArmSrc(vi.Else, add)
	}
}

func addValueArmSrc(arm *gsxast.ValueArm, add func(string)) {
	if arm == nil {
		return
	}
	add(arm.Expr)
	for _, st := range arm.Stages {
		if st.Args != "" {
			add(st.Args)
		}
	}
}

// valueFormArms returns the arm value-expression nodes of a value-form part in
// source order, for type-harvest alignment. For an if-form, the order is:
// Then, then the ElseIf chain (recursively), then Else. For a switch, each
// case's Value in case-declaration order. nil arms are skipped.
func valueFormArms(cf *gsxast.ValueCF) []*gsxast.ValueArm {
	var out []*gsxast.ValueArm
	if cf.If != nil {
		var walk func(vi *gsxast.ValueIf)
		walk = func(vi *gsxast.ValueIf) {
			if vi.Then != nil {
				out = append(out, vi.Then)
			}
			if vi.ElseIf != nil {
				walk(vi.ElseIf)
			}
			if vi.Else != nil {
				out = append(out, vi.Else)
			}
		}
		walk(cf.If)
		return out
	}
	for _, c := range cf.Switch.Cases {
		if c.Value != nil {
			out = append(out, c.Value)
		}
	}
	return out
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
// it yields the param name's .gsx position (for go-to-definition). typeOff/typeLen
// are the byte span of the param's TYPE within that same trimmed source (for
// go-to-definition on the type's identifiers); typeSrc is the verbatim type text
// (emitted into the skeleton so the LSP can bridge a cursor into it by relative
// offset, exactly as it does for interpolation expressions).
type param struct {
	name, typ string
	nameOff   int
	typeOff   int
	typeLen   int
	typeSrc   string
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
	synth := paramSynthPrefix + src + ") {}"
	f, err := parser.ParseFile(fset, "", synth, 0)
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
		// Byte span of the type within the synthetic source → its offset/length in
		// the trimmed param source (subtract the prefix). typeSrc is the verbatim
		// type text, kept identical to the .gsx so the skeleton can copy it through
		// and the LSP can bridge a cursor into it by relative offset.
		tStart := fset.Position(field.Type.Pos()).Offset
		tEnd := fset.Position(field.Type.End()).Offset
		typeOff := tStart - len(paramSynthPrefix)
		typeSrc := synth[tStart:tEnd]
		for _, nm := range field.Names {
			out = append(out, param{
				name:    nm.Name,
				typ:     typ,
				nameOff: fset.Position(nm.Pos()).Offset - len(paramSynthPrefix),
				typeOff: typeOff,
				typeLen: tEnd - tStart,
				typeSrc: typeSrc,
			})
		}
	}
	return out, nil
}

// typeParamSynthPrefix is the synthetic wrapper prepended to a component's raw
// type-param source so it parses as a generic func declaration. Offsets in the
// parsed result relate to the trimmed source by subtracting len(typeParamSynthPrefix).
const typeParamSynthPrefix = "package p\nfunc _["

// parseTypeParamFieldList parses a component's raw type-param source (the text
// between the signature's brackets) via the synthetic wrapper and returns the
// type-param field list plus the FileSet that resolves its positions. The
// FieldList is nil (with a nil error) for an empty or bracket-less list.
func parseTypeParamFieldList(src string) (*goast.FieldList, *token.FileSet, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil, nil
	}
	fset := token.NewFileSet()
	synth := typeParamSynthPrefix + src + "]() {}"
	f, err := parser.ParseFile(fset, "", synth, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("codegen: parse type params %q: %w", src, err)
	}
	fd, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok {
		return nil, nil, fmt.Errorf("codegen: parse type params %q: unexpected declaration shape", src)
	}
	return fd.Type.TypeParams, fset, nil
}

func parseTypeParamNames(src string) ([]string, error) {
	tpl, _, err := parseTypeParamFieldList(src)
	if err != nil || tpl == nil {
		return nil, err
	}
	var names []string
	for _, field := range tpl.List {
		for _, nm := range field.Names {
			names = append(names, nm.Name)
		}
	}
	return names, nil
}

func typeParamDecl(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	return "[" + src + "]"
}

func typeParamUse(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return "[" + strings.Join(names, ", ") + "]"
}

func typeArgUse(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	return "[" + src + "]"
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
			return fmt.Errorf("codegen: param name %q is reserved (explicit attribute forwarding)", p.name)
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
// typeParamNames/typeParamsDecl are the results of emitComponentSkeleton's
// SINGLE parseTypeParamNames call, hoisted to the top of that function and
// threaded through every call site — this function must not re-parse
// c.TypeParams itself (a second, guaranteed-to-fail parse used to swallow the
// error and fall through to a non-generic stub whose prop fields still
// referenced the undeclared type param). Callers pass (nil, "") when the
// type-param list itself failed to parse.
//
// CRITICAL: the stub props struct MUST mirror the Children/Attrs field synthesis
// that emitComponentSkeleton/genComponent use — otherwise a sibling that
// instantiates the bad component WITH CHILDREN will get a spurious "unknown field
// Children" type error from the overlay, masking the real diagnostic. We use the
// SAME gating (usesChildren / usesAttrs) on the body so the stub
// struct shape matches what siblings reference.
//
// omitFunc, when true, stops after emitting the props struct (if any) and
// skips the func/method declaration entirely — for a generic METHOD component
// on a toolchain whose go/parser rejects methods with type parameters, even
// this stub's `func (recv) Name[T ...](...)` signature would fail to parse.
func emitComponentStub(sb *strings.Builder, c *gsxast.Component, params []param, withRecv bool, recvTypeName string, typeParamNames []string, typeParamsDecl string, omitFunc bool) {
	// MIRROR emitComponentSkeleton's own propsName computation (and
	// genComponent's, at emit time): a method component's props struct is
	// named <RecvTypeName><Name>Props, not just <Name>Props. Every early-exit
	// stub above threads recvTypeName through so this holds even when the
	// stub is reached before (or instead of) the main propsName computation —
	// a mismatch here means a sibling call site's caller-side inference probe
	// (which always names the receiver-qualified props type, since it mirrors
	// the real emitter) resolves against an undeclared type, producing a
	// confusing "undefined: PageRowProps"-shaped hard type error that MASKS
	// the real, positioned diagnostic (e.g. unsupported-toolchain) instead of
	// coexisting with it — see TestGenericMethodGuardedCallSiteNoUndefinedSelector.
	propsName := c.Name + "Props"
	if withRecv && recvTypeName != "" {
		propsName = recvTypeName + c.Name + "Props"
	}
	typeParamsUse := typeParamUse(typeParamNames)
	// MIRROR emitComponentSkeleton: compute Children/Attrs gates from the body.
	hasChildren := usesChildren(c.Body)
	manual := usesAttrs(c.Body)
	// MIRROR emitComponentSkeleton line 384: hasProps gating.
	// When params is nil (parse failed), treat as len(params)==0 for gating.
	hasProps := c.Recv == "" || len(params) > 0 || hasChildren || manual
	// Track which field names the params already declare (e.g. a bad param named
	// "children" → field "Children") so we do not double-declare the synthesized
	// Children/Attrs fields and produce a "redeclared" type-error in the overlay.
	paramFields := make(map[string]bool, len(params))
	for _, p := range params {
		paramFields[fieldName(p.name)] = true
	}
	if hasProps {
		fmt.Fprintf(sb, "type %s%s struct {\n", propsName, typeParamsDecl)
		for _, p := range params {
			fmt.Fprintf(sb, "\t%s %s\n", fieldName(p.name), p.typ)
		}
		if hasChildren && !paramFields["Children"] {
			sb.WriteString("\tChildren _gsxrt.Node\n")
		}
		if manual && !paramFields["Attrs"] {
			sb.WriteString("\tAttrs _gsxrt.Attrs\n")
		}
		sb.WriteString("}\n")
	}
	if omitFunc {
		// A generic-method stub would itself fail to parse on a toolchain without
		// generic methods (see toolchainHasGenericMethods) — keep the props struct
		// (above, if any) so GoChunk imports stay used, but stop before emitting
		// any func/method declaration.
		return
	}
	if hasProps {
		if withRecv && c.Recv != "" {
			fmt.Fprintf(sb, "func %s %s%s(_gsxp %s%s) _gsxrt.Node { return nil }\n", c.Recv, c.Name, typeParamsDecl, propsName, typeParamsUse)
		} else {
			fmt.Fprintf(sb, "func %s%s(_gsxp %s%s) _gsxrt.Node { return nil }\n", c.Name, typeParamsDecl, propsName, typeParamsUse)
		}
	} else {
		if withRecv && c.Recv != "" {
			fmt.Fprintf(sb, "func %s %s%s() _gsxrt.Node { return nil }\n", c.Recv, c.Name, typeParamsDecl)
		} else {
			fmt.Fprintf(sb, "func %s%s() _gsxrt.Node { return nil }\n", c.Name, typeParamsDecl)
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
