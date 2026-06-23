# Parser Error Recovery (Slice 2) — Design

**Date:** 2026-06-24
**Status:** Design (approved for plan)
**Slice:** 2 of 2 of the diagnostics work toward the LSP. **Slice 1** (`2026-06-23-diagnostics-foundation-design.md`) shipped the `internal/diag` model, rich/compact/JSON rendering, and **semantic-layer** recovery (all `go/types` errors + component-boundary codegen recovery). **This spec:** the **parser layer** — give parser errors structured `token.Pos` positions, and recover at the **component boundary** so a file reports one syntax diagnostic per broken component instead of stopping at the first.

## 1. Problem

The gsx parser is strictly **fail-fast**: every error is `return nil, fmt.Errorf(...)`, and `ParseFile` returns a single `error`. Two consequences:

- **Positions live in strings, not structure.** Most parser errors hand-format `fmt.Errorf("%d:%d: msg", line, col)`; ~8 sites have no position at all. Slice 1 wrapped that single error into ONE *positionless* `diag.Diagnostic` (its `Start` is zero), so the CLI renders parser errors as `error: /path/index.gsx: 28:3: mismatched close tag …` — the real location is buried in the message text, and the file path is duplicated. (The Slice-1 render fix removed the ugly `:0:0:` prefix, but the position is still un-structured.)
- **One error per file.** A syntax error in the first component hides every other component's errors — poor for an editor/LSP and for batch fixing.

**Goal:** parser errors carry a real `token.Pos` (so they render `index.gsx:28:3: error: …` like every other diagnostic), and a syntax error in one component no longer suppresses the others.

## 2. Scope

**In (Slice 2):**
- **(A) Structured parser errors** — the parser accumulates positioned errors (`token.Pos` + clean message) instead of returning a single pre-formatted string.
- **(B) Component-boundary recovery** — on a syntax error inside a component, record it and resync to the next top-level `component`, continuing; the returned AST holds the cleanly-parsed components.
- Convert parser errors into positioned `diag.Diagnostic`s (`Source:"parser"`) at the codegen boundary; render via the existing diag renderers.

**Out (deferred):**
- **Intra-component recovery** — reporting *multiple* syntax errors *within* one component (requires resync at element/attribute/child boundaries + cascading-error suppression; the classic-hard part). A broken component still yields exactly one diagnostic.
- **Type errors alongside parser errors** — a package with any parser error skips type-resolution/emit (as in Slice 1); we do not also report `go/types` errors for its still-valid components. (LSP-era refinement.)
- **rustc-style `//~` inline test annotations** (noted in Slice 1 §9 as a possible later refinement).

## 3. Structured parser errors

Add an accumulator + helper to the `parser` struct (`parser/parser.go`):

```go
// Error is a positioned parser diagnostic. Pos resolves to file:line:col via the
// FileSet the parser was created with. Exported so codegen can convert it.
type Error struct {
    Pos token.Pos
    Msg string
}

// on parser:
type parser struct {
    // … existing fields (file, src, base, i, classifier) …
    errs []Error
}

// errorf records a positioned error and returns a sentinel error so existing
// `return nil, <sentinel>` control flow is preserved with minimal churn.
func (p *parser) errorf(pos token.Pos, format string, args ...any) error {
    p.errs = append(p.errs, Error{Pos: pos, Msg: fmt.Sprintf(format, args...)})
    return errParse // package-level sentinel; callers propagate it up the stack
}
```

Migrate the ~50 `return nil, fmt.Errorf("%d:%d: msg", line, col, …)` sites (across `component.go`, `attrs.go`, `markup.go`, `pipe.go`, `file.go`) to `return nil, p.errorf(pos, "msg", …)`:
- The message **drops** the hand-formatted `%d:%d:` prefix — the position is now structured in `Error.Pos`.
- Sites that compute a `token.Pos` already (most) pass it directly.
- The ~8 **positionless** sites (`unterminated…` in `attrs.go`/`pipe.go`, `missing/malformed package clause` in `file.go`) pass the **cursor** position (`p.pos()`) as a best-effort `Pos`.
- A returned sentinel `errParse` unwinds the current parse the same way today's errors do; the real detail lives in `p.errs`.

## 4. Plumbing (minimal blast radius)

Two entry points, only one changes shape:

- **`ParseFileWithClassifier`** returns `(*ast.File, []Error)` — the `.gsx` callers (`internal/codegen/batch.go`, `internal/codegen/codegen.go`) use this and get the full positioned list.
- **`ParseFile`** keeps its `(*ast.File, error)` contract — it delegates to `ParseFileWithClassifier` and, if `len(errs) > 0`, returns `errs[0]` formatted as `line:col: msg` (so its behavior for the **Go-fragment sub-parsers** in `analyze.go` — `parseGoExpr`/`parseGoStmt`/`parseTypeParams` — and the existing unit tests is unchanged). These sub-parsers parse synthetic Go and only care whether parsing failed.

At the codegen boundary, `batch.go`/`codegen.go` convert each `Error` to a diagnostic into the per-package `Bag`:

```go
f, perrs := gsxparser.ParseFileWithClassifier(fset, m, src, 0, cls)
for _, e := range perrs {
    bag.Report(e.Pos, e.Pos, diag.Error, "syntax", "parser", "%s", e.Msg)
}
```

(positioned via the gsx parser fset — `bag.Report` resolves `e.Pos`). This replaces Slice 1's positionless parse-error wrap. A single `Code:"syntax"` is fine for Slice 2 (finer codes are a later refinement).

## 5. Component-boundary recovery

In `parser/file.go`'s top-level loop:

```go
for {
    off, found := nextTopLevelComponent(srcStr, cursor)
    if !found { break }
    // … append any GoChunk between cursor and off …
    p.i = off
    c, err := p.parseComponent()
    if err != nil {
        // Error already recorded in p.errs. Resync to the next top-level
        // `component`, skipping this broken one. CRITICAL: search must start
        // STRICTLY AFTER this component's `component` keyword, or the next
        // nextTopLevelComponent could re-match the same offset and loop. Use
        // max(p.i, off+len("component")) so progress is guaranteed even when
        // parseComponent failed at the very start (p.i == off).
        resyncFrom := off + len("component")
        if p.i > resyncFrom { resyncFrom = p.i }
        cursor = resyncFrom
        continue
    }
    f.Decls = append(f.Decls, c)
    cursor = p.i
}
return f, p.errs
```

**Forward-progress guarantee:** the resync cursor is always `>= off + len("component")`, strictly past the failed component's keyword, so the next `nextTopLevelComponent(srcStr, cursor)` cannot re-find the same component and the loop always advances toward EOF. Because we abandon the rest of a broken component rather than limp through it, **there is no intra-component cascade**. The returned `*ast.File` contains exactly the components that parsed cleanly; `p.errs` holds one error per broken component (plus any fatal package-clause error, which still aborts early — there is nothing to recover to before the package clause).

## 6. Resolve/emit gate (correctness)

If a package produced **any** parser diagnostic, `batch.go`/`codegen.go` report all parser diagnostics and **skip type-resolution and emit** for that package — writing nothing (same all-or-nothing stance as Slice 1). Type-checking a syntactically incomplete AST (missing the broken components) would yield spurious cascades, so syntax must be clean before semantic phases run for that package. Other packages are unaffected.

## 7. Rendering payoff

Parser errors now flow through the standard positioned renderers (rich/compact/JSON):

```
index.gsx:21:4: error[syntax]: mismatched close tag </Layout>, expected </h1>
```

— file:line:col up front, clean message, no duplicated path, no `:0:0:`. The Slice-1 positionless-prefix special-case remains for any genuinely positionless diagnostic but is no longer exercised by parser errors.

## 8. Corpus impact (small)

- **Existing parser error goldens** (`parser/testdata/cases/parser/e01`–`e05`, and `internal/corpus/testdata/cases/parser/*`) already render `line:col: message`. The structured path renders the **same** `line:col: message`, so these are **largely unchanged**. A few positionless cases (e.g. `e02` "unexpected EOF") gain a `line:col` (cursor position) — rebaseline those.
- **New cases:** a multi-component file with two broken components → two diagnostics (proves recovery); a broken component followed by a valid one → the valid one is still emitted (proves the AST holds clean components).
- Rebaseline via the corpus `-update` flag; review the diff.

## 9. Testing strategy

- **`parser` unit:** `errorf` accumulates positioned `Error`s; `ParseFileWithClassifier` returns them; `ParseFile` still returns a single `line:col:`-formatted error (back-compat). Component-boundary resync: two broken components → two `Error`s with correct positions; a broken-then-valid file → the valid component is present in the returned AST.
- **Codegen boundary:** a `.gsx` with parser errors yields positioned `Source:"parser"` diagnostics in `PackageResult.Diags`, and the package writes no `.x.go`.
- **Corpus:** existing parser goldens stay green (or rebaseline the few positionless ones); the new multi-component recovery case pins two diagnostics.
- **CLI:** the unclosed-`<h1>` repro renders `index.gsx:21:4: error[syntax]: mismatched close tag …` (positioned, no `:0:0:`, no duplicated path).

## 10. LSP-readiness

This closes the parser side of the Slice-1 LSP-readiness checklist: `[]Diagnostic` for a document now includes **parser** diagnostics (positioned), not just semantic ones — so "all diagnostics for a document" holds for both layers (modulo intra-component multiplicity, deferred). Combined with Slice 1, the diagnostics foundation is LSP-ready end to end: parser + types + codegen + jsx all produce positioned, structured diagnostics through one `Bag`.
