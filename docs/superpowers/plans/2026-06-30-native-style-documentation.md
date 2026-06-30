# Native Style Documentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Document GSX's native style contribution model and its deferred object-style alternative, with a style-specific `if`/`else` corpus case.

**Architecture:** Keep the language unchanged. Add one corpus case that exercises static and exclusive conditional style contributions, regenerate its generated-code golden and the coverage manifest, then clarify the syntax guide and roadmap using the behavior already shipped in PR 15.

**Tech Stack:** Markdown documentation, GSX corpus `txtar` fixtures, Go test tooling.

---

### Task 1: Add style `if`/`else` parity coverage

**Files:**
- Create: `internal/corpus/testdata/cases/style/value_if_else.txtar`
- Modify: `internal/corpus/testdata/coverage.golden`

- [ ] **Step 1: Add the corpus source and expected render**

Create `internal/corpus/testdata/cases/style/value_if_else.txtar` with:

```txt
-- input.gsx --
package views

component Box(active bool) {
	<div style={ "display: block", if active { "color: green" } else { "color: gray" } }>x</div>
}
-- invoke --
Box(BoxProps{Active: true})
-- render.golden --
<div style="display: block; color: green">x</div>
-- diagnostics.golden --
```

- [ ] **Step 2: Generate and verify the case goldens**

Run:

```bash
GOCACHE=/tmp/gsx-native-style-gocache go test ./internal/corpus -run 'TestCorpus/style/value_if_else' -update
GOCACHE=/tmp/gsx-native-style-gocache go test ./internal/corpus -run 'TestCorpus/style/value_if_else' -count=1
```

Expected: both commands pass. The update command appends
`generated.x.go.golden`, and the generated code assigns `"color: green"` or
`"color: gray"` to a hoisted string selected by a Go `if`.

- [ ] **Step 3: Verify the coverage manifest**

Run:

```bash
rg -n 'style/value_(if_else|switch)|TOTAL:' internal/corpus/testdata/coverage.golden
```

Expected output includes:

```text
style/value_if_else	diag gen render
style/value_switch	diag gen render
TOTAL: 337 cases (render: 256, error: 60, gen-pinned: 133)
```

- [ ] **Step 4: Commit the corpus coverage**

```bash
git add internal/corpus/testdata/cases/style/value_if_else.txtar internal/corpus/testdata/coverage.golden
git commit -m "test(corpus): cover style value if else"
```

### Task 2: Clarify native style syntax and the deferred object form

**Files:**
- Modify: `docs/guide/syntax.md`
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Expand the native style explanation**

In `docs/guide/syntax.md`, immediately after the introductory composable
`class`/`style` example, add:

```markdown
For `style`, each part is a complete CSS declaration. Static declarations,
dynamic declarations, and independent guards can be mixed:

```gsx
style={
    "display: block",
    "color: " + color,
    "opacity: 0": hidden,
}
```

Parts evaluate strictly from left to right. Dynamic parts pass through GSX's
CSS value safety filter; use `gsx.RawCSS` only for trusted CSS that deliberately
bypasses that filter.
```

- [ ] **Step 2: Add a style-specific `if` example**

In the `if / else if / else` subsection of `docs/guide/syntax.md`, after the
class example, add:

```markdown
For styles, each selected arm still produces one complete declaration:

```gsx
style={
    "display: block",
    if active { "color: green" } else { "color: gray" },
}
```
```

- [ ] **Step 3: Document why object-style declarations are deferred**

Before the existing `Out of scope` table in `docs/guide/syntax.md`, add:

```markdown
### Why not `style={{ "color": color }}`?

GSX currently has one inline-style model: ordered declaration contributions.
An object-like property/value form would reduce string composition for heavily
dynamic inline styles, but it would also introduce a second way to express the
same output, with additional grammar, formatting, code generation, and
documentation surface. Current project usage has not shown enough repeated
dynamic declaration construction to justify that cost.

The form is deferred rather than rejected. It can be reconsidered if real
projects commonly build many dynamic declarations and the native contribution
syntax becomes a material usability problem. A future design should prefer
quoted native CSS names such as `"font-size"` and `"--accent"`; it should not
adopt JSX camelCase conversion or automatic numeric units.
```

- [ ] **Step 4: Add the roadmap adoption criterion**

In `docs/ROADMAP.md`, after Done item 11, add:

```markdown
12. `[ ]` **Ordered style property bags (deferred)** — consider
    `style={{ "color": color, "font-size": size }}` only if real-world GSX
    projects repeatedly construct many dynamic declarations and declaration
    string composition becomes a material usability problem. The feature would
    add a second inline-style model plus parser, formatter, codegen, and
    documentation surface, so current usage does not justify it. If adopted,
    prefer quoted native CSS property names; do not add JSX camelCase conversion
    or automatic numeric units.
```

- [ ] **Step 5: Check documentation formatting**

Run:

```bash
git diff --check -- docs/guide/syntax.md docs/ROADMAP.md
```

Expected: no output.

- [ ] **Step 6: Commit the documentation**

```bash
git add docs/guide/syntax.md docs/ROADMAP.md
git commit -m "docs: clarify native style contributions"
```

### Task 3: Verify the complete change

**Files:**
- Verify: `docs/guide/syntax.md`
- Verify: `docs/ROADMAP.md`
- Verify: `internal/corpus/testdata/cases/style/value_if_else.txtar`
- Verify: `internal/corpus/testdata/coverage.golden`

- [ ] **Step 1: Run focused package tests**

Run:

```bash
GOCACHE=/tmp/gsx-native-style-gocache go test ./internal/corpus -count=1
```

Expected: PASS.

- [ ] **Step 2: Run repository verification**

Run:

```bash
GOCACHE=/tmp/gsx-native-style-gocache go test ./... -count=1
git diff --check
```

Expected: all Go tests pass and `git diff --check` prints no output.

- [ ] **Step 3: Confirm PR commit scope**

Run:

```bash
git status --short
git log --oneline origin/pr/15..HEAD
```

Expected: the worktree is clean, and the commits above PR 15 are limited to the
design spec, this implementation plan, the style corpus case, and documentation.

- [ ] **Step 4: Push the verified commits to PR 15**

```bash
git push origin HEAD:worktree-style-control-flow
```

Expected: GitHub updates PR 15 to the verified HEAD.
