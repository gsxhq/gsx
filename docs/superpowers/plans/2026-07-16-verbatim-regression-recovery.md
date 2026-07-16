# Verbatim Component Regression Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: use
> `superpowers:subagent-driven-development`, with
> `superpowers:test-driven-development` for every behavior change and
> `superpowers:verification-before-completion` before every completion claim.

**Goal:** Restore every semantic behavior lost during the verbatim-signature
cutover, enforce the approved static-name rule for factory-produced callables,
and return the canonical corpus to successful generated-and-rendered end-to-end
coverage without compatibility paths.

**Architecture:** Component signature analysis remains universal and
type-driven. It requires a usable static name for every parameter and assigns
the attrs role only to a parameter literally named `attrs`. Positional planning
records one exact-`gsx.Node` boundary adapter and uses its adapted semantic fact
through inference, assignment checking, final call validation, and emission.
Nested expression-fact aggregation describes which AST nodes actually own
facts. Pipeline stage names use GSX's Go-identifier lexical grammar rather than
Go keyword classification. Corpus migration follows production semantics and
the updater is repaired so one update pass is authoritative.

**Source of truth:**
`docs/superpowers/specs/2026-07-14-verbatim-component-signatures-design.md`.

## Constraints

- No generated Props ABI, attrs-only classifier, initializer chasing,
  attrs-shaped intent inference, deprecation path, compatibility wrapper, or
  fallback heuristic.
- Every parameter, fixed or variadic, must have a non-empty, non-blank static
  name to be used as a component tag.
- `attrs` diagnostics are call-site diagnostics only: emit them for an authored
  fallthrough input when and only when the target has no literal `attrs`
  parameter.
- A differently named attrs-bag parameter is an ordinary exact-name prop.
- Node promotion applies only when the destination is semantically identical to
  canonical `gsx.Node`; a concrete Node implementation remains exact-type.
- Do not add scalar-specific Node wrappers in this recovery. Static/`f` strings
  use Text; other promoted renderable values use the existing Val contract.
- Adapter choice is a planning artifact. Emission consumes it and does not
  rediscover types or syntax.
- Do not add `packages.Load` or any second dependency-analysis path.
- Do not hand-edit generated `.x.go`, `*.golden`, or corpus coverage. Use the
  corpus updater after source/expectation edits.
- Keep commits small and green for their owned focused tests. Each task gets an
  implementation review before integration. Shared-file tasks are sequential.
- Record task commits and verification in `.superpowers/sdd/progress.md`.

## Execution order and concurrency

Tasks 1, 3, and 5 may run in parallel: they own disjoint production files.
Task 2 and Task 4 both touch `component_positional_plan.go` and therefore run
sequentially, with Task 2 first. Task 6 starts only after Tasks 1–5 are reviewed
and integrated. Task 7 is the final adversarial gate.

---

## Task 1: Universal named-parameter rule and factory signatures

**Files:**

- Modify: `internal/codegen/component_signature.go`
- Modify: `internal/codegen/component_signature_test.go`
- Modify: `internal/codegen/component_call_plan.go`
- Modify: `internal/codegen/component_target_importer_test.go`
- Modify: `internal/codegen/component_target_test.go`
- Modify: `internal/codegen/component_lsp_facts_test.go`
- Modify: relevant `internal/lsp/*_test.go` factory definition/hover tests

- [ ] Add RED signature tests proving unnamed and `_` fixed and variadic
  parameters all fail with the general diagnostic:
  `function parameters must be named to be used as a component; parameter N is
  unnamed|blank`.
- [ ] Add RED positive tests proving named ordinary variadics remain valid and
  Go-only, and named non-variadic bag parameters (`a myAttrs`,
  `someAttrs gsx.Attrs`) are ordinary exact-name props.
- [ ] Move the usable-name check ahead of role/variadic classification. Use no
  parameter-type special case in this check. Remove comments and branches that
  permit blank/unnamed variadics.
- [ ] Add a call-planner test proving `<Badge a={{...}}/>` binds `a` normally,
  while `<Badge class="x"/>` emits the existing missing-attrs diagnostic only
  because `class` is unmatched and no literal `attrs` role exists.
- [ ] Add source-backed target tests for:
  `var X = factory()` where the return type is
  `func(name, label string) gsx.Node`; a named func type; an alias; and the
  corresponding unnamed return signatures. Do not inspect the returned closure
  or initializer.
- [ ] Assert static parameter objects retain the return-type/type-declaration
  source positions. Add same-package and imported definition/hover coverage.
  Pin export-only/no-position behavior at the fact layer and keep plain-Go
  parameters outside GSX semantic rename.
- [ ] Verify `ast.Element.IsComponent` still records semantic component identity
  while downstream signature validation owns the positioned parameter error.
- [ ] Run:
  `go test ./internal/codegen -run 'TestAnalyzeComponentSignature|Test.*Factory|Test.*Component.*Param|Test.*MissingAttrs' -count=1`.
- [ ] Run focused LSP definition/hover/rename tests and
  `gopls check -severity=hint` on changed Go files.
- [ ] Commit: `fix(codegen): require named component parameters`.
- [ ] Independent review with an adversarial factory-return probe; fix every
  Critical/Important finding before marking complete.

## Task 2: Restore exact-Node boundary adaptation

**Files:**

- Modify: `internal/codegen/component_zero.go`
- Modify: `internal/codegen/component_positional_plan.go`
- Modify: `internal/codegen/component_positional_emit.go`
- Modify: `internal/codegen/component_positional_plan_test.go`
- Modify: `internal/codegen/component_positional_emit_test.go`
- Modify: related inference/materialization tests as required

- [ ] Add RED planner tests for identity, NodeText, and NodeVal using: static
  string, `f`-literal string, Stringer, int, bool, existing `gsx.Node`, markup,
  `(Stringer, error)`, and `(gsx.Node, error)`.
- [ ] Add RED boundary tests proving aliases of canonical `gsx.Node` promote,
  while a concrete type implementing Node does not receive Text/Val and retains
  ordinary assignment diagnostics.
- [ ] Add a RED generic test where a promoted Node operand participates beside
  another inferred parameter. Inference must see the adapted `gsx.Node` fact,
  not the raw scalar fact.
- [ ] Extend `suppliedOperand` with one semantic adapter enum and enough stable
  value identity for assembly/emission. Decide the adapter once after tuple
  unwrapping and against the authoritative destination parameter.
- [ ] Preserve the raw expression fact for authored-order and materialization,
  but pass the adapted TypeAndValue through authored-operand inference,
  assignment validation, and assembled-call validation.
- [ ] Apply Text/Val after syntax lowering and tuple/error unwrapping. The
  emitter must switch only on the recorded adapter. Static strings and
  `f`-literals use `gsx.Text`; supported other non-Node values use `gsx.Val`;
  markup/already-Node values remain unchanged.
- [ ] Add emission tests proving adapter placement after fallible pipeline/tuple
  lowering, once-only evaluation, and unchanged leaf `f`-literal direct-write
  behavior.
- [ ] Add cross-package and Node-alias coverage without adding package loads.
- [ ] Run focused positional/inference/emission tests, then
  `go test ./internal/codegen -count=1` and `gopls check -severity=hint` on
  changed files.
- [ ] Commit: `fix(codegen): restore Node prop promotion`.
- [ ] Independent reviewer must build throwaway generic, tuple, concrete-Node,
  and cross-package probes before approval.

## Task 3: Pipeline registry names may be Go keywords

**Files:**

- Modify: `parser/pipe.go`
- Modify: `parser/pipe_test.go`
- Reference: `parser/identifier.go`

- [ ] Add RED tests for `default`, Unicode names, dotted names containing a
  keyword segment, malformed UTF-8, digits-first, whitespace, empty segments,
  and punctuation.
- [ ] Replace `token.IsIdentifier` for pipeline stage segments with the exact
  lexical identifier grammar already implemented by `scanGoIdentifier`:
  consume one complete segment and require end-of-string. Keywords are valid
  registry keys here; component declarations continue using Go's keyword-aware
  rule.
- [ ] Keep dotted-stage validation segment-by-segment and preserve existing
  positioned diagnostics.
- [ ] Run `go test ./parser -run 'TestParsePipeStage|TestPipe' -count=1` and
  `gopls check -severity=hint parser/pipe.go parser/pipe_test.go`.
- [ ] Commit: `fix(parser): allow keyword pipeline names`.
- [ ] Independent review for lexical parity and malformed UTF-8.

## Task 4: Correct the nested fact-bearing-node contract

**Files:**

- Modify: `internal/codegen/component_positional_plan.go`
- Modify: `internal/codegen/component_positional_plan_test.go`
- Reference: `internal/codegen/analyze.go` probe collection

- [ ] Add RED tests for class/style parts whose value is control flow and for
  `CSSSegments`. Include sibling component calls so an absent fact cannot be
  hidden by an unrelated diagnostic.
- [ ] Replace the hard-coded assumption that every `ClassPart` owns an operand
  fact with one function describing actual fact-bearing nodes. A plain
  expression ClassPart owns a fact; a control-flow ClassPart delegates to its
  `ValueArm` facts; CSSSegments delegate to their embedded value nodes. Preserve
  ordered-operation detection from the nested facts.
- [ ] Cross-check the contract against both expression probing passes in
  `analyze.go`; do not synthesize a type or mark incomplete facts complete.
- [ ] Run focused positional tests and the two restored class corpus cases after
  Task 6 regenerates them.
- [ ] Commit: `fix(codegen): align nested component operand facts`.
- [ ] Independent review with CF and CSS-literal probes.

## Task 5: Make one corpus update pass authoritative

**Files:**

- Modify: `internal/corpus/corpus_test.go`
- Modify: `internal/corpus/loader.go` only if the ownership model requires it
- Add/modify: focused corpus harness tests

- [ ] Add a RED harness test that starts with a case missing `render.golden`,
  runs the update path once, and asserts the coverage report immediately sees
  the new render facet.
- [ ] Repair the case model so updating a section changes both the archive and
  the in-memory golden-facet source of truth before coverage is computed. Do not
  solve this by running the updater twice or reparsing the entire corpus as a
  hidden second pass.
- [ ] Add coverage for replacement/removal of an existing facet if supported by
  the updater, proving archive and metadata cannot diverge.
- [ ] Run focused harness tests, then one updater pass in a disposable test
  fixture and the normal corpus test without update.
- [ ] Commit: `fix(corpus): update golden facets in one pass`.
- [ ] Independent review of archive/metadata consistency.

## Task 6: Migrate and restore the canonical corpus

**Files:**

- Modify: affected `internal/corpus/testdata/cases/**/*.txtar` inputs
- Regenerate: generated/render/diagnostic sections and
  `internal/corpus/testdata/coverage.golden`
- Modify: migration manifest if its source hashes require reconciliation

- [ ] Re-run the full corpus before editing and save the exact failing-case list.
  Classify it against Node promotion, named factory signatures, intended
  missing-attrs migration, class facts, pipeline names, and genuinely unrelated
  failures. Stop on any new category.
- [ ] Restore Group 1 inputs unchanged wherever possible and regenerate them to
  successful Text/Val/identity calls and render output. Cover all original
  slots, tuple, component, pipeerr, and renderer families rather than approving
  error goldens.
- [ ] Migrate factory return types and named func types so every callable
  parameter is named. Use literal `attrs` only when the case intends
  fallthrough. Preserve differently named bag props as ordinary props and add
  explicit ordinary-binding cases.
- [ ] For `orderedattrs/*`, `urlattrs/bag_*`, and other custom bag names, either
  rename the actual fallthrough role to `attrs` or keep an intentional
  missing-attrs error. Do not revive arbitrary-name bag classification.
- [ ] Restore the class-CF/CSS and `default` pipeline cases to their original
  successful generated/rendered expectations.
- [ ] Regenerate once with
  `go test ./internal/corpus -run TestCorpus -update`, then immediately verify
  `go test ./internal/corpus -run TestCorpus -count=1`. A second updater pass
  must produce a clean diff; needing it is a Task 5 failure.
- [ ] Run a before/after diagnostic inventory and prove the reported regression
  groups are gone while intentional missing-attrs/named-parameter diagnostics
  remain.
- [ ] Reconcile the migration manifest using its generator/validator, never by
  manually changing hashes.
- [ ] Commit coherent corpus migrations in small category commits, each with
  focused corpus verification and review.

## Task 7: End-to-end adversarial and authoritative verification

- [ ] Run focused regression suites, then `go test ./internal/codegen ./parser
  ./internal/corpus ./internal/lsp -count=1`.
- [ ] Run `make check`, `make lint`, and finally `make ci`.
- [ ] Use an independent adversarial reviewer who does more than read the diff.
  Required throwaway programs/corpus probes:
  factory return with named/unnamed anonymous signature; named type and alias;
  ordinary `someAttrs={{...}}`; unmatched attribute without `attrs`; generic and
  tuple Node promotion; concrete Node destination; imported Node alias; class CF
  and CSS segments; keyword and malformed pipeline names.
- [ ] Compare the complete corpus diagnostic inventory with the pre-recovery
  capture. Investigate every added or removed diagnostic outside the approved
  categories.
- [ ] Update `.superpowers/sdd/progress.md` with commits, exact commands, results,
  and any surfaced non-blocking issue. Do not mark complete with deferred
  correctness or technical debt.
- [ ] After the core is green, resume the parent plan's real-consumer migration
  gate (structpages and one-learning) against the exact core commit; no release
  or merge before those consumers pass end to end.
