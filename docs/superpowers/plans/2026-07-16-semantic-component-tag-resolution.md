# Semantic Component-Tag Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `ast.Element.IsComponent` the final semantic truth for every tag by requiring an allowed callable with exactly one result assignable to canonical `gsx.Node`, while preserving lowercase HTML fallback and positioned errors for invalid capital-first/dotted claims.

**Architecture:** Preprocessing materializes embedded markup and records syntactic component candidates without mutating `IsComponent`. Exact target discovery type-checks those candidates in their real lexical scopes; one finalizer stamps valid components, turns definitively non-component lowercase package names into leaves, and rejects invalid explicit claims. Full skeleton analysis, generation, and the LSP consume that final stamp; declaration-only and importer-free lanes consume syntax-only candidate facts and never classify components themselves.

**Tech Stack:** Go 1.26.1, `go/ast`, `go/token`, `go/types`, GSX AST/codegen/LSP, txtar corpus, `gopls`, `make check`, `make ci`.

## Global Constraints

- Component identity is: allowed callable provenance plus exactly one result assignable to the imported runtime's canonical `gsx.Node`.
- Assignability is intentionally broader than exact identity; aliases and concrete node implementations remain valid.
- Identity does not validate parameters. Reserved roles, variadics, names, zeros, and operand binding remain downstream signature diagnostics.
- `Element.IsComponent` is false until semantic resolution succeeds and is written once per successfully preprocessed package.
- Capital-first and dotted tags are explicit claims: invalid claims are positioned errors, never HTML fallbacks.
- Lowercase tags become components only for component-capable package symbols; definitive non-callables and wrong-result functions remain leaves.
- Lowercase self-exclusion occurs before candidate discovery; explicit capital-first/dotted recursion remains a candidate.
- No `main` special case, HTML-name allowlist, text scan, new `packages.Load`, Go subprocess, provisional stamp, compatibility path, or fallback classifier.
- Active companions and variants come from the existing authoritative build context.
- Every production change follows red-green-refactor and ends in a small commit.

---

## File Structure

- Create `internal/codegen/component_identity.go`: shared provenance/result-only identity validation.
- Modify `internal/codegen/component_signature.go`: reuse result validation before parameter analysis.
- Modify `internal/codegen/tagresolve.go` and `internal/codegen/component_target.go`: candidate recording and the single final stamp.
- Modify `internal/codegen/component_target_package.go`, `internal/codegen/analyze.go`, and `internal/codegen/module_importer.go`: discover candidates before full skeleton analysis.
- Modify declaration resolvers and importer-free unused-import analysis to consume syntax preparation without semantic stamps.
- Modify `ast/ast.go`, focused tests, LSP fixtures, corpus cases, and canonical guide wording.

---

### Task 1: Extract the Shared Result Contract

**Files:**
- Create: `internal/codegen/component_identity.go`
- Modify: `internal/codegen/component_signature.go`
- Test: `internal/codegen/component_signature_test.go`

**Interfaces:**
- Produces: `func componentResultType(sig *types.Signature, runtime runtimeContract) (types.Type, error)`.
- Preserves error prefixes `component-result-count` and `component-result-type`.
- Task 2 uses this helper without invoking parameter validation.

- [ ] **Step 1: Capture the pre-change component benchmark baseline**

```bash
go test ./internal/codegen -run '^$' -bench 'BenchmarkModuleGenerateComponent(Cold|Warm)$' -benchmem -count=5 | tee /tmp/gsx-component-before.txt
```

Expected: every benchmark completes and `/tmp/gsx-component-before.txt` contains five samples per case.

- [ ] **Step 2: Write a failing result-only test**

```go
func TestComponentResultTypeDoesNotValidateParameters(t *testing.T) {
	fx := newSignatureRuntimeFixture(t)
	user := types.NewPackage("example.test/results", "results")
	badAttrs := testParam(user, "attrs", types.Typ[types.String])
	sig := testSignature(user, nil, []*types.Var{badAttrs}, []types.Type{fx.runtime.node}, false)

	result, err := componentResultType(sig, fx.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if !types.Identical(result, fx.runtime.node) {
		t.Fatalf("result = %v, want canonical Node", result)
	}
	if _, err := analyzeComponentSignature(sig, fx.runtime); err == nil || !strings.Contains(err.Error(), "component-attrs-type") {
		t.Fatalf("full signature error = %v, want component-attrs-type", err)
	}
}
```

- [ ] **Step 3: Verify red**

Run:

```bash
go test ./internal/codegen -run 'Test(ComponentResultTypeDoesNotValidateParameters|AnalyzeComponentSignatureResultContract)$' -count=1
```

Expected: FAIL because `componentResultType` does not exist.

- [ ] **Step 4: Implement the helper and remove duplicate checks**

```go
func componentResultType(sig *types.Signature, runtime runtimeContract) (types.Type, error) {
	if sig == nil {
		return nil, fmt.Errorf("component-signature: nil callable signature")
	}
	checked := make(map[types.Type]bool)
	if invalidSemanticTypeSeen(runtime.node, checked) {
		return nil, fmt.Errorf("component-signature-runtime: incomplete runtime node type")
	}
	results := sig.Results()
	if results.Len() != 1 {
		return nil, fmt.Errorf("component-result-count: callable has %d results; want exactly one", results.Len())
	}
	result := results.At(0).Type()
	if invalidSemanticTypeSeen(result, checked) || !types.AssignableTo(result, runtime.node) {
		return nil, fmt.Errorf("component-result-type: result %s is not assignable to %s", result, runtime.node)
	}
	return result, nil
}
```

`analyzeComponentSignature` must validate the full runtime `Node`/`Attr`/`Attrs` contract, call this helper, then validate parameters.

- [ ] **Step 5: Verify green and static analysis**

```bash
go test ./internal/codegen -run 'Test(ComponentResultTypeDoesNotValidateParameters|AnalyzeComponentSignature)' -count=1
gopls check -severity=hint internal/codegen/component_identity.go internal/codegen/component_signature.go
```

Expected: PASS with no new diagnostics.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/component_identity.go internal/codegen/component_signature.go internal/codegen/component_signature_test.go
git commit -m "refactor(codegen): centralize component identity result contract"
```

---

### Task 2: Record Candidates Without Stamping Components

**Files:**
- Modify: `ast/ast.go`
- Modify: `internal/codegen/tagresolve.go`
- Modify: `internal/codegen/component_target.go`
- Test: `internal/codegen/tagresolve_test.go`
- Test: `internal/codegen/component_target_test.go`

**Interfaces:**
- Produces candidate kinds `componentCandidateExplicit` and `componentCandidateLowercasePackage`.
- Adds final dispositions `componentSitePlanned`, `componentSiteLeaf`, `componentSiteRejected`, and preserved-invalid-region.
- Produces `(*callSiteRegistry).finalizeComponentIdentity(...)` as the only writer of `IsComponent`.

- [ ] **Step 1: Write failing lifecycle tests**

After preprocessing a file containing `<Upper/>`, `<ui.Card/>`, `<lower/>`, `<main/>`, and `<div/>`, assert:

- capital/dotted and declared lowercase names have candidate records;
- undeclared `div` is absent;
- every element still has `IsComponent == false`;
- a self-excluded lowercase tag is absent;
- a second preprocessing claim still fails.

Add table tests for finalization:

| Tag | Semantic fact | Expected |
|---|---|---|
| `Card` | allowed callable returning Node | component/planned |
| `card` | allowed callable returning concrete Node | component/planned |
| `main` | allowed zero-result function | leaf/no diagnostic |
| `Main` | allowed zero-result function | rejected/`component-result-count` |
| `time` | resolved non-callable package object | leaf/no diagnostic |
| `Missing` | unresolved | rejected/`invalid-component-target` |
| `broken` | Node result plus bad `attrs` param | component/planned; parameter error later |

- [ ] **Step 2: Verify red**

```bash
go test ./internal/codegen -run 'Test(ComponentCandidate|FinalizeComponentIdentity|PreprocessComponent)' -count=1
```

Expected: FAIL because preprocessing currently stamps from syntax/declaration names.

- [ ] **Step 3: Replace `resolveTag` with candidate classification**

```go
type componentCandidateKind uint8

const (
	componentCandidateNone componentCandidateKind = iota
	componentCandidateExplicit
	componentCandidateLowercasePackage
)

func componentCandidateFor(tag string, declNames map[string]bool, exclusions componentExclusions) componentCandidateKind {
	if gsxast.IsComponentTag(tag) {
		return componentCandidateExplicit
	}
	if exclusions[tag] {
		return componentCandidateNone
	}
	if token.IsIdentifier(tag) && declNames[tag] {
		return componentCandidateLowercasePackage
	}
	return componentCandidateNone
}
```

Candidate collection must not write `IsComponent`. Move leaf type-argument diagnostics to finalization, because candidate syntax is not a semantic answer.

- [ ] **Step 4: Add explicit rejection provenance**

Extend `componentTargetFact` with an enum, set from resolved `go/types.Object` facts—not messages:

```go
const (
	componentTargetAccepted componentTargetRejection = iota
	componentTargetDefinitiveNonCallablePackageObject
	componentTargetUnresolved
	componentTargetDisallowedProvenance
)
```

Only resolved package consts/types/non-callable vars are definitive lowercase leaves. Incomplete objects, locals, fields, interface dispatch, and method expressions remain failures.

- [ ] **Step 5: Implement the one finalizer**

`finalizeComponentIdentity` uses Task 1's `componentResultType` and this policy:

- valid explicit or lowercase callable: set `IsComponent=true`, mark planned;
- invalid explicit claim: keep false, mark rejected, report exact target/result diagnostic;
- definitive lowercase non-callable or invalid-result callable: keep false, mark leaf, suppress only that site's component diagnostic;
- unresolved/incomplete lowercase candidate: keep false, mark rejected, report the semantic diagnostic;
- final leaf with type arguments: report `type-args-on-element`;
- never call `analyzeComponentSignature` here.

- [ ] **Step 6: Verify and commit**

```bash
go test ./internal/codegen -run 'Test(ComponentCandidate|FinalizeComponentIdentity|PreprocessComponent|AnalyzeComponentSignature)' -count=1
gopls check -severity=hint ast/ast.go internal/codegen/tagresolve.go internal/codegen/component_target.go
git add ast/ast.go internal/codegen/tagresolve.go internal/codegen/tagresolve_test.go internal/codegen/component_target.go internal/codegen/component_target_test.go
git commit -m "refactor(codegen): separate component candidates from identity"
```

Expected: tests pass and comments describe `IsComponentTag` as candidate syntax only.

---

### Task 3: Discover Targets Before Full Skeleton Analysis

**Files:**
- Modify: `internal/codegen/analyze.go`
- Modify: `internal/codegen/component_target_package.go`
- Modify: `internal/codegen/module_importer.go`
- Test: `internal/codegen/component_target_importer_test.go`
- Test: `internal/codegen/module_perf_test.go`
- Test: `internal/codegen/module_test.go`

**Interfaces:**
- Target discovery consumes exact candidate element identity, not `IsComponent`.
- Full skeletons, wrapper-cycle analysis, positional planning, emission, and LSP publication consume final stamps.

- [ ] **Step 1: Write failing end-to-end regressions**

Use a package containing:

```gsx
package main

import "github.com/gsxhq/gsx"

func main() {}
func concrete() concreteNode { return concreteNode{} }
func broken(attrs string) gsx.Node { return nil }

component Page() {
	<main><concrete/></main>
}
```

Assert generation emits literal `<main>`, calls `concrete()`, and retains stamps `main=false`, `concrete=true`. Separate cases must prove `<Missing/>`, `<Zero/>`, and `<dep.Zero/>` are positioned errors, while `<broken/>` remains a component and receives `component-attrs-type` downstream.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/codegen -run 'Test(SemanticComponentIdentity|PackageMainTag|WarmRegenDoesNoGoListReloads)$' -count=1
```

Expected: package-main fails under the current declaration-name stamp.

- [ ] **Step 3: Make discovery probes use registry membership**

```go
candidateProbe := targetRegistry != nil && targetRegistry.hasCandidate(t)
if t.IsComponent || candidateProbe {
	if candidateProbe {
		if err := targetRegistry.emitBinding(sb, t, fset); err != nil {
			return err
		}
	}
	// existing component operand probes
} else {
	// existing leaf probes
}
```

`hasCandidate` must be a pointer-identity lookup. Do not inspect spelling in `emitProbes`.

- [ ] **Step 4: Reorder `Module.analyze`**

The exact order is:

```text
parse GSX and active companions
materialize syntax, exclusions, scripts, and candidate IDs
validate reserved declarations and bundle imports
compute package path, environment, component variant plan, and filter table
discover candidate targets in lexical scope
derive canonical runtime contract
finalize identity and stamp IsComponent once
report wrapper cycles
build/type-check full skeletons using final stamps
plan positional calls from retained target/expression facts
publish generation and LSP facts only after every gate succeeds
```

Reuse the existing target importer and target package. Add no semantic load or command.

- [ ] **Step 5: Preserve exact error ownership**

Suppress site diagnostics only for a lowercase fact proven to be a non-callable package object or an allowed callable with an invalid result. Unrelated `types.Error` values remain fatal. Runtime or target incompleteness rejects the package before full skeleton construction.

- [ ] **Step 6: Verify planned-site invariants**

Assert every planned record has `IsComponent=true`; leaf/rejected/preserved records have false. Positional planning and import-qualifier accounting skip non-planned sites after finalization.

```bash
go test ./internal/codegen -run 'Test(SemanticComponentIdentity|PackageMainTag|WarmRegenDoesNoGoListReloads|ComponentTarget)' -count=1
gopls check -severity=hint internal/codegen/analyze.go internal/codegen/component_target_package.go internal/codegen/module_importer.go
git add internal/codegen/analyze.go internal/codegen/component_target_package.go internal/codegen/module_importer.go internal/codegen/component_target_importer_test.go internal/codegen/module_perf_test.go internal/codegen/module_test.go
git commit -m "fix(codegen): resolve component identity before lowering"
```

Expected: all focused tests pass with unchanged external-load and Go-command counts.

---

### Task 4: Keep Declaration and Import-Usage Lanes Honest

**Files:**
- Modify: `internal/codegen/component_target_importer.go`
- Modify: `internal/codegen/renderer_decls.go`
- Modify: `internal/codegen/unused_imports_syntactic.go`
- Modify: `internal/lsp/definition_test.go`
- Test: corresponding target-importer, renderer, unused-import, and LSP tests

**Interfaces:**
- Declaration packages prepare syntax and build inert bodies without classifying tags.
- Importer-free analysis uses candidate membership for syntactic target references but never writes `IsComponent`.

- [ ] **Step 1: Write failing boundary tests**

Pin that target/renderer declaration packages expose authored signatures even with `func main()` plus body `<main>`, declaration bodies remain unstamped, `<uix.Card/>` keeps aliased import `uix` with zero external loads, and retained LSP unused-import results match `Module.UnusedImports`.

- [ ] **Step 2: Verify red**

```bash
go test ./internal/codegen -run 'Test(TargetDeclaration|RendererDeclaration|BuildPackageSkeletons|PackageParityWithModuleUnusedImports)' -count=1
```

Expected: callers still assume preprocessing produced final stamps.

- [ ] **Step 3: Split caller responsibilities**

`targetDeclarationPackage` and `sourceDeclResolver.packageForDir` run materialization, exact GoWithElements exclusion mapping, and script resolution, then build inert declaration skeletons. They do not discover body targets or stamp components.

`buildPackageSkeletons` uses candidate pointer membership to emit syntactic target references so target qualifiers and prop expressions stay live in the parse-only skeleton. It never calls semantic finalization. An unbuildable file conservatively keeps imports.

- [ ] **Step 4: Remove the LSP's syntactic truth helper**

Replace `stampSyntacticComponents` with analyzed-package fixtures or explicit setup marking only known valid callables. No test helper may encode “capital/dotted means component truth.”

- [ ] **Step 5: Verify and commit**

```bash
go test ./internal/codegen -run 'Test(TargetDeclaration|RendererDeclaration|BuildPackageSkeletons|PackageParityWithModuleUnusedImports)' -count=1
go test ./internal/lsp -run 'Test(Definition|Hover|Diagnostics)' -count=1
gopls check -severity=hint internal/codegen/component_target_importer.go internal/codegen/renderer_decls.go internal/codegen/unused_imports_syntactic.go internal/lsp/definition_test.go
git add internal/codegen/component_target_importer.go internal/codegen/renderer_decls.go internal/codegen/unused_imports_syntactic.go internal/codegen/component_target_importer_test.go internal/codegen/renderer_decls_test.go internal/codegen/unused_imports_syntactic_test.go internal/codegen/unused_imports_lsp_test.go internal/lsp/definition_test.go
git commit -m "refactor(codegen): isolate syntax-only component candidates"
```

Expected: boundary tests pass and `TestBuildPackageSkeletonsNoExternalLoad` remains at zero loads.

---

### Task 5: Pin Canonical Language Semantics

**Files:**
- Create: `internal/corpus/testdata/cases/lowertags/semantic_result_identity.txtar`
- Create: `internal/corpus/testdata/cases/components/reject_explicit_noncomponent.txtar`
- Create: `internal/corpus/testdata/cases/xpkg/reject_explicit_noncomponent.txtar`
- Regenerate: matching corpus goldens and `internal/corpus/testdata/coverage.golden`
- Modify: `docs/guide/syntax/composition.md`

**Interfaces:**
- Produces executable truth for package-main fallback, assignable results, explicit failures, and parameter/identity separation.

- [ ] **Step 1: Write corpus inputs first**

The lowercase fixture must use an importable package such as `package views` and contain `func main() {}`, a `text() string`, and a `card() concreteNode` whose concrete type implements `gsx.Node`. Its render must contain literal `<main><text></text>` plus the component output. The real executable-package shape stays in Task 3's module integration test because the corpus runner imports renderable case packages and Go packages named `main` are not importable.

Same-package and cross-package explicit fixtures pin zero-result, multi-result, wrong-result, unknown, and disallowed provenance diagnostics. Include a Node-result/bad-`attrs` callable and expect `component-attrs-type`, not leaf output.

- [ ] **Step 2: Verify red, regenerate, and verify green**

```bash
go test ./internal/corpus -run TestCorpus -count=1
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
```

Expected: first command fails on new cases; updater and final run pass. Never hand-edit generated `.x.go` or goldens.

- [ ] **Step 3: Update the guide concisely**

Use this rule:

```markdown
A tag is a component only when its target is an allowed function value with
one result assignable to `gsx.Node`. Capitalized and dotted tags claim a
component and report an error when invalid. A lowercase tag without a
component-capable package symbol remains HTML, so `func main()` does not
capture `<main>`.
```

- [ ] **Step 4: Verify and commit**

```bash
go test ./internal/gsxfmt -run TestFmtCorpus -count=1
git diff --check
git add internal/corpus/testdata/cases/lowertags/semantic_result_identity.txtar internal/corpus/testdata/cases/components/reject_explicit_noncomponent.txtar internal/corpus/testdata/cases/xpkg/reject_explicit_noncomponent.txtar internal/corpus/testdata/coverage.golden docs/guide/syntax/composition.md
git commit -m "test: pin semantic component tag identity"
```

Expected: corpus and formatter corpus pass with no hand-edited generated files.

---

### Task 6: Prove Core and Structpages End to End

**Files:**
- Verify/modify: `/Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/examples/simple/pages.gsx`
- Regenerate: `/Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/examples/simple/pages.x.go`
- Verify/modify: `/Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/examples/simple/main_test.go`

**Interfaces:**
- Produces a real HTTP/rendering proof of authored `Page(h History)`, `Content(h History)`, and `Layout(children gsx.Node)` with no generated Props ABI.

- [ ] **Step 1: Run focused core tests and benchmarks**

```bash
go test ./internal/codegen ./internal/lsp ./internal/corpus -count=1
go test ./internal/codegen -run '^$' -bench 'BenchmarkModuleGenerateComponent(Cold|Warm)$' -benchmem -count=5 | tee /tmp/gsx-component-after.txt
```

Expected: tests pass and no new load/command event. Compare `/tmp/gsx-component-after.txt` with `/tmp/gsx-component-before.txt`; stop and report before continuing if the median `ns/op` rises over 10% or `allocs/op` rises over 5% in any case.

- [ ] **Step 2: Regenerate the structpages example**

```bash
GOWORK=/Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/go.work \
go run ./cmd/gsx -C /Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/examples/simple generate -no-cache .
```

Expected: generation succeeds and `<main>` is literal HTML despite `func main()`.

- [ ] **Step 3: Inspect API residue and run HTTP tests**

```bash
rg -n 'Props|Adapter|Wrapper|legacy|deprecated' /Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/examples/simple/pages.x.go
rg -n '^func .*?(Page|Content|Layout)\(' /Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/examples/simple/pages.x.go
GOWORK=/Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures/go.work go test ./examples/simple -count=1
```

Expected: no generated ABI residue; exact signatures and full-page/HTMX tests pass.

- [ ] **Step 4: Commit the structpages proof separately**

```bash
git -C /Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures add examples/simple/pages.gsx examples/simple/pages.x.go examples/simple/main_test.go
git -C /Users/jackieli/personal/structpages/.worktrees/verbatim-component-signatures commit -m "test: prove verbatim gsx signatures end to end"
```

- [ ] **Step 5: Run authoritative core gates**

```bash
make check
make lint
make ci
```

Expected: all exit 0; `make ci` is the uncached authority.

- [ ] **Step 6: Request the required adversarial review**

The independent reviewer must build throwaway probes for package-main HTML, invalid capital/dotted claims, concrete assignable results, valid-result/bad-`attrs`, importer-free `<uix.Card/>`, cross-package parameters, and an opaque/unnameable omitted zero. Address confirmed issues test-first and rerun `make ci`.

---

## Self-Review Record

- **Spec coverage:** final semantic stamp, assignable results, provenance, parameter separation, lowercase fallback, explicit errors, self-exclusion, build context, declaration/importer-free boundaries, LSP, corpus/docs, performance, and structpages are covered.
- **No compatibility debt:** no ABI fallback, flag, dual mode, deprecation, HTML table, or `main` exception appears.
- **No provisional truth:** candidate records exist before checking, but `Element.IsComponent` remains untouched until one finalizer.
- **Type consistency:** every task uses the same `componentResultType`, candidate kinds, target rejection categories, stable `callSiteID`, and final dispositions.
- **Performance:** existing semantic work is reordered and reused; count guards and benchmarks catch regressions.
