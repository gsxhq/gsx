# README and Status Current-State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite the README and status guide as a concise, accurate description
of the current standalone gsx product.

**Architecture:** `README.md` remains the short repository front door;
`docs/guide/status.md` owns maturity, shipped surfaces, and durable boundaries.
Detailed syntax gaps stay in the roadmap instead of being duplicated in the
status guide.

**Tech Stack:** Markdown, gsx canonical corpus, repository Make targets.

## Global Constraints

- Keep the public maturity label `alpha`.
- State that gsx is already used in production.
- Explain that alpha means compatibility may change before 1.0.
- Remove every reference to the templ project from `README.md`; `templating`
  remains valid vocabulary.
- Keep the README concise and retain its runnable example and navigation.
- Do not modify interop, comparisons, performance, roadmap, or syntax guides.
- Do not publish machine-specific performance figures as product status.

---

### Task 1: Present gsx independently in the README

**Files:**
- Modify: `README.md:3-30`

**Interfaces:**
- Consumes: the maturity wording and shipped-surface evidence in
  `docs/superpowers/specs/2026-07-29-readme-status-current-state-design.md`.
- Produces: the repository's concise product introduction and a status link to
  `docs/guide/status.md`.

- [ ] **Step 1: Replace the opening product description**

Use this product identity:

```markdown
A JSX-like templating language for Go, compiled to plain Go that streams HTML.
```

The opening must describe gsx directly and must not qualify its syntax as
templ-style.

- [ ] **Step 2: Correct the alpha notice**

State that gsx is used in production today while language and API compatibility
may change before 1.0. Keep links to the status guide and roadmap.

- [ ] **Step 3: Refresh the product bullets**

Keep four concise points:

1. exact authored Go signatures and Go-checked calls;
2. HTML-shaped markup with ordinary Go;
3. contextual HTML/URL/CSS/JavaScript escaping and a standard-library-only
   runtime;
4. Go-native generation/build tooling, with Node.js only when the application
   chooses frontend tooling such as Vite.

Do not add a competitor, interop, benchmark, or framework comparison.

- [ ] **Step 4: Verify the README contract**

Run:

```bash
rg -ni '\btempl\b' README.md
git diff --check -- README.md
```

Expected: `rg` prints nothing and `git diff --check` exits successfully. Read
the rendered Markdown structure to confirm the existing example and navigation
remain intact.

- [ ] **Step 5: Commit the README update**

```bash
git add README.md
git commit -m "docs: present gsx independently in README"
```

### Task 2: Rewrite status around shipped surfaces and durable boundaries

**Files:**
- Modify: `docs/guide/status.md:1-32`

**Interfaces:**
- Consumes: the README maturity statement from Task 1 and current CLI/LSP/corpus
  evidence.
- Produces: the canonical concise public status page linked by README and the
  guide.

- [ ] **Step 1: Write the maturity section**

State:

- gsx is alpha and used in production;
- the compiler, runtime, and toolchain work end to end;
- language and API compatibility are not guaranteed before 1.0.

Do not imply that alpha means prototype-only or production-ineligible.

- [ ] **Step 2: Replace the shipped-surface inventory**

Use three short subsections:

- `Language and rendering`: typed authored signatures, components, children,
  slots, control flow, pipelines, attribute forwarding, contextual escaping,
  and explicit trust boundaries.
- `Toolchain`: init, generate, format, info, and dev loop; Vite is an optional
  front door.
- `Editor support`: diagnostics, navigation, references, symbols, formatting,
  code actions, completion, completion documentation, and auto-imports.

Link the relevant existing guide pages instead of expanding each feature.

- [ ] **Step 3: Replace the drifting limits list**

Use durable boundaries only:

- generation produces `.x.go` files that are compiled with the application;
- find-references is project-scoped and excludes external package references;
- pre-1.0 releases may require source migrations.

Remove the `140µs` completion figure, obsolete component-style statement, and
raw `[]string` class-part item. Link the roadmap for changing engineering
details.

- [ ] **Step 4: Verify every claim against current sources**

Check:

```bash
rg -n '"(generate|fmt|info|init|dev|lsp)"|case "(generate|fmt|info|init|dev|lsp)"' cmd gen
rg -n 'textDocument/(definition|references|hover|formatting|completion|codeAction)|workspace/symbol|documentSymbol' internal/lsp
rg -n 'component_composable_class|class_style_targets' internal/corpus/testdata/coverage.golden
git diff --check -- docs/guide/status.md
```

Expected: each shipped surface has a current implementation or canonical corpus
entry, and the Markdown diff has no whitespace errors.

- [ ] **Step 5: Commit the status update**

```bash
git add docs/guide/status.md
git commit -m "docs: describe current alpha production status"
```

### Task 3: Run repository verification and review the final public copy

**Files:**
- Verify: `README.md`
- Verify: `docs/guide/status.md`

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: a verified documentation-only branch ready for review.

- [ ] **Step 1: Run focused documentation checks**

```bash
rg -ni '\btempl\b' README.md
rg -n '140µs|not on component invocations|\[\]string' docs/guide/status.md
git diff --check main...HEAD
```

Expected: both searches print nothing and the diff check succeeds.

- [ ] **Step 2: Run the project gate**

```bash
make check
```

Expected: every Go/module/example/format/lint check passes.

- [ ] **Step 3: Review the complete branch diff**

```bash
git diff --stat main...HEAD
git diff --word-diff=plain main...HEAD -- README.md docs/guide/status.md
git status --short --branch
```

Expected: the public-doc diff is concise, every status claim is evidence-backed,
and the worktree is clean.
