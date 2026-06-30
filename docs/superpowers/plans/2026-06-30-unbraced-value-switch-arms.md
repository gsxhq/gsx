# Unbraced Value-Switch Arms Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace braced value-form `switch` case values with the same unbraced case-body shape used by markup switches.

**Architecture:** Keep value-form `if` unchanged because its braces delimit its branches. For value-form `switch`, scan each case value until the next top-level `case`, `default`, or switch-closing `}`, respecting nested Go delimiters; store the same `ast.ValueArm` and leave codegen unchanged. Make the printer emit only the new canonical form and reject legacy braced case values with a migration diagnostic.

**Tech Stack:** Go parser/scanner, GSX AST/printer, txtar corpus, Markdown docs, tree-sitter grammar, VS Code TextMate grammar.

---

### Task 1: Parse unbraced switch case values

**Files:**
- Modify: `parser/boundary.go`
- Modify: `parser/valueform.go`
- Test: `parser/valueform_test.go`

- [ ] Add parser tests proving multiline expressions, pipelines, composite literals, nested delimiters, multi-value cases, and tagless switches stop at the correct top-level boundary; prove `{ "old" }` receives a migration diagnostic.
- [ ] Run `go test ./parser -run ValueSwitch -count=1` and confirm the new tests fail for the missing syntax.
- [ ] Add a scanner-based `valueSwitchArmEnd` boundary helper and parse the unbraced source through `parsePipe`, preserving accurate spans.
- [ ] Run the focused parser tests and `go test ./parser -count=1`.

### Task 2: Print and migrate the canonical syntax

**Files:**
- Modify: `internal/printer/printer.go`
- Modify: `internal/printer/printer_test.go`
- Modify: `internal/corpus/testdata/cases/class/*.txtar`
- Modify: `internal/corpus/testdata/cases/style/*.txtar`
- Modify: `internal/corpus/testdata/cases/components/child_value_switch.txtar`
- Modify: `internal/corpus/testdata/coverage.golden`

- [ ] Add printer expectations for `case A:\n\t"value"` and `default:\n\t"value"` without arm braces.
- [ ] Run focused printer tests and confirm they fail.
- [ ] Remove switch-arm brace emission while retaining value-form `if` branch braces.
- [ ] Mechanically migrate corpus inputs, regenerate generated/render/diagnostic goldens with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`.

### Task 3: Update documentation

**Files:**
- Modify: `docs/guide/syntax.md`
- Modify: `docs/guide/syntax/styling.md`
- Modify: `docs/ROADMAP.md`
- Modify: `docs/superpowers/specs/2026-06-29-style-control-flow-design.md`

- [ ] Replace every value-switch example with unbraced case values and state that switch cases follow markup-switch case-body syntax while value-form `if` retains branch braces.
- [ ] Remove claims that all value-form arms are brace-delimited.
- [ ] Run `git diff --check` and search for remaining `case ...: {` value-form examples.

### Task 4: Update downstream syntax tooling

**Files:**
- Modify in `../tree-sitter-gsx`: grammar and corpus fixtures for value-form switch arms.
- Modify in `../vscode-gsx`: TextMate grammar and syntax fixtures for value-form switch arms.

- [ ] Change tree-sitter value-switch arms to consume an unbraced Go expression until the next case boundary, regenerate the parser, and run its test suite.
- [ ] Update VS Code scopes/fixtures and run its available test/build commands.

### Task 5: Verify and publish

**Files:**
- Verify all changed files.

- [ ] Run `make ci`.
- [ ] Run `git diff --check` and confirm a clean worktree after commits.
- [ ] Push the feature branch and open a ready PR with the breaking syntax and migration diagnostic called out.
