package codegen

import (
	"fmt"
	"regexp"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// inferRegistry records every inference probe emitted into one file's
// skeleton. It replaces the old exported, declaring-side inference-helper
// convention: instead of ONE helper per generic component (shared by every
// call site, requiring every declared prop to be supplied), each OMITTED-
// type-arg tag gets its OWN caller-side helper named from a private,
// monotonically increasing counter (`_gsxinfer1`, `_gsxinfer2`, ...). Because
// the name is synthesized (never derived from user-visible identifiers), a
// same-package user function merely sharing the old naming prefix can no
// longer be mistaken for a probe (finding 3), and because each helper's
// parameter list is built from
// exactly the props SUPPLIED at that tag (not every declared prop), omitting
// an optional prop no longer disables inference for that tag (finding 5).
//
// One registry is constructed per file by buildSkeleton and threaded through
// emitComponentSkeleton/emitProbes as probes are emitted, then returned so
// module_importer.go can carry it alongside the file's compiled skeleton
// (compsByXGo, factsByXGo, ...) to harvest, which maps each probe's
// go/types-inferred instantiation back onto the *gsxast.Element it was
// emitted for.
type inferRegistry struct {
	n     int
	sites map[string]*inferSite // helper name "_gsxinfer3" -> site

	// funcs accumulates the hoisted, package-level helper func decls (one per
	// emitInferProbe call). A helper's own type parameters make it valid at
	// package scope regardless of where its tag appears (even inside a `for`/
	// `if` block), but the PROBE CALL passing the tag's actual supplied
	// expressions must stay INLINE at the tag's position — those expressions
	// may reference loop variables or other block-scoped locals only in scope
	// there. buildSkeleton appends funcs.String() at package level (after the
	// component skeletons) once every component's skeleton has been emitted.
	funcs strings.Builder
}

// inferSite is one recorded inference probe: the tag it was emitted for, the
// props-struct type name being inferred, and the probe call statement's byte
// span within the final skeleton string (file-relative, after buildSkeleton
// adjusts it past the component-skeleton prefix — mirrors ctrlOff's
// compBuf-relative-to-file-relative adjustment).
type inferSite struct {
	el        *gsxast.Element // the tag this probe infers for
	propsType string          // e.g. "ButtonProps" or "components.ButtonProps"
	span      inferSpan       // skeleton byte span of the emitted probe stmt (Task 8 uses it)
}

// inferSpan is a byte range into the skeleton string returned by buildSkeleton.
type inferSpan struct{ start, end int }

// newInferRegistry returns an empty registry ready to record probes for one
// file's skeleton.
func newInferRegistry() *inferRegistry {
	return &inferRegistry{sites: map[string]*inferSite{}}
}

// nextName returns the next probe helper name: "_gsxinfer1", "_gsxinfer2", ....
// Names are 1-indexed and unique within this registry (one per file).
func (r *inferRegistry) nextName() string {
	r.n++
	return fmt.Sprintf("_gsxinfer%d", r.n)
}

// record associates a probe helper name with its site.
func (r *inferRegistry) record(name string, s *inferSite) {
	r.sites[name] = s
}

// lookup finds the site recorded under name. Exact-name match ONLY — this is
// what makes the registry immune to the finding-3 attack (a user func merely
// sharing the old naming prefix never matches "_gsxinfer1").
func (r *inferRegistry) lookup(name string) (*inferSite, bool) {
	s, ok := r.sites[name]
	return s, ok
}

// isInferProbeNameRE matches EXACTLY the shape nextName produces: the literal
// prefix "_gsxinfer" followed by one or more digits, and nothing else. A
// user-declared identifier can never collide with this shape because gsx
// reserves the leading underscore + "gsx" prefix for its own synthesized
// names throughout the skeleton (see checkReservedParams/checkReservedRecvVar).
var isInferProbeNameRE = regexp.MustCompile(`^_gsxinfer[0-9]+$`)

// isInferProbeName reports whether name is a probe helper name synthesized by
// nextName. harvest uses this to find probe calls in a component's skeleton
// body without keying on anything a user could spell (unlike the old
// exported-helper naming convention, which any same-package func sharing that
// prefix would satisfy).
func isInferProbeName(name string) bool {
	return isInferProbeNameRE.MatchString(name)
}

// suppliedInDeclOrder filters params (a component's full declared parameter
// list) down to the subset whose field name appears in supplied, preserving
// the component's OWN declaration order. Declaration order (rather than the
// old code's alphabetical-by-field-name order) is simplest to produce here
// since params is already in that order; Go's generic type inference unifies
// each type parameter across every argument regardless of argument order, so
// the ordering choice has no effect on which types are inferred.
func suppliedInDeclOrder(params []param, supplied map[string]string) []param {
	var ordered []param
	for _, p := range params {
		if _, ok := supplied[fieldName(p.name)]; ok {
			ordered = append(ordered, p)
		}
	}
	return ordered
}

// emitInferProbe writes one per-site inference helper + probe statement for a
// tag whose type args were omitted. params is the TARGET component's full
// declared param list; supplied maps FIELD name -> the attr's probe arg
// expression for the props actually supplied at this tag (a subset is fine —
// omitted props are simply absent, mirroring a Go call that infers from
// whatever arguments it's given). typeParamsDecl/typeParamsUse are the target
// component's bracketed type-param declaration/use lists, rendered so they
// resolve in the CALLING file (same-package callers use them verbatim; a
// later task requalifies them for an imported target).
//
// The probe helper is a fresh, uniquely-named generic function:
//
//	func _gsxinferN[T ...](_gsxv0 <type0>, _gsxv1 <type1>, ...) PropsType[T] {
//		return PropsType[T]{}
//	}
//
// declared at PACKAGE level (written into r.funcs, hoisted into the skeleton
// separately from the component bodies — see the funcs field doc) so it type-
// checks independent of the tag's surrounding scope. The probe CALL,
//
//	_ = _gsxinferN(<supplied arg 0>, <supplied arg 1>, ...)
//
// is written inline into sb at the tag's current position (the same builder/
// scope emitProbes is already writing the tag's other probes into), so an
// argument expression referencing an enclosing loop variable or block-local
// still resolves. go/types instantiates T from these arguments exactly as it
// would for a real call, so a successful check's result type
// (info.Types[call].Type) is the fully-instantiated props type — harvest
// reads it back onto the recorded site's element.
//
// Returns false — emitting nothing — when no supplied prop mentions any
// declared field (an empty arg list gives go/types nothing to infer a type
// parameter from; the tag then falls through to its plain, uninstantiated
// composite-literal probe and surfaces the type-checker's own "without
// instantiation" error, which a later task rewrites into a positioned
// diagnostic).
func (r *inferRegistry) emitInferProbe(sb *strings.Builder, el *gsxast.Element,
	propsType, typeParamsDecl, typeParamsUse string,
	params []param, supplied map[string]string) bool {

	ordered := suppliedInDeclOrder(params, supplied)
	if len(ordered) == 0 {
		return false
	}
	name := r.nextName()

	fmt.Fprintf(&r.funcs, "func %s%s(", name, typeParamsDecl)
	for i, p := range ordered {
		if i > 0 {
			r.funcs.WriteString(", ")
		}
		fmt.Fprintf(&r.funcs, "_gsxv%d %s", i, p.typeSrc)
	}
	fmt.Fprintf(&r.funcs, ") %s%s { return %s%s{} }\n", propsType, typeParamsUse, propsType, typeParamsUse)

	start := sb.Len()
	fmt.Fprintf(sb, "_ = %s(", name)
	for i, p := range ordered {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(supplied[fieldName(p.name)])
	}
	sb.WriteString(")\n")
	r.record(name, &inferSite{el: el, propsType: propsType, span: inferSpan{start, sb.Len()}})
	return true
}
