# `gsx init` Demo Selector Scope Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent the simple GSX starter's demo CSS from overriding vendored component styles.

**Architecture:** Keep GSX framework-agnostic and replace global demo element selectors with hooks already owned by the starter markup. Pin the generated scaffold contract first, then verify the real GSX → GSXUI → Tailwind → Chromium path.

**Tech Stack:** Go embedded templates and tests, GSX, Vite, Tailwind CSS v4, GSXUI, Chromium.

## Global Constraints

- Work directly on `main` as requested.
- Preserve every pre-existing dirty HTML-attribute change.
- Do not add Tailwind or GSXUI behavior to GSX itself.
- Do not use `!important`, specificity escalation, or approximate CSS rewriting.
- Start no persistent development server; every validation process must be cleaned up.

---

### Task 1: Pin and correct the embedded scaffold

**Files:**

- Modify: `gen/init_test.go`
- Modify: `gen/templates/init/simple/app.gsx`
- Modify: `gen/templates/init/simple/web/style.css`

**Interfaces:**

- Consumes: `initNI(t, "--module", module, dir)` and the embedded `simple` template.
- Produces: a generated `web/style.css` whose demo selectors are `.logos a`, `.app-title`, and `#counter`.

- [ ] **Step 1: Write the failing scaffold test**

Add `TestInitScaffoldScopesDemoStyles` beside the other scaffold contract
tests. Generate the real scaffold, read `web/style.css` and `app.gsx`, assert
literal required selector blocks for `.logos a`, `.app-title`, `#counter`,
`#counter:hover`, and `#counter:focus`, and reject selector lines beginning
with `a`, `h1`, or `button`.

- [ ] **Step 2: Verify the test fails for the global selectors**

Run:

```sh
go test ./gen -run TestInitScaffoldScopesDemoStyles -count=1
```

Expected: FAIL because the generated stylesheet still contains global `a`,
`h1`, and `button` selectors and `app.gsx` has no `.app-title` hook.

- [ ] **Step 3: Scope the demo markup and CSS**

Change the starter heading to:

```gsx
<h1 class="app-title">gsx + Vite</h1>
```

Replace only demo presentation selectors:

```css
.logos a { ... }
.logos a:hover { ... }
.app-title { ... }
#counter { ... }
#counter:hover { ... }
#counter:focus,
#counter:focus-visible { ... }
```

Use `.logos a:hover` and `#counter` again inside the light color-scheme media
query. Leave `:root`, `body`, and named demo classes unchanged.

- [ ] **Step 4: Verify the focused contract**

Run:

```sh
go test ./gen -run 'Test(InitScaffoldScopesDemoStyles|ScaffoldSimpleTemplate)' -count=1
gopls check -severity=hint gen/init_test.go
```

Expected: PASS with no gopls diagnostics.

### Task 2: Prove the cross-repository consumer behavior

**Files:**

- No committed files; use a temporary project outside both repositories.

**Interfaces:**

- Consumes: the local `gsx` command, the `recipe-model` GSXUI command, and the compact Nova preset `gsxui:p1:4GG`.
- Produces: a production Vite bundle and a Chromium computed-style record for a destructive large Button.

- [ ] **Step 1: Build local CLIs and create the consumer**

Build both commands into a temporary tool directory. Run local
`gsx init --yes --module example.com/app`, local
`gsxui init --preset gsxui:p1:4GG`, and `gsxui add button`.

- [ ] **Step 2: Add one Button to the generated app and build**

Import `example.com/app/ui`, render:

```gsx
<ui.Button variant="destructive" size="lg">Click me</ui.Button>
```

Run `go tool gsx generate`, `npm run build`, and `go build ./...`.

- [ ] **Step 3: Assert actual browser cascade output**

Start the built Go server on a temporary loopback port, open it in Chromium,
and assert the `Click me` Button computes:

```text
background-color: oklch(0.577 0.245 27.325)
height: 36px
font-size: 14px
padding-inline: 10px
border-radius: 10px
```

Stop the server and remove the temporary project.

### Task 3: Run the upstream gates

**Files:**

- No additional files.

**Interfaces:**

- Consumes: the completed scaffold change.
- Produces: focused and authoritative verification evidence.

- [ ] **Step 1: Run focused scaffold tests**

```sh
go test ./gen -count=1
```

- [ ] **Step 2: Run the cached repository gate**

```sh
make check
```

- [ ] **Step 3: Run the authoritative repository gate**

```sh
make ci
```

If an unrelated dirty HTML-attribute change fails either gate, record the
exact failure and separately rerun every scaffold-owned test so this change's
status remains explicit.

- [ ] **Step 4: Inspect scope**

```sh
git diff --check
git status --short --branch
```

Confirm only the scaffold, its focused test, and these design/plan documents
belong to this correction; all pre-existing dirty files retain their previous
unstaged state.
