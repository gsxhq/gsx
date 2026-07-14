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
- Produces: one shared `parseParamFieldList` AST parse; final `componentParamDecl` entries preserving every logical parameter including unnamed, `_`, and variadic; unchanged existing `parseParams` behavior for the old Props path; `componentDeclarationFor(*gsxast.Component) (componentDeclaration, error)`; collision-safe `componentDeclaration.canonical()` for variant checks and later skeletons. Task 7 deletes the old `param`/`parseParams` path outright.

- [ ] **Step 1: Add declaration-parser tests with valid named and unnamed families**

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

- [ ] **Step 2: Run the focused declaration test and verify the final parser is absent**

Run: `go test ./internal/codegen -run TestParseComponentParamDecls -count=1`

Expected: FAIL to compile because `componentParamDecl` and `parseComponentParamDecls` do not exist.

- [ ] **Step 3: Implement the final declaration parser without changing the old Props path**

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

`parseComponentParamDecls` walks the shared field list, emits one entry for an unnamed field and one per name for a named/grouped field, computes variadic with `*goast.Ellipsis`, and records `nameOff=-1` for unnamed entries. It never derives shape from strings. This is the model Tasks 2-13 retain; the old `param` type is not extended and no compatibility/projection helper is added.

- [ ] **Step 4: Add declaration normalization tests before the implementation**

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

- [ ] **Step 5: Implement the syntax-only declaration shape**

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

- [ ] **Step 6: Replace sorted Props identity with ordered declaration identity**

Make `componentSignature` in `variantcollide.go` return `componentDeclarationFor(c).canonical()`. On parse failure, length-prefix the trimmed raw receiver/type-param/param source so malformed alternatives still compare deterministically without collisions. Remove the body-derived `usesChildren`, `usesAttrs`, field capitalization, and prop sorting.

Update `TestComponentSignature` to assert:

```go
if componentSignature(d) == componentSignature(e) {
	t.Fatal("parameter order must affect the verbatim component contract")
}
```

Add assertions that rename, `[]T` versus `...T`, receiver type, and constraint spelling differ; body-only changes and grouped-versus-ungrouped equivalent lists match.

- [ ] **Step 7: Run tests and static checks**

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

Record all baseline `ns/op`, `B/op`, and `allocs/op` rows in `.superpowers/sdd/progress.md`; Task 9 reruns the exact benchmark names after the two-phase analyzer/importer work.

- [ ] **Step 8: Commit**

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

**Interfaces:**
- Produces `callSiteRegistry`, allocated after one preprocessing pass:

```go
type callSiteID uint32
type callSiteDisposition uint8 // plan or preserveUnsupportedGoBlock
type callSiteRecord struct { ID callSiteID; Element *gsxast.Element; Disposition callSiteDisposition }
type callSiteRegistry struct {
	byElement map[*gsxast.Element]callSiteID
	byID map[callSiteID]callSiteRecord
}
func preprocessComponentCallSites(files map[string]*gsxast.File, ...) (*callSiteRegistry, error)
```

- Target discovery produces `map[callSiteID]componentTargetFact`, where the fact contains the resolved object and its origin, raw generic `*types.Signature`, `types.SelectionKind`, provenance enum, explicit type arguments, and later inferred instance. Marker identifiers in the discovery skeleton map directly back to `callSiteID`; tag text is never a key.

- [ ] **Step 1: Add failing tests for identity and provenance**

Pin: two same-text tags get distinct IDs; a supported tag embedded in an interpolation or top-level `GoWithElements` keeps the same pointer/ID across discovery and a second walk; a direct tag inside `{{ }}` retains one ID only until the existing `unsupported-node` diagnostic and never gets a target fact; package funcs/func vars and concrete `MethodVal` are accepted; `MethodExpr`, `FieldVal`, locals, parameters, interface dispatch, and shadowed package selectors are rejected.

Run: `go test ./internal/codegen -run 'TestAssignCallSites|TestHarvestComponentTargets' -count=1`

Expected: FAIL because the registry and fact types do not exist.

- [ ] **Step 2: Pre-materialize embedded AST once**

Move embedded element splitting/materialization ahead of both skeleton phases. Resolve/stamp `Element.IsComponent` once, then build `callSiteRegistry`; later phases must consume those nodes and may not call the embedded splitter or clone markup. Mark direct `{{ }}` element literals `preserveUnsupportedGoBlock`, emit the existing single diagnostic, and exclude them from discovery, validation, and emission.

- [ ] **Step 3: Add a target-discovery skeleton mode and harvest exact origins**

Emit a registered target expression at each site inside its real lexical scope. Harvest `types.Info.Uses`, `Selections`, and `Instances` for that expression. Tolerate only the registry-marked `cannot use generic function ... without instantiation` error; retain the generic origin object/signature for Task 5. Provenance permits package-scope `types.Func`, package-scope function-valued `types.Var`, and concrete `types.MethodVal`; it rejects every other origin even if its current type is a signature.

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

Cover exact-case matching, duplicate ordinary props, `_foo`, legacy capitalized `Attrs`/`Children` migration diagnostics, non-identifier fallthrough, strict missing attrs, body without children, explicit `children=` rejection, `attrs={expr}`, repeated `attrs={{...}}`, `{bag...}`, conditional bags, ordinary `someAttrs={{...}}`, node-valued markup props, class/style exact targets, omitted named/unnamed ordinary variadics, and rejection of component struct splat.

- [ ] **Step 2: Run the failing test**

Run: `go test ./internal/codegen -run TestPlanComponentInputs -count=1`

Expected: FAIL to compile with `undefined: planComponentInputs`.

- [ ] **Step 3: Implement routing before value lowering**

Match an identifier-shaped name only against an ordinary parameter's exact `Name`. Treat `attrs` as a repeatable contributor stream, not a prop slot. Keep every attrs contributor's authored index; never create `attrsLitIdx` or a forced-last marker. Conditional branch names never fill props. Emit positioned planner errors with stable codes (`duplicate-prop`, `component-missing-attrs`, `component-missing-children`, `reserved-input-form`, `ordinary-variadic-prop`).

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
- Reference: `internal/codegen/component_value_order_test.go`, `internal/codegen/infer.go`

**Interfaces:**
- Produces semantic analysis records, not emitted helpers:

```go
type suppliedOperand struct { ParamIndex int; Expr goast.Expr; TV types.TypeAndValue }
type inferenceContext struct { Pkg *types.Package; Fset *token.FileSet; Scope *types.Scope }
type typeSpellingContext struct {
	Pkg *types.Package
	Imports []importSpec
	ImportAliases map[string]string
	TypeParams map[*types.TypeParam]string
}
type expressionFact struct {
	TV types.TypeAndValue
	IsNil bool
	HasOrderedOperation bool
	Tuple *types.Tuple
}
func inferAuthoredInstance(inferenceContext, componentTargetFact, []suppliedOperand) (types.Instance, []diag.Diagnostic)
func zeroCandidates(types.Type, typeSpellingContext) []zeroCandidate
func planComponentMaterialization(componentCallPlan, map[gsxast.Node]expressionFact) materializationPlan
```

- The inference carrier is a semantic `types.Func` installed in the checker scope; no constraint/type is copied to source and omitted parameters are absent. A positional validation skeleton accepts only a fully instantiated signature.

- [ ] **Step 1: Add failing semantic tests**

Pin `Infer[T](*T)` with authored `nil` as inference failure, constraint-derived inference success, imported unexported constraints, explicit type args, operand-error precedence, incomplete-inference hints, native constraint failures, type-independent `0`/`nil`, nameable `*new(T)`, accessible unnamed `*new(U)`, imported opaque struct omission failure, and opaque numeric/nilable success. Pin source order `first(), second()` against reversed parameter order, untyped `min(1, 2)` remaining contextual, and `(T,error)` consumed as one value.

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/codegen -run 'TestInferAuthoredInstance|TestZeroCandidates|TestPlanComponentMaterialization' -count=1`

Expected: FAIL because the semantic planner functions do not exist.

- [ ] **Step 3: Implement inference before zero-fill**

Construct a semantic `types.Signature` from the origin signature's `*types.TypeParam` objects and supplied parameter types, install a private `types.Func` in the analysis package scope, and harvest its `types.Info.Instances`. This must work when a constraint is imported and unexported because no Go source spelling is involved. Instantiate the origin signature with that result; never use omitted-zero expressions as carrier arguments. Preserve diagnostic precedence: authored operand error, then incomplete-inference hint, then native constraint failure.

- [ ] **Step 4: Implement semantically validated inline zeros**

Try untyped `""`, `0`, `false`, and `nil` with `go/types`; then exact type spelling; then accessible unnamed underlying spelling. Validate identity/assignability in the positional skeleton. If every candidate fails, produce the positioned required-attribute diagnostic. Do not branch on exported spelling or underlying kind alone.

- [ ] **Step 5: Implement selective materialization**

Build each `expressionFact` from `go/types` first. A `TypeAndValue` with `Value != nil` or an untyped nil stays contextual and is never `:=`-materialized; only a non-constant typed expression then consults the existing exhaustive AST walk `exprHasOrderedOperation`. Materialize a value only when it crosses later Go-ordered work or needs statement lowering. Consume `(T,error)` before positional assembly for props, spreads, explicit attrs expressions, and every ordered-literal pair.

- [ ] **Step 6: Verify the pre-cutover planners and stop on semantic drift**

Run: `go test ./internal/codegen -run 'TestInferAuthoredInstance|TestZeroCandidates|TestPlanComponentMaterialization|TestComponentValue' -count=1`

Expected: PASS. If any test requires a helper declaration, guessed zero, or broad syntactic-call materialization, stop with that test as the reproducer.

```bash
git add internal/codegen/component_zero.go internal/codegen/component_zero_test.go internal/codegen/component_call_plan.go internal/codegen/component_call_plan_test.go
git commit -m "feat(codegen): plan inferred positional component calls"
```

### Task 6: Build the Fail-Closed Structural Migration Inventory

**Files:**
- Create: `internal/codegen/verbatim_migration_inventory_test.go` (test-only migration machinery; deleted in Task 7)
- Create: `docs/superpowers/plans/2026-07-14-verbatim-component-signatures-migration-manifest.json`

**Interfaces:** The inventory parses sources; it never rewrites with regex or name guesses.

```go
type migrationKind string
type migrationEdit struct { Start, End int; Replacement string }
type migrationEntry struct {
	Path, SHA256 string
	Kinds []migrationKind
	Edits []migrationEdit
	ReviewNote string
}
type migrationManifest struct { Entries []migrationEntry; Unresolved []string }
```

- Corpus archives are parsed with `txtar.Parse`; `input.gsx` uses the GSX parser/current component facts, `generated.x.go.golden` and `invoke` use `go/parser`, and edits use token offsets.
- Go test fixtures are found by parsing `_test.go` files, unquoting string literals, and accepting only literals that parse as GSX or Go fixture input. Candidate text that cannot be semantically classified is an unresolved error, never a guessed edit.
- Manifest kinds are `declare-children`, `declare-attrs`, `props-invoke`, `byo-whole-value`, `byo-field-address`, `component-struct-splat`, `legacy-role-spelling`, `generated-only`, and `manual-semantic-choice`.

- [ ] **Step 1: Add a red completeness test**

`TestVerbatimMigrationInventory` walks the corpus plus `internal/codegen/*_test.go`, recomputes the manifest, and fails when the committed manifest is missing, has a source-hash mismatch, or contains any unresolved candidate. It verifies every `Props{`, implicit free `children`/`attrs`, BYO field-address call, and component struct splat belongs to exactly one classified entry.

Run: `go test ./internal/codegen -run TestVerbatimMigrationInventory -count=1`

Expected: FAIL because the manifest is absent.

- [ ] **Step 2: Implement AST/type-driven inventory and deterministic edits**

Use current `usesChildren`/`usesAttrs`, BYO facts, resolved component targets, parsed Props field order, and parsed invoke expressions while those old facts still exist. Reserved roles are inserted at explicit source offsets; direct Props invokes are reordered from parsed keyed fields; whole-value BYO/splat cases use the resolved sole parameter. A BYO field-address case that needs an author choice first enters `Unresolved`; after review, its exact token-span replacement is recorded in `Edits` and the reason in `ReviewNote`. Nothing is guessed from field-name text.

Define test flags `-update-verbatim-manifest` and `-write-verbatim-migration`. The update flag writes only the sorted JSON manifest. The write flag validates every SHA-256, requires `Unresolved: []`, and applies only recorded token-span edits.

- [ ] **Step 3: Generate, review, and pin zero unresolved cases**

Run: `go test ./internal/codegen -run TestVerbatimMigrationInventory -update-verbatim-manifest -count=1`

Inspect every unresolved/manual-semantic-choice, add its exact reviewed `Edits` plus `ReviewNote`, clear `Unresolved`, and rerun:

`go test ./internal/codegen -run TestVerbatimMigrationInventory -count=1`

Expected: PASS with `Unresolved: []`. Review category counts against:

```bash
rg -l 'Props\{' internal/corpus/testdata/cases -g '*.txtar' | wc -l
rg -l 'component ' internal/codegen -g '*_test.go' | sort
```

These commands are completeness backstops only; they do not drive rewrites.

- [ ] **Step 4: Commit the reviewed inventory**

```bash
git add internal/codegen/verbatim_migration_inventory_test.go docs/superpowers/plans/2026-07-14-verbatim-component-signatures-migration-manifest.json
git commit -m "test(codegen): inventory verbatim signature migration"
```

### Task 7: Atomic Core Cutover — Verbatim Declarations and Positional Calls

**Files:**
- Modify: `internal/codegen/analyze.go` (`emitComponentSkeleton`, `emitComponentStub`, positional phase)
- Modify: `internal/codegen/emit.go` (`genComponent`, `genChildComponent`, declared bindings, value materialization)
- Modify: `internal/codegen/module_importer.go`
- Modify: every `internal/codegen/*_test.go` fixture listed by the Task 6 manifest
- Modify: `internal/corpus/testdata/cases/**/*.txtar`, `internal/corpus/testdata/coverage.golden`
- Delete: `internal/codegen/verbatim_migration_inventory_test.go` after its write/verification pass
- Create: `internal/corpus/testdata/cases/verbatim/core_positional.txtar`
- Create: `internal/corpus/testdata/cases/verbatim/direct_go_signature.txtar`

**Interfaces:**
- Consumes the Task 1-5 facts/plans.
- Produces one shipping ABI only: authored declaration and positional markup call. This commit is the explicit rollback unit.

- [ ] **Step 1: Add the two failing corpus cases**

`core_positional.txtar` must combine reversed authored/signature order, an untyped `min(1, 2)` into a defined numeric parameter, and a `(string,error)` prop. Its render output records authored order. `direct_go_signature.txtar` contains:

```go
type History struct{ Label string }
component Page(h History) { <p>{h.Label}</p> }
var _ func(History) gsx.Node = Page
```

Its `-- invoke --` is `Page(History{Label: "ok"})`, and its generated golden contains neither `PageProps` nor an analysis helper declaration.

- [ ] **Step 2: Verify the old ABI fails**

Run: `go test ./internal/corpus -run 'TestCorpus/verbatim/(core_positional|direct_go_signature)' -count=1`

Expected: FAIL because the generator still emits Props structs/calls.

- [ ] **Step 3: Replace declaration and call paths together and delete the old model**

Emit `func [recv] Name[typeparams](<authored params>) gsx.Node` from Task 1's parsed declaration/source spans, with block `/*line file:line:col*/` anchors preserving each parameter name/type in the skeleton and final declaration. The render closure captures authored parameters directly; bind no `_gsxp` fields. `children`/`attrs` availability comes from the signature model, not body scanning. Build the positional validation skeleton from the completed plan and emit the same call shape after validation.

In this same change, delete the old `param`/`parseParams`, Props struct/stub emission, `_gsxp` bindings, `componentPropFieldsFor`, and every BYO/attrs-only branch consumed by declaration/call emission. No legacy projection, flag, adapter, or dormant second path remains.

- [ ] **Step 4: Replace the attrs field builder with authored-position parts**

Reuse the existing sanitizing `gsx.Attrs`, `ConcatAttrs`, renderer, URL, class, and style lowering. Store explicit `attrs={}` and `attrs={{}}` as ordinary source-indexed segments/pairs; delete forced-last composition. Convert the canonical bag to defined slices via `[]gsx.Attr(bag)` and expand only `attrs ...gsx.Attr`.

- [ ] **Step 5: Regenerate and run the implementation checkpoint**

Run: `go test ./internal/corpus -run 'TestCorpus/verbatim/(core_positional|direct_go_signature)' -update`

Then: `go test ./internal/corpus -run 'TestCorpus/verbatim/(core_positional|direct_go_signature)' -count=1`

Expected: PASS; inspect the golden and confirm authored expression order, contextual untyped value, tuple unwrap, exact function type, and no Props/helper declaration. If any one fails, stop here and discuss before Task 8.

- [ ] **Step 6: Apply the reviewed manifest inside the rollback unit**

Run: `go test ./internal/codegen -run TestVerbatimMigrationInventory -write-verbatim-migration -count=1`

The write pass applies every reviewed edit. Verify `git diff` by manifest category, then delete the test-only migration machinery. Every existing corpus/codegen fixture that uses implicit roles, direct Props invokes, BYO field addressing, or component struct splat must be covered by a manifest entry. Regenerate all corpus goldens; do not leave the repository between declaration and caller ABIs.

Run: `go test ./internal/corpus -run TestCorpus -update`

Then: `go test ./internal/corpus -run TestCorpus -count=1`

Expected: PASS across the complete canonical corpus.

- [ ] **Step 7: Run core packages and commit the atomic rollback unit**

Run: `go test ./internal/codegen ./internal/corpus -count=1`

Expected: PASS after applying every reviewed structural migration entry in the same commit; the old ABI no longer exists.

```bash
git add internal/codegen internal/corpus/testdata/cases internal/corpus/testdata/coverage.golden
git commit -m "feat(codegen): emit verbatim component signatures"
```

### Task 8: Complete Callable, Children, Attrs, Zero, and Cross-Package Semantics

**Files:**
- Modify: `internal/codegen/component_target.go`, `component_signature.go`, `component_call_plan.go`, `component_zero.go`, `emit.go`, `module_importer.go`
- Create: `internal/corpus/testdata/cases/verbatim/children_attrs.txtar`
- Create: `internal/corpus/testdata/cases/verbatim/attrs_shapes.txtar`
- Create: `internal/corpus/testdata/cases/verbatim/callable_origins.txtar`
- Create: `internal/corpus/testdata/cases/verbatim/zero_inference_xpkg.txtar`
- Create: `internal/corpus/testdata/cases/verbatim/class_style_targets.txtar`

**Interfaces:** Completes every accepted/rejected row in the spec without adding a second call convention.

- [ ] **Step 1: Add the five corpus matrices**

Pin scalar children with an empty body producing `nil`, scalar/variadic non-empty children, static-node counts, body rejection, node-valued markup props, all attrs target/contributor shapes and exact element identity (including instantiated defined-slice targets), repeated explicit attrs in authored order, ordinary named bags, legacy capitalized `Attrs`/`Children` migration diagnostics, exact class/style forms, named/blank/unnamed ordinary variadic omission and fill rejection, generic inference failures, opaque cross-package zeros, free funcs/func vars/named func types/bound methods, and every rejected dynamic origin. Include a concrete result assignable to `gsx.Node`.

- [ ] **Step 2: Run and observe only feature-specific failures**

Run: `go test ./internal/corpus -run 'TestCorpus/verbatim/(children_attrs|attrs_shapes|callable_origins|zero_inference_xpkg|class_style_targets)' -count=1`

Expected: FAIL only on unimplemented rows; any regression in Task 7's core case is a blocker.

- [ ] **Step 3: Implement each matrix through the universal facts/planner**

Do not revive `attrsOnlySig`, BYO classification, struct splat, or name guessing. Error on unnamed fixed params, non-reserved ordinary variadic attrs, true method expressions, interface dispatch, and unresolved signatures.

- [ ] **Step 4: Regenerate, verify, and commit**

Run: `go test ./internal/corpus -run TestCorpus -update`

Run: `go test ./internal/corpus -run TestCorpus -count=1`

Expected: PASS.

```bash
git add internal/codegen internal/corpus/testdata/cases internal/corpus/testdata/coverage.golden
git commit -m "test(codegen): complete verbatim call semantics"
```

### Task 9: Authoritative Project-Local Import Graph, Cache, Stale Output, and Performance

**Files:**
- Modify: `internal/codegen/module_importer.go`, `module.go`, `resolver.go`, `module_stale_xgo_test.go`, `depfacts_test.go`, `invalidation_test.go`, `module_perf_test.go`, `bundle_module_test.go`
- Modify: `gen/orphan_e2e_test.go`, `gen/poison_e2e_test.go`
- Delete: `internal/codegen/byo.go`, `byo_*_test.go`, `attrsonly.go`, `attrsonly_test.go`, `freeuse.go`, obsolete free-use tests, `fieldmatch.go`, `fieldmatch_test.go`
- Keep/modify: `internal/codegen/reserved_scan.go`, `reserved_bindings.go` (retain `_gsx...` and `ctx`; move the needed statement-binding parser out of `freeuse.go`, then remove only implicit `children`/`attrs` machinery)
- Modify: `gen/options.go`, `attrtypes.go`, `main.go`, `cache.go`, `cachekey.go`, `configfile.go`, `info.go`, `manifest.go`, `watch*.go`, `lsp.go`, associated tests

**Interfaces:** Declaration/signature facts replace dep prop facts; invalidation keys include ordered name/type/variadic/role data. The one normal-mode cold load retains exact project source metadata:

```go
type projectPackageSource struct {
	ImportPath, Name, Dir string
	CompiledGoFiles []string
	Syntax []*goast.File // same order/build context as CompiledGoFiles
	Imports []string
}
// Module fields:
projectSources map[string]projectPackageSource
projectTypes map[string]*types.Package
```

- [ ] **Step 1: Add failing `gsx -> Go-only -> gsx` and signature-only invalidation tests**

Create a temp graph where `page.gsx` imports project `bridge` Go source, `bridge` imports `ui`, and `ui/card.gsx` has a poisoned old-Props `card.x.go`. Give `bridge` mutually exclusive build-tag variants and put the stale-ABI reference only in the inactive variant. Assert the retained inventory matches the active `CompiledGoFiles`, first-run analysis sees the current verbatim `ui.Card` signature, and the inactive file is never parsed. Rename only a `ui.Card` parameter and assert importer/page facts invalidate through `bridge`, with `externalLoads()==1`.

- [ ] **Step 2: Retain the cold load's authoritative compiled-file inventory**

Add `packages.NeedCompiledGoFiles | packages.NeedSyntax` to the existing `packages.Config.Mode`. With the shared `m.fset`, retain aligned `CompiledGoFiles`/`Syntax` only for module-local Go-only packages. Do not glob, call `build.ImportDir`, or parse source again: that would diverge on build tags, cgo-generated files, tests, and the load's environment.

- [ ] **Step 3: Re-type-check retained syntax through `moduleImporter`**

When a project-local Go-only import is reached, type-check its retained ASTs with the same recursive `moduleImporter`; route GSX children to current declaration skeletons, cache the result in `projectTypes`, and record forward/reverse edges for both Go-only and GSX directories. A GSX signature edit invalidates the reverse closure and rechecks retained syntax without another load.

- [ ] **Step 4: Add and implement Bundle fail-closed behavior**

Build a Bundle test for the same `page -> bridge -> ui.gsx` graph with a stale bundled `ui` type. Assert generation returns `bundle-project-gsx-transitive` and directs the caller to the normal resolver. In Bundle mode, inspect project-local import edges from prebuilt `types.Package.Imports`; if a Go-only package reaches a local GSX source directory, reject before its stale type can enter a target fact. Do not add source metadata or a compatibility ABI to Bundle without a real consumer.

- [ ] **Step 5: Preserve and strengthen existing stale/orphan behavior**

Extend `TestModuleIgnoresStaleOnDiskXGo` with a stale generated `PageProps` wrapper and assert it is absent from scope/output. Keep the existing exact-header ownership gate and `Result.Removed` reporting, including a directory with no remaining `.gsx`; do not replace orphan removal with a new diagnostic mechanism.

- [ ] **Step 6: Remove obsolete subsystems and `WithFieldMatcher` end to end**

Delete BYO, attrs-only, implicit-role free-use, and field-matcher code and remove `WithFieldMatcher` from option/config/cache-key/manifest/info/watch/LSP wiring outright. Keep `reserved_scan.go`. Before deleting `freeuse.go`, move `fragKind`, the Go statement parser, `boundIdent`, and the binding collector needed by `checkReservedBodyBindings` into `reserved_bindings.go` (or `reserved_ctx_bindings.go`), filter it to `ctx`, and retain focused top-scope/nested-scope tests. Replace old tests with signature-model equivalents and update `docs/ROADMAP.md`. Add no deprecated aliases or ignored config key.

- [ ] **Step 7: Run phase-count and cold/warm benchmarks**

Rerun the Task 1 `same-package`, `imported`, and `embedded` rows unchanged, and add `attrs-stream` and `variadic-children` rows for the new paths. Run:

`go test ./internal/codegen -run '^$' -bench 'BenchmarkModuleGenerateComponent(Cold|Warm)' -benchmem -count=5`

Record results beside the Task 1 baseline in `.superpowers/sdd/progress.md` and compare `ns/op`, `B/op`, and `allocs/op` case by case. Any material warm-regeneration regression is a discuss-before-continuing checkpoint. Assert the load-count test remains exactly one external load and zero filter-table reloads through ten edits.

- [ ] **Step 8: Verify and commit**

Run: `go test ./internal/codegen ./gen -run 'Test.*(Stale|Orphan|Poison|Invalidat|GoOnly|WarmRegen|FieldMatcher)' -count=1`

Expected: PASS, including Bundle failure and active-build-file selection.

Run: `rg -n 'FieldMatcher|attrsOnlySig|soleParamTypeName|usesChildren|usesAttrs' internal gen cmd playground --glob '*.go'`

Expected: no matches. `reservedPrefix` and `checkReservedDecls` remain live.

```bash
git add internal/codegen gen docs/ROADMAP.md
git commit -m "refactor(codegen): remove obsolete generated-props subsystems"
```

### Task 10: LSP Exact Navigation, Hover, and Semantic Parameter Rename

**Files:**
- Modify: `internal/codegen/results.go`, `internal/codegen/module.go`
- Modify: `gen/lsp.go`
- Modify: `internal/lsp/analysis.go`, `definition_attr.go`, `definition.go`, `hover.go`, `protocol.go`, `server.go`
- Delete: `internal/lsp/definition_attrsonly.go`, `definition_attrsonly_test.go`
- Create: `internal/lsp/rename.go`, `internal/lsp/rename_test.go`
- Modify: `internal/lsp/definition_attr_test.go`, `hover_test.go`, `variant_nav_test.go`

**Interfaces:** Retain a stable parameter key `(package path, component key, ordinal)` plus `types.Var.Origin()` and exact call-site refs. Add `textDocument/prepareRename` and `textDocument/rename`.

- [ ] **Step 1: Change attr definition/hover tests to exact names**

Assert `title` resolves only `title`, never `Title`; reserved attrs contributors resolve to the declared `attrs` parameter; ordinary `someAttrs={{}}` resolves normally.

- [ ] **Step 2: Add rename protocol tests**

Pin rename from a GSX declaration and invocation, cross-package calls, instantiated generic calls normalized by `Var.Origin()`, and equivalent GSX build-tag variants updated by the same ordinal. Pin rejection for renaming `children`/`attrs`, renaming any ordinary param to `_`, `children`, `attrs`, `ctx`, or `_gsxName`, invalid/colliding identifiers, and variant sets whose Task 1 canonical identity is not equivalent. Pin that `prepareRename` is not offered for a plain-Go callable parameter even though definition/hover still resolve it.

- [ ] **Step 3: Implement semantic rename**

Expose GSX parameter declaration/ref facts from codegen analysis. Advertise `RenameProvider`, dispatch both methods, and return one atomic `WorkspaceEdit`. Resolve a generic instantiated param through `Var.Origin`; resolve GSX variants by canonical contract plus ordinal, never by text-only search. Do not partially rename plain-Go callable parameters: inactive Go variants are outside GSX's all-source variant model, so `prepareRename` rejects that target.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/lsp ./gen -run 'Test.*(Attr|Hover|Rename|Variant|Definition)' -count=1`

Expected: PASS.

```bash
git add internal/codegen/results.go internal/codegen/module.go gen/lsp.go internal/lsp
git commit -m "feat(lsp): navigate and rename exact component parameters"
```

### Task 11: Formatter, Corpus Migration, Examples, Docs, and Init Scaffold

**Files:**
- Modify: `internal/gsxfmt/testdata/cases/*.txtar`
- Modify: `examples/*.txtar`, `examples/tailwind-merge/**`
- Modify: `internal/examplegen/**`
- Modify: `gen/templates/init/simple/app.gsx`, `main.go.tmpl`, `gen/init_test.go`
- Modify: `playground/server/run.go`, `cache_test.go`, `render_test.go`
- Modify: `README.md`, `skills/gsx/SKILL.md`
- Modify: `docs/guide/syntax/{props,composition,attributes,interop,basic-syntax}.md`, `docs/guide/{extensions,syntax,learn,status,vision}.md`, `docs/guide/patterns/render-once.md`, generated syntax pages
- Modify: `ast/ast.go` (`OrderedAttrsAttr` comment)

**Interfaces:** All shipped source and prose uses declared roles and direct Go calls; no generated Props examples remain.

- [ ] **Step 1: Add fmt cases for declared roles and grouped/variadic params**

Pin `component Card(title string, attrs gsx.Attrs, children gsx.Node)` and `component Tabs(children ...gsx.Node)` formatting.

- [ ] **Step 2: Migrate examples mechanically**

Declare `children`/`attrs` where used; change manual invokes from `X(XProps{...})` to positional `X(...)`; replace whole struct splats with exact named props; rename attrs-only function parameters to `attrs`. Apply the same direct-call migration to playground server code/tests and both init template files. Replace examples `250-byo-props`, `251-props-heuristic`, and `252-splat` with verbatim options-struct/direct-prop examples under names matching the new concepts. The canonical internal corpus was already migrated atomically in Task 7; this task changes only shipped examples, scaffolds, playground consumers, skills, and documentation artifacts.

- [ ] **Step 3: Rewrite guide pages rule-first with runnable examples**

Document exact signatures, direct Go interop, options structs, strict fallthrough, both explicit attrs contributor forms, children forms, exact matching, zero-fill/required opacity, and callable eligibility. Remove `WithFieldMatcher`, BYO heuristic, generated Props, and struct-splat guidance from every listed guide, README, and the GSX skill; do not leave deprecated syntax. Wrap literal `{{ }}` prose in `::: v-pre`.

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
git add README.md skills/gsx/SKILL.md ast internal/gsxfmt internal/examplegen examples gen/templates gen/init_test.go playground/server docs/guide
git commit -m "docs: migrate shipped surfaces to verbatim component signatures"
```

### Task 12: Separately Gated Ecosystem and Real-World Migration Follow-Up

**Files:**
- Sibling repos: `/Users/jackieli/personal/gsxhq/tree-sitter-gsx`, `/Users/jackieli/personal/gsxhq/vscode-gsx`, `/Users/jackieli/personal/gsxhq/gsxhq.github.io`
- Consumer repos: `/Users/jackieli/personal/structpages`, `/Users/jackieli/work/one-learning-gsx`

**Interfaces:** Starts only after Tasks 1-11 pass `make check`; each repository gets its own plan/commit/verification and can be rolled back independently.

- [ ] **Step 1: Audit grammar/highlighting impact before editing siblings**

Create a feature worktree in each sibling before editing. Run each sibling's tests against declared `children`/`attrs`, grouped/variadic parameters, and attrs literals. If grammar is unchanged, commit only fixture/highlighting/docs updates; do not create syntax churn.

```bash
cd /Users/jackieli/personal/gsxhq/tree-sitter-gsx && npm ci && npm run generate && npm test && npm run test:authoritative
cd /Users/jackieli/personal/gsxhq/vscode-gsx && npm ci && npm run gen:grammar && npm run typecheck && npm run lint && npm test
cd /Users/jackieli/personal/gsxhq/gsxhq.github.io && npm ci && GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/gsx/.worktrees/verbatim-component-signatures VITE_GSX_PLAYGROUND_API=https://example.invalid npm run build
```

- [ ] **Step 2: Prove structpages interop with the motivating type match**

In `/Users/jackieli/personal/structpages`, add a route fixture whose `Props() History` feeds `Page(h History)`, drive full and partial HTTP renders, and assert no wrapper type appears in reflection errors or generated source. Run `go test ./... -count=1`.

- [ ] **Step 3: Write and execute a separate one-learning migration plan**

Inventory the current 841 declarations/71 manual calls again, migrate shared leaf components first, declare reserved roles, convert manual Props calls to positional/direct options structs, and keep root children-taking layouts last. Verify DOM equivalence and browser behavior. Any unsupported real signature becomes a minimal GSX corpus reproducer and a discussion checkpoint; do not add an application workaround.

- [ ] **Step 4: Commit and report each gate independently**

Run the native test/build commands in each repository and record commit IDs and remaining migration slices in the execution ledger.

### Task 13: Independent Adversarial Review and Authoritative Verification

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
rg -n 'componentPropFieldsFor|childPropsLiteral|attrsOnlySig|soleParamTypeName|WithFieldMatcher|_gsxcall' internal/codegen gen cmd playground --glob '*.go' --glob '!*_test.go'
git diff --check
git status --short
```

Expected: no obsolete live implementation symbols, no whitespace errors, and only intentional changes. Generated-output tests—not a name regex—prove that no synthesized Props/helper declaration exists, because author-owned types are allowed to end in `Props` and corpus diagnostics intentionally exercise `_gsxcall`.

- [ ] **Step 5: Commit any review fixes, rerun the failed gate, then rerun `make ci`**

Use one focused fix commit per independently reviewable defect. Do not fold an out-of-ordinary semantic discovery into cleanup; raise it to the user with the probe before changing the design.

## Self-Review

- **Spec coverage:** Task 1 covers ordered declaration/build-tag identity; Task 2 roles and attrs family; Task 3 stable AST IDs/provenance; Tasks 4-5 exact routing, inference, zeros, and order; Task 6 builds the reviewed structural migration inventory; Task 7 is the atomic ABI rollback unit; Task 8 covers the callable/edge matrix; Task 9 covers authoritative import graph, cache, stale/orphan, removal, and performance; Task 10 covers LSP and rename; Task 11 covers fmt/examples/docs/scaffold; Task 12 gates siblings, structpages, and one-learning; Task 13 provides adversarial and authoritative verification.
- **Placeholder scan:** The plan contains no deferred implementation marker; later ecosystem work is an explicit post-core gate with named repositories, evidence, and commands determined by each repository's checked-in workflow.
- **Type consistency:** `componentDeclaration`, `componentSignatureModel`, `componentTargetFact`, `callSiteID`, `componentCallPlan`, `componentParam`, and `Var.Origin()` are introduced once and consumed in dependency order.
- **Known execution checkpoint:** The atomic cutover is intentionally one large commit because declaration and call ABIs cannot compile independently. Tasks 1-5 are pure/tested foundations, Task 6 proves every repository fixture has a structural migration action, and Task 7 is the single revertible switch.
