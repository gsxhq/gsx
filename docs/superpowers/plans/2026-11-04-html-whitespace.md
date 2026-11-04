# HTML Whitespace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Replace JSX whitespace processing with the approved HTML literal-content contract, without modifying consumer repositories.

**Architecture:** The parser owns trimming only at GSX syntactic body edges. HTML content stays literal through codegen and printing. Remove JSX-only spacing analysis rather than retaining a no-op normalization layer; the Node-only child contract retains literal whitespace as content.

**Tech Stack:** Go 1.27.1, standard-library runtime, existing parser/pretty engine and txtar corpora.

## Task 1: Parser and generation semantics

Files: parser/component.go, parser/markup.go, parser/blockbody_ws_test.go,
internal/wsnorm, internal/codegen/module_importer.go, internal/codegen/emit.go,
internal/codegen/component_call_plan.go, internal/codegen/barecomment.go,
internal/corpus/testdata/cases, and callers of normalization/tag predicates.

- [x] Pin PATH Go to 1.27.1; use the same executable for all checks.
- [x] Add regression fixtures demonstrating exact literal boundaries and the
  trim-only-at-body-edges rule. Concrete expectations: `<p>A\n<b>B</b></p>`
  retains LF; `<p>{ if ok { Yes } }!</p>` emits `Yes!`; a repeated loop body
  adds no implicit iteration separator; `{ " " }` still emits explicit space.
- [x] Parse component and slot bodies with preserved interior text, reusing a
  clearly named syntactic-edge trim implementation; do not call skipSpace
  between child nodes. Preserve spans consistently when trimming.
- [x] Remove generation's normalization calls; replace the old package with
  accurately named shared predicates only where still needed. No no-op APIs.
- [x] Confirm the Node-only child contract and preserve scalar/variadic Node
  payloads, including whitespace. Cover each path in existing fixtures.
- [x] Refactor bare-comment validation to ignore whitespace-only neighbours.
- [x] Run affected parser and child-binding tests, then regenerate and verify
  changed corpus cases. Review expected output differences before accepting.
- [x] Spec review, then quality review; fix findings before the next task.

## Task 2: Literal-content formatter

Files: internal/printer/printer.go, segment.go, inline.go, blockform.go,
breakwide.go, goparts.go, internal/gsxfmt/gsxfmt.go, associated tests and corpus.

- [x] Add cases for exact text, touching elements, multiline tags, fragments,
  control flow, embedded literals, comments and source preserving LF/tabs.
- [x] Replace child segmentation/prose-fill/spacing-bond machinery with a
  literal child-list printer; `pretty.Text(t.Value)` must preserve Text data.
  Compose children with `pretty.Concat`, never a rendered line separator.
- [x] Keep indentation/layout changes inside GSX syntax or tag syntax. Remove
  JSX-only helpers/tests and update tests that encoded intentional old behavior.
- [x] Preserve literal content inside embedded Go markup when gofmt reindents
  surrounding code; use the existing structural hole mechanism, extending it
  where needed rather than reconstructing children as flattened Go strings.
- [x] Replace normalized-AST formatter oracle with exact emitted-text facts.
  Retain existing property, width and idempotence coverage under new semantics.
- [x] Regenerate fmt corpus through the test update flag, then verify it.
- [x] Spec review, then quality review; fix findings before the next task.

## Task 3: Retire lint and integrate

Files: internal/codegen/childspacing*, internal/diag/lint*, internal/lsp/codeaction*,
gen lint/cache/diagnostic fixtures, docs/guide, docs/ROADMAP.md, CHANGELOG.md,
examples and generated example Go.

- [x] Remove child-element-spacing analysis, registration and quick fixes.
- [x] Add an actionable error for retired configuration: remove that lint entry
  because GSX now preserves HTML whitespace. Update warning-dependent test
  fixtures to use a live diagnostic, retaining the original behavior tested.
- [x] Document literal HTML content, syntactic body trimming, deliberate
  adjacency, explicit loop separators and the narrower formatter contract.
- [x] Mark the approved spec implemented only after validation completes.
- [x] Review sibling syntax integrations: no new grammar token is needed;
  update relevant whitespace documentation/tests if necessary without editing
  EOS, one-learning, or gsxui.
- [x] Regenerate semantic corpus coverage and example output using generators,
  verify the corpus and formatter, and run meaningful affected test clusters.
- [x] Run an independent adversarial review with throwaway render/format probes;
  resolve findings, then run make lint and one final make ci, capturing exits.
- [x] Report final behavior, validation, migration implications and remaining
  limitations. Leave the worktree available for review; do not merge.

## Review and validation evidence

Parser/generation, formatter, and integration tasks passed separate spec and
quality reviews. Independent throwaway programs exercised CLI generation,
rendering, actual formatter rewrites, idempotence, and retired configuration
errors. Literal CRLF, tabs, NBSP, vertical tabs, and syntactic formfeed edges
were checked independently.

Go 1.27.1 and golangci-lint 2.13.2 match CI. Lint and the VitePress build
(Node 24, temporary docs-site checkout against this source) pass. The first
full CI run found three old test reference expectations; exact references
were updated without production changes and reviewed. The final uncached
`make ci` rerun passed, including both Go modules, vet, tests, format checks,
example drift and playground type-bundle parity.

No new grammar tokens were introduced. The docs-site integration builds; local
tree-sitter and VS Code sibling checkouts were unavailable. EOS, one-learning,
and gsxui were not modified.
