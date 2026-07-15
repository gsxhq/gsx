# Verbatim Component Signatures Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit every component with exactly its authored Go parameter list and lower markup calls positionally, with declared `children`/`attrs` roles, exact-name props, source-ordered attrs composition, and no generated Props ABI.

**Architecture:** First establish ordered declaration facts, a type-driven signature model, stable per-element target facts, and pure call planners while the shipping path is unchanged. Then replace declaration and call emission together in one rollback-sized atomic cutover; the two in-memory `go/types` phases share one preprocessed AST and the existing importer, and generated source contains only the authored function/method plus local evaluation temporaries.

**Tech Stack:** Go 1.26.1, `go/ast`, `go/parser`, `go/printer`, `go/token`, `go/types`, the existing warm module importer, GSX AST/parser/emitter, txtar semantic corpus, fmt corpus, gopls, VitePress.

## Global Constraints

- Read `docs/superpowers/specs/2026-07-14-verbatim-component-signatures-design.md` before each task; it is the semantic source of truth.
- The emitted component parameter list is exactly the authored list. Emit no Props type, wrapper, adapter, ABI sentinel, or helper declaration.
- Local temporaries are allowed only to preserve Go evaluation semantics or lower an inline zero; every authored expression evaluates exactly once.
- This is one atomic source/generated/manual-caller migration. Do not ship a dual-mode compatibility path.
- The core branch is non-mergeable and non-releasable until Task 11's mandatory
  structpages and one-learning consumer branches pass against the exact core
  commit; use the cross-repository release transaction there, never a
  compatibility interval.
- Delete obsolete APIs and subsystems outright. Do not add deprecation aliases, compatibility adapters, temporary production helpers, or behavior flags.
- Keep implementation types, fields, and methods unexported unless a concrete serialization or cross-package API requires export.
- Do not implement component struct splat. `{bag...}` remains only an attrs-bag contributor.
- Matching is exact parameter identifier matching. Do not add a heuristic or retain `FieldMatcher` behavior.
- `children` accepts exactly `gsx.Node` or `...gsx.Node`; `attrs` accepts the exact attrs-bag family in the spec.
- `attrs={expr}`, `attrs={{...}}`, unmatched attrs, spreads, and conditional bags compose at authored positions; there is no forced-last branch.
- Pre-materialize embedded markup and stamp component classification once. Discovery, validation, LSP, and emission reuse the same AST and stable call-site IDs.
- Discovery first harvests the origin generic object/signature; authored-operands-only inference then uses a transient carrier; only after inference may omitted arguments be zero-filled.
- New analysis phases reuse the existing importer. They must not call `packages.Load`, write dependency `.x.go` files early, or depend on generation order.
- In normal mode, retain `NeedCompiledGoFiles|NeedSyntax` from the existing single cold load and source-check project-local Go-only packages through the module importer. In Bundle mode, reject a project-local `gsx -> Go-only -> gsx` chain because the bundle has no authoritative source inventory.
- Saved `.gsx` events refresh authoritative disk facts before warm invalidation.
  Normal analysis and persistent cache metadata share one source-manifest
  implementation covering GSX-only packages, authored GSX imports, local
  replacements, and paired-output exclusions; do not maintain a second
  approximate `go list` graph.
- Normal mode uses cmd/go's last-flag-wins `GOFLAGS` semantics, rejects an effective non-empty `-overlay`, and rejects any explicit or frozen-PATH-discovered `GOPACKAGESDRIVER` before the cold load. After proving no driver was effective, pin `GOPACKAGESDRIVER=off` only to prevent x/tools from re-evaluating a later live PATH. Never hide configured state or support only the deletion-free overlay subset; interoperability requires a separate shared virtual-filesystem loader.
- Logical build variants apply only to duplicate component declarations whose generated files are all effectively constrained. Unconstrained or mixed duplicate components are errors; raw Go declarations receive no cross-file suppression and platform-specific implementations live in constrained `.go` files behind one stable API.
- Variant equality is semantic: exact ordered value-parameter names/roles plus `types.Identical` signatures and receiver types. Source spelling and import aliases are not acceptance criteria; type-parameter names are alpha-equivalent.
- Analyze every GSX variant body/import in the Module's one frozen Go universe. A platform-only import used directly by an inactive GSX file is an error; do not approximate a multi-context analysis.
- Root runtime remains standard-library only. Tooling may continue using `golang.org/x/tools`.
- Every syntax/codegen behavior is pinned in `internal/corpus/testdata/cases/**/*.txtar`; never hand-edit generated or golden sections.
- Use `go test ./internal/corpus -run TestCorpus -update` and `go test ./internal/gsxfmt -run TestFmtCorpus -update` to regenerate, then rerun without `-update`.
- Run `gopls check -severity=hint` on each changed Go file, `make lint`, and final `make ci` with the repository's Go 1.26.1 toolchain.
- Any signature that cannot obey inline-only zeros, authored-order evaluation, contextual untyped values, or `(T, error)` unwrapping is a stop-and-discuss checkpoint with a minimal corpus reproducer.

## File Structure

- Create `internal/codegen/component_signature.go`: syntax declaration shape plus the universal type-driven component signature and reserved-role classifier.
- Create `internal/codegen/component_target.go`: stable call-site IDs, target-discovery registry, provenance classification, and harvested per-element facts.
- Create `internal/codegen/component_call_plan.go`: pure exact-name/input-role routing and signature-ordered argument planning.
- Create `internal/codegen/component_zero.go`: semantically validated zero candidates, type spelling, authored-operands inference result, and instantiated-signature handling.
- Create `internal/codegen/component_signature_test.go`, `component_target_test.go`, `component_call_plan_test.go`, and `component_zero_test.go`: focused unit tests before the atomic emitter cutover.
- Modify `internal/codegen/analyze.go`, `module_importer.go`, `module.go`, and `results.go`: shared AST preprocessing, two analysis phases, project-local source importer bridge, retained facts, caches, and diagnostics.
- Modify `internal/codegen/emit.go`: verbatim declarations, positional calls, declared children/attrs bindings, and source-ordered value materialization.
- Delete `internal/codegen/byo.go`, `attrsonly.go`, `freeuse.go`, and `fieldmatch.go` after their sound behavior is represented by the universal model. Keep `reserved_scan.go` for `_gsx...`; reduce `reserved_bindings.go` to the still-ambient `ctx` rule.
- Modify `gen/*.go`, `docs/guide/**`, `docs/ROADMAP.md`, `examples/**`, and `gen/templates/init/simple/**`: remove `FieldMatcher`/Props surfaces and migrate shipped examples.
- Modify `internal/lsp/definition_attr.go`, `definition.go`, `hover.go`, `protocol.go`, `server.go`; create `internal/lsp/rename.go`: exact-name navigation and semantic parameter rename.

---

### Task 1: Ordered Declaration Shape and Build-Tag Contract Identity

**Files:**
- Create: `internal/codegen/component_signature.go`
- Create: `internal/codegen/component_signature_test.go`
- Modify: `internal/codegen/analyze.go` (`parseParams` delegates parsing only; shipping consumers stay unchanged)
- Modify: `internal/codegen/params_sigtype_test.go`
- Modify: `internal/codegen/variantcollide.go:1-70`
- Modify: `internal/codegen/variantcollide_test.go:25-95`
- Modify: `internal/codegen/module_perf_test.go`

**Interfaces:**
- Consumes: `*gsxast.Component`, `parseRecv`, `parseTypeParamFieldList`, Go parser/printer normalization.
- Produces: one shared `parseParamFieldList` AST parse; final `componentParamDecl` entries preserving every logical parameter including unnamed, `_`, and variadic; unchanged existing `parseParams` behavior for the old Props path; `componentDeclarationFor(*gsxast.Component) (componentDeclaration, error)`; collision-safe `componentDeclaration.canonical()` as a deterministic syntax key for later skeletons. Task 3 replaces syntax spelling as the variant-acceptance authority with exact semantic comparison; Task 8 deletes the old `param`/`parseParams` path outright.

- [x] **Step 1: Add declaration-parser tests with valid named and unnamed families**

Go forbids mixing named and unnamed parameters in one list. Add two independent cases to `component_signature_test.go` for `parseComponentParamDecls`:

```go
{src: "a, b string, _ bool, rest ...byte", nTypes: []string{"string", "string", "bool", "...byte"}},
{src: "string, bool, ...byte", nTypes: []string{"string", "bool", "...byte"}},
```

Assert both logical results and the invalid name position for unnamed entries:

```go
assertDeclParams("a, b string, _ bool, rest ...byte",
	[]string{"a", "b", "_", "rest"}, []bool{false, false, false, true})
unnamed := assertDeclParams("string, bool, ...byte",
	[]string{"", "", ""}, []bool{false, false, true})
for i, p := range unnamed {
	if p.nameOff != -1 { t.Fatalf("unnamed param %d nameOff=%d, want -1", i, p.nameOff) }
}
```

- [x] **Step 2: Run the focused declaration test and verify the final parser is absent**

Run: `go test ./internal/codegen -run TestParseComponentParamDecls -count=1`

Expected: FAIL to compile because `componentParamDecl` and `parseComponentParamDecls` do not exist.

- [x] **Step 3: Implement the final declaration parser without changing the old Props path**

Extract the current synthetic Go parse into a shared helper:

```go
type parsedParamFieldList struct {
	src   string
	synth string
	fset  *token.FileSet
	list  *goast.FieldList
}

func parseParamFieldList(src string) (parsedParamFieldList, error)
```

Keep `parseParams`'s output and field loop unchanged so every old Props consumer remains byte-for-byte stable. Add the final model in `component_signature.go`:

```go
type componentParamDecl struct {
	name           string
	normalizedType string
	typeSrc        string
	nameOff        int // -1 for unnamed
	typeOff        int
	typeLen        int
	variadic       bool
	role           declarationParamRole
}

type declarationParamRole uint8

const (
	declarationParamOrdinary declarationParamRole = iota
	declarationParamChildren
	declarationParamAttrs
)

func parseComponentParamDecls(src string) ([]componentParamDecl, error)
```

`parseComponentParamDecls` walks the shared field list, emits one entry for an unnamed field and one per name for a named/grouped field, computes variadic with `*goast.Ellipsis`, and records `nameOff=-1` for unnamed entries. It never derives shape from strings. This is the model Tasks 2-12 retain; the old `param` type is not extended and no compatibility/projection helper is added.

- [x] **Step 4: Add declaration normalization tests before the implementation**

Create `component_signature_test.go` with:

```go
func TestComponentDeclarationCanonical(t *testing.T) {
	a := mustParseComponent(t, "package v\ncomponent C(a, b string, attrs ...gsx.Attr) { <i/> }\n")
	b := mustParseComponent(t, "package v\ncomponent C(a string, b string, attrs ...gsx.Attr) { <b/> }\n")
	c := mustParseComponent(t, "package v\ncomponent C(b string, a string, attrs ...gsx.Attr) { <i/> }\n")
	d := mustParseComponent(t, "package v\ncomponent C(a string, b string, attrs []gsx.Attr) { <i/> }\n")
	sa, err := componentDeclarationFor(a); if err != nil { t.Fatal(err) }
	sb, err := componentDeclarationFor(b); if err != nil { t.Fatal(err) }
	sc, err := componentDeclarationFor(c); if err != nil { t.Fatal(err) }
	sd, err := componentDeclarationFor(d); if err != nil { t.Fatal(err) }
	if sa.canonical() != sb.canonical() { t.Fatal("grouped and ungrouped logical parameters must match") }
	if sa.canonical() == sc.canonical() { t.Fatal("parameter reorder must change the contract") }
	if sa.canonical() == sd.canonical() { t.Fatal("variadic position must change the contract") }
}

func TestComponentDeclarationRenameChangesContract(t *testing.T) {
	a := mustParseComponent(t, "package v\ncomponent C(value string) { <i/> }\n")
	b := mustParseComponent(t, "package v\ncomponent C(label string) { <i/> }\n")
	sa, _ := componentDeclarationFor(a)
	sb, _ := componentDeclarationFor(b)
	if sa.canonical() == sb.canonical() { t.Fatal("parameter name is part of the markup contract") }
}
```

- [x] **Step 5: Implement the syntax-only declaration shape**

In `component_signature.go`, add:

```go
type componentDeclaration struct {
	recvType   string
	typeParams string
	params     []componentParamDecl
}

func componentDeclarationFor(c *gsxast.Component) (componentDeclaration, error)
func (d componentDeclaration) canonical() string
func normalizedTypeParams(src string) (string, error)
```

`componentDeclarationFor` uses `parseRecv`, `parseComponentParamDecls`, and `normalizedTypeParams`. `canonical` never reads the component body. Encode every field with a collision-safe length prefix (`<decimal byte length>:<bytes>`) and encode variadic/role as fixed bytes; do not join free-form Go type text with a delimiter. Include receiver type, normalized type parameters, each logical parameter's exact name, normalized type, variadic bit, and syntactic role (`ordinary`, `children`, or `attrs`) in order. Normalize type text through the Go AST/printer, not `strings.Fields`, so aliases are preserved rather than expanded.

- [x] **Step 6: Replace sorted Props identity with ordered declaration identity**

Make `componentSignature` in `variantcollide.go` return `componentDeclarationFor(c).canonical()`. On parse failure, length-prefix the trimmed raw receiver/type-param/param source so malformed alternatives still compare deterministically without collisions. Remove the body-derived `usesChildren`, `usesAttrs`, field capitalization, and prop sorting. This is the pre-semantic foundation key only; it must not remain the final variant-equivalence test after Task 3.

Update `TestComponentSignature` to assert:

```go
if componentSignature(d) == componentSignature(e) {
	t.Fatal("parameter order must affect the verbatim component contract")
}
```

Add assertions that rename, `[]T` versus `...T`, receiver type, and constraint spelling differ; body-only changes and grouped-versus-ungrouped equivalent lists match.

- [x] **Step 7: Run tests and static checks**

Run: `go test ./internal/codegen -run 'TestParseParamsTypeSpans|TestParseComponentParamDecls|TestComponentDeclaration|TestComponentSignature|TestDetectSignatureConflicts' -count=1`

Expected: PASS.

Run: `go test ./internal/codegen -count=1`

Run: `go test ./internal/corpus -run TestCorpus -count=1`

Expected: PASS; extracting the shared Go parse has not changed the shipping Props path.

Run: `gopls check -severity=hint internal/codegen/component_signature.go internal/codegen/analyze.go internal/codegen/variantcollide.go`

Expected: no new errors or unused declarations.

Add `BenchmarkModuleGenerateComponentCold` and `BenchmarkModuleGenerateComponentWarm` to `module_perf_test.go` with sub-benchmarks `same-package`, `imported`, and `embedded`. Their GSX fixtures use only explicit ordinary props so the sources are valid before and after the cutover. Call `b.ReportAllocs()` in every sub-benchmark.

For each cold sub-benchmark, create one complete filesystem fixture before starting the timer. Every timed iteration constructs a fresh `Module` over that existing fixture and calls `Generate`; do not create a temp directory or rewrite fixture files inside the timed region.

For each warm sub-benchmark, open one `Module` and prime it with one successful `Generate` before starting the timer. Prepare two byte-distinct, semantically equivalent valid source variants. On every iteration, stop the timer, alternate variants with `SetOverride`, restart the timer, and time only `Generate`. Alternating bytes is required because an unchanged override does not invalidate the module cache; `SetOverride` itself is setup, not part of the generation measurement.

Run: `go test ./internal/codegen -run '^$' -bench 'BenchmarkModuleGenerateComponent(Cold|Warm)' -benchmem -count=5`

Record all baseline `ns/op`, `B/op`, and `allocs/op` rows in `.superpowers/sdd/progress.md`; Task 6 reruns the exact benchmark names after the two-phase analyzer/importer work.

- [x] **Step 8: Commit**

```bash
git add internal/codegen/component_signature.go internal/codegen/component_signature_test.go internal/codegen/analyze.go internal/codegen/params_sigtype_test.go internal/codegen/variantcollide.go internal/codegen/variantcollide_test.go internal/codegen/module_perf_test.go
git commit -m "refactor(codegen): preserve ordered component declaration contracts"
```

### Task 2: Universal Type-Driven Signature Classifier

**Files:**
- Modify: `internal/codegen/component_signature.go`
- Modify: `internal/codegen/component_signature_test.go`
- Reference then later delete: `internal/codegen/attrsonly.go`, `byo.go`

**Interfaces:**
- Consumes: a concrete `*types.Signature` plus `runtimeContract{node, attr, attrs types.Type}`. The canonical `attrs` identity is explicit because `gsx.Attrs` is passed directly while another defined `[]gsx.Attr` type requires conversion; never recover this distinction from package-path/name strings.
- Produces: `analyzeComponentSignature(*types.Signature, runtimeContract) (componentSignatureModel, error)`, ordered `componentParam{variable, origin, name, typ, index, role, attrsMode}`, and exact tag-eligibility diagnostics.

- [x] **Step 1: Write table tests for every role and rejection**

Construct signatures with `types.NewSignatureType`; cover ordinary fixed params, `children gsx.Node`, `children ...gsx.Node`, all four attrs forms, aliases, defined and instantiated-defined `[]gsx.Attr`, rejected `[]MyAttr`, rejected `children []gsx.Node`, blank/unnamed fixed params, named and unnamed ordinary variadics (Go-only and omittable), and result assignable-to-Node versus zero/multiple results. Use a non-empty synthetic Node interface so unrelated values are not accidentally assignable. Pin that only the final parameter is variadic, that `ctx`/`_gsx...` fixed parameters remain reserved, and that direct, nested, variadic, and incomplete-alias `types.Invalid` shapes fail closed while a valid nullary function prop remains eligible.

Instantiate `func[T any](attrs Bag[T]) Node` at `T=gsx.Attr`: the model must retain the instantiated current `*types.Var` and `Bag[gsx.Attr]` type while separately recording `Var.Origin()`; the raw `Bag[T]` and an instantiation at `string` are not admitted as attrs bags. Include a same-path-but-distinct-`types.Package` Attr to prove classification uses semantic identity rather than path/name matching.

- [x] **Step 2: Run the unit test and verify the classifier is undefined**

Run: `go test ./internal/codegen -run TestAnalyzeComponentSignature -count=1`

Expected: FAIL to compile with `undefined: analyzeComponentSignature`.

- [x] **Step 3: Implement the model without source-name heuristics**

Use these stable types:

```go
type runtimeContract struct {
	node  types.Type
	attr  types.Type
	attrs types.Type
}

type paramRole uint8
const (roleProp paramRole = iota; roleChildren; roleAttrs; roleGoOnlyVariadic)
type attrsParamMode uint8
const (attrsDirect attrsParamMode = iota; attrsDefinedSlice; attrsVariadic)
type componentParam struct {
	variable, origin *types.Var
	name string
	typ types.Type
	index int
	role paramRole
	attrsMode attrsParamMode
}
type componentSignatureModel struct {
	goSig *types.Signature
	params []componentParam
	result types.Type
}
```

Record instantiated parameter identity through `types.Var.Origin()` without replacing the current variable or its substituted type. Walk each parameter/result semantic type graph with cycle detection and reject any nested `types.Invalid` or incomplete alias; never use printed text or a top-level-only check. Use `types.Identical` for `gsx.Node`, `gsx.Attr`, and canonical `gsx.Attrs`; use `types.AssignableTo` only for the valid result contract; and use the exact underlying-slice rule for other defined attrs types. Variadic status comes only from `sig.Variadic()` on the final parameter, never from seeing a slice type. Ordinary non-reserved variadics are `roleGoOnlyVariadic` and cannot bind markup.

- [x] **Step 4: Run and commit**

Run: `go test ./internal/codegen -run 'TestAnalyzeComponentSignature|TestAttrs' -count=1`

Expected: PASS.

```bash
git add internal/codegen/component_signature.go internal/codegen/component_signature_test.go
git commit -m "feat(codegen): model component signatures by ordered Go parameters"
```

### Task 3: Stable AST Preprocessing and Per-Element Target Discovery

**Files:**
- Create: `internal/codegen/component_target.go`
- Create: `internal/codegen/component_target_test.go`
- Modify: `ast/ast.go`, `parser/markup.go`, parser position tests, `internal/jsx/jsx.go`
- Modify: `internal/codegen/analyze.go` (`buildSkeleton`, embedded markup handling)
- Modify: `internal/codegen/tagresolve.go`, `unused_imports_syntactic.go`
- Modify: `internal/codegen/module_importer.go` (`analyze`, imported declaration facts, target-declaration import graph/cache)
- Modify: `internal/codegen/renderer_decls.go`
- Modify: `internal/codegen/results.go`

**Interfaces:** Produces one deterministic registry after the only preprocessing pass:

```go
type callSiteID uint32

const invalidCallSiteID callSiteID = 0

type callSiteDisposition uint8

const (
	callSitePlanned callSiteDisposition = iota
	callSitePreserveUnsupportedGoBlock
)

type callSiteRecord struct {
	id          callSiteID
	path        string
	element     *gsxast.Element
	disposition callSiteDisposition
}

type callSiteRegistry struct {
	byElement map[*gsxast.Element]callSiteID
	records   []callSiteRecord // ID n is records[n-1]; IDs are one-based.
}

type callSitePreprocessResult struct {
	registry  *callSiteRegistry
	syntaxOK  bool
	scriptsOK bool
}

func (r callSitePreprocessResult) analysisReady() bool {
	return r.syntaxOK && r.scriptsOK
}

func preprocessComponentCallSites(
	files map[string]*gsxast.File,
	declNames map[string]bool,
	fset *token.FileSet,
	classifier *attrclass.Classifier,
	bag *diag.Bag,
) (callSitePreprocessResult, error)
```

Sort paths before walking. `parsePackageWithFset` returns a private `parsedGSXPackage` that owns a copied file map from one fresh parse. Its atomic one-shot package transition is claimed before the first mutation; concurrent or repeated use is an internal hard error emitted before diagnostics, never a retry against partial state. Do not store lifecycle state on public `ast.File` nodes: it would affect AST equality, be bypassable by shallow copies, and could not provide package atomicity. The preprocessing transition subsumes both `resolveComponentTags` and `splitInterpEmbedded`: materialize all embedded markup; fail closed if a split fails; and reconstruct every top-level `GoWithElements` through one canonical source-mapped lowering. That lowering applies the shared decorative-parenthesis rules and `goexprshape.Sanitize`, represents every non-text GSX part with a unique non-call value marker, and maps every sanitizer/parser byte offset back to the authored `token.Pos`. The marker category matches final value semantics: element/fragment emission is a `gsx.Func(...)` conversion value, not an invoked function, so `go`/`defer` must reject it just as they reject `f`/`js`/`css` values. Reject any Go parser error or recovery AST. Require every marker to map to one exact enclosing declaration/self-exclusion set; classify parser failures as `parse-error`/`parser` and structural mapping failures as `invalid-go-declaration`/`codegen`; run the JavaScript safety resolver over the complete expanded tree; stamp every resulting element once; then allocate IDs in authored AST order. `syntaxOK` and `scriptsOK` preserve the package-skip reasons explicitly; `registry` is nil whenever `analysisReady()` is false, so no consumer can accidentally use partial facts. Register supported sites as `callSitePlanned`; register direct element literals beneath `GoBlock.Embedded` as `callSitePreserveUnsupportedGoBlock`, emit the existing diagnostic once, and never probe or plan them. Store the registry on `analyzed`; both skeleton phases and emission consume it. Remove materialization from `buildSkeleton`; no later phase clones, reparses, or restamps embedded markup.

Target discovery produces immutable facts:

```go
type componentTargetProvenance uint8

const (
	targetPackageFunc componentTargetProvenance = iota + 1
	targetPackageVar
	targetConcreteMethodValue
)

type authoredTypeArgFact struct {
	expr goast.Expr
	typ  types.Type // nil when the authored type expression is invalid
}

type componentTargetFact struct {
	site callSiteID
	expr goast.Expr // exact target expression in the discovery skeleton

	object types.Object // nil when target lookup/provenance failed
	origin types.Object // nil on failure; otherwise (*types.Func).Origin or (*types.Var).Origin

	// Pre-explicit-arguments call shape. For a bound method this is
	// Selection.Type(): receiver omitted from callable Params and receiver
	// arguments substituted; Signature.Recv metadata remains present.
	raw *types.Signature // nil when no static callable signature was established

	// The exact authored prefix, including partial F[A] for F[A, B].
	authoredTypeArgs []authoredTypeArgFact

	// Set only when target-only checking completed the whole instantiation.
	explicitInstance *types.Instance

	// Positioned site-local target/type-argument errors, deferred so Task 5
	// can preserve authored-operand diagnostic precedence.
	targetDiags []diag.Diagnostic

	provenance componentTargetProvenance

	hasSelection  bool
	selectionKind types.SelectionKind
	selectionRecv types.Type
}

func (f componentTargetFact) effectiveSignature() *types.Signature
```

`effectiveSignature` returns `f.explicitInstance.Type.(*types.Signature)` only when target-only checking completed the entire instantiation, otherwise `f.raw`. A partial authored prefix is retained even without an instance. Inferred instances belong to Task 5's result, not this immutable discovery fact. Marker identifiers map directly to `callSiteID`; tag text is never a key.

The target-fact map is total over every `callSitePlanned` record. Lookup, provenance, callability, and type-argument failures still produce a fact with nullable semantic fields plus deferred `targetDiags`; provenance rejection is site-local and follows the same deferred path. Only `callSitePreserveUnsupportedGoBlock` records have no target fact. Task 5 must not need a second synthetic “missing target” path to recover diagnostic precedence.

- [ ] **Step 1: Add failing tests for identity and provenance**

Pin: two same-text tags get distinct IDs; a supported tag embedded in an interpolation or top-level `GoWithElements` keeps the same pointer/ID across discovery and a second walk; a direct tag inside `{{ }}` retains one ID only until the existing `unsupported-node` diagnostic and never gets a target fact; package funcs/func vars and concrete `MethodVal` are accepted; `MethodExpr`, `FieldVal`, locals, parameters, and interface dispatch are rejected. Also pin a same-text import alias shadowed by a concrete receiver value: package-selector provenance is unavailable, but a real `MethodVal` remains eligible because eligibility follows the resolved semantic object/selection, not the qualifier's spelling.

Run: `go test ./internal/codegen -run 'TestAssignCallSites|TestHarvestComponentTargets' -count=1`

Expected: FAIL because the registry and fact types do not exist.

- [x] **Step 2: Pre-materialize embedded AST once**

Move embedded element splitting/materialization ahead of both skeleton phases. Claim the private parsed-package owner once before mutation and reject any concurrent or repeated pass before it can duplicate diagnostics. Before semantic work, validate all top-level `GoWithElements` exclusion mappings through the canonical source-mapped GSX expression lowering: reuse `goWithElementsParenShapes`, the matching leading/trailing parenthesis stripping, and `goexprshape.Sanitize`; emit a unique non-call value marker for every non-text GSX part; and retain exact output spans through every whitespace transformation so parser diagnostics map back to the original `token.Pos`. This preserves final `go`/`defer` rejection for every GSX value, including element/fragment conversion values. Reject any `go/parser` error rather than trusting its recovery AST, and reject any non-text part that cannot be mapped to one exact declaration rather than returning a partial exclusion map. Preserve `parse-error`/`parser` for parser failures and use `invalid-go-declaration`/`codegen` for exact mapping failures. Run JSX resolution only after expansion and make its canonical walker cover `Interp.Embedded`, `GoBlock.Embedded`, Go-part literals, and markup-bearing attributes. Resolve/stamp `Element.IsComponent` once, then build `callSiteRegistry`; later phases must consume those nodes and may not call the embedded splitter or clone markup. Mark the first direct element/fragment in a `{{ }}` block once on `GoBlock.UnsupportedMarkup`; JSX, stamping, registry collection, skeleton construction, body-fact analysis, reserved-name validation, and emission consume that annotation rather than rediscovering the policy. Emit the existing single diagnostic, preserve direct element records, and exclude the entire block from further expression materialization, secondary diagnostics, discovery, validation, and emission while still stamping the already-materialized direct elements.

This package-level prepass **replaces** the existing `resolveComponentTags` loop, the pre-expansion `ResolveScripts` loops, and the `buildSkeleton(skeletonFull)` call to `splitInterpEmbedded`; it must not run alongside any of them. Low-level split functions remain pointer-idempotent on an already-populated `Embedded` field, but the package prepass deliberately rejects reuse because stamping and diagnostics are single-shot effects. Every separately parsed production AST must use this function before reading body-derived declaration facts: retained analysis, importer-free unused-import/formatter analysis, imported dependency facts, and `rendererDeclResolver`. Those lanes may discard the registry, but may not preserve a second materializer or semantic walker hidden in a downstream helper. A not-ready result is fail-closed for that lane; it is never permission to consume partial body facts. The unused-import lane also omits every exact source file carrying a preprocessing error, because a preserved/excluded region may contain that file's only import reference; positioned clean siblings may still be classified, while an unpositioned or unattributable error blocks the package. If any imported GSX dependency fact fails, omit the importing file too: a skeleton built from whichever dependency facts happened to succeed is partial and cannot authorize import removal.

Route `packageDeclNames` for `GoWithElements` through the same canonical `reconstructGoWithElements` source and reject any parser error; it may not substitute `nil` independently or consume a recovery AST before the prepass. Freeze the legacy implicit `usesChildren`/`usesAttrs`/`usedParams` semantics during these foundation commits except for consuming `GoBlock.UnsupportedMarkup`. Do not extend those syntax-only scanners over newly materialized parts: exact lexical identity at `S{attrs: 1}` versus `map[any]int{attrs: 1}` requires type information, while a second transitional typed analyzer would duplicate this feature's planned phases and then be deleted. Task 8 removes the scanners atomically and pins the materialized/shadowed forms against real authored Go parameters.

- [ ] **Step 3: Add a target-discovery skeleton mode and harvest exact origins**

Add distinct `skeletonTargetDiscovery` and `skeletonTargetDeclarations` modes; do not alter or reuse the shipping `skeletonDeclarations` Props ABI. Target declarations emit exact authored component signatures with inert bodies. Discovery preserves the real lexical/probe scopes but replaces each legacy child Props call with one registered, untyped target binding whose RHS is the exact target expression. Give those bindings a separate marker registry keyed by call-site ID; never mutate `callSiteRegistry` into a second role.

Before emitting a target RHS, syntax-check that one synthesized target expression without trusting a recovery AST. Add exact parser-recorded `Element.TagPos`, `TypeArgsOpenPos`, and `TypeArgsClosePos`; do not derive the closing bracket from trimmed `TypeArgs` bytes because trailing whitespace is accepted. Map tag, brackets, and argument bytes as separate source segments. A bad target/type-argument expression creates that site's total fact with a positioned deferred parse diagnostic and emits a parse-safe inert binding, so one malformed `<F[!]/>` cannot make the whole discovery skeleton unparseable or suppress sibling facts.

Emit each syntax-valid registered target expression inside its real lexical scope. For indexed targets, obtain the supplier identifier structurally from `Ident`, `SelectorExpr.Sel`, `IndexExpr.X`, or `IndexListExpr.X`; retain every authored index expression in order and its `types.Info.Types` result. Harvest `types.Info.Instances` only when target-only checking completes the whole instantiation; a partial prefix remains valid input for Task 5 even though no instance exists yet. Go/types may populate `Info.Instances` even when an explicit argument violates its constraint, so install `explicitInstance` only when the instance exists **and that site has zero target-check errors**. Capture site-local generic/target/type-argument diagnostics on the fact rather than emitting them immediately, so Task 5 can report authored operand errors first; unrelated skeleton diagnostics remain fatal. Retain the generic origin signature even when the target alone reports `cannot use generic function ... without instantiation` or too few type arguments.

Add `Instances`, `Selections`, and `Implicits` to the discovery `types.Info`. Give every emitted marker expression an exact raw skeleton byte span and partition `types.Error` positions through the shared FileSet with `PositionFor(pos, false)`; only an error inside that registered span may become that site's deferred target diagnostic. Assert each planned syntax-valid site has exactly one marker, marker spans do not overlap, and harvested raw AST offsets equal the recorded bytes. Never recognize an expected target failure from message text. Default import `PkgName` objects come from `Implicits`, explicit aliases from `Defs`, and package-selector provenance requires the target qualifier's actual `Uses` object to equal that `PkgName`. A same-text local is not package provenance; continue with its actual semantic shape, accepting a concrete `MethodVal` and rejecting a `FieldVal`, local callable, or interface dispatch under the ordinary rules.

Classify provenance exactly: a package func is a `*types.Func` whose parent is `obj.Pkg().Scope()`; a package var is callable only when its `*types.Var` has that same package-scope parent; a bound method requires `Selection.Kind()==types.MethodVal` and a declared receiver that is not interface-based. For promotion through a concrete struct embedding an interface, inspect the selected method object's declared signature receiver—not only `Selection.Recv()`, which is concrete—and reject it as interface dispatch. Reject `MethodExpr`, `FieldVal`, local/parameter callables, and interface methods even when their current type is callable. This rejection is independent of `explicitInstance`: go/types can give a generic `MethodExpr` an instance whose parameters omit the explicit receiver, but that does not make it a bound method value. The raw bound-method call shape is `Selection.Type().(*types.Signature)`; its callable `Params()` omit the receiver and substitute receiver arguments, while `Signature.Recv()` metadata remains present.

The only target-check error omitted from `targetDiags` is structurally proven incomplete generic instantiation: the supplier resolves to the raw generic function/method, the authored prefix is shorter than its target type-parameter arity, and the sole site-local error lands at the exact raw target expression start. This rule uses no diagnostic message text. Bare and partial generic method values report at the whole target start rather than the selector identifier; type-argument arity, constraint, and lookup failures land elsewhere and remain deferred diagnostics. A full instance is still installed only when the site has zero target-check errors.

Discovery uses a phase-specific exact-declaration importer/cache. The current `pkgTypes` graph exposes the shipping pre-cutover Props ABI and is therefore invalid for this phase. Recursively source-check every module-local package in the exact graph: project GSX packages contribute `skeletonTargetDeclarations`, while Go-only intermediaries contribute the retained active compiled-file ASTs described below. Ignore paired disk `.x.go`, reuse the existing external importer only beyond that module-local source graph, keep an independent cycle guard/cache cleared by the normal invalidation and FileSet rebuild paths, and never call another `packages.Load`. Record edges through both GSX and Go-only packages so a `gsx → Go-only → gsx` chain invalidates transitively. Same-package and cross-package target discovery must therefore observe the same current authored signature without changing the shipping ABI before Task 8. Reject any external dependency whose transitive imports re-enter the main module, with a positioned semantic-boundary diagnostic on the authored import. Do not source-recheck such an external package, ignore its bodies, or retain phase-specific reconstructed external packages; that unsupported topology requires a separate whole-graph design.

Extend the existing cold load with `NeedCompiledGoFiles|NeedSyntax`. Before that
same load, scan authoritative `.gsx` paths (including pre-load overrides) and
overlay each existing paired `.x.go` with a contradictory build constraint so
the Go command excludes it before package classification. Retain module-local
source packages by exact clean-directory plus `importPathForDir` identity, not a
module-path prefix. Exact target checking reuses the retained AST for each active
compiled file, including cgo-transformed files; it does not reparse disk source.
Freeze the Module's build environment at Open. A new override that first claims
an already-compiled paired output marks the source inventory dirty and forces an
atomic FileSet/importer/cache rebuild before the next analysis. Bundle mode has
no source inventory and fails closed when companion source would need selection.

Retain `NeedTypesSizes` and authoritative module language provenance in the
cold inventory, and pass both to shipping, exact-target/preflight, and renderer
manual checkers. Normalize an existing module with no `go` directive to cmd/go's
`go1.16` default; do not accept missing module provenance. The typebundle format
is a versioned target envelope carrying compiler, GOOS, GOARCH, cgo,
ToolchainVersion, LanguageVersion, and observed build/tool/release tags. Its
producer seals one Go-launcher identity plus immutable Env/Dir/BuildFlags for the
context query and `packages.Load`: direct queries execute the sealed absolute
launcher, while x/tools resolves `go` through the frozen PATH and is guarded by
exact launcher validation immediately before and after the load. It disables
ambient GO configuration and external drivers and requires non-nil loaded
`TypesSizes` without trying to infer target identity from layout comparisons.
The format accepts only `gc`, rejects a producer ToolchainVersion newer than the
pinned Go 1.26.1 reader, and treats `gcexportdata` bytes as an opaque upstream
payload. The outer envelope validates exact payload length, SHA-256, and
canonical metadata; do not port, parse, canonicalize, or otherwise couple gsx
to x/tools' private indexed-export framing. Treat the archive as a trusted
embedded build artifact: SHA-256 pins integrity, not hostile-input authenticity.

Before constructing `packages.Config`, resolve the Go command's effective
`GOFLAGS` (including `go env -w` and last-flag-wins repetition) and reject an
effective non-empty `-overlay`. Also reject an explicit or frozen-PATH-discovered
external `GOPACKAGESDRIVER`. After that proof, pin `GOPACKAGESDRIVER=off` in the
load environment so x/tools cannot re-evaluate a later live PATH; this does not
hide configured driver state. Do not materialize only replacement overlays or
let `Config.Overlay` replace user configuration. These are explicit normal-mode
boundaries; Bundle mode never reaches them.

Validate component variant membership before folding declarations: every member
must have an effective Go constraint on its generated filename/source. Preserve
same-file duplicates for native errors and reject unconstrained or mixed
families. Emit every valid variant signature under a unique analysis-only name,
then require the exact ordered parameter name/role vector, a
`types.Identical` signature, and a separately identical receiver type before
choosing one public representative. Alias spelling and type-parameter names are
not identity. Remove generic cross-file redeclaration suppression: only component
declarations have a logical variant plan, while raw Go alternatives are errors
and belong in constrained `.go` files. All GSX bodies/imports remain subject to
the one frozen analysis universe; unavailable inactive-platform imports fail.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/codegen -run 'TestAssignCallSites|TestHarvestComponentTargets|TestTargetDiscovery' -count=1`

Expected: PASS, including embedded sites and repeated/shadowed tags.

```bash
git add ast/ast.go internal/jsx/jsx.go internal/codegen/component_target.go internal/codegen/component_target_test.go internal/codegen/analyze.go internal/codegen/tagresolve.go internal/codegen/unused_imports_syntactic.go internal/codegen/module_importer.go internal/codegen/renderer_decls.go internal/codegen/results.go
git commit -m "feat(codegen): discover callable targets by stable call-site identity"
```

### Task 4: Pure Exact-Name Input and Attrs-Stream Planner

**Files:**
- Create: `internal/codegen/component_call_plan.go`
- Create: `internal/codegen/component_call_plan_test.go`

**Interfaces:**
- Produces these pure, syntax-level records:

```go
type componentInputKind uint8 // prop, body, attrs pair/segment/contributor, omitted
type componentInputValue struct {
	kind componentInputKind
	sourceIndex, paramIndex, contributorIndex int
	node gsxast.Node
	attrsNode *componentAttrsStreamNode
	children []gsxast.Markup // source-comment-free static body children
}
type componentAttrsStreamKind uint8 // pair, spread, explicit contributor, conditional
type componentAttrsStreamNode struct {
	kind componentAttrsStreamKind
	sourceIndex int // local to the immediately containing attribute list
	attr gsxast.Attr
	then, otherwise []componentAttrsStreamNode
	hasSyntaxError bool // invalid descendants omitted from the value tree
}
type componentArgSlot struct { param componentParam; valueIndexes []int; omitted bool }
type componentCallPlan struct {
	site callSiteID
	call *gsxast.Element
	callStart, callEnd token.Position
	target componentSignatureModel
	args []componentArgSlot       // signature order
	values []componentInputValue  // authored order
}
func planComponentInputs(site callSiteID, el *gsxast.Element, target componentSignatureModel, fset *token.FileSet) (componentCallPlan, []diag.Diagnostic)
```

- The plan distinguishes ordinary prop bindings, one body binding, attrs bag
  pairs/segments/explicit contributors, omitted fixed params, and omitted
  Go-only variadics. It retains both the authored call node and its already-
  resolved range so Task 5 can diagnose an omitted required prop without a
  second registry or `FileSet`.

- [x] **Step 1: Write planner tests**

Cover exact-case matching, duplicate ordinary props, `_foo`, exact ordinary
`Attrs` and `Children` parameters coexisting with lowercase reserved roles,
undeclared capitalized names following ordinary unmatched/fallthrough and
missing-`attrs` behavior, non-identifier fallthrough, strict missing attrs, body
without children, comment-only and interspersed-comment bodies, explicit
`children=` rejection, `attrs={expr}`, repeated `attrs={{...}}`, `{bag...}`,
conditional bags including nested/leafless/comment-only and invalid-only
branches, ordinary `someAttrs={{...}}`, ordinary
`someAttrs={computedBag}`, node-valued markup props, class/style exact targets,
resolved call provenance, and omitted named/unnamed ordinary variadics.

At this syntax-only phase every `{expr...}` is routed as an attrs-stream contributor. A struct expression and a `gsx.Attrs` expression share the same `SpreadAttr` syntax; Task 5 rejects non-bag semantic types after `go/types` facts exist. Do not classify a struct splat from expression text.

- [x] **Step 2: Run the failing test**

Run: `go test ./internal/codegen -run TestPlanComponentInputs -count=1`

Expected: FAIL to compile with `undefined: planComponentInputs`.

- [x] **Step 3: Implement routing before value lowering**

Match an identifier-shaped name only against an ordinary parameter's exact
`Name`. Treat `attrs` as a repeatable contributor stream, not a prop slot. Keep
every attrs contributor's authored index; never create `attrsLitIdx` or a
forced-last marker. Normalize a conditional into one top-level contributor with
a recursive, branch-local ordered tree; branch names never fill props. Validate
lowercase reserved forms at every leaf. Normalize syntax independently from the
presence of an `attrs` destination: each valid leaf gets its own positioned
missing-attrs diagnostic, a genuinely leafless conditional gets one at its own
range, nested parents do not duplicate it, and invalid-only branches retain only
their precise syntax diagnostics. Route spreads without inspecting expression
text or type. Filter source comments once into the body value's retained child
slice. Use the supplied package `token.FileSet` to resolve both call provenance
and stable planner errors (`duplicate-prop`, `component-missing-attrs`,
`component-missing-children`, `reserved-input-form`,
`ordinary-variadic-prop`).

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/codegen -run TestPlanComponentInputs -count=1`

Expected: PASS.

```bash
git add internal/codegen/component_call_plan.go internal/codegen/component_call_plan_test.go
git commit -m "feat(codegen): plan exact-name component inputs"
```

### Task 5: Authored-Operand Inference, Inline Zeros, and Effect Planning

**Files:**
- Create: `internal/codegen/component_zero.go`
- Create: `internal/codegen/component_zero_test.go`
- Modify: `internal/codegen/component_call_plan.go`
- Modify: `internal/codegen/component_call_plan_test.go`
- Modify: `internal/codegen/infer.go`, `infer_test.go` (generalize the existing allocator; preserve probe output)
- Reference: `internal/codegen/component_value_order_test.go`

**Interfaces:**
- Produces semantic analysis records, not emitted helpers:

```go
type suppliedOperand struct { paramIndex int; expr goast.Expr; tv types.TypeAndValue }
type inferenceContext struct { pkg *types.Package; fset *token.FileSet; scope *types.Scope }

type generatedImportAllocator struct {
	prefix string
	next   int
	byPath map[string]string
	order  []importSpec
}

type generatedImportTxn struct {
	owner   *generatedImportAllocator
	baseLen int
	work    *generatedImportAllocator
}

func newGeneratedImportAllocator(prefix string) *generatedImportAllocator
func (a *generatedImportAllocator) begin() *generatedImportTxn
func (t *generatedImportTxn) qualifier(current *types.Package) types.Qualifier
func (t *generatedImportTxn) commit()
func (a *generatedImportAllocator) specs() []importSpec

type typeSpellingContext struct {
	pkg        *types.Package
	typeParams map[*types.TypeParam]string
	imports    *generatedImportAllocator
}

type zeroCandidate struct {
	expr    string
	imports *generatedImportTxn
}

type expressionFact struct {
	tv types.TypeAndValue
	isNil bool
	hasOrderedOperation bool
	tuple *types.Tuple
}
func inferAuthoredInstance(inferenceContext, componentTargetFact, []suppliedOperand) (types.Instance, []diag.Diagnostic)
func validateComponentOperands(componentCallPlan, map[gsxast.Node]expressionFact, runtimeContract) (componentCallPlan, []diag.Diagnostic)
func semanticZeroLiteral(types.Type) (string, bool)
func zeroCandidates(types.Type, typeSpellingContext) []zeroCandidate
func planComponentMaterialization(componentCallPlan, map[gsxast.Node]expressionFact) materializationPlan
```

- The inference carrier is a semantic `types.Func` installed in the checker scope; no constraint/type is copied to source and omitted parameters are absent. A positional validation skeleton accepts only a fully instantiated signature.
- Generalize the existing `aliasAllocator` into `generatedImportAllocator`; do not create a second alias implementation. Existing skeleton requalification constructs it with prefix `_gsxti` so current probe naming remains stable; final type spelling constructs the same implementation with `_gsxty`. A candidate type spelling starts a transaction and commits only after the positional `go/types` validation succeeds, so rejected exact/underlying spellings cannot leak unused imports. The reserved `_gsx` namespace makes both prefixes collision-safe. Every foreign type referenced by generated spelling uses its reserved alias even if user source already imports the package, so local shadowing cannot break generated code.

- [ ] **Step 1: Add failing semantic tests**

Pin `Infer[T](*T)` with authored `nil` as inference failure, constraint-derived inference success, imported unexported constraints, full and partial explicit type-argument prefixes, operand-error precedence over deferred target/constraint errors, incomplete-inference hints, native constraint failures, semantic numeric/nilable literals, `any` and a non-empty interface lowering specifically to `nil`, a mixed type set having no literal candidate, nameable `*new(T)`, accessible unnamed `*new(U)`, imported opaque struct omission failure, and opaque numeric/nilable success. Pin a `gsx.Attrs` spread as valid and a struct/other non-bag spread as `component-attrs-spread-type`. Pin source order `first(), second()` against reversed parameter order, untyped `min(1, 2)` remaining contextual, and `(T,error)` consumed as one value.

For import allocation, pin: an exported foreign type absent from source imports; an accessible unnamed type containing an exported foreign type; two packages with the same declared package name; a local/parameter shadowing the user's package alias; repeated references to one path reusing one generated alias; an opaque rejected candidate committing no import; and an exact candidate rejected while an unnamed-underlying candidate succeeds and commits only the winner's imports.

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/codegen -run 'TestInferAuthoredInstance|TestZeroCandidates|TestPlanComponentMaterialization' -count=1`

Expected: FAIL because the semantic planner functions do not exist.

- [ ] **Step 3: Implement inference before zero-fill**

Construct a semantic `types.Signature` from the origin signature's `*types.TypeParam` objects and supplied parameter types, install a private `types.Func` in the analysis package scope, and call that carrier with the authored type-argument prefix plus authored operands. Harvest its `types.Info.Instances`; this permits `F[int]` to infer remaining type parameters from operands and works with imported unexported constraints because no constraint source is copied. Instantiate the origin signature with that result; never use omitted-zero expressions as carrier arguments. Preserve diagnostic precedence by deferring site-local target/type-argument diagnostics from Task 3: authored operand error first, incomplete inference second, native explicit/inferred constraint failure third.

- [ ] **Step 4: Implement semantically validated inline zeros**

Do not iterate arbitrary assignable literals. Compute the actual zero-literal category from the instantiated `go/types.Type`; for a remaining type parameter, evaluate its complete type set and use a literal only when the same literal is the zero of every permitted type. Validate that one literal with `go/types`. Thus `any` and non-empty interfaces use `nil`, while a mixed numeric/string type set skips literal lowering. Then try exact type spelling and accessible unnamed underlying spelling. Produce foreign type text with `types.TypeString` and a candidate-local import transaction; validate identity/assignability in the positional skeleton and commit only the winning transaction. If every candidate fails, emit the positioned required-attribute diagnostic. Do not infer zero category from exported spelling or a partial underlying-kind check.

- [ ] **Step 5: Implement selective materialization**

Build each `expressionFact` from `go/types` first. A `TypeAndValue` with `Value != nil` or an untyped nil stays contextual and is never `:=`-materialized; only a non-constant typed expression then consults the existing exhaustive AST walk `exprHasOrderedOperation`. Materialize a value only when it crosses later Go-ordered work or needs statement lowering. Consume `(T,error)` before positional assembly for props, spreads, explicit attrs expressions, and every ordered-literal pair.

- [ ] **Step 6: Verify the pre-cutover planners and stop on semantic drift**

Run: `go test ./internal/codegen -run 'TestInferAuthoredInstance|TestZeroCandidates|TestPlanComponentMaterialization|TestComponentValue' -count=1`

Expected: PASS. If any test requires a helper declaration, guessed zero, or broad syntactic-call materialization, stop with that test as the reproducer.

```bash
git add internal/codegen/component_zero.go internal/codegen/component_zero_test.go internal/codegen/component_call_plan.go internal/codegen/component_call_plan_test.go internal/codegen/infer.go internal/codegen/infer_test.go
git commit -m "feat(codegen): plan inferred positional component calls"
```

### Task 6: Authoritative Project-Local Import Graph, Cache, Stale Output, and Performance

**Files:**
- Create: `internal/sourceview/**` (one authoritative owned-source manifest used
  by codegen and cache metadata)
- Modify: `internal/codegen/source_inventory.go`, `module_importer.go`, `module.go`,
  `resolver.go`, `module_stale_xgo_test.go`, `depfacts_test.go`,
  `invalidation_test.go`, `snapshot_cache_test.go`, `module_perf_test.go`,
  `bundle_module_test.go`
- Modify: `gen/cachekey.go`, `cachekey_test.go`, `watch.go`, `watchsession.go`,
  their tests, `orphan_e2e_test.go`, `poison_e2e_test.go`

**Interfaces:** This is ABI-neutral infrastructure and lands green before cutover. One authoritative project-package resolver always rechecks project-local Go-only packages against the **current in-memory GSX declaration skeleton**, whether that skeleton is the pre-cutover Props ABI or Task 8's verbatim ABI. Every local type consumer routes through it: ordinary shipping imports, exact-target/preflight, renderer declarations, filter packages/aliases, and class-merger validation. External packages alone come from the cold importer's prebuilt types. Declaration facts drive invalidation. The one normal-mode cold load retains exact project source metadata:

```go
type projectPackageSource struct {
	importPath      string
	name            string
	dir             string
	compiledGoFiles []string
	syntax          []*goast.File // aligned with compiledGoFiles/build context
	imports         []string
}

// Module fields:
projectSources map[string]projectPackageSource
projectTypes   map[string]*types.Package
```

`internal/sourceview.Manifest` is the single source of package membership,
authored GSX import edges/load roots, paired-output exclusions, and local module
replacement provenance. The codegen cold load enriches that manifest with the
active compiled-Go syntax above; persistent cache metadata consumes an immutable
cache projection of the same manifest. Neither consumer independently walks GSX
imports or runs a plain un-overlaid `go list` approximation.

- [ ] **Step 1: Add failing `gsx -> Go-only -> gsx` and signature-invalidation tests**

Create a temp graph where `page.gsx` imports project `bridge` Go source, `bridge` imports `ui`, and `ui/card.gsx` has a poisoned stale `card.x.go`. Give `bridge` mutually exclusive build-tag variants and put the stale-ABI reference only in the inactive variant. Before Task 8, assert bridge checking uses the current in-memory Props declaration rather than poisoned disk; the Task 7 ledger marks this fixture for conversion to the verbatim signature during cutover. Rename only a `ui.Card` parameter and assert importer/page facts invalidate through `bridge`, with `externalLoads()==1`.

Add watch/module tests proving saved disk changes are refreshed before warm
invalidation: create the first GSX source in a new package, add an import that was
absent from the cold importer, and change a file's package clause/membership.
Each next cycle must observe one coherent new manifest and never reuse the prior
FileSet/importer package selection. `Invalidate` without the refresh operation is
not a valid implementation of these cases.

Add persistent-cache parity tests for a GSX-only dependency/import edge, a
versionless local replacement whose `go.mod` changes, and a poisoned/changed
paired `.x.go`. The GSX-only and replacement changes must update dependency
identity; the still-paired output must remain excluded from both dependency
edges and cache source identity while orphan ownership transitions remain
visible to cleanup.

- [ ] **Step 2: Retain the cold load's authoritative compiled-file inventory**

Build `internal/sourceview.Manifest` once from owned disk sources plus overrides,
excluding nested modules/vendor, and use its exact overlays, sentinels, and roots
for the existing cold query. Its saved membership and bytes are the immutable
initial snapshot consumed by normal package parsing/analysis and persistent
cache identity; do not glob or reread live GSX files after publication. The
explicit refresh transition replaces that snapshot atomically while retaining
current overrides. Add `packages.NeedCompiledGoFiles |
packages.NeedSyntax` to the existing `packages.Config.Mode`. With the shared
`m.fset`, retain aligned `CompiledGoFiles`/`Syntax` only for module-local Go-only
packages. Do not glob, call `build.ImportDir`, or parse source again: that would
diverge on build tags, cgo-generated files, tests, and the load's environment.

`projectSources.syntax` and every `projectTypes` object carry positions tied to `m.fset`. Extend `rebuildFset`'s atomic reset to clear **both** maps alongside the existing position-bearing caches. The next single external load repopulates `projectSources` with ASTs parsed into the new FileSet; never reuse retained ASTs from the discarded FileSet.

Extend the existing FileSet-rebuild regression to use the Go-only bridge: assert both maps are empty immediately after rebuild, the next generation performs exactly one new external load, repopulates/rechecks the bridge, and every retained AST/object/diagnostic position resolves through the new `m.fset` rather than the discarded one.

Add the minimum cross-package refresh operation on `Module` for watch to publish
saved disk facts before calling `Invalidate`. It re-enumerates the complete
affected package directories (thereby observing new, changed, renamed, and absent
`.gsx` paths), compares package/import/membership facts with the published
manifest epoch, and chooses body-only invalidation or an atomic
FileSet/importer/manifest rebuild. Watch must route create, write, rename, and
remove events through this same refresh; it may not infer dependency-surface
changes from filename or event kind alone.

Pin the shared `watch`/`dev` structural rules with real fsnotify tests: do not
exit when a requested module has zero current GSX files; watch the requested
roots plus their owning module trees so newly-created sibling packages are seen;
register directory creates and scan contents before applying filename filters;
and ignore an `.x.go` event as generated output only when its exact same-base
`.gsx` source currently exists. An unpaired authored `.x.go` must invalidate.

Separate observation from output. Derive additional watched workspace/local-
replacement roots and their `go.mod`/`go.work` provenance from the shared frozen
source manifest. Generate only the requested roots plus their exact current GSX
dependency closure; an unrelated observed sibling must not gain an output merely
because the owning module tree is watched. Pin dynamic closure/root updates after
an authored import or module-provenance change.

Close the startup event gap with an integration test that edits a source after
watch registration but before initial generation completes: resolve roots, arm
watches, snapshot, generate, then drain queued events, and assert the edit gets a
second generation. On a generation error, retain the complete dirty set and
retry it on the next relevant event; `dev` must not discard the pending state or
publish it as clean. Treat both top-level regeneration errors and per-directory
operational `cycleResult.Err` values as uncommitted; a completed authored-
diagnostic/poison cycle has no operational error and commits normally.

Add real LSP lifecycle tests around the same source-view graph. Unsaved `.go`
buffers must participate in the cold `packages.Config.Overlay` used for both
selection and syntax. Refactor one exact affected-directory primitive from the
existing reverse closure plus renderer/configured-source whole-cache rule; use
it both for invalidation and the result of Set/Clear transitions. The server
evicts and supersedes the returned open-directory intersection, including
cross-directory reverse dependents. Model disk source as
present/absent/unreadable so Clear always removes buffer authority and an
unreadable saved path fails closed. A failed root transfer retires the previous
owner and stale package facts before returning the old affected set. Version
every debounce event at document mutation; advance the epoch on
set/cancel/close/transfer, reject queued stale or no-open-document events, and
supersede in-flight analysis at `didChange` receipt rather than timer fire.

- [ ] **Step 3: Re-type-check retained syntax through the shared project resolver**

When a project-local Go-only import is reached, type-check its retained ASTs with the same recursive project resolver; route GSX children to the current declaration skeletons, cache the result in `projectTypes`, and record forward/reverse edges for both Go-only and GSX directories. Shipping, target, renderer, filter/alias harvest, and merger validation select the appropriate GSX declaration mode while sharing retained Go-only syntax and graph ownership. A GSX declaration edit invalidates the reverse closure and rechecks retained syntax without another load. Do not add another `packages.Load` or a source parser. Pin local filter and merger packages whose Go-only source reaches a poisoned-stale GSX package; neither may harvest the cold load's partial/stale `Types`.

- [ ] **Step 4: Add and implement Bundle fail-closed behavior before cutover**

Build a Bundle test for the same `page -> bridge -> ui.gsx` graph with a stale bundled `ui` type. Assert generation returns `bundle-project-gsx-transitive` and directs the caller to the normal resolver. In Bundle mode, inspect project-local import edges from prebuilt `types.Package.Imports`; if a Go-only package reaches a local GSX source directory, reject before its stale type can enter a target fact. Respect actual nested `go.mod` ownership rather than lexical main-module prefixes. Walk an external package's complete prebuilt graph whether it is the authored import or is reached through a project Go-only bridge, and reject any external-to-main-module re-entry with the same semantic-boundary diagnostic as normal mode, positioned on the authored import available to Bundle. `SourceOnly` remains exactly one authored in-memory package plus prebuilt externals and performs no ownership/filesystem inspection. Do not add source metadata or a compatibility ABI to Bundle without a real consumer.

Root-bind the public module-backed `gen.NewCachedResolver`: capture the physical
module-root identity and declared module path at construction, and before every
disk `Generate` resolve the target directory's nearest `go.mod`. Reject a
different root with the same module path, a nested module, a replaced root, or a
changed module directive. Make module-root discovery fail closed on an
unreadable or malformed nearest `go.mod` and on a missing module directive rather
than walking through either boundary. Require the requested directory's resolved
physical path to remain inside the bound root so an in-root symlink cannot admit
outside GSX source.

- [ ] **Step 5: Preserve and strengthen stale/orphan behavior**

Extend `TestModuleIgnoresStaleOnDiskXGo` with a poisoned generated wrapper and assert it is absent from scope/output. Keep the existing exact-header ownership gate and `Result.Removed` reporting, including a directory with no remaining `.gsx`; do not replace orphan removal with a new diagnostic mechanism.

- [ ] **Step 6: Run phase-count and cold/warm benchmarks**

Rerun the Task 1 `same-package`, `imported`, and `embedded` rows unchanged. Run:

`go test ./internal/codegen -run '^$' -bench 'BenchmarkModuleGenerateComponent(Cold|Warm)' -benchmem -count=5`

Record results beside the Task 1 baseline in `.superpowers/sdd/progress.md` and compare `ns/op`, `B/op`, and `allocs/op` case by case. Any material warm-regeneration regression is a discuss-before-continuing checkpoint. Assert the load-count test remains exactly one external load and zero filter-table reloads through ten edits.

- [ ] **Step 7: Verify and commit the ABI-neutral importer foundation**

Run: `go test ./internal/codegen ./gen -run 'Test.*(Stale|Orphan|Poison|Invalidat|Refresh|Manifest|Cache|GoOnly|WarmRegen|Bundle|BuildTag)' -count=1`

Expected: PASS, including saved-source refresh, cache/source-view parity, Bundle
rejection, active-build-file selection, and current-skeleton checking through the
Go-only bridge.

```bash
git add internal/codegen gen
git commit -m "feat(codegen): type-check project-local import chains"
```

### Task 7: Pin the Section-Aware Structural Migration Ledger

**Files:**
- Create: `internal/codegen/verbatim_migration_inventory_test.go` (metadata-only test utility; deleted in Task 8)
- Create: `docs/superpowers/plans/2026-07-14-verbatim-component-signatures-migration-manifest.json`

**Interfaces:** This utility enumerates and verifies migration units; it never proposes or applies source edits. There is deliberately no decoded-string offset, `migrationEdit`, replacement engine, partial evaluator, or write-migration flag.

```go
type migrationAction string
type migrationKind string

const (
	migrationUnreviewed       migrationAction = "unreviewed"
	migrationManualEdit       migrationAction = "manual-edit"
	migrationDelete           migrationAction = "delete"
	migrationRegenerate       migrationAction = "regenerate"
	migrationReviewedNoChange migrationAction = "reviewed-no-change"
)

type migrationUnit struct {
	Kind         string // raw-file, txtar-comment, txtar-section
	SectionIndex *int
	SectionName  string
	BeforeSHA256 string
	AfterSHA256  string
	Action       migrationAction
	Kinds        []migrationKind
	ReviewNote   string
}

type migrationEntry struct {
	Path         string
	BeforeSHA256 string
	AfterSHA256  string
	Units        []migrationUnit
}

type migrationManifest struct {
	Version int
	Phase   string // planned or applied
	Entries []migrationEntry
}
```

These fields are exported only because the manifest is serialized; use explicit JSON field tags. The fixed source universe is every regular file matching these scopes at the pre-cutover revision:

```text
internal/corpus/testdata/cases/**/*.txtar
internal/corpus/testdata/loadertest/**/*.txtar
internal/examplegen/testdata/**/*.txtar
examples/*.txtar
internal/codegen/**/*_test.go
internal/corpus/**/*_test.go
internal/examplegen/**/*_test.go
internal/lsp/**/*_test.go
gen/**/*_test.go
playground/playbundle/**/*_test.go
playground/server/**/*.go
examples/tailwind-merge/**/*.go
examples/tailwind-merge/**/*.gsx
gen/templates/init/simple/app.gsx
gen/templates/init/simple/main.go.tmpl
```

Also record generated outputs that Task 8 must regenerate: `internal/corpus/testdata/coverage.golden`, `docs/examples.json`, `playground/server/examples.json`, `docs/guide/syntax/_generated/**`, and `examples/tailwind-merge/views/card.x.go`.

For each txtar, parse with the repository's txtar parser, record the archive comment as `txtar-comment`, and record every file using both its zero-based section index and exact section name with an independent data hash. Mark `generated.x.go.golden`, `render.golden`, `diagnostics.golden`, and `ast.golden` only as `regenerate`; never hand-edit them. Manually review every `.gsx`, `invoke`, helper-Go, and documentation/comment section.

For a Go fixture host, the complete raw `.go` file is the unit. Never unquote literals or map decoded GSX offsets back into host source. Dynamic concatenations, `fmt.Sprintf` inputs, fuzz builders, and variable-parameterized fixtures are reviewed and later edited at their actual raw Go construction sites; the review note names those tests/helpers.

Audit labels are `declare-children`, `declare-attrs`, `direct-props-invoke`, `byo-whole-value`, `byo-field-address`, `component-struct-splat`, `attrs-only-param-rename`, `field-matcher-expectation`, `generated-output`, and `manual-semantic-choice`.
`attrs-only-param-rename` is ledger metadata for locating source to edit; it must
not create a production classifier or diagnostic. A surviving
`func(extra ...gsx.Attr)` follows only the universal ordinary-variadic omission
and markup-fill rejection rules.

- [ ] **Step 1: Add the red ledger test**

`TestVerbatimMigrationInventory` enumerates the fixed universe and fails because the manifest is absent.

Run: `go test ./internal/codegen -run TestVerbatimMigrationInventory -count=1`

Expected: FAIL because the manifest does not exist.

- [ ] **Step 2: Implement metadata-only enumeration**

Add `-update-verbatim-inventory`. It may create or refresh manifest metadata while preserving reviewed records whose path, section identity, and before-hash still match. It must never modify source files or propose replacements. Default verification requires a one-to-one universe/manifest match, exact archive section identity and hashes, no `unreviewed` action, a non-empty review note for each manual edit/deletion/semantic choice, and generated sections classified only as `regenerate`.

- [ ] **Step 3: Generate and manually review the complete ledger**

Run:

```bash
go test ./internal/codegen -run TestVerbatimMigrationInventory -update-verbatim-inventory -count=1
```

Review every unit and fill `action`, `kinds`, and `review_note`. Existing `usesChildren`/`usesAttrs`, BYO, attrs-only, and resolved-component facts may produce a read-only findings report, but never edits. Use searches only as human-review backstops:

```bash
rg -n 'Props\{' internal/corpus/testdata/cases examples internal/codegen internal/lsp gen playground
rg -n 'WithFieldMatcher|FieldMatcher|usesChildren|usesAttrs|attrsOnlySig|soleParamTypeName' internal gen
```

Have an independent reviewer compare the manifest to all enumerated units and explicitly resolve BYO field-address and struct-splat choices. Proceed only with `phase: "planned"` and no unreviewed unit.

- [ ] **Step 4: Verify and commit the planned ledger**

```bash
go test ./internal/codegen -run TestVerbatimMigrationInventory -count=1
git diff --check
```

Expected: PASS.

```bash
git add internal/codegen/verbatim_migration_inventory_test.go docs/superpowers/plans/2026-07-14-verbatim-component-signatures-migration-manifest.json
git commit -m "test(codegen): inventory verbatim signature cutover"
```

### Task 8: Atomic Verbatim ABI Cutover, Complete Semantics, and Executable-Consumer Migration

**Files:**
- Modify: `internal/codegen/analyze.go`, `emit.go`, `module_importer.go`, `component_target.go`, `component_signature.go`, `component_call_plan.go`, `component_zero.go`
- Delete: `internal/codegen/byo.go`, `byo_*_test.go`, `attrsonly.go`, `attrsonly_test.go`, `freeuse.go`, obsolete free-use tests, `fieldmatch.go`, `fieldmatch_test.go`
- Keep/modify: `internal/codegen/reserved_scan.go`, `reserved_bindings.go` (retain `_gsx...` and `ctx`; move the required statement-binding parser before deleting `freeuse.go`)
- Modify: every `internal/codegen/*_test.go` fixture listed by the Task 7 ledger
- Modify: `internal/corpus/testdata/cases/**/*.txtar`, `internal/corpus/testdata/coverage.golden`
- Create: `internal/corpus/testdata/cases/verbatim/{core_positional,direct_go_signature,children_attrs,attrs_shapes,callable_origins,zero_inference_xpkg,class_style_targets}.txtar`
- Modify: `internal/codegen/results.go`, `module.go`, `gen/lsp.go`, and the minimum `internal/lsp/**` consumers needed to consume signature facts directly; semantic rename remains Task 9
- Delete: `internal/lsp/definition_attrsonly.go`, `definition_attrsonly_test.go`; exact-name definition/hover stays live through signature facts
- Modify: `gen/options.go`, `attrtypes.go`, `main.go`, `cache.go`, `cachekey.go`, `configfile.go`, `info.go`, `manifest.go`, `watch*.go`, `lsp.go`, and associated tests to remove `WithFieldMatcher` outright
- Modify: `examples/**/*.txtar`, `examples/**/*.gsx`, and regenerated example `.x.go` outputs named by the ledger
- Modify: `gen/templates/init/simple/app.gsx`, `main.go.tmpl`, `gen/init_test.go`
- Modify: `playground/server/**`
- Regenerate: `docs/guide/examples.md`, `docs/examples.json`, `playground/server/examples.json`, `docs/guide/syntax/_generated/**`

**Interfaces:** Consumes the complete Task 1-6 facts/planners/importer and produces exactly one shipping ABI and call convention. Treat this as one coordinated rollback unit with one final task review and one commit; scoped subagents may own tests, source migration, or a named subsystem, but no subagent creates an intermediate ABI commit. Do not checkpoint a partial declaration-only or core-only cutover: the existing corpus already exercises children, attrs, and generics, and `TestCorpus` also runs shipped examples. The working tree may be temporarily red during this task; no commit may exist between ABIs. Task 6 already guarantees that normal resolution uses current in-memory declarations and Bundle rejects unsupported transitive project-local chains, so no stale-ABI window opens at this commit.

The playground migration is part of this same atomic unit. Pin the archive and
server to `gc/linux/amd64`, `CGO_ENABLED=0`, and the repo toolchain/language
contract. Build both browser and server codegen from the same embedded rootless
bundle resolver and pass complete in-memory GSX source sets; never construct the
server resolver from the first disposable workspace and reuse that module
universe for later workspaces. Update the prepared server workspace `go.mod`, pin the Docker build
platform, and validate the archive's exact compiler/GOOS/GOARCH/cgo,
toolchain/language, and tag manifest at server startup. Clear inherited
`GOEXPERIMENT`, `GOAMD64`, and `GOTOOLCHAIN` from every child compile environment
before applying the pinned target; ambient deployment settings may not mutate the
declared engine. Because the website WASM and Cloud Run server deploy
independently, `/run` carries exact `engineID` and `targetManifestID` fields, the
server result/cache key includes both IDs, and any mismatch returns HTTP 409 with
a structured reload handshake echoing the server IDs. A regenerated archive must
never ship against the old server language/target contract.

- [ ] **Step 1: Establish the last green pre-cutover baseline**

Run:

```bash
go test ./internal/codegen ./internal/corpus ./internal/lsp ./gen -count=1
go test ./... -count=1
make ci-playground
make ci-tailwind-example
```

Expected: PASS. Preserve the output in the task report.

- [ ] **Step 2: Add the two core failing corpus cases**

`core_positional.txtar` combines reversed authored/signature order, an untyped `min(1, 2)` into a defined numeric parameter, and a `(string,error)` prop; its render output records authored evaluation order. `direct_go_signature.txtar` contains:

```go
type History struct{ Label string }
component Page(h History) { <p>{h.Label}</p> }
var _ func(History) gsx.Node = Page
```

Its `-- invoke --` is `Page(History{Label: "ok"})`, and its generated golden contains neither `PageProps` nor an analysis helper declaration.

- [ ] **Step 3: Add the five complete semantic matrices before implementation**

Pin scalar children with an empty body producing `nil`, scalar/variadic non-empty children, static-node counts, body rejection, node-valued markup props, all attrs target/contributor shapes and exact element identity (including instantiated defined-slice targets), repeated explicit attrs in authored order, ordinary named bags, ordinary exact capitalized `Attrs`/`Children` parameters plus undeclared capitalized names following normal unmatched behavior, exact class/style forms, named/blank/unnamed ordinary variadic omission and fill rejection, generic inference failures, opaque cross-package zeros, free funcs/func vars/named func types/bound methods, and every rejected dynamic origin.

In `attrs_shapes.txtar`, define an ordinary `inputAttrs gsx.Attrs` parameter and
spread that same value onto two different inner elements. Invoke it once with
`inputAttrs={{...}}` and once with `inputAttrs={computedBag}`; pin both generated
calls and rendered attributes on both descendants. In the cross-package matrix,
emit and render a call whose multiple same-typed ordinary parameters appear out
of call order and whose imported callee also declares `attrs`, proving exact
names, positional ordering, and role classification survive final resolution.
The callable-origin matrix also includes a package variable declared through a
named func-type alias, not merely a direct named func type.

Include declared `children`, `attrs`, and ordinary params used beneath materialized interpolation elements, markup-valued attributes, and nested `f`/`js`/`css` literals in both interpolations and Go blocks. Shadow each name around a materialized element with a function parameter and prove ordinary Go lexical identity wins. Include a declared `attrs` parameter used as the bare key in `map[any]int{attrs: 1}` inside a non-invoked closure: it must resolve directly to that authored parameter, proving the cutover no longer depends on the obsolete syntactic free-use guess that cannot distinguish a named struct field from a named map key. Include a concrete result assignable to `gsx.Node`.

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/verbatim/(core_positional|direct_go_signature|children_attrs|attrs_shapes|callable_origins|zero_inference_xpkg|class_style_targets)' -count=1
```

Expected: FAIL because the old ABI and call planner are still active. Preserve this RED output in the task report.

- [ ] **Step 4: Replace declaration and call paths together and delete the old model**

Emit `func [recv] Name[typeparams](<authored params>) gsx.Node` from Task 1's parsed declaration/source spans, with block `/*line file:line:col*/` anchors preserving each parameter name/type in the skeleton and final declaration. The render closure captures authored parameters directly; bind no `_gsxp` fields. `children`/`attrs` availability comes from the signature model, not body scanning. Build the positional validation skeleton from the completed plan and emit the same call shape after validation.

In this same change, delete the old `param`/`parseParams`, Props struct/stub emission, `_gsxp` bindings, `componentPropFieldsFor`, BYO, attrs-only, fuzzy-field, forced-last attrs, and struct-splat paths. Move the Go-AST statement-binding parser needed by `ctx` into `reserved_bindings.go` (or `reserved_ctx_bindings.go`) before deleting `freeuse.go`; keep `reserved_scan.go`. Remove `WithFieldMatcher` from option/config/cache/info/manifest/watch/LSP wiring with no ignored key or deprecated alias. Adapt existing codegen/LSP definition and hover consumers and their tests directly to exact signature facts—never through a Props-shaped projection—so Task 8 is green; Task 9 adds semantic rename and broader navigation coverage. No legacy flag, adapter, dormant path, or compatibility API remains.

- [ ] **Step 5: Complete every matrix through the universal facts and planners**

Implement scalar/variadic children, every accepted attrs signature and authored-order contributor form, exact class/style targeting, callable provenance, authored-operands-only generic inference, semantic inline zeros, contextual untyped values, tuple unwrap, and selective source-order materialization. Do not revive `attrsOnlySig`, BYO classification, struct splat, or name guessing. Error on unnamed fixed params, markup fill of non-reserved ordinary variadics (including `extra ...gsx.Attr`) through the universal variadic rule with no migration-specific branch, true method expressions, interface dispatch, unresolved signatures, and unspellable omitted zeros.

Reuse the existing sanitizing `gsx.Attrs`, `ConcatAttrs`, renderer, URL, class, and style lowering. Store explicit `attrs={}` and `attrs={{}}` as ordinary source-indexed segments/pairs; delete forced-last composition. Convert the canonical bag to defined slices via `[]gsx.Attr(bag)` and expand only `attrs ...gsx.Attr`.

Construct one `_gsxty` `generatedImportAllocator` per generated file. Route both inferred type-argument spelling and zero-value type spelling through it, replace the current `typeArgAliases` output map, and pass `allocator.specs()` to `writeImports`. There must be one collision-safe generated-type import mechanism, not parallel type-argument and zero-value paths.

- [ ] **Step 6: Regenerate and inspect the seven new semantic cases**

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/verbatim/(core_positional|direct_go_signature|children_attrs|attrs_shapes|callable_origins|zero_inference_xpkg|class_style_targets)' -update
go test ./internal/corpus -run 'TestCorpus/verbatim/(core_positional|direct_go_signature|children_attrs|attrs_shapes|callable_origins|zero_inference_xpkg|class_style_targets)' -count=1
```

Expected: PASS. Inspect generated goldens for authored expression order, contextual untyped values, tuple unwrap, exact direct function types, source-ordered attrs, and absence of Props/helper declarations. If any row requires emitted helper machinery, guessed zeros, or a second call convention, stop and discuss.

- [ ] **Step 7: Apply the reviewed ledger manually inside the same rollback unit**

Work row by row through the Task 7 ledger at the real source container. Edit named txtar sections, normal `.gsx` files, Go call sites, and exact construction sites of literal or dynamic test fixtures—including Task 6's Go-only bridge fixture. Declare reserved roles and convert manual Props calls to positional/direct options values. Replace **every** component struct splat; its reviewed choice selects only the replacement form (a whole-value named prop, individual ordinary params, or another explicit non-splat source shape), never retention. Replace `250-byo-props`, `251-props-heuristic`, and `252-splat` with verbatim/direct-options concepts here. Do not edit generated `.x.go`, golden sections, coverage manifests, or JSON by hand.

- [ ] **Step 8: Regenerate every owned output**

Run:

```bash
go test ./internal/corpus -run TestCorpus -update
make examples
go test ./internal/corpus -run TestCorpus -count=1
```

Expected: PASS across the complete canonical corpus and shipped txtar examples. Re-run Task 6's Go-only bridge test and assert it now observes `func(...authored params...) gsx.Node`, never the poisoned Props signature; the Bundle case remains fail-closed.

- [ ] **Step 9: Record the applied ledger and remove the temporary verifier**

Add `-record-verbatim-after` to the metadata-only utility. It may update only `after_sha256` fields and `phase`; it must not write source or change review decisions.

```bash
go test ./internal/codegen -run TestVerbatimMigrationInventory -record-verbatim-after -count=1
go test ./internal/codegen -run TestVerbatimMigrationInventory -count=1
```

Expected: PASS with `phase: "applied"`; every surviving file/archive/section matches its after hash, every planned deletion is absent, and every generated output was recorded after regeneration. Delete `internal/codegen/verbatim_migration_inventory_test.go`, retain the JSON audit record, then run `go test ./internal/codegen -count=1` again.

- [ ] **Step 10: Prove every obsolete production path is absent**

Run:

```bash
rg -n 'componentPropFieldsFor|childPropsLiteral|attrsOnlySig|soleParamTypeName|usesChildren|usesAttrs|WithFieldMatcher|FieldMatcher|_gsxp\b' internal/codegen internal/lsp gen cmd playground --glob '*.go' --glob '!*_test.go'
```

Expected: no matches. Keep the direct-Go corpus assertion as the proof that generated output has the exact function type and no synthesized helper/type; do not scan broadly for `Props`, because author-owned options types remain valid.

- [ ] **Step 11: Prove the repository is green before the ABI-changing commit**

Add `attrs-stream` and `variadic-children` sub-benchmarks to the exact Task 1 cold/warm benchmark functions, then run:

```bash
go test ./internal/codegen -run '^$' -bench 'BenchmarkModuleGenerateComponent(Cold|Warm)' -benchmem -count=5
```

Record the new paths and the unchanged `same-package`, `imported`, and `embedded` rows beside the Task 1 and Task 6 measurements in `.superpowers/sdd/progress.md`. Compare `ns/op`, `B/op`, and `allocs/op` case by case; any material warm-regeneration regression is a discuss-before-commit checkpoint.

Run:

```bash
go test ./internal/codegen ./internal/corpus ./internal/lsp ./gen -count=1
go test ./... -count=1
make ci-examples
make ci-playground
make ci-tailwind-example
make ci-tailwind-example-drift
make check
git diff --check
```

Run `gopls check -severity=hint` on every changed non-generated Go source file. Expected: every command is clean. All executable repository consumers use the new ABI, and the old ABI no longer exists. Documentation prose may still describe the old model until Task 10, but no compiled source, generated artifact, example, scaffold, or playground surface does.

- [ ] **Step 12: Commit the single atomic rollback unit**

```bash
git add -A internal gen cmd playground examples docs/examples.json docs/guide/examples.md docs/guide/syntax/_generated docs/superpowers/plans/2026-07-14-verbatim-component-signatures-migration-manifest.json docs/ROADMAP.md
git commit -m "feat(codegen): cut over to verbatim component signatures"
```

### Task 9: LSP Exact Navigation, Hover, and Semantic Parameter Rename

**Files:**
- Modify: `internal/codegen/results.go`, `internal/codegen/module.go`
- Modify: `gen/lsp.go`
- Modify: `internal/lsp/analysis.go`, `definition_attr.go`, `definition.go`, `hover.go`, `protocol.go`, `server.go`
- Create: `internal/lsp/rename.go`, `internal/lsp/rename_test.go`
- Modify: `internal/lsp/definition_attr_test.go`, `hover_test.go`, `variant_nav_test.go`

**Interfaces:** Retain a stable parameter key `(package path, component key, ordinal)` plus `types.Var.Origin()` and exact call-site refs. Add `textDocument/prepareRename` and `textDocument/rename`.

- [ ] **Step 1: Extend exact-name definition/hover coverage**

Task 8 already migrated existing definition/hover behavior to exact signature facts. Add cross-package, generic-origin, and build-variant coverage; assert `title` resolves only `title`, never `Title`, reserved attrs contributors resolve to the declared `attrs` parameter, and ordinary `someAttrs={{}}` resolves normally.

- [ ] **Step 2: Add rename protocol tests**

Pin rename from a GSX declaration and invocation, cross-package calls, instantiated generic calls normalized by `Var.Origin()`, and semantically equivalent GSX build variants updated by the same ordinal. Pin rejection for renaming `children`/`attrs`, renaming any ordinary param to `_`, `children`, `attrs`, `ctx`, or `_gsxName`, invalid/colliding identifiers, and variant sets whose Task 3 semantic contract is not equivalent. Pin that `prepareRename` is not offered for a plain-Go callable parameter even though definition/hover still resolve it.

- [ ] **Step 3: Implement semantic rename**

Expose GSX parameter declaration/ref facts from codegen analysis. Advertise `RenameProvider`, dispatch both methods, and return one atomic `WorkspaceEdit`. Resolve a generic instantiated param through `Var.Origin`; resolve GSX variants by the validated semantic contract plus ordinal, never by text-only search. Do not partially rename plain-Go callable parameters: inactive Go variants are outside GSX's all-source variant model, so `prepareRename` rejects that target.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/lsp ./gen -run 'Test.*(Attr|Hover|Rename|Variant|Definition)' -count=1`

Expected: PASS.

Run: `rg -n 'attrsOnlySig|definition_attrsonly|FieldMatcher' internal/lsp gen/lsp.go --glob '*.go'`

Expected: no matches; this guards the obsolete-path removal completed atomically in Task 8.

```bash
git add internal/codegen/results.go internal/codegen/module.go gen/lsp.go internal/lsp
git commit -m "feat(lsp): navigate and rename exact component parameters"
```

### Task 10: Formatter, Documentation, and Shipped-Surface Verification

**Files:**
- Modify: `internal/gsxfmt/testdata/cases/*.txtar`
- Modify: `examples/*.txtar`, `examples/tailwind-merge/**`
- Modify: `internal/examplegen/**`
- Modify: `gen/templates/init/simple/app.gsx`, `main.go.tmpl`, `gen/init_test.go`
- Modify: `playground/server/run.go`, `cache_test.go`, `render_test.go`
- Modify: `README.md`, `skills/gsx/SKILL.md`
- Modify: `internal/corpus/README.md`, `docs/ROADMAP.md`
- Modify: `docs/guide/syntax/{props,composition,attributes,interop,basic-syntax}.md`, `docs/guide/{extensions,syntax,learn,status,vision}.md`, `docs/guide/patterns/render-once.md`, generated syntax pages
- Regenerate: `docs/guide/examples.md`, `docs/examples.json`, `playground/server/examples.json`, `docs/guide/syntax/_generated/**`
- Modify: `ast/ast.go` (`OrderedAttrsAttr` comment)

**Interfaces:** All shipped source and prose uses declared roles and direct Go calls; no generated Props examples remain.

- [ ] **Step 1: Add fmt cases for declared roles and grouped/variadic params**

Pin `component Card(title string, attrs gsx.Attrs, children gsx.Node)` and `component Tabs(children ...gsx.Node)` formatting.

- [ ] **Step 2: Verify the atomic source migration and add docs-led examples only if needed**

Task 8 already migrated every compiled example, scaffold, playground consumer, and generated artifact in the atomic rollback unit, including replacement of `250-byo-props`, `251-props-heuristic`, and `252-splat`. Re-audit those surfaces against the Task 7 ledger before writing prose. Add or adjust an `examples/*.txtar` only when a guide concept lacks a complete runnable example; never use this task to repair a source consumer left broken by Task 8.

- [ ] **Step 3: Rewrite guide pages rule-first with runnable examples**

Document exact signatures, direct Go interop, options structs, strict fallthrough, both explicit attrs contributor forms, children forms, exact matching, zero-fill/required opacity, and callable eligibility. Remove `WithFieldMatcher`, BYO heuristic, generated Props, and struct-splat guidance from every listed guide, README, and the GSX skill; do not leave deprecated syntax. Wrap literal `{{ }}` prose in `::: v-pre`.

Run a current-surface audit that deliberately excludes historical design records:

```bash
rg -n 'WithFieldMatcher|BYO Props|props heuristic|generated Props|struct splat|[A-Za-z]+Props\{' README.md internal/corpus/README.md skills/gsx docs/guide docs/ROADMAP.md
```

Review every match. Expected: no current instruction teaches a generated wrapper, fuzzy field matching, or struct splat; author-owned options types and historical roadmap completion notes must be explicitly distinguishable from the removed generator behavior.

- [ ] **Step 4: Regenerate every owned artifact**

Run: `go test ./internal/gsxfmt -run TestFmtCorpus -update`

Run: `make examples`

Then run: `go test ./internal/gsxfmt -run TestFmtCorpus -count=1 && go test ./internal/corpus -run TestCorpus -count=1 && make ci-examples`

Expected: PASS with no drift.

- [ ] **Step 5: Verify docs/scaffold and commit**

Run: `go test ./gen -run TestInit -count=1`

Run: `make ci-playground && make ci-tailwind-example && make ci-tailwind-example-drift`

Run:

```bash
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io
npm ci
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/verbatim-component-signatures \
VITE_GSX_PLAYGROUND_API=https://example.invalid npm run build
```

Expected: PASS; no Vue interpolation failure from unguarded `{{ }}`.

```bash
git add README.md skills/gsx/SKILL.md ast internal/gsxfmt internal/examplegen internal/corpus/README.md examples gen/templates gen/init_test.go playground/server docs/guide docs/examples.json docs/ROADMAP.md
git commit -m "docs: migrate shipped surfaces to verbatim component signatures"
```

### Task 11: Mandatory Ecosystem and Real-World Migration Release Gate

**Files:**
- Sibling repos: `/Users/jackieli/personal/gsxhq/tree-sitter-gsx`, `/Users/jackieli/personal/gsxhq/vscode-gsx`, `/Users/jackieli/personal/gsxhq/gsxhq.github.io`
- Consumer repos: `/Users/jackieli/personal/structpages`, `/Users/jackieli/work/one-learning-gsx`

**Interfaces:** Starts only after Tasks 1-10 pass `make check`. This is not an
optional follow-up: the core branch remains non-mergeable and non-releasable
until the structpages and one-learning branches below pass against the exact core
commit. Each repository gets its own plan/commit/verification and rollback unit;
the frozen commit set becomes the input to Task 12 and the release transaction.

- [ ] **Step 1: Audit grammar/highlighting impact before editing siblings**

Create feature worktrees at the paths below before editing. Run the checked-in sync command inside each feature worktree against the core feature worktree before generation or tests. Exercise declared `children`/`attrs`, grouped/variadic parameters, and attrs literals. If grammar is unchanged, commit only fixture/highlighting/docs updates; do not create syntax churn.

```bash
cd /Users/jackieli/personal/gsxhq/tree-sitter-gsx/.worktrees/verbatim-component-signatures
npm ci
GSX_REPO=/Users/jackieli/personal/gsxhq/gsx/.worktrees/verbatim-component-signatures npm run sync:authoritative
npm run generate && npm test && npm run test:authoritative

cd /Users/jackieli/personal/gsxhq/vscode-gsx/.worktrees/verbatim-component-signatures
npm ci
GSX_REPO=/Users/jackieli/personal/gsxhq/gsx/.worktrees/verbatim-component-signatures npm run sync:corpus
npm run gen:grammar && npm run typecheck && npm run lint && npm test

cd /Users/jackieli/personal/gsxhq/gsxhq.github.io/.worktrees/verbatim-component-signatures
npm ci
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/verbatim-component-signatures VITE_GSX_PLAYGROUND_API=https://example.invalid npm run build
```

- [ ] **Step 2: Prove structpages interop with the motivating type match**

Create `/Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures`. In its `examples/simple` module, make the product route's `Props() History` feed `component (p product) Page(h History)`, add an HTTP integration test for full and partial renders, and assert the generated `pages.x.go` exposes `func (p product) Page(h History) gsx.Node` with no generated wrapper type.

Create an ignored Go workspace containing the core feature worktree and this exact example module; do not commit an absolute `replace` directive. From `examples/simple`, run the feature generator and that module's tests through the workspace:

```bash
GOWORK=<integration-workspace> go run github.com/gsxhq/gsx/cmd/gsx generate -no-cache .
GOWORK=<integration-workspace> go test ./... -count=1
```

The root `structpages` suite alone is not evidence for this gate because nested example modules are excluded.

- [ ] **Step 3: Write and execute a separate one-learning migration plan**

Inventory the current 841 declarations/71 manual calls again, migrate shared leaf components first, declare reserved roles, convert manual Props calls to positional/direct options structs, and keep root children-taking layouts last. Verify DOM equivalence and browser behavior. Any unsupported real signature becomes a minimal GSX corpus reproducer and a discussion checkpoint; do not add an application workaround.

- [ ] **Step 4: Commit and report each gate independently**

Run the native test/build commands in each repository and record exact core,
sibling, structpages, and one-learning commit IDs plus complete gate output in the
execution ledger. Commit the prepared branches independently, but do not merge or
release any repository yet. Any remaining one-learning migration slice is a
failed gate, not accepted follow-up work. Task 12 reviews this frozen commit set;
if an adversarial fix changes core, rerun both mandatory consumer gates and freeze
the replacement set.

### Task 12: Independent Adversarial Review and Authoritative Verification

**Files:**
- Create during execution: `.superpowers/sdd/progress.md`
- Modify only if probes find defects: owning implementation/test files

**Interfaces:** A fresh reviewer builds throwaway probe programs rather than relying only on the diff.

- [ ] **Step 1: Dispatch an independent adversarial reviewer**

Require probes for: reflection/direct-Go ABI, opaque cross-package omission, the
final same-typed/out-of-order cross-package call with attrs, generic authored-only
inference, repeated attrs source order, ordinary literal/computed `inputAttrs`
routed to two descendants, contextual untyped values plus tuple unwrap, every
rejected provenance, embedded stable IDs, saved-source refresh, cache/source-view
parity, `gsx -> Go-only -> gsx` with stale disk output, Bundle external backedges
and nested-module ownership, build-tag rename, playground 409/ID/cache/child-env
target integrity, and no generated helper/type declarations.

- [ ] **Step 2: Run static and focused verification**

Run: `gopls check -severity=hint` for every changed Go file.

Run: `go test ./internal/codegen ./internal/corpus ./internal/gsxfmt ./internal/lsp ./gen -count=1`

Expected: PASS.

- [ ] **Step 3: Run project gates**

Run: `make lint`

Run: `make ci`

Run: `make ci-examples`

Run: `go test ./internal/codegen -run TestWarmRegenDoesNoGoListReloads -count=1`

Expected: all PASS; external/filter load counts remain `1,0`.

- [ ] **Step 4: Inspect the removal and generated API directly**

Run:

```bash
rg -n 'componentPropFieldsFor|childPropsLiteral|attrsOnlySig|soleParamTypeName|usesChildren|usesAttrs|WithFieldMatcher|FieldMatcher|_gsxp\b|_gsxcall' internal/codegen internal/lsp gen cmd playground --glob '*.go' --glob '!*_test.go'
git diff --check
git status --short
```

Expected: no obsolete live implementation symbols, no whitespace errors, and only intentional changes. Generated-output tests—not a name regex—prove that no synthesized Props/helper declaration exists, because author-owned types are allowed to end in `Props` and corpus diagnostics intentionally exercise `_gsxcall`.

- [ ] **Step 5: Commit any review fixes, rerun the failed gate, then rerun `make ci`**

Use one focused fix commit per independently reviewable defect. Do not fold an out-of-ordinary semantic discovery into cleanup; raise it to the user with the probe before changing the design.

If a review fix changes the frozen core commit, rerun Task 11's structpages and
one-learning gates against that exact replacement commit and update the execution
ledger. The branch remains non-mergeable until those reruns pass.

- [ ] **Step 6: Execute the cross-repository release transaction**

Confirm the ledger names the exact reviewed core and consumer commits and that no
working tree has drifted. Then:

1. merge the consumer-verified core commit and publish its version;
2. replace each ignored integration workspace reference with that released
   version and rerun the identical structpages and one-learning generation,
   compile, HTTP/DOM, and browser-behavior gates;
3. merge the already-prepared consumer branches only after those released-version
   reruns pass, then merge/publish the verified sibling updates in their recorded
   dependency order.

Stop the transaction immediately on any mismatch. Do not add a compatibility
mode, release an unverified replacement core commit, or leave either mandatory
consumer on the old ABI.

## Self-Review

- **Spec coverage:** Task 1 covers ordered declaration/build-tag identity; Task 2 roles and attrs family; Task 3 stable AST IDs/provenance; Tasks 4-5 cover exact routing, semantic spread validation, partial explicit inference, actual zeros, import spelling, and order; Task 6 lands the authoritative Go-only import graph, saved-source refresh, shared cache manifest, Bundle guard, stale/orphan handling, and performance before cutover; Task 7 pins the section-aware metadata ledger; Task 8 performs the complete core semantic cutover, removes obsolete systems, and migrates every executable in-repository consumer in one green rollback commit; Task 9 covers LSP exact navigation and rename; Task 10 covers formatting and prose documentation; Task 11 prepares and verifies the mandatory sibling, structpages, and one-learning commit set; Task 12 provides adversarial/authoritative verification and executes the gated release transaction.
- **Placeholder scan:** The plan contains no deferred implementation marker;
  ecosystem and real-world work is a mandatory pre-merge/pre-release gate with
  named repositories, evidence, frozen commits, and a controlled release
  transaction.
- **Type consistency:** `componentDeclaration`, `componentSignatureModel`, `componentTargetFact`, `callSiteID`, `componentCallPlan`, `componentParam`, and `Var.Origin()` are introduced once and consumed in dependency order.
- **Known execution checkpoint:** The core atomic cutover is intentionally one large commit because declaration/call ABIs, existing corpus semantics, and executable in-repository consumers cannot be green independently. Tasks 1-5 are pure/tested foundations, Task 6 makes normal/Bundle resolution authoritative without changing ABI, Task 7 proves every source container has a reviewed metadata action, and Task 8 is the single revertible core switch; no intermediate commit exists between core ABIs. That commit is not independently mergeable or releasable: Tasks 11-12 must verify and advance the cross-repository transaction.
