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
- Delete obsolete APIs and subsystems outright. Do not add deprecation aliases, compatibility adapters, temporary production helpers, or behavior flags.
- Do not implement component struct splat. `{bag...}` remains only an attrs-bag contributor.
- Matching is exact parameter identifier matching. Do not add a heuristic or retain `FieldMatcher` behavior.
- `children` accepts exactly `gsx.Node` or `...gsx.Node`; `attrs` accepts the exact attrs-bag family in the spec.
- `attrs={expr}`, `attrs={{...}}`, unmatched attrs, spreads, and conditional bags compose at authored positions; there is no forced-last branch.
- Pre-materialize embedded markup and stamp component classification once. Discovery, validation, LSP, and emission reuse the same AST and stable call-site IDs.
- Discovery first harvests the origin generic object/signature; authored-operands-only inference then uses a transient carrier; only after inference may omitted arguments be zero-filled.
- New analysis phases reuse the existing importer. They must not call `packages.Load`, write dependency `.x.go` files early, or depend on generation order.
- In normal mode, retain `NeedCompiledGoFiles|NeedSyntax` from the existing single cold load and source-check project-local Go-only packages through the module importer. In Bundle mode, reject a project-local `gsx -> Go-only -> gsx` chain because the bundle has no authoritative source inventory.
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
- Produces: one shared `parseParamFieldList` AST parse; final `componentParamDecl` entries preserving every logical parameter including unnamed, `_`, and variadic; unchanged existing `parseParams` behavior for the old Props path; `componentDeclarationFor(*gsxast.Component) (componentDeclaration, error)`; collision-safe `componentDeclaration.canonical()` for variant checks and later skeletons. Task 8 deletes the old `param`/`parseParams` path outright.

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

Make `componentSignature` in `variantcollide.go` return `componentDeclarationFor(c).canonical()`. On parse failure, length-prefix the trimmed raw receiver/type-param/param source so malformed alternatives still compare deterministically without collisions. Remove the body-derived `usesChildren`, `usesAttrs`, field capitalization, and prop sorting.

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
- Consumes: a concrete `*types.Signature` plus `runtimeContract{Node, Attr types.Type}`.
- Produces: `analyzeComponentSignature(*types.Signature, runtimeContract) (componentSignatureModel, error)`, ordered `componentParam{Var, Origin, Name, Type, Index, Role, AttrsMode}`, and exact tag-eligibility diagnostics.

- [ ] **Step 1: Write table tests for every role and rejection**

Construct signatures with `types.NewSignatureType`; cover ordinary fixed params, `children gsx.Node`, `children ...gsx.Node`, all four attrs forms, aliases, defined and instantiated-defined `[]gsx.Attr`, rejected `[]MyAttr`, rejected `children []gsx.Node`, blank/unnamed fixed params, named and unnamed ordinary variadics (Go-only and omittable), and result assignable-to-Node versus zero/multiple results.

- [ ] **Step 2: Run the unit test and verify the classifier is undefined**

Run: `go test ./internal/codegen -run TestAnalyzeComponentSignature -count=1`

Expected: FAIL to compile with `undefined: analyzeComponentSignature`.

- [ ] **Step 3: Implement the model without source-name heuristics**

Use these stable types:

```go
type paramRole uint8
const (roleProp paramRole = iota; roleChildren; roleAttrs; roleGoOnlyVariadic)
type attrsParamMode uint8
const (attrsDirect attrsParamMode = iota; attrsDefinedSlice; attrsVariadic)
type componentParam struct {
	Var, Origin *types.Var
	Name string
	Type types.Type
	Index int
	Role paramRole
	AttrsMode attrsParamMode
}
type componentSignatureModel struct {
	Go *types.Signature
	Params []componentParam
	Result types.Type
}
```

Normalize instantiated parameter identity through `types.Var.Origin()`. Use `types.Identical` for `gsx.Node`/`gsx.Attr`, `types.AssignableTo` only for the result contract, and the exact underlying-slice rule for attrs. Ordinary non-reserved variadics are `roleGoOnlyVariadic` and cannot bind markup.

- [ ] **Step 4: Run and commit**

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
- Modify: `internal/codegen/analyze.go` (`buildSkeleton`, embedded markup handling)
- Modify: `internal/codegen/module_importer.go` (`analyze`, retained facts)
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

func preprocessComponentCallSites(
	files map[string]*gsxast.File,
	declNames map[string]bool,
	fset *token.FileSet,
	classifier *attrclass.Classifier,
	bag *diag.Bag,
) (*callSiteRegistry, error)
```

Sort paths before walking. This function subsumes both `resolveComponentTags` and `splitInterpEmbedded`: materialize all embedded markup, stamp every resulting element once, then allocate IDs in authored AST order. Register supported sites as `callSitePlanned`; register direct element literals beneath `GoBlock.Embedded` as `callSitePreserveUnsupportedGoBlock`, emit the existing diagnostic once, and never probe or plan them. Store the registry on `analyzed`; both skeleton phases and emission consume it. Remove materialization from `buildSkeleton`; no later phase clones, reparses, or restamps embedded markup.

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

	object types.Object
	origin types.Object // (*types.Func).Origin or (*types.Var).Origin

	// Pre-explicit-arguments call shape. For a bound method this is
	// Selection.Type(): receiver removed and receiver arguments substituted.
	raw *types.Signature

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

- [ ] **Step 1: Add failing tests for identity and provenance**

Pin: two same-text tags get distinct IDs; a supported tag embedded in an interpolation or top-level `GoWithElements` keeps the same pointer/ID across discovery and a second walk; a direct tag inside `{{ }}` retains one ID only until the existing `unsupported-node` diagnostic and never gets a target fact; package funcs/func vars and concrete `MethodVal` are accepted; `MethodExpr`, `FieldVal`, locals, parameters, interface dispatch, and shadowed package selectors are rejected.

Run: `go test ./internal/codegen -run 'TestAssignCallSites|TestHarvestComponentTargets' -count=1`

Expected: FAIL because the registry and fact types do not exist.

- [ ] **Step 2: Pre-materialize embedded AST once**

Move embedded element splitting/materialization ahead of both skeleton phases. Resolve/stamp `Element.IsComponent` once, then build `callSiteRegistry`; later phases must consume those nodes and may not call the embedded splitter or clone markup. Mark direct `{{ }}` element literals `preserveUnsupportedGoBlock`, emit the existing single diagnostic, and exclude them from discovery, validation, and emission.

- [ ] **Step 3: Add a target-discovery skeleton mode and harvest exact origins**

Emit a registered target expression at each planned site inside its real lexical scope. For indexed targets, obtain the supplier identifier structurally from `Ident`, `SelectorExpr.Sel`, `IndexExpr.X`, or `IndexListExpr.X`; retain every authored index expression in order and its `types.Info.Types` result. Harvest `types.Info.Instances` only when target-only checking completes the whole instantiation; a partial prefix remains valid input for Task 5 even though no instance exists yet. Capture site-local generic/target/type-argument diagnostics on the fact rather than emitting them immediately, so Task 5 can report authored operand errors first; unrelated skeleton diagnostics remain fatal. Retain the generic origin signature even when the target alone reports `cannot use generic function ... without instantiation` or too few type arguments.

Classify provenance exactly: a package func is a `*types.Func` whose parent is `obj.Pkg().Scope()`; a package var is a callable `*types.Var` with the same parent; a bound method requires `Selection.Kind()==types.MethodVal` and a declared receiver that is not interface-based. Reject `MethodExpr`, `FieldVal`, locals, parameters, interface methods (including promotion from an embedded interface), and shadowed package qualifiers even when their current type is callable.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/codegen -run 'TestAssignCallSites|TestHarvestComponentTargets|TestTargetDiscovery' -count=1`

Expected: PASS, including embedded sites and repeated/shadowed tags.

```bash
git add internal/codegen/component_target.go internal/codegen/component_target_test.go internal/codegen/analyze.go internal/codegen/module_importer.go internal/codegen/results.go
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
	Kind componentInputKind
	SourceIndex, ParamIndex, ContributorIndex int
	Node gsxast.Node
}
type componentArgSlot struct { Param componentParam; ValueIndexes []int; Omitted bool }
type componentCallPlan struct {
	Site callSiteID
	Target componentSignatureModel
	Args []componentArgSlot       // signature order
	Values []componentInputValue  // authored order
}
func planComponentInputs(site callSiteID, el *gsxast.Element, target componentSignatureModel) (componentCallPlan, []diag.Diagnostic)
```

- The plan distinguishes ordinary prop bindings, one body binding, attrs bag pairs/segments/explicit contributors, omitted fixed params, and omitted Go-only variadics.

- [ ] **Step 1: Write planner tests**

Cover exact-case matching, duplicate ordinary props, `_foo`, legacy capitalized `Attrs`/`Children` migration diagnostics only when no exact capitalized parameter exists, positive exact ordinary `Attrs` and `Children` parameters coexisting with lowercase reserved roles, non-identifier fallthrough, strict missing attrs, body without children, explicit `children=` rejection, `attrs={expr}`, repeated `attrs={{...}}`, `{bag...}`, conditional bags, ordinary `someAttrs={{...}}`, ordinary `someAttrs={computedBag}`, node-valued markup props, class/style exact targets, and omitted named/unnamed ordinary variadics.

At this syntax-only phase every `{expr...}` is routed as an attrs-stream contributor. A struct expression and a `gsx.Attrs` expression share the same `SpreadAttr` syntax; Task 5 rejects non-bag semantic types after `go/types` facts exist. Do not classify a struct splat from expression text.

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/codegen -run TestPlanComponentInputs -count=1`

Expected: FAIL to compile with `undefined: planComponentInputs`.

- [ ] **Step 3: Implement routing before value lowering**

Match an identifier-shaped name only against an ordinary parameter's exact `Name`. Treat `attrs` as a repeatable contributor stream, not a prop slot. Keep every attrs contributor's authored index; never create `attrsLitIdx` or a forced-last marker. Conditional branch names never fill props. Route spreads without inspecting their expression text or type. Emit positioned planner errors with stable codes (`duplicate-prop`, `component-missing-attrs`, `component-missing-children`, `reserved-input-form`, `ordinary-variadic-prop`).

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
type suppliedOperand struct { ParamIndex int; Expr goast.Expr; TV types.TypeAndValue }
type inferenceContext struct { Pkg *types.Package; Fset *token.FileSet; Scope *types.Scope }

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
	TV types.TypeAndValue
	IsNil bool
	HasOrderedOperation bool
	Tuple *types.Tuple
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
- Modify: `internal/codegen/module_importer.go`, `module.go`, `resolver.go`, `module_stale_xgo_test.go`, `depfacts_test.go`, `invalidation_test.go`, `snapshot_cache_test.go`, `module_perf_test.go`, `bundle_module_test.go`
- Modify: `gen/orphan_e2e_test.go`, `gen/poison_e2e_test.go`

**Interfaces:** This is ABI-neutral infrastructure and lands green before cutover. It always rechecks project-local Go-only packages against the **current in-memory GSX declaration skeleton**, whether that skeleton is the pre-cutover Props ABI or Task 8's verbatim ABI. Declaration facts drive invalidation. The one normal-mode cold load retains exact project source metadata:

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

- [ ] **Step 1: Add failing `gsx -> Go-only -> gsx` and signature-invalidation tests**

Create a temp graph where `page.gsx` imports project `bridge` Go source, `bridge` imports `ui`, and `ui/card.gsx` has a poisoned stale `card.x.go`. Give `bridge` mutually exclusive build-tag variants and put the stale-ABI reference only in the inactive variant. Before Task 8, assert bridge checking uses the current in-memory Props declaration rather than poisoned disk; the Task 7 ledger marks this fixture for conversion to the verbatim signature during cutover. Rename only a `ui.Card` parameter and assert importer/page facts invalidate through `bridge`, with `externalLoads()==1`.

- [ ] **Step 2: Retain the cold load's authoritative compiled-file inventory**

Add `packages.NeedCompiledGoFiles | packages.NeedSyntax` to the existing `packages.Config.Mode`. With the shared `m.fset`, retain aligned `CompiledGoFiles`/`Syntax` only for module-local Go-only packages. Do not glob, call `build.ImportDir`, or parse source again: that would diverge on build tags, cgo-generated files, tests, and the load's environment.

`projectSources.syntax` and every `projectTypes` object carry positions tied to `m.fset`. Extend `rebuildFset`'s atomic reset to clear **both** maps alongside the existing position-bearing caches. The next single external load repopulates `projectSources` with ASTs parsed into the new FileSet; never reuse retained ASTs from the discarded FileSet.

Extend the existing FileSet-rebuild regression to use the Go-only bridge: assert both maps are empty immediately after rebuild, the next generation performs exactly one new external load, repopulates/rechecks the bridge, and every retained AST/object/diagnostic position resolves through the new `m.fset` rather than the discarded one.

- [ ] **Step 3: Re-type-check retained syntax through `moduleImporter`**

When a project-local Go-only import is reached, type-check its retained ASTs with the same recursive `moduleImporter`; route GSX children to the current declaration skeletons, cache the result in `projectTypes`, and record forward/reverse edges for both Go-only and GSX directories. A GSX declaration edit invalidates the reverse closure and rechecks retained syntax without another load. Do not add another `packages.Load` or a source parser.

- [ ] **Step 4: Add and implement Bundle fail-closed behavior before cutover**

Build a Bundle test for the same `page -> bridge -> ui.gsx` graph with a stale bundled `ui` type. Assert generation returns `bundle-project-gsx-transitive` and directs the caller to the normal resolver. In Bundle mode, inspect project-local import edges from prebuilt `types.Package.Imports`; if a Go-only package reaches a local GSX source directory, reject before its stale type can enter a target fact. Do not add source metadata or a compatibility ABI to Bundle without a real consumer.

- [ ] **Step 5: Preserve and strengthen stale/orphan behavior**

Extend `TestModuleIgnoresStaleOnDiskXGo` with a poisoned generated wrapper and assert it is absent from scope/output. Keep the existing exact-header ownership gate and `Result.Removed` reporting, including a directory with no remaining `.gsx`; do not replace orphan removal with a new diagnostic mechanism.

- [ ] **Step 6: Run phase-count and cold/warm benchmarks**

Rerun the Task 1 `same-package`, `imported`, and `embedded` rows unchanged. Run:

`go test ./internal/codegen -run '^$' -bench 'BenchmarkModuleGenerateComponent(Cold|Warm)' -benchmem -count=5`

Record results beside the Task 1 baseline in `.superpowers/sdd/progress.md` and compare `ns/op`, `B/op`, and `allocs/op` case by case. Any material warm-regeneration regression is a discuss-before-continuing checkpoint. Assert the load-count test remains exactly one external load and zero filter-table reloads through ten edits.

- [ ] **Step 7: Verify and commit the ABI-neutral importer foundation**

Run: `go test ./internal/codegen ./gen -run 'Test.*(Stale|Orphan|Poison|Invalidat|GoOnly|WarmRegen|Bundle|BuildTag)' -count=1`

Expected: PASS, including Bundle rejection, active-build-file selection, and current-skeleton checking through the Go-only bridge.

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

Audit labels are `declare-children`, `declare-attrs`, `direct-props-invoke`, `byo-whole-value`, `byo-field-address`, `component-struct-splat`, `legacy-role-spelling`, `attrs-only-param-rename`, `field-matcher-expectation`, `generated-output`, and `manual-semantic-choice`.

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

Pin scalar children with an empty body producing `nil`, scalar/variadic non-empty children, static-node counts, body rejection, node-valued markup props, all attrs target/contributor shapes and exact element identity (including instantiated defined-slice targets), repeated explicit attrs in authored order, ordinary named bags, legacy capitalized `Attrs`/`Children` migration diagnostics, exact class/style forms, named/blank/unnamed ordinary variadic omission and fill rejection, generic inference failures, opaque cross-package zeros, free funcs/func vars/named func types/bound methods, and every rejected dynamic origin. Include a concrete result assignable to `gsx.Node`.

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/verbatim/(core_positional|direct_go_signature|children_attrs|attrs_shapes|callable_origins|zero_inference_xpkg|class_style_targets)' -count=1
```

Expected: FAIL because the old ABI and call planner are still active. Preserve this RED output in the task report.

- [ ] **Step 4: Replace declaration and call paths together and delete the old model**

Emit `func [recv] Name[typeparams](<authored params>) gsx.Node` from Task 1's parsed declaration/source spans, with block `/*line file:line:col*/` anchors preserving each parameter name/type in the skeleton and final declaration. The render closure captures authored parameters directly; bind no `_gsxp` fields. `children`/`attrs` availability comes from the signature model, not body scanning. Build the positional validation skeleton from the completed plan and emit the same call shape after validation.

In this same change, delete the old `param`/`parseParams`, Props struct/stub emission, `_gsxp` bindings, `componentPropFieldsFor`, BYO, attrs-only, fuzzy-field, forced-last attrs, and struct-splat paths. Move the Go-AST statement-binding parser needed by `ctx` into `reserved_bindings.go` (or `reserved_ctx_bindings.go`) before deleting `freeuse.go`; keep `reserved_scan.go`. Remove `WithFieldMatcher` from option/config/cache/info/manifest/watch/LSP wiring with no ignored key or deprecated alias. Adapt existing codegen/LSP definition and hover consumers and their tests directly to exact signature facts—never through a Props-shaped projection—so Task 8 is green; Task 9 adds semantic rename and broader navigation coverage. No legacy flag, adapter, dormant path, or compatibility API remains.

- [ ] **Step 5: Complete every matrix through the universal facts and planners**

Implement scalar/variadic children, every accepted attrs signature and authored-order contributor form, exact class/style targeting, callable provenance, authored-operands-only generic inference, semantic inline zeros, contextual untyped values, tuple unwrap, and selective source-order materialization. Do not revive `attrsOnlySig`, BYO classification, struct splat, or name guessing. Error on unnamed fixed params, non-reserved ordinary variadic attrs, true method expressions, interface dispatch, unresolved signatures, and unspellable omitted zeros.

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

Work row by row through the Task 7 ledger at the real source container. Edit named txtar sections, normal `.gsx` files, Go call sites, and exact construction sites of literal or dynamic test fixtures—including Task 6's Go-only bridge fixture. Declare reserved roles, convert manual Props calls to positional/direct options values, and replace each struct splat according to its reviewed semantic choice. Replace `250-byo-props`, `251-props-heuristic`, and `252-splat` with verbatim/direct-options concepts here. Do not edit generated `.x.go`, golden sections, coverage manifests, or JSON by hand.

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

Pin rename from a GSX declaration and invocation, cross-package calls, instantiated generic calls normalized by `Var.Origin()`, and equivalent GSX build-tag variants updated by the same ordinal. Pin rejection for renaming `children`/`attrs`, renaming any ordinary param to `_`, `children`, `attrs`, `ctx`, or `_gsxName`, invalid/colliding identifiers, and variant sets whose Task 1 canonical identity is not equivalent. Pin that `prepareRename` is not offered for a plain-Go callable parameter even though definition/hover still resolve it.

- [ ] **Step 3: Implement semantic rename**

Expose GSX parameter declaration/ref facts from codegen analysis. Advertise `RenameProvider`, dispatch both methods, and return one atomic `WorkspaceEdit`. Resolve a generic instantiated param through `Var.Origin`; resolve GSX variants by canonical contract plus ordinal, never by text-only search. Do not partially rename plain-Go callable parameters: inactive Go variants are outside GSX's all-source variant model, so `prepareRename` rejects that target.

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

### Task 11: Separately Gated Ecosystem and Real-World Migration Follow-Up

**Files:**
- Sibling repos: `/Users/jackieli/personal/gsxhq/tree-sitter-gsx`, `/Users/jackieli/personal/gsxhq/vscode-gsx`, `/Users/jackieli/personal/gsxhq/gsxhq.github.io`
- Consumer repos: `/Users/jackieli/personal/structpages`, `/Users/jackieli/work/one-learning-gsx`

**Interfaces:** Starts only after Tasks 1-10 pass `make check`; each repository gets its own plan/commit/verification and can be rolled back independently.

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

Run the native test/build commands in each repository and record commit IDs and remaining migration slices in the execution ledger.

### Task 12: Independent Adversarial Review and Authoritative Verification

**Files:**
- Create during execution: `.superpowers/sdd/progress.md`
- Modify only if probes find defects: owning implementation/test files

**Interfaces:** A fresh reviewer builds throwaway probe programs rather than relying only on the diff.

- [ ] **Step 1: Dispatch an independent adversarial reviewer**

Require probes for: reflection/direct-Go ABI, opaque cross-package omission, generic authored-only inference, repeated attrs source order, contextual untyped values plus tuple unwrap, every rejected provenance, embedded stable IDs, `gsx -> Go-only -> gsx` with stale disk output, build-tag rename, and no generated helper/type declarations.

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

## Self-Review

- **Spec coverage:** Task 1 covers ordered declaration/build-tag identity; Task 2 roles and attrs family; Task 3 stable AST IDs/provenance; Tasks 4-5 cover exact routing, semantic spread validation, partial explicit inference, actual zeros, import spelling, and order; Task 6 lands the authoritative Go-only import graph, Bundle guard, cache, stale/orphan handling, and performance before cutover; Task 7 pins the section-aware metadata ledger; Task 8 performs the complete semantic cutover, removes obsolete systems, and migrates every executable consumer in one green rollback commit; Task 9 covers LSP exact navigation and rename; Task 10 covers formatting and prose documentation; Task 11 gates siblings, structpages, and one-learning; Task 12 provides adversarial and authoritative verification.
- **Placeholder scan:** The plan contains no deferred implementation marker; later ecosystem work is an explicit post-core gate with named repositories, evidence, and commands determined by each repository's checked-in workflow.
- **Type consistency:** `componentDeclaration`, `componentSignatureModel`, `componentTargetFact`, `callSiteID`, `componentCallPlan`, `componentParam`, and `Var.Origin()` are introduced once and consumed in dependency order.
- **Known execution checkpoint:** The atomic cutover is intentionally one large commit because declaration/call ABIs, existing corpus semantics, and executable consumers cannot be green independently. Tasks 1-5 are pure/tested foundations, Task 6 makes normal/Bundle resolution authoritative without changing ABI, Task 7 proves every source container has a reviewed metadata action, and Task 8 is the single revertible switch; no intermediate commit exists between ABIs.
