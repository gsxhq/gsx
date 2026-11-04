# HTML Whitespace Review Corrections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development task-by-task, with separate spec and quality reviews.

**Goal:** Correct PR 274's reviewed regressions while retaining literal HTML content semantics.

**Architecture:** Treat whitespace-only variadic argument separators as GSX syntax at resolved binding, not as a global text-normalization pass. Preserve layout around multiline literal islands, isolate opening-tag width decisions, and explicitly migrate example source when preferred layout changes literal output.

**Tech Stack:** Go 1.27.1, existing AST/printer/pretty engine and canonical txtar corpus; standard-library runtime unchanged.

## Task 1: Variadic argument binding

- [x] In component positional planning, omit only whitespace-only literal Text
  from resolved variadic children arguments. Preserve AST and scalar content.
  Use exact HTML ASCII whitespace, retaining explicit expressions and Unicode
  text. Do not add tag, CSS, or punctuation heuristics.
- [x] Extend existing planner tests and corpus with a component wrapping each
  argument in li: multiline two-element input produces exactly two li, whereas
  explicit whitespace expressions still produce an argument. Cover empty and
  whitespace-only bodies, mixed substantive text, NBSP/VT, source comments,
  fragments/control flow, scalar children and slots.
- [x] Regenerate affected goldens and verify narrow tests. Spec review, then
  quality review before proceeding.

## Task 2: Source-aware syntax layout

- [x] Refactor body-edge and embedded-Go layout to preserve placement around
  literal multiline islands. Add only source layout information needed by this
  contract; do not resurrect JSX normalization or alter literal Text bytes.
- [x] Make opening-tag width decisions local to opening syntax. Keep generic
  Go group measurement intact and preserve tag-attribute wrapping when needed.
- [x] Add readable expected-output tests in addition to exact text/idempotence
  tests for if/case/slot bodies, source comments, component roots, Go composite
  operands and nested markup. Review each changed formatter fixture.
- [x] Restore nested tab-width fixtures without dropping the original depth
  regression; use a separate fixture for an independent threshold if necessary.
- [x] Regenerate formatter goldens through the test update flag; verify all
  affected cheap packages and corpus faithfulness at existing widths. Spec and
  quality review before proceeding.

## Task 3: Corpus and example intent

- [x] Correct stale empty-children/comment-deletion/prose-reflow descriptions
  and misleading fixture names. Keep intentional raw whitespace cases explicit.
- [x] Migrate examples/231-conditional-attrs.txtar and 233-ordered-attrs.txtar
  source to readable multiline HTML layout. Regenerate exact rendered goldens
  and generated docs/presets; never hand-edit generated output.
- [x] Document variadic separator semantics and formatter placement guarantees.
- [x] Make semantic/example render golden checks byte-sensitive. Define an
  explicit archive framing newline so txtar cannot conflate output ending in
  LF with output lacking LF; test leading, internal and trailing whitespace.
  Structural HTML comparison may enrich failures but must not decide equality.
- [x] Compare new diffs against the 283-case audit ledger; individually inspect
  each newly changed corpus case and preserve its original test purpose.
- [x] Independent adversarial probes for list cardinality and formatter output,
  then make lint, docs build and final uncached make ci. Fix failures narrowly.
- [x] Push reviewed commits to PR 274. Keep consumer application repositories
  unchanged and do not merge.

## Review evidence

- Initial audit read all 283 changed corpus cases individually (252 semantic,
  31 formatter). Corrections retain nested-width coverage and literal HTML
  output; every subsequently changed formatter fixture was inspected.
- Variadic binding, source-aligned formatting and exact render comparison each
  passed separate spec and quality review. Final independent throwaway probes
  rebuilt the CLI and exercised actual `fmt -w`, exact rendered CRLF/Unicode
  bytes, idempotence, list cardinality and current-AST mutations.
- `make lint` passed with Go 1.27.1 and golangci-lint 2.13.2. Generated examples
  have no drift; the VitePress build passed with Node 24.
- Final uncached `make ci` passed on Go 1.27.1 (exit 0), including both
  modules, vet, tests, formatting, examples and type-bundle parity. Reviewed
  corrections and the updated description are delivered in
  [PR 274](https://github.com/gsxhq/gsx/pull/274).
