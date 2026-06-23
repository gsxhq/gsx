# Parser Error Recovery (Slice 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give gsx parser errors structured `token.Pos` positions (so they render `index.gsx:21:4: error[syntax]: …`) and recover at the component boundary so a file reports one syntax diagnostic per broken component instead of stopping at the first.

**Architecture:** Add an error accumulator (`[]parser.Error{Pos, Msg}`) + an `errorf` helper to the `parser` struct; migrate the ~50 fail-fast `return nil, fmt.Errorf("%d:%d: msg")` sites to `return nil, p.errorf(pos, "msg")` (a pure internal refactor — public API unchanged in Task 1). Then expose the structured list via `ParseFileWithClassifier` and add a component-boundary resync loop with a forward-progress guarantee; the codegen `.gsx` callers convert each `Error` into a positioned `Source:"parser"` diagnostic in the per-package `diag.Bag`. `ParseFile` keeps its single-`error` contract for the Go-fragment sub-parsers and tests.

**Tech Stack:** Go; `go/token` (`token.Pos`/`token.Position`/`FileSet`); existing `parser`, `internal/codegen`, `internal/diag`, `internal/corpus` packages.

## Global Constraints

- **Slice 2 of 2.** In scope: (A) structured parser positions, (B) **component-boundary** recovery. OUT: intra-component recovery (multiple errors *within* one component); reporting `go/types` errors for the still-valid components of a syntax-broken package.
- **No behavior change in Task 1** — migrating error sites to structured form must keep `ParseFile`/`ParseFileWithClassifier` returning the SAME `(*ast.File, error)` with byte-identical `line:col: message` text, so the whole suite stays green (the refactor is invisible until Task 2 exposes the list).
- **Forward-progress guarantee** in recovery: the resync cursor is always `>= off + len("component")` (strictly past the failed component's keyword), so `nextTopLevelComponent` cannot re-match the same component → no infinite loop.
- **Resolve/emit gate:** a package with ANY parser-error diagnostic reports all parser diagnostics and skips type-resolution + emit (writes no `.x.go`) — same all-or-nothing stance as Slice 1.
- **Minimal blast radius:** only `ParseFileWithClassifier` changes signature (`→ (*ast.File, []Error)`); `ParseFile` keeps `(*ast.File, error)`.
- **Module:** `github.com/gsxhq/gsx`. **Run tests:** `go test ./...` from the worktree root.
- **Worktree:** all work in `.claude/worktrees/parser-recovery` on branch `worktree-parser-recovery` (CLI dev work is isolated in a worktree per the user's standing rule).

---

## File Structure

- **Modify** `parser/parser.go` — add `Error` type, `errs []Error` field, `errParse` sentinel, `errorf` helper.
- **Modify** `parser/component.go`, `parser/attrs.go`, `parser/markup.go`, `parser/pipe.go` — migrate ~50 error sites from `fmt.Errorf("%d:%d: …")` to `p.errorf(pos, "…")`.
- **Modify** `parser/file.go` — `scanPackage` error → one `Error`; `ParseFileWithClassifier` returns `(*ast.File, []Error)` + the recovery loop; `ParseFile` wraps to `error`.
- **Modify** `internal/codegen/batch.go`, `internal/codegen/codegen.go` — consume `[]Error`, convert to positioned `Source:"parser"` diagnostics, gate resolve/emit.
- **Modify** `parser/*_test.go` (the few that call `ParseFileWithClassifier` directly) — adapt to `[]Error`.
- **Create/Modify** `internal/corpus/testdata/cases/parser/*.txtar` — rebaseline positionless cases; add multi-component recovery cases.
- **Modify** `docs/ROADMAP.md`.

---

## Task 1: Structured parser errors (pure internal refactor)

**Files:**
- Modify: `parser/parser.go` (add `Error`, `errs`, `errParse`, `errorf`)
- Modify: `parser/component.go`, `parser/attrs.go`, `parser/markup.go`, `parser/pipe.go`, `parser/file.go` (migrate sites)
- Test: `parser/recovery_test.go` (new)

**Interfaces:**
- Consumes: stdlib (`go/token`, `fmt`, `errors`).
- Produces:
  - `type Error struct { Pos token.Pos; Msg string }` (exported).
  - `var errParse = errors.New("parse error")` (unexported sentinel).
  - `func (p *parser) errorf(pos token.Pos, format string, args ...any) error` — appends `Error{pos, fmt.Sprintf(...)}` to `p.errs`, returns `errParse`.
  - `parser.errs []Error` field (unexported).
  - **Unchanged public API:** `ParseFile`/`ParseFileWithClassifier` still return `(*ast.File, error)`; the returned `error` is formatted `"<line>:<col>: <msg>"` from the FIRST accumulated `Error` (so existing golden text is byte-identical).

- [ ] **Step 1: Write the failing test**

Create `parser/recovery_test.go`:

```go
package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
)

// errorf accumulates a positioned Error and the public ParseFile still returns
// the SAME "line:col: message" text (back-compat for Task 1's pure refactor).
func TestParseFileBackCompatErrorText(t *testing.T) {
	src := "package p\n\ncomponent X() { <div>hi</span> }\n"
	_, err := ParseFile(token.NewFileSet(), "c.gsx", []byte(src), 0)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), "mismatched close tag </span>, expected </div>") {
		t.Errorf("message text changed: %q", err.Error())
	}
	// Position prefix preserved in the formatted error.
	if !strings.HasPrefix(err.Error(), "3:") {
		t.Errorf("expected a 3:<col>: position prefix, got %q", err.Error())
	}
}

// The parser accumulates a structured, positioned Error (in-package test can see errs).
func TestErrorfAccumulatesPositioned(t *testing.T) {
	fset := token.NewFileSet()
	// Drive ParseFileWithClassifier; in Task 1 it still returns (*ast.File, error),
	// but the parser's errs must hold a positioned Error. Re-parse via a small helper
	// that exposes errs: parse and then assert through the formatted error's position.
	_, err := ParseFileWithClassifier(fset, "c.gsx", []byte("package p\n\ncomponent X() { <div>hi</span> }\n"), 0, attrclass.Builtin())
	if err == nil || !strings.Contains(err.Error(), "3:") {
		t.Fatalf("expected positioned error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser/ -run 'TestParseFileBackCompat|TestErrorfAccumulates'`
Expected: compiles and runs, but FAILS only if you regress text; if it already passes pre-change that's fine — the REAL gate for Task 1 is that the FULL suite stays green after the refactor (Step 5). (These tests lock the back-compat contract you must not break.)

- [ ] **Step 3: Add the accumulator + helper (`parser/parser.go`)**

```go
import (
	"errors"
	"fmt"
	"go/token"
)

// Error is a positioned parser diagnostic. Pos resolves to file:line:col via the
// FileSet the parser was created with. Exported so codegen can convert it to a
// diag.Diagnostic.
type Error struct {
	Pos token.Pos
	Msg string
}

// errParse is the sentinel returned by errorf so existing `return nil, <err>`
// control flow unwinds unchanged; the positioned detail lives in p.errs.
var errParse = errors.New("parse error")

type parser struct {
	file       *token.File
	src        string
	base       int
	i          int
	classifier *attrclass.Classifier
	errs       []Error
}

// errorf records a positioned error and returns the errParse sentinel.
func (p *parser) errorf(pos token.Pos, format string, args ...any) error {
	p.errs = append(p.errs, Error{Pos: pos, Msg: fmt.Sprintf(format, args...)})
	return errParse
}
```

- [ ] **Step 4: Migrate every parser error site**

Across `parser/component.go`, `parser/attrs.go`, `parser/markup.go`, `parser/pipe.go`, convert each `return nil, fmt.Errorf(...)` (and `return false, …`/`return "", …` variants) to `p.errorf`. **Pattern:**

```go
// BEFORE (positioned site, e.g. markup.go parseChildren mismatched close):
mmPos := p.file.Position(p.pos())
// …
return nil, fmt.Errorf("%d:%d: mismatched close tag </%s>, expected </%s>", mmPos.Line, mmPos.Column, got, closeTag)
// AFTER (pass the token.Pos; drop the %d:%d: prefix; message text otherwise identical):
mmTokPos := p.pos()   // capture the token.Pos at the same cursor the mmPos came from
// …
return nil, p.errorf(mmTokPos, "mismatched close tag </%s>, expected </%s>", got, closeTag)
```

```go
// BEFORE (positionless site, e.g. attrs.go unterminated attribute string):
return nil, fmt.Errorf("unterminated attribute string for %q", name)
// AFTER (best-effort cursor position):
return nil, p.errorf(p.pos(), "unterminated attribute string for %q", name)
```

Apply to ALL sites enumerated in the design's §3 / the parser exploration: `component.go` (5 sites), `attrs.go` (~9), `markup.go` (~25 across parseElement/parseChildren/parseBang/parseRawTextBody/parseInterp/parseGoBlock/control-flow/case/parseMarkupUntilClose), `pipe.go` (~5). For each site, the `pos` argument is the `token.Pos` the site already used to compute its `line:col` (use `p.pos()` / `p.posAt(start)` / the captured start position). Where a site currently resolves `cp := p.file.Position(somePos)` only to format `%d:%d:`, pass `somePos` (the pre-resolution `token.Pos`) to `errorf` and delete the now-unused `cp`.

> The compiler is your checklist: after adding `errorf`, grep `parser/` for `fmt.Errorf(` and convert each (except `file.go`'s `scanPackage` + the `invalid src type` guard, handled in Task 2). Keep message wording IDENTICAL minus the `%d:%d: ` prefix.

- [ ] **Step 5: Keep the public API returning `(*ast.File, error)` (back-compat)**

In `parser/file.go`, `ParseFileWithClassifier` still returns `(*ast.File, error)` in Task 1. At each `return nil, err` from `p.parseComponent()` and any propagated `errParse`, convert `p.errs[0]` into the formatted error before returning:

```go
// helper in file.go:
func (p *parser) firstErr() error {
	if len(p.errs) == 0 {
		return nil
	}
	e := p.errs[0]
	pos := p.file.Position(e.Pos)
	if pos.IsValid() {
		return fmt.Errorf("%d:%d: %s", pos.Line, pos.Column, e.Msg)
	}
	return fmt.Errorf("%s", e.Msg)
}
```

At the top-level loop's `c, err := p.parseComponent(); if err != nil { return nil, p.firstErr() }`. This reproduces today's exact `line:col: message` output.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: ALL green — the refactor is invisible (same error text). The two new tests pass.

- [ ] **Step 7: Commit**

```bash
git add parser/
git commit -m "refactor(parser): structured positioned Error accumulator (errorf); public API unchanged"
```

---

## Task 2: Expose `[]Error`, component-boundary recovery, positioned diagnostics

**Files:**
- Modify: `parser/file.go` (`ParseFileWithClassifier` → `(*ast.File, []Error)`; recovery loop; `scanPackage` error → one `Error`; `ParseFile` wraps)
- Modify: `internal/codegen/batch.go:92`, `internal/codegen/codegen.go:66` (consume `[]Error` → positioned diagnostics; gate)
- Modify: `parser/*_test.go` calling `ParseFileWithClassifier` directly (e.g. `parser/attrclass_test.go`)
- Test: `parser/recovery_test.go` (extend)

**Interfaces:**
- Consumes: `Error`/`errorf`/`errs` (Task 1); `diag.Bag.Report` (from Slice 1: `Report(pos, end token.Pos, sev diag.Severity, code, source, format string, args ...any)`).
- Produces:
  - `func ParseFileWithClassifier(fset *token.FileSet, filename string, src any, mode Mode, cls *attrclass.Classifier) (*ast.File, []Error)`.
  - `func ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (*ast.File, error)` — unchanged signature; wraps via `firstErr`-style formatting of the returned `[]Error`.

- [ ] **Step 1: Write the failing test**

Extend `parser/recovery_test.go`:

```go
func TestComponentBoundaryRecovery(t *testing.T) {
	// Two broken components: each has a mismatched close tag. Recovery must
	// report BOTH (one per component), not just the first.
	src := "package p\n\n" +
		"component A() { <div>hi</span> }\n\n" +
		"component B() { <p>yo</b> }\n"
	_, errs := ParseFileWithClassifier(token.NewFileSet(), "c.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) != 2 {
		t.Fatalf("expected 2 recovered errors, got %d: %+v", len(errs), errs)
	}
}

func TestRecoveryKeepsValidComponents(t *testing.T) {
	// A broken component followed by a valid one: the valid one must still be
	// in the returned AST, and exactly one error reported.
	src := "package p\n\n" +
		"component Bad() { <div>hi</span> }\n\n" +
		"component Good() { <p>ok</p> }\n"
	f, errs := ParseFileWithClassifier(token.NewFileSet(), "c.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	var names []string
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			names = append(names, c.Name)
		}
	}
	if len(names) != 1 || names[0] != "Good" {
		t.Errorf("expected only Good component in AST, got %v", names)
	}
}

func TestParseFileStillReturnsSingleError(t *testing.T) {
	_, err := ParseFile(token.NewFileSet(), "c.gsx", []byte("package p\n\ncomponent X() { <div>hi</span> }\n"), 0)
	if err == nil || !strings.Contains(err.Error(), "3:") {
		t.Fatalf("ParseFile must still return one formatted error, got %v", err)
	}
}
```

(Add `"github.com/gsxhq/gsx/ast"` to the test imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./parser/ -run 'TestComponentBoundaryRecovery|TestRecoveryKeeps'`
Expected: FAIL — `ParseFileWithClassifier` returns `error`, not `[]Error` (compile error), and there's no recovery.

- [ ] **Step 3: Change `ParseFileWithClassifier` to return `[]Error` + recovery loop**

In `parser/file.go`:

```go
func ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (*ast.File, error) {
	f, errs := ParseFileWithClassifier(fset, filename, src, mode, attrclass.Builtin())
	if len(errs) > 0 {
		e := errs[0]
		pos := fset.Position(e.Pos)
		if pos.IsValid() {
			return f, fmt.Errorf("%d:%d: %s", pos.Line, pos.Column, e.Msg)
		}
		return f, fmt.Errorf("%s", e.Msg)
	}
	return f, nil
}

func ParseFileWithClassifier(fset *token.FileSet, filename string, src any, mode Mode, cls *attrclass.Classifier) (*ast.File, []Error) {
	// … src decode … on the `invalid src type` / os.ReadFile failure, return
	// nil, []Error{{Pos: token.NoPos, Msg: <msg>}} (positionless: no file context yet).
	// … scanPackage: if it errors, return nil, []Error{{Pos: pkgKwPos-or-NoPos, Msg: <msg>}} (fatal; nothing to recover to before the package clause).
	cursor := pkgEnd
	p := newParser(file, srcStr)
	p.classifier = cls
	for {
		off, found := nextTopLevelComponent(srcStr, cursor)
		if !found {
			break
		}
		if chunk := strings.TrimSpace(srcStr[cursor:off]); chunk != "" {
			gc := &ast.GoChunk{Src: srcStr[cursor:off]}
			ast.SetSpan(gc, file.Pos(cursor), file.Pos(off))
			f.Decls = append(f.Decls, gc)
		}
		p.i = off
		c, err := p.parseComponent()
		if err != nil {
			// Error already recorded in p.errs. Resync past this component's
			// `component` keyword so the next scan can't re-match it (forward
			// progress), skip the broken component, and continue.
			resyncFrom := off + len("component")
			if p.i > resyncFrom {
				resyncFrom = p.i
			}
			cursor = resyncFrom
			continue
		}
		f.Decls = append(f.Decls, c)
		cursor = p.i
	}
	// … tail GoChunk …
	return f, p.errs
}
```

Convert `scanPackage`'s two error returns + the `invalid src type` guard to the `[]Error` return form shown above (they're fatal/early, not recovered). Delete the now-unused `firstErr` helper from Task 1 if present (its logic now lives in `ParseFile`).

- [ ] **Step 4: Consume `[]Error` at the codegen boundary**

In `internal/codegen/batch.go` (~line 92) and `internal/codegen/codegen.go` (~line 66), change:

```go
// BEFORE:
f, err := gsxparser.ParseFileWithClassifier(fset, m, src, 0, cls)
if err != nil {
	bag.Add(diag.Diagnostic{Severity: diag.Error, Message: fmt.Sprintf("%s: %s", m, err.Error()), Source: "parser"})
	// … skip file …
}
// AFTER:
f, perrs := gsxparser.ParseFileWithClassifier(fset, m, src, 0, cls)
for _, e := range perrs {
	bag.Report(e.Pos, e.Pos, diag.Error, "syntax", "parser", "%s", e.Msg)
}
if len(perrs) > 0 {
	// parser errors → report them; do NOT type-resolve/emit this file's package
	// (the AST is incomplete). Mirror the existing skip path (mark the dir failed
	// / exclude from the resolve+emit set), exactly as the old `err != nil` branch did.
	continue // or whatever the existing control flow used to skip the file
}
```

Keep the existing read-error / glob-error `bag.Add` paths (batch.go:80/88) as-is — those are operational, not parser diagnostics. Ensure the resolve/emit gate: a dir with any parser diagnostic must not proceed to `resolveTypesPkg…`/`generateFile` (preserve the Slice-1 behavior where a parse failure excluded the file/package).

- [ ] **Step 5: Fix the parser tests that call `ParseFileWithClassifier` directly**

`parser/attrclass_test.go` (and any other) call `ParseFileWithClassifier` expecting `(*ast.File, error)`. Update them to `f, errs := ParseFileWithClassifier(...)` and check `len(errs) == 0` (or `> 0`) instead of `err != nil`. (The contract those tests assert — a holey attr splits, etc. — is unchanged; only the error-shape of the call changes.)

- [ ] **Step 6: Run tests**

Run: `go test ./parser/ -run 'TestComponentBoundary|TestRecoveryKeeps|TestParseFileStill' -v` then `go test ./...`
Expected: the recovery tests PASS; full suite green (codegen/corpus consume the new diagnostics; existing parser error goldens still render the same `line:col: message`).

- [ ] **Step 7: Commit**

```bash
git add parser/ internal/codegen/
git commit -m "feat(parser): component-boundary recovery + []Error; positioned parser diagnostics at codegen boundary"
```

---

## Task 3: Corpus recovery cases + rebaseline + CLI verification

**Files:**
- Create: `internal/corpus/testdata/cases/parser/recover_two_broken_components.txtar`, `internal/corpus/testdata/cases/parser/recover_broken_then_valid.txtar`
- Modify: any positionless parser golden that now gains a `line:col` (rebaseline)
- Test: corpus (`go test ./internal/corpus/`)

**Interfaces:**
- Consumes: the positioned parser diagnostics from Task 2.

- [ ] **Step 1: Add the multi-component recovery corpus case**

Create `internal/corpus/testdata/cases/parser/recover_two_broken_components.txtar`:

```
-- input.gsx --
package p

component A() { <div>hi</span> }

component B() { <p>yo</b> }
-- diagnostics.golden --
3:22: mismatched close tag </span>, expected </div>
5:20: mismatched close tag </b>, expected </p>
```

(Adjust the exact columns to whatever the harness emits — generate via `-update`, then confirm both lines are present and point at the two `</…>` tags.)

- [ ] **Step 2: Add the broken-then-valid case**

Create `internal/corpus/testdata/cases/parser/recover_broken_then_valid.txtar`:

```
-- input.gsx --
package p

component Bad() { <div>hi</span> }

component Good() { <p>ok</p> }
-- diagnostics.golden --
3:23: mismatched close tag </span>, expected </div>
```

(One diagnostic only — `Good` parsed cleanly. Columns via `-update`.)

- [ ] **Step 3: Run the corpus with update + review**

Run: `go test ./internal/corpus/ -update` (use the repo's actual update flag — check `internal/corpus/*_test.go` for its name), then `git diff internal/corpus/testdata`.
Expected: the two new cases get their `diagnostics.golden` filled; review that positions are correct. A few pre-existing positionless parser goldens (e.g. an "unexpected EOF" case) may gain a `line:col` prefix — confirm each change is the cursor position and sane.

- [ ] **Step 4: Run the corpus + full suite**

Run: `go test ./internal/corpus/ ./...`
Expected: green.

- [ ] **Step 5: Verify the CLI end-to-end (the original wart)**

```bash
mkdir -p tmpverify && printf 'package tmpverify\n\ncomponent Layout(title string) {\n\t<html><body>{children}</body></html>\n}\n\ncomponent Index() {\n\t<Layout title="x">\n\t\t<h1>Welcome\n\t</Layout>\n}\n' > tmpverify/v.gsx
go run ./cmd/gsx generate ./tmpverify 2>&1 | head -5
rm -rf tmpverify
```

Expected: a positioned diagnostic like `…/tmpverify/v.gsx:9:3: error[syntax]: mismatched close tag </Layout>, expected </h1>` — file:line:col up front, no `:0:0:`, no duplicated path.

- [ ] **Step 6: Commit**

```bash
git add internal/corpus/
git commit -m "test(corpus): parser component-boundary recovery cases; rebaseline positionless parser goldens"
```

---

## Task 4: Docs

**Files:**
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Update ROADMAP**

In the CLI / diagnostics rows: record parser error recovery (Slice 2) done — structured parser positions (parser errors render `file:line:col: error[syntax]: …`) + component-boundary recovery (one diagnostic per broken component). Note the LSP-readiness checklist is now closed for the parser layer (all of parser/types/codegen/jsx produce positioned, structured diagnostics through one `Bag`). State that **intra-component** parser recovery and **type-errors-alongside-parser-errors** remain deferred. Reference the spec/plan filenames (`2026-06-24-parser-error-recovery-design.md` / `2026-06-24-parser-error-recovery.md`).

- [ ] **Step 2: Commit**

```bash
git add docs/
git commit -m "docs: parser error recovery (Slice 2) — structured positions + component-boundary recovery"
```

---

## Self-Review

**Spec coverage:**
- §2 (A) structured positions → Task 1 ✓; (B) component-boundary recovery → Task 2 ✓
- §3 structured errors (`Error`, `errorf`, `errs`, migrate ~50 sites, positionless→cursor) → Task 1 ✓
- §4 plumbing (`ParseFileWithClassifier`→`[]Error`; `ParseFile` keeps `error`; convert at codegen boundary) → Task 2 ✓
- §5 component-boundary recovery + forward-progress guarantee → Task 2 Step 3 ✓
- §6 resolve/emit gate (parser errors → skip resolve/emit, write nothing) → Task 2 Step 4 ✓
- §7 rendering payoff → Task 3 Step 5 (CLI verify) ✓
- §8 corpus impact (existing largely unchanged; new multi-component cases; rebaseline positionless) → Task 3 ✓
- §9 testing (errorf accumulates; ParseFile back-compat; recovery 2-broken + broken-then-valid; codegen boundary; CLI) → Tasks 1–3 ✓
- §10 LSP-readiness closed for parser → Task 4 ✓

**Placeholder scan:** No "TBD"/"add error handling"/"similar to Task N". Task 1 Step 4's bulk migration shows the exact before/after pattern for both positioned and positionless sites + names every file and site count; "the compiler is your checklist" is mechanical-completion guidance backed by the pattern, not deferred design (the full site inventory is in the spec §3 + the parser exploration).

**Type consistency:** `parser.Error{Pos token.Pos; Msg string}`, `errParse`, `p.errorf(pos token.Pos, …) error`, `p.errs`, `ParseFileWithClassifier(...) (*ast.File, []Error)`, `ParseFile(...) (*ast.File, error)`, `bag.Report(e.Pos, e.Pos, diag.Error, "syntax", "parser", "%s", e.Msg)` — used consistently across tasks. Task 1's temporary `firstErr` helper is explicitly removed in Task 2 Step 3 (its logic moves into `ParseFile`), noted to avoid a dangling reference.

**Note for the implementer:** the ~50-site migration (Task 1) and the recovery loop (Task 2) are the substance; let the compiler drive the migration (grep `fmt.Errorf(` in `parser/` after adding `errorf`). The corpus is the regression oracle — existing parser error goldens must keep rendering the same `line:col: message`; only the new recovery cases + a few positionless rebaselines should change.
