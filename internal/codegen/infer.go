package codegen

import (
	"bytes"
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"maps"
	"regexp"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// genericSig is the ONE representation of an eligible generic component's
// caller-side-probe-relevant signature — built uniformly for a SAME-PACKAGE
// component (genericSigsFor over the package's own gsx files) and an
// IMPORTED one (genericSigsFor over a dep package's files, via
// module_importer.go's depPropFacts/fileFacts). This replaces the old
// bool-only genericProps map plus, for same-package components only, the
// declaring *gsxast.Component AST — Task 4 restores imported-component
// inference by giving BOTH cases the same struct and ONE emitter code path
// in emitProbes, branching only on whether requalification is needed.
type genericSig struct {
	typeParams string  // raw decl text as written in the declaring file ("T string | int")
	params     []param // the component's full declared param list (typeSrc in the DECLARING file's context)
	arity      int     // number of type params (Task 8's hint consumes this)

	// imports is the DECLARING FILE's own hoisted import specs — the
	// requalification engine's depImports input for an IMPORTED caller (see
	// requalifyTypeExpr/requalifyTypeParams). A same-package caller never
	// requalifies (the calling file IS the declaring file, so params/
	// typeParams already resolve verbatim), so this field goes unused there;
	// it is still populated uniformly so one construction path serves both.
	imports []importSpec
}

// inferRegistry records every inference probe emitted into one file's
// skeleton. It replaces the old exported, declaring-side inference-helper
// convention: instead of ONE helper per generic component (shared by every
// call site, requiring every declared prop to be supplied), each OMITTED-
// type-arg tag gets its OWN caller-side helper named from a private,
// monotonically increasing counter (`_gsxinfer1`, `_gsxinfer2`, ...). Because
// the name is synthesized (never derived from user-visible identifiers), a
// same-package user function merely sharing the old naming prefix can no
// longer be mistaken for a probe (finding 3), and because each helper's
// parameter list is built from exactly the props SUPPLIED at that tag (not
// every declared prop), omitting an optional prop no longer disables
// inference for that tag (finding 5).
//
// One registry is constructed per file by buildSkeleton and threaded through
// emitComponentSkeleton/emitProbes as probes are emitted, then returned so
// module_importer.go can carry it alongside the file's compiled skeleton
// (compsByXGo, factsByXGo, ...) to harvest, which maps each probe's
// go/types-inferred instantiation back onto the *gsxast.Element it was
// emitted for.
//
// POSITION MAPPING — probe call statements are emitted UNDER the enclosing
// tag's //line directive ON PURPOSE (the emitSkeletonLine(sb, fset, t.Pos())
// preceding the tag's props-literal probe in emitProbes stays in effect
// through the probe call). This is load-bearing: module_importer.go's
// diagnostic loop drops any type error whose //line-ADJUSTED position still
// names the synthetic .x.go overlay (the `strings.HasSuffix(p.Filename,
// ".x.go")` filter in its type-error loop), so a //line-free probe would
// make every inference-failure diagnostic silently vanish instead of
// surfacing at the tag. A consumer that needs a probe error's RAW skeleton
// offset (e.g. to match it against inferSite.span) must resolve the error
// position with fset.PositionFor(pos, false) — adjusted=false ignores //line
// directives, yielding the byte offset into the skeleton string, directly
// comparable to the recorded span. Both properties (the adjusted position
// survives the filter; the raw offset falls inside the span) are pinned by
// TestInferProbeRawSpanRecovery.
type inferRegistry struct {
	// names is the PACKAGE-WIDE helper-name allocator shared by every sibling
	// file's registry (see inferNameAllocator's doc) — nextName delegates to
	// it instead of counting locally, so two files' registries never both
	// mint the literal name "_gsxinfer1".
	names *inferNameAllocator
	sites map[string]*inferSite // helper name "_gsxinfer3" -> site

	// spans holds EVERY recorded inferSite (every value also reachable via
	// sites, PLUS the probe shapes that have no correlating helper NAME to key
	// a sites entry by — recordProbeSpan's callers: the bare-call-candidate
	// `_gsxcompsig(F)` probe, the no-props/method nullary `_ = F()` probe, and
	// emitInferProbe's own no-supplied-args composite-literal fallback). Task
	// 8's siteAt does a linear scan over this ONE slice to resolve a type
	// error's raw skeleton offset to the probe it landed inside — module_
	// importer.go's diagnostic rewrite consumes this instead of guessing from
	// a tag's overall .gsx line/column range (the finding-6/7 hijack bug).
	// buildSkeleton's compBuf-relative-to-file-relative span adjustment also
	// ranges over this slice (not sites) so every recorded span, named or not,
	// is adjusted exactly once.
	spans []*inferSite

	// funcs accumulates the hoisted, package-level helper func decls (one per
	// emitInferProbe call). A helper's own type parameters make it valid at
	// package scope regardless of where its tag appears (even inside a `for`/
	// `if` block), but the PROBE CALL passing the tag's actual supplied
	// expressions must stay INLINE at the tag's position — those expressions
	// may reference loop variables or other block-scoped locals only in scope
	// there. buildSkeleton appends funcs.String() at package level (after the
	// component skeletons) once every component's skeleton has been emitted.
	funcs strings.Builder

	// failed records every *gsxast.Element whose cross-package generic probe
	// was skipped due to a requalification failure (recordFailed, called
	// alongside recordInferenceUnavailable — see analyze.go's emitProbes).
	// Such an element has NO probe call anywhere in the skeleton (harvest
	// therefore never visits it), yet it must still be marked so emit time
	// (genChildComponent) skips it too — generating a call for it would
	// necessarily be invalid, uninstantiated Go (there is no resolved type
	// arg to use). module_importer.go's analyze marks every entry here with
	// the types.Invalid sentinel in the shared `resolved` map right after
	// harvest runs, so genChildComponent's existing `resolved` parameter is
	// the ONLY new signal it needs — no new parameter threading through
	// emit.go's generateFile/genComponent/genChildComponent chain.
	// genChildComponent then emits a never-executed SINK consuming the tag's
	// value expressions instead of a call — see genSkippedTagSink for why
	// emitting NOTHING is not an option (an outer local whose only use sits
	// in the skipped tag would trip "declared and not used" in the output).
	failed []gsxast.Node

	// failedAliases records, for every failed element, the tag's package
	// qualifier ("components" in <components.Widget ...>) — always non-empty,
	// since only imported (dotted, non-method) tags can fail requalification.
	// module_importer.go's analyze resolves each alias to its import PATH
	// (via fileFacts.depAliasSpecs) to decide whether a skeleton
	// `"path" imported and not used` type error for this FILE is SPURIOUS:
	// the import IS used in the .gsx source (the failed tag), but the
	// skeleton sink dropped its only skeleton reference. Such an error is
	// filtered out (see the sunk-import filtering in analyze) instead of
	// hard-failing the whole file, and the emitted file rewrites that import
	// to a blank `_` import (writeImports via generateFile's sunkImports) so
	// the output still compiles. An import ALSO used elsewhere in the file
	// never produces the unused error, so nothing is filtered or rewritten
	// for it — the named import stays.
	failedAliases map[string]bool

	// alloc coalesces "_gsxtiN" skeleton-only import aliases (Task 4) across
	// EVERY requalified probe emitted into this ONE file's skeleton: two
	// probes — even for different imported components declared in different
	// dep files, each with its own import table — that resolve to the SAME
	// dependency path share ONE alias, so buildSkeleton's import assembly
	// emits exactly one `import _gsxtiN "path"` line per distinct path. This
	// is load-bearing, not cosmetic: requalifyTypeExpr/requalifyTypeParams's
	// OWN per-call aliasMinter restarts its "_gsxti1" counter every call (see
	// the ALIAS COUNTER SCOPE doc below), so two INDEPENDENT calls emitting
	// into the same file could otherwise both mint the literal text
	// "_gsxti1" for two DIFFERENT paths — a Go redeclaration error if each
	// were also given its own `import` line. Routing every Task-4
	// requalification call in this file through registry.requalifyTypeExpr/
	// requalifyTypeParams (which share this ONE allocator) avoids that by
	// construction.
	alloc *generatedImportAllocator
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
	arity     int             // the target component's type-param count, from genericSig.arity — Task 8's diagnostic hint (arityTypeHint) reads this directly instead of re-parsing the component's TypeParams from a tag name guess.

	// declSpan is the emitted helper func decl's own byte span in the final
	// skeleton (zero value {0,0} — never a real span, since emitInferProbe
	// always writes a non-empty decl — for every site recordProbeSpan creates,
	// which never allocates a helper decl). emitComponentSkeleton's hoisted
	// helper decls (registry.funcs, appended after every component body) are
	// NOT followed by a //line reset, so they carry whatever //line was last
	// in effect from the LAST tag processed — a stale .gsx position that
	// still survives module_importer's ".x.go"-suffix drop filter (it names a
	// real, just WRONG, .gsx location). An error genuinely positioned inside
	// the decl body itself (not the inline call site) would, before this
	// field existed, either misreport at that unrelated stale tag or (worse)
	// leak the raw go/types message verbatim. siteAt checks this span
	// alongside span so such an error still resolves to the RIGHT tag.
	declSpan inferSpan

	// args records, for emitInferProbe's call-form sites only, each supplied
	// argument expression's byte span within the probe call plus the prop's
	// declared name — in the same declaration order the args were written.
	// rewriteProbeDiag's argument-positioned leak arm (`cannot use ... in
	// argument to _gsxinferN`, a go/types error reported AT the offending
	// argument expression) resolves the raw error offset through argAt to
	// name the exact prop in the user-facing message instead of the internal
	// helper. Empty for recordProbeSpan's unnamed shapes (no per-arg spans).
	args []inferProbeArg
}

// inferProbeArg is one supplied argument of a call-form probe: the declared
// prop name it carries and its expression's byte span in the skeleton.
type inferProbeArg struct {
	name string
	span inferSpan
}

// argAt returns the declared prop name of the probe argument whose span
// contains rawOffset — see inferSite.args.
func (s *inferSite) argAt(rawOffset int) (string, bool) {
	for _, a := range s.args {
		if rawOffset >= a.span.start && rawOffset < a.span.end {
			return a.name, true
		}
	}
	return "", false
}

// inferSpan is a byte range into the skeleton string returned by buildSkeleton.
type inferSpan struct{ start, end int }

// newInferRegistry returns an empty registry ready to record probes for one
// file's skeleton. names is the PACKAGE-WIDE helper-name allocator (shared
// across every sibling file's buildSkeleton call by module_importer.go's
// analyze — see inferNameAllocator's doc); the registry itself, and every
// OTHER piece of per-file state (sites, spans, alloc, failed, ...), stays
// scoped to this one file, since harvest/diagnostic lookups are keyed by the
// file's own absXpath.
func newInferRegistry(names *inferNameAllocator) *inferRegistry {
	return &inferRegistry{sites: map[string]*inferSite{}, alloc: newGeneratedImportAllocator("_gsxti"), names: names}
}

// inferNameAllocator hands out globally-unique "_gsxinferN" probe helper
// names across EVERY .gsx file in one package. Each emitted helper
// (inferRegistry.emitInferProbe) is hoisted to PACKAGE scope in its file's
// skeleton (see inferRegistry's doc), and every sibling file's skeleton is
// type-checked TOGETHER as one package (module_importer.go's analyze), so
// per-file numbering would let two files independently mint the literal name
// "_gsxinfer1" — a `redeclared in this block` go/types error that fails the
// WHOLE package even though neither file, considered alone, did anything
// wrong (the final whole-branch review's Critical-1 finding). analyze
// constructs exactly ONE allocator per package and threads it into every
// file's buildSkeleton call so names are unique package-wide, while each
// file still gets its OWN inferRegistry — harvest and the diagnostic
// rewrite stay keyed by that file's absXpath, unaffected by this change.
type inferNameAllocator struct{ n int }

// newInferNameAllocator returns a fresh allocator starting its "_gsxinferN"
// sequence at 1.
func newInferNameAllocator() *inferNameAllocator {
	return &inferNameAllocator{}
}

// next returns the next probe helper name in this allocator's sequence:
// "_gsxinfer1", "_gsxinfer2", .... Names are 1-indexed and unique across
// every registry sharing this one allocator.
func (a *inferNameAllocator) next() string {
	a.n++
	return fmt.Sprintf("_gsxinfer%d", a.n)
}

// requalifyTypeExpr rewrites a single type expression for an IMPORTED
// generic component's probe (Task 4), sharing this registry's ONE per-file
// aliasAllocator so repeated calls (one per supplied param, across every
// probe in the file) coalesce onto one alias per distinct dependency path —
// see the alloc field doc. Delegates the actual parse/walk/print to
// requalifyTypeExprWithMinter; depImports is the SPECIFIC dep component's
// declaring-file import table (each imported generic component may live in
// a different dep file with a different table — only the alias NAMESPACE is
// shared, not the qualifier-resolution table).
func (r *inferRegistry) requalifyTypeExpr(src, depAlias string, depImports []importSpec, declared map[string]bool) (string, error) {
	mint := newAliasMinterShared(depImports, r.alloc)
	return requalifyTypeExprWithMinter(src, depAlias, declared, mint)
}

// requalifyTypeParams rewrites a whole bracketed type-param declaration list
// for an IMPORTED generic component's probe (Task 4) — the type-param
// counterpart of requalifyTypeExpr above, sharing the same per-file
// aliasAllocator.
func (r *inferRegistry) requalifyTypeParams(decl, depAlias string, depImports []importSpec) (string, error) {
	mint := newAliasMinterShared(depImports, r.alloc)
	return requalifyTypeParamsWithMinter(decl, depAlias, mint)
}

// importAssembly returns the coalesced extra imports registered by every
// requalified probe emitted into this file (in first-seen order), for
// buildSkeleton's import-block assembly.
func (r *inferRegistry) importAssembly() []importSpec {
	return r.alloc.specs()
}

// recordFailed marks el as a tag whose cross-package generic probe was
// skipped due to a requalification failure — see the failed and
// failedAliases field docs. The tag of a failed element is always dotted
// (only imported tags requalify), so its qualifier is recorded as a failed
// alias for the sunk-import bookkeeping.
func (r *inferRegistry) recordFailed(el *gsxast.Element) {
	r.failed = append(r.failed, el)
	if alias, _, ok := strings.Cut(el.Tag, "."); ok {
		if r.failedAliases == nil {
			r.failedAliases = map[string]bool{}
		}
		r.failedAliases[alias] = true
	}
}

// nextName returns the next probe helper name: "_gsxinfer1", "_gsxinfer2",
// .... Delegates to the shared, PACKAGE-WIDE names allocator (see
// inferNameAllocator's doc) so the name is unique across every sibling
// file's registry, not just this one.
func (r *inferRegistry) nextName() string {
	return r.names.next()
}

// record associates a probe helper name with its site, also recording it into
// spans (see the field doc) for Task 8's siteAt.
func (r *inferRegistry) record(name string, s *inferSite) {
	r.sites[name] = s
	r.spans = append(r.spans, s)
}

// lookup finds the site recorded under name. Exact-name match ONLY — this is
// what makes the registry immune to the finding-3 attack (a user func merely
// sharing the old naming prefix never matches "_gsxinfer1").
func (r *inferRegistry) lookup(name string) (*inferSite, bool) {
	s, ok := r.sites[name]
	return s, ok
}

// recordProbeSpan records a probe site with NO correlating helper name —
// Task 8's diagnostic rewrite needs the span (and arity) of every OTHER
// caller-side probe shape emitProbes emits for a generic tag: the
// bare-call-candidate `_gsxcompsig(F)` probe and the no-props/method nullary
// `_ = F()` probe (both in emitProbes, for a tag whose target is generic but
// never reaches emitInferProbe's own call-form path), and emitInferProbe's
// own no-supplied-args composite-literal fallback (analyze.go's `default:`
// branch, when every declared param went unsupplied at this tag). None of
// these shapes ever successfully resolves an instantiation to harvest (each
// is missing the argument(s) go/types would need to infer from), so unlike
// emitInferProbe's record there is no name for harvest to look up — this
// entry exists purely for siteAt's position-based lookup.
func (r *inferRegistry) recordProbeSpan(el *gsxast.Element, propsType string, arity, start, end int) {
	r.spans = append(r.spans, &inferSite{el: el, propsType: propsType, arity: arity, span: inferSpan{start, end}})
}

// siteAt returns the recorded probe site (named call-form or one of
// recordProbeSpan's unnamed shapes) whose span — the inline call/composite-
// literal probe (span) OR, for emitInferProbe's call-form sites, the hoisted
// helper func decl (declSpan; see its field doc for why a decl-body error is
// possible and would otherwise misreport or leak) — contains rawOffset. This
// is the RAW (adjusted=false) byte offset of a type-checker error's
// position, recovered via fset.PositionFor(pos, false) per the POSITION
// MAPPING doc above. module_importer.go's cannot-infer/constraint-violation
// diagnostic rewrite uses this as the ONLY signal deciding whether (and how)
// to rewrite an error: a miss means the error belongs to something other
// than one of this file's own inference probes and must pass through
// untouched.
func (r *inferRegistry) siteAt(rawOffset int) (*inferSite, bool) {
	for _, s := range r.spans {
		if rawOffset >= s.span.start && rawOffset < s.span.end {
			return s, true
		}
		if s.declSpan.end > s.declSpan.start && rawOffset >= s.declSpan.start && rawOffset < s.declSpan.end {
			return s, true
		}
	}
	return nil, false
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
// The call is deliberately NOT //line-free: it inherits the enclosing tag's
// //line mapping (see the POSITION MAPPING note on inferRegistry), so an
// inference-failure error survives module_importer.go's synthetic-position
// filter and reports at the tag in the .gsx. The recorded span covers the
// call statement's RAW skeleton bytes; recover an error's raw offset with
// fset.PositionFor(pos, false) to compare against it.
//
// Returns false — emitting nothing — when no supplied prop mentions any
// declared field (an empty arg list gives go/types nothing to infer a type
// parameter from; the tag then falls through to its plain, uninstantiated
// composite-literal probe and surfaces the type-checker's own "without
// instantiation" error, which a later task rewrites into a positioned
// diagnostic).
//
// arity is the target component's type-param count (genericSig.arity),
// recorded on the site verbatim for Task 8's diagnostic hint
// (arityTypeHint) — never re-derived from typeParamsDecl/typeParamsUse here,
// since for an IMPORTED component those are already requalified text (e.g.
// "K components.Foo") that arity-by-counting-commas would have to re-parse.
func (r *inferRegistry) emitInferProbe(sb *strings.Builder, el *gsxast.Element,
	propsType, typeParamsDecl, typeParamsUse string,
	params []param, supplied map[string]string, arity int) bool {

	ordered := suppliedInDeclOrder(params, supplied)
	if len(ordered) == 0 {
		return false
	}
	name := r.nextName()

	declStart := r.funcs.Len()
	fmt.Fprintf(&r.funcs, "func %s%s(", name, typeParamsDecl)
	for i, p := range ordered {
		if i > 0 {
			r.funcs.WriteString(", ")
		}
		fmt.Fprintf(&r.funcs, "_gsxv%d %s", i, p.typeSrc)
	}
	fmt.Fprintf(&r.funcs, ") %s%s { return %s%s{} }\n", propsType, typeParamsUse, propsType, typeParamsUse)
	declEnd := r.funcs.Len()

	start := sb.Len()
	fmt.Fprintf(sb, "_ = %s(", name)
	args := make([]inferProbeArg, 0, len(ordered))
	for i, p := range ordered {
		if i > 0 {
			sb.WriteString(", ")
		}
		argStart := sb.Len()
		sb.WriteString(supplied[fieldName(p.name)])
		args = append(args, inferProbeArg{name: p.name, span: inferSpan{argStart, sb.Len()}})
	}
	sb.WriteString(")\n")
	r.record(name, &inferSite{el: el, propsType: propsType, span: inferSpan{start, sb.Len()}, arity: arity, declSpan: inferSpan{declStart, declEnd}, args: args})
	return true
}

// importedTagAlias reports whether tag is a package-qualified component
// invocation (`components.Button`), returning the qualifier as the calling
// file's import alias for that dependency package. isMethod (from
// childInvocation) disambiguates a method invocation (`p.Method`, driven by
// the enclosing receiver var) — its tag text also contains a dot, but it is
// never package-qualified.
func importedTagAlias(tag string, isMethod bool) (alias string, ok bool) {
	if isMethod {
		return "", false
	}
	alias, _, ok = strings.Cut(tag, ".")
	if !ok {
		return "", false
	}
	return alias, true
}

// recordInferenceUnavailable records the Task 4 fail-safe diagnostic for an
// imported generic tag whose type-param decl or a supplied param's type
// could not be requalified into the calling file's context (an unexported
// dep-local type, a dot-imported dep-local name, or an unresolvable/
// ambiguous dep qualifier — see requalifyTypeExpr's doc for the full list),
// and marks el on registry (registry.recordFailed) so emit time skips it too
// — see inferRegistry.failed's doc for why: a call for this tag would
// otherwise need to be emitted uninstantiated (invalid Go), since no probe
// ever type-checked to harvest a resolved type arg from. Warning severity:
// the probe for THIS tag is skipped, but the rest of the file/package still
// generates. A nil bag (some buildSkeleton callers, e.g. unit tests
// exercising the skeleton directly, pass none) skips the diagnostic but
// still marks the element failed.
func recordInferenceUnavailable(bag *diag.Bag, registry *inferRegistry, el *gsxast.Element, cause error) {
	registry.recordFailed(el)
	if bag == nil {
		return
	}
	msg := strings.TrimPrefix(cause.Error(), "codegen: ")
	bag.Report(el.Pos(), el.End(), diag.Warning, "inference-unavailable", "codegen",
		"type inference for <%s> needs %s; instantiate explicitly with <%s[type] ...>", el.Tag, msg, el.Tag)
}

// --- Requalification engine (Task 3) ---------------------------------------
//
// A generic component's type-param constraints and declared param types are
// stored as raw source SNIPPETS captured from the component's OWN file (see
// param.typeSrc, c.TypeParams). Verbatim, those snippets only resolve inside
// that file: a bare `Row` means "the Row type declared in (or imported by)
// THAT file", and `pq.T` qualifies through THAT file's own import table.
// emitInferProbe already handles the same-package case by splicing such
// source in verbatim (the calling file IS the dep file). For an IMPORTED
// generic component (Task 4), the snippet must instead be rewritten so it
// resolves in the CALLING file's skeleton:
//
//   - a bare exported dep-local type (`Row`) becomes `<depAlias>.Row`, using
//     the same alias the caller already has bound to the dep package;
//   - a bare unexported dep-local type (`secret`) is unspeakable outside the
//     dep package and is rejected;
//   - a qualified dep-local reference (`fmt.Stringer`, i.e. the DEP file
//     imports "fmt" itself) is re-imported by the caller under a fresh,
//     collision-proof alias ("_gsxti1", "_gsxti2", ...) registered via the
//     addImport callback, and the snippet is rewritten to that fresh alias;
//   - a type-param name declared in the SAME constraint/param list (e.g. `K`
//     in `K any, V interface{ ~[]K }`) is never qualified — it is a locally
//     bound name, not a dep-package reference;
//   - a predeclared universe name (string, int, any, comparable, error, ...)
//     is never qualified.
//
// requalifyTypeExpr handles a single type expression (a param's typeSrc, or
// one constraint element); requalifyTypeParams handles a whole bracketed
// type-param declaration list (c.TypeParams — a FIELD LIST, not one expr,
// because names in the list must be collected before any field's constraint
// is rewritten). Both are pure: nothing in this package calls them yet —
// Task 4 wires them into the imported-component probe path.
//
// ALIAS COUNTER SCOPE: each call mints its OWN "_gsxti<N>" sequence starting
// at 1 (an aliasMinter is local to one requalifyTypeExpr/requalifyTypeParams
// invocation), deduplicated by resolved import PATH so repeated qualifiers
// for the same dep import within one call share one alias. A caller issuing
// several calls for one probe helper (e.g. one per supplied param plus one
// for the type-param decl) will see the "_gsxti1" sequence restart each
// call — this is deliberately safe rather than coordinated: Go permits
// importing the same path multiple times under different aliases in one
// file, so two calls minting independent aliases for the SAME dep path is
// merely redundant, never a compile error. The only way two calls could
// collide is the SAME alias text naming two DIFFERENT paths, which cannot
// happen with per-call addImport callbacks unless a caller merges the
// callback across calls without namespacing — Task 4 must give each call
// (or otherwise namespace) its own addImport if it wants to avoid this.

// requalifyTypeExpr rewrites a type expression src (written in the dep
// package's file context) for use in the calling file's skeleton.
//
//	depAlias:   the caller's import alias/name for the dep package ("components")
//	depImports: the dep FILE's import specs (alias -> path), so `pq.T` in dep
//	            context can be re-imported by the caller under a fresh alias
//	declared:   type-param names bound in src's scope — for a component
//	            param's typeSrc, the component's OWN type-param names
//	            (parseTypeParamNames(c.TypeParams)). These stay bare: `T` in
//	            `[]T` names the probe helper's type parameter, NOT a dep
//	            type. The caller MUST supply this set for a generic
//	            component's param types; with it empty/nil, `[]T` is
//	            misqualified to `[]<depAlias>.T`, which — if the dep happens
//	            to export a same-named type — COMPILES pointing at the wrong
//	            type (silent corruption; the trap is pinned by the
//	            "shadowing trap" test case).
//	addImport:  callback registering an extra skeleton import (path, alias);
//	            alias "" = plain import
//
// Returns an error for any unexported ident that would need qualification
// (unspeakable outside the dep), for exprs it cannot parse, and for a
// qualifier that cannot be resolved in depImports — including an AMBIGUOUS
// resolution (two or more unaliased dep imports whose paths end in the same
// segment): the engine fails safe instead of guessing, because a wrong pick
// yields a broken (or worse, silently wrong) skeleton with no diagnostic
// pointing at the cause. See lookupDepImportPath for the resolution rules:
// unaliased imports are matched by the path's LAST SEGMENT (the conventional
// default package identifier), which is a heuristic — a package whose
// declared name differs from its path's last segment is misresolved (or
// unresolved). Once the cross-package task has go/types-loaded dep packages
// available, callers should supply types-resolved package names in
// depImports (spec.name filled from types.Package.Name()) so the heuristic
// is never consulted.
//
// KNOWN LIMITATION — dot imports: a bare ident the dep file pulled in via a
// dot import (`import . "example.com/x"`) is textually indistinguishable
// from a dep-local type here, so it is qualified as `<depAlias>.X` — the
// wrong package. Dep components whose signatures rely on dot-imported names
// are unsupported by this engine (resolving them needs go/types scope
// information this pure, standalone pass deliberately does not load).
//
// Deviation from the brief's suggested implementation: the brief describes
// parsing src via the `type _t = <src>` file trick (matching
// parseTypeParamNames's synth style). That trick only accepts src shapes
// legal as a top-level Type production, which EXCLUDES bare union
// (`string | int`) and tilde (`~string`) expressions — both appear directly
// in this function's own test table as constraint elements, and both fail
// to parse under `type _t = string | int` ("expected ';', found '|'"),
// confirmed empirically before writing this implementation. go/parser's
// expression grammar accepts both directly (as *ast.BinaryExpr(token.OR) and
// *ast.UnaryExpr(token.TILDE) respectively) alongside every other shape the
// table needs, so requalifyTypeExpr parses src with go/parser.ParseExpr
// directly instead. requalifyTypeParams still uses the file-trick style
// (parseTypeParamFieldList, i.e. `func _[<decl>]() {}`) exactly as the brief
// specifies, because a type-param FIELD LIST legitimately allows union/tilde
// constraint syntax in that grammar position.
func requalifyTypeExpr(src, depAlias string, depImports []importSpec, declared map[string]bool, addImport func(path, alias string)) (string, error) {
	mint := newAliasMinter(depImports, addImport)
	return requalifyTypeExprWithMinter(src, depAlias, declared, mint)
}

// requalifyTypeExprWithMinter is requalifyTypeExpr's implementation, taking
// an already-constructed *aliasMinter instead of building a fresh one. The
// public requalifyTypeExpr always passes a private, per-call minter (see
// newAliasMinter) so its documented per-call alias-counter behavior is
// unchanged; inferRegistry.requalifyTypeExpr (Task 4) instead passes a
// minter sharing the registry's ONE per-file aliasAllocator, so several
// calls coalesce their aliases — see inferRegistry.alloc's doc.
func requalifyTypeExprWithMinter(src, depAlias string, declared map[string]bool, mint *aliasMinter) (string, error) {
	src = strings.TrimSpace(src)
	expr, err := parser.ParseExpr(src)
	if err != nil {
		return "", fmt.Errorf("codegen: parse type expr %q: %w", src, err)
	}
	rewritten, err := rewriteTypeNode(expr, depAlias, declared, mint)
	if err != nil {
		return "", err
	}
	return printTypeExpr(rewritten)
}

// requalifyTypeParams rewrites a whole bracketed type-param declaration list
// (the raw source between a generic component's signature brackets, e.g.
// "K comparable, V Renderer") for use in the calling file's skeleton, the
// same way requalifyTypeExpr rewrites one expression. Unlike a single
// constraint expression, a decl list is a Go FIELD LIST (possibly several
// comma-separated `names Constraint` groups), so every declared type-param
// name is collected FIRST — across the whole list — before any field's
// constraint expression is walked; a constraint may reference a sibling
// type-param name declared later in the same list (`K any, V interface{
// ~[]K }`), and that name must stay bare in every field, not just its own.
//
// Parses decl via parseTypeParamFieldList (the `func _[<decl>]() {}` synth
// trick already established by analyze.go's parseTypeParamNames), rewrites
// each field's constraint with the same walker requalifyTypeExpr uses, and
// prints each field back as "name1, name2 Constraint", joined by ", " to
// match the original decl's comma-separated shape.
//
// Unlike requalifyTypeExpr there is no declared parameter: the declared set
// IS the decl list's own names, collected here. Error paths and limits are
// shared with requalifyTypeExpr and documented there in full: unexported
// idents needing qualification are rejected; a constraint's qualifier that
// cannot be resolved in depImports — or resolves ambiguously (two unaliased
// dep imports sharing a last path segment) — is an error rather than a
// guess; unaliased dep imports are matched by last path segment (callers
// should fill spec.name from types.Package.Name() once loaded packages are
// available); dep files whose constraints rely on dot-imported bare names
// are unsupported (misqualified as <depAlias>.X).
func requalifyTypeParams(decl, depAlias string, depImports []importSpec, addImport func(path, alias string)) (string, error) {
	mint := newAliasMinter(depImports, addImport)
	return requalifyTypeParamsWithMinter(decl, depAlias, mint)
}

// requalifyTypeParamsWithMinter is requalifyTypeParams's implementation,
// taking an already-constructed *aliasMinter instead of building a fresh
// one — see requalifyTypeExprWithMinter's doc for why (inferRegistry's Task
// 4 wiring shares one allocator across every call in a file).
func requalifyTypeParamsWithMinter(decl, depAlias string, mint *aliasMinter) (string, error) {
	decl = strings.TrimSpace(decl)
	if decl == "" {
		return "", nil
	}
	tpl, _, err := parseTypeParamFieldList(decl)
	if err != nil {
		return "", err
	}
	if tpl == nil {
		return "", nil
	}

	declared := map[string]bool{}
	for _, field := range tpl.List {
		for _, nm := range field.Names {
			declared[nm.Name] = true
		}
	}

	parts := make([]string, 0, len(tpl.List))
	for _, field := range tpl.List {
		rewritten, err := rewriteTypeNode(field.Type, depAlias, declared, mint)
		if err != nil {
			return "", err
		}
		typeStr, err := printTypeExpr(rewritten)
		if err != nil {
			return "", err
		}
		names := make([]string, len(field.Names))
		for i, nm := range field.Names {
			names[i] = nm.Name
		}
		parts = append(parts, strings.Join(names, ", ")+" "+typeStr)
	}
	return strings.Join(parts, ", "), nil
}

// rewriteTypeNode walks a type expression parsed from the dep file's
// context, rewriting every dep-local reference for the calling file per the
// rules documented on requalifyTypeExpr, and recursing structurally through
// every other Go type-expression shape (pointers, slices, arrays, maps,
// channels, funcs, structs, interfaces, unions, tildes, parens, ellipses,
// and generic instantiations). Nodes are mutated and returned in place
// except for a bare Ident or SelectorExpr that needs qualification, which is
// replaced with a freshly built SelectorExpr — the parsed AST belongs solely
// to this call (freshly parsed from src/decl, never shared), so in-place
// mutation is safe.
func rewriteTypeNode(e goast.Expr, depAlias string, declared map[string]bool, mint *aliasMinter) (goast.Expr, error) {
	if e == nil {
		return nil, nil
	}
	switch t := e.(type) {
	case *goast.Ident:
		if declared[t.Name] {
			return t, nil
		}
		if types.Universe.Lookup(t.Name) != nil {
			return t, nil
		}
		if !goast.IsExported(t.Name) {
			return nil, fmt.Errorf("codegen: unexported type %s", t.Name)
		}
		return &goast.SelectorExpr{X: goast.NewIdent(depAlias), Sel: goast.NewIdent(t.Name)}, nil

	case *goast.SelectorExpr:
		qual, ok := t.X.(*goast.Ident)
		if !ok {
			s, _ := printTypeExpr(t)
			return nil, fmt.Errorf("codegen: unsupported qualified type expression %s", s)
		}
		if !goast.IsExported(t.Sel.Name) {
			return nil, fmt.Errorf("codegen: unexported type %s", t.Sel.Name)
		}
		alias, err := mint.resolve(qual.Name)
		if err != nil {
			return nil, err
		}
		return &goast.SelectorExpr{X: goast.NewIdent(alias), Sel: goast.NewIdent(t.Sel.Name)}, nil

	case *goast.StarExpr:
		x, err := rewriteTypeNode(t.X, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.X = x
		return t, nil

	case *goast.ParenExpr:
		x, err := rewriteTypeNode(t.X, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.X = x
		return t, nil

	case *goast.ArrayType:
		elt, err := rewriteTypeNode(t.Elt, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.Elt = elt
		return t, nil

	case *goast.Ellipsis:
		elt, err := rewriteTypeNode(t.Elt, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.Elt = elt
		return t, nil

	case *goast.MapType:
		k, err := rewriteTypeNode(t.Key, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		v, err := rewriteTypeNode(t.Value, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.Key, t.Value = k, v
		return t, nil

	case *goast.ChanType:
		v, err := rewriteTypeNode(t.Value, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.Value = v
		return t, nil

	case *goast.UnaryExpr:
		if t.Op != token.TILDE {
			return nil, fmt.Errorf("codegen: unsupported type expression operator %s", t.Op)
		}
		x, err := rewriteTypeNode(t.X, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.X = x
		return t, nil

	case *goast.BinaryExpr:
		if t.Op != token.OR {
			return nil, fmt.Errorf("codegen: unsupported type expression operator %s", t.Op)
		}
		x, err := rewriteTypeNode(t.X, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		y, err := rewriteTypeNode(t.Y, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.X, t.Y = x, y
		return t, nil

	case *goast.StructType:
		if t.Fields != nil {
			for _, f := range t.Fields.List {
				ft, err := rewriteTypeNode(f.Type, depAlias, declared, mint)
				if err != nil {
					return nil, err
				}
				f.Type = ft
			}
		}
		return t, nil

	case *goast.InterfaceType:
		if t.Methods != nil {
			for _, f := range t.Methods.List {
				ft, err := rewriteTypeNode(f.Type, depAlias, declared, mint)
				if err != nil {
					return nil, err
				}
				f.Type = ft
			}
		}
		return t, nil

	case *goast.FuncType:
		if t.TypeParams != nil {
			for _, f := range t.TypeParams.List {
				ft, err := rewriteTypeNode(f.Type, depAlias, declared, mint)
				if err != nil {
					return nil, err
				}
				f.Type = ft
			}
		}
		if t.Params != nil {
			for _, f := range t.Params.List {
				ft, err := rewriteTypeNode(f.Type, depAlias, declared, mint)
				if err != nil {
					return nil, err
				}
				f.Type = ft
			}
		}
		if t.Results != nil {
			for _, f := range t.Results.List {
				ft, err := rewriteTypeNode(f.Type, depAlias, declared, mint)
				if err != nil {
					return nil, err
				}
				f.Type = ft
			}
		}
		return t, nil

	case *goast.IndexExpr:
		x, err := rewriteTypeNode(t.X, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		idx, err := rewriteTypeNode(t.Index, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		t.X, t.Index = x, idx
		return t, nil

	case *goast.IndexListExpr:
		x, err := rewriteTypeNode(t.X, depAlias, declared, mint)
		if err != nil {
			return nil, err
		}
		for i, idx := range t.Indices {
			ni, err := rewriteTypeNode(idx, depAlias, declared, mint)
			if err != nil {
				return nil, err
			}
			t.Indices[i] = ni
		}
		t.X = x
		return t, nil

	default:
		s, _ := printTypeExpr(e)
		return nil, fmt.Errorf("codegen: unsupported type expression %s (%T)", s, e)
	}
}

// printTypeExpr renders a (possibly rewritten) type expression back to Go
// source text via go/printer, matching the AST->text convention already used
// by analyze.go/emit.go for the skeleton generator. Rewritten nodes may mix
// original positions (unchanged subtrees) with token.NoPos (freshly built
// SelectorExpr/Ident replacements); go/printer formats both consistently
// (verified empirically: a fresh, position-free node prints identically to
// one carrying its original parse position for every shape this engine
// produces), so no FileSet threading from the original parse is needed here.
func printTypeExpr(e goast.Expr) (string, error) {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), e); err != nil {
		return "", fmt.Errorf("codegen: print type expr: %w", err)
	}
	return buf.String(), nil
}

// generatedImportAllocator mints fresh "<prefix>N" import aliases, deduplicated
// by resolved import PATH: the same path always gets the same alias from one
// allocator, regardless of how many times or through how many different
// qualifiers it is resolved. It is the ONE alias implementation shared by two
// callers with different reserved prefixes:
//
//   - skeleton requalification (Task 4) constructs it with prefix "_gsxti" so
//     current probe naming remains stable; requalifyTypeExpr/requalifyTypeParams
//     each build a PRIVATE, single-call allocator by default (see the ALIAS
//     COUNTER SCOPE note above — every standalone call restarts at "_gsxti1"),
//     while inferRegistry holds ONE allocator for a whole file's worth of probes
//     so repeated calls coalesce onto it — see inferRegistry.alloc's doc.
//   - final zero-fill type spelling (Task 5) constructs it with prefix "_gsxty"
//     and drives it transactionally through begin()/commit() so a rejected
//     candidate spelling leaks no import. The reserved "_gsx" namespace makes
//     both prefixes collision-safe.
type generatedImportAllocator struct {
	prefix string
	next   int
	byPath map[string]string
	order  []importSpec // first-seen order, for deterministic import assembly
}

func newGeneratedImportAllocator(prefix string) *generatedImportAllocator {
	return &generatedImportAllocator{prefix: prefix, byPath: map[string]string{}}
}

// alloc returns the alias bound to path, minting (and recording in order) a
// fresh "<prefix>N" the first time path is seen by this allocator, and
// returning the SAME alias on every later call for that path.
func (a *generatedImportAllocator) alloc(path string) string {
	if alias, ok := a.byPath[path]; ok {
		return alias
	}
	a.next++
	alias := fmt.Sprintf("%s%d", a.prefix, a.next)
	a.byPath[path] = alias
	a.order = append(a.order, importSpec{name: alias, path: path})
	return alias
}

// specs returns the coalesced import specs registered by this allocator, in
// first-seen order, for import-block assembly.
func (a *generatedImportAllocator) specs() []importSpec {
	return a.order
}

// generatedImportTxn is a candidate-scoped view of a generatedImportAllocator.
// A candidate type spelling begins a transaction, allocates aliases through its
// qualifier while types.TypeString renders the spelling, and commits only after
// the positional go/types validation accepts that spelling. A rejected
// candidate is simply never committed, so its speculative aliases never reach
// the owner and cannot leak an unused import.
type generatedImportTxn struct {
	owner   *generatedImportAllocator
	baseLen int
	work    *generatedImportAllocator
}

// begin snapshots the allocator into a working copy. Every allocation made
// through the returned transaction lands only on the copy until commit.
func (a *generatedImportAllocator) begin() *generatedImportTxn {
	work := &generatedImportAllocator{
		prefix: a.prefix,
		next:   a.next,
		byPath: maps.Clone(a.byPath),
		order:  append([]importSpec(nil), a.order...),
	}
	return &generatedImportTxn{owner: a, baseLen: len(a.order), work: work}
}

// qualifier returns a types.Qualifier that spells every foreign package under
// its reserved generated alias, even when the caller's source already imports
// that package, so local shadowing cannot break generated code. Types in the
// current package are left unqualified.
func (t *generatedImportTxn) qualifier(current *types.Package) types.Qualifier {
	return func(p *types.Package) string {
		if p == nil || p == current {
			return ""
		}
		return t.work.alloc(p.Path())
	}
}

// commit publishes the transaction's allocations onto the owning allocator. It
// asserts the owner was not concurrently mutated while the transaction was open
// (an internal invariant, never a guessed reconciliation).
func (t *generatedImportTxn) commit() {
	if len(t.owner.order) != t.baseLen {
		panic("codegen: generated import allocator mutated during an open transaction")
	}
	t.owner.next = t.work.next
	t.owner.byPath = t.work.byPath
	t.owner.order = t.work.order
}

// aliasMinter resolves a dep-file qualifier (e.g. "fmt" or an explicit
// alias) to the fresh alias registered for its import path, for one
// requalifyTypeExpr/requalifyTypeParams walk. depImports is the SPECIFIC
// dep file's own import table (qualifier -> path resolution is always
// per-call/per-dep-file); alloc is the path -> alias allocator, which may be
// either private to this call (newAliasMinter) or shared across many calls
// (newAliasMinterShared) — see aliasAllocator's doc.
type aliasMinter struct {
	depImports []importSpec
	alloc      *generatedImportAllocator
	addImport  func(path, alias string) // nil for the shared (Task 4) path; see newAliasMinterShared
}

// newAliasMinter builds a minter over a fresh, PRIVATE allocator — the
// standalone requalifyTypeExpr/requalifyTypeParams entry points' behavior,
// unchanged by Task 4: addImport fires exactly once per NEW path seen by
// THIS call.
func newAliasMinter(depImports []importSpec, addImport func(path, alias string)) *aliasMinter {
	return &aliasMinter{depImports: depImports, alloc: newGeneratedImportAllocator("_gsxti"), addImport: addImport}
}

// newAliasMinterShared builds a minter over a caller-owned allocator
// (inferRegistry.alloc, Task 4) instead of a fresh private one, so repeated
// calls into the SAME allocator coalesce onto one "_gsxtiN" sequence
// deduplicated by path — see inferRegistry.alloc's doc. There is no
// addImport callback: the caller reads the coalesced set directly off
// alloc.order (inferRegistry.importAssembly) once every probe for the file
// has been emitted, rather than being notified per-registration.
func newAliasMinterShared(depImports []importSpec, alloc *generatedImportAllocator) *aliasMinter {
	return &aliasMinter{depImports: depImports, alloc: alloc}
}

// resolve returns the fresh alias to use in place of qualifier (a package
// identifier as it appears in the dep file's source, e.g. "fmt" or an
// explicit alias), minting a new "_gsxtiN" alias the first time a given
// import PATH is seen by this minter's allocator (invoking addImport, when
// set, exactly once for that new path), and reusing it for any later
// qualifier that resolves to the same path. Unresolvable and ambiguous
// qualifiers are errors (see lookupDepImportPath).
func (m *aliasMinter) resolve(qualifier string) (string, error) {
	path, err := lookupDepImportPath(m.depImports, qualifier)
	if err != nil {
		return "", err
	}
	_, existed := m.alloc.byPath[path]
	alias := m.alloc.alloc(path)
	if !existed && m.addImport != nil {
		m.addImport(path, alias)
	}
	return alias, nil
}

// lookupDepImportPath resolves qualifier (a package identifier as spelled in
// the dep file's source) to its import path within depImports. An explicit
// alias/name match is tried first (exact, and unambiguous by construction —
// Go rejects a file binding one name to two imports); failing that, a plain
// import (name == "") matches if qualifier equals the import path's last
// "/"-separated segment — the conventional default package identifier for an
// unaliased import, and the only signal available here short of loading the
// dep import's own package clause (out of scope for this pure, standalone
// engine; callers with go/types-loaded deps should pre-fill spec.name from
// types.Package.Name() so this heuristic is never consulted).
//
// Because the last-segment match IS a heuristic, it fails safe: if MORE THAN
// ONE unaliased import's path ends in the qualifier's segment (legal in the
// dep file only when their declared package names differ — e.g.
// "example.com/a/util" + "example.com/b/util" where one declares `package
// utilx`), picking either would silently qualify the caller's skeleton
// against the wrong package with no diagnostic pointing at the cause, so an
// ambiguity error is returned instead.
func lookupDepImportPath(depImports []importSpec, qualifier string) (string, error) {
	for _, spec := range depImports {
		if spec.name != "" && spec.name == qualifier {
			return spec.path, nil
		}
	}
	var matches []string
	for _, spec := range depImports {
		if spec.name == "" && pathBase(spec.path) == qualifier {
			matches = append(matches, spec.path)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("codegen: cannot resolve import for type qualifier %s", qualifier)
	default:
		return "", fmt.Errorf("codegen: ambiguous import for type qualifier %s: %s", qualifier, strings.Join(matches, ", "))
	}
}

// pathBase returns the last "/"-separated segment of an import path (its
// conventional default package identifier), e.g. "fmt" -> "fmt",
// "example.com/mod/components" -> "components".
func pathBase(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}
