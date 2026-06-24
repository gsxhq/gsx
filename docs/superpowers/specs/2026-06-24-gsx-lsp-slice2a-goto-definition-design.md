# gsx LSP — Slice 2a: Go-to-Definition (both directions)

**Status:** approved design, ready to plan
**Date:** 2026-06-24
**Builds on:** Slice 1 (in-process diagnostics LSP) — `docs/superpowers/specs/2026-06-23-gsx-lsp-design.md`

## 1. Goal & non-goals

Deliver go-to-definition for gsx in **both** directions:

- **`.gsx` → definition** (served by `gsx lsp`, in-process on `go/types`): from a
  cursor in a `.gsx` file, jump to the definition of the Go symbol / component /
  param under it.
- **`.go` → `.gsx`** (served by **gopls**, not us): from a `.go` caller, jump to
  the `component` declaration in the `.gsx` source. This rides on `//line`
  directives in the generated `.x.go` — which today are missing at declarations
  (§4), so this slice adds them.

**Non-goals:** find-references, hover, document symbols, formatting (later
sub-slices); column-perfect target positions (line-accurate is the bar here);
completion (slice 3). We do not proxy gopls; the `.go → .gsx` path is gopls
reading our generated `//line`s, which we improve but do not orchestrate.

## 2. Background: what Slice 1 left us

- The analyzer (`gen.lspAnalyzer`) returns only `[]diag.Diagnostic` and the
  codegen pipeline **discards** `pkg.TypesInfo`, `pkg.Fset`, and the skeleton
  AST after harvesting `map[gsxast.Node]types.Type`
  (`internal/codegen/analyze.go`, `harvest` at ~`analyze.go:662`).
- The skeleton already contains every user Go expression **verbatim**, wrapped
  as `_gsxuse(expr)` calls, type-checked by `go/packages`.
- `harvest` already aligns the *k*-th `_gsxuse` call with the *k*-th gsx
  expression node by **enumeration order** (`componentExprs` /`collectExprs`,
  depth-first source order) — an existing gsx-AST ↔ skeleton-AST correspondence.
- `//line` in the **skeleton** gives correct `.gsx` file + line but a
  skeleton-relative (wrong) column (the column-accuracy TODO, `analyze.go:624`).
- **Correction to Slice-1 design §5.1:** that section assumed the generated
  `func Card` was preceded by `//line card.gsx:N`. It is **not**. The emitter
  (`emit.go`) writes `//line` only on component **body** content, never at the
  `func` declaration or props-struct fields. Verified against generated output.
  Consequently `.go → .gsx` does **not** work today — go-to-def on `Card` from a
  `.go` file lands in the generated `card.x.go`. §4 fixes this.

## 3. `.gsx`-side go-to-definition (in-process)

### 3.1 Retention refactor — the analyzer returns a package handle

Slice 1's `Analyzer.Diagnose(...) ([]diag.Diagnostic, error)` evolves to return a
retained handle:

```go
// internal/lsp — the consumer owns the interface and the return type, so
// internal/lsp imports only go/{types,ast,token} + gsxast (no codegen, no cycle).
type Analyzer interface {
    Analyze(dir string, override map[string][]byte) (*Package, error)
}

type Package struct {
    Diags   []diag.Diagnostic
    Fset    *token.FileSet              // honors //line: skeleton pos → .gsx
    Info    *types.Info                 // Uses / Defs / Types for the package
    ExprMap map[gsxast.Node]ast.Expr    // gsx expr node → its skeleton go/ast expr
    Files   map[string]*gsxast.File     // .gsx path → gsx AST (param/tag nav)
}
```

`internal/codegen` is refactored so its batch entrypoint can return, in addition
to diagnostics: the shared `Fset`, a merged per-package `*types.Info`, the
`gsxast.Node → ast.Expr` correspondence (extend `harvest` to keep the
`_gsxuse` **argument node**, not just `info.Types[arg].Type`), and the parsed
`.gsx` files. `gen` assembles these into `lsp.Package`. The slice-1
diagnostics-only path is preserved (diagnostics read straight off `Package.Diags`).

Publishing diagnostics (slice 1) now reads `Package.Diags`; the document
lifecycle and `analyzeAndPublish` are unchanged except for the new return type.

### 3.2 Reverse mapper (request side) — relative offset in byte-identical text

The `.gsx` Go expression and its skeleton counterpart are **byte-identical text**.
So a cursor at byte offset *O* inside the `.gsx` Go expression corresponds to
offset *O* inside the skeleton expression. To find the symbol under a cursor:

1. cursor `(uri, line, character)` → byte offset in the `.gsx` buffer.
2. find the innermost gsx expression node whose span contains the cursor (gsx AST
   positions are exact). Anchor at the **Go-expression start** the parser tracks,
   not the `{` delimiter.
3. `skelExpr := ExprMap[gsxNode]`; `relOff := cursorOffset − gsxGoExprStartOffset`.
4. `skelPos := skelExpr.Pos() + relOff`; walk `skelExpr`'s subtree to the
   innermost `go/ast` node whose `[Pos,End)` contains `skelPos` — typically the
   `*ast.Ident` under the cursor.

This is exact and **does not depend on `//line` columns**.

### 3.3 Response side — one uniform resolution, three cases

The definition handler has **two cursor contexts**:

- **(a) cursor inside a Go expression** (interpolation / attr expr) → use the
  reverse mapper (§3.2) to get the skeleton `*ast.Ident`, then
  `obj := Info.Uses[ident]` (fall back to `Info.Defs[ident]`), then
  `pos := Fset.Position(obj.Pos())` → `Location`. Covers **D1** and **D3**.
- **(b) cursor on a component tag name** (`<Card/>`) → a tag is markup, not a
  `_gsxuse` argument, so it is **not** in `ExprMap`. Resolve it directly from the
  gsx AST: the tag's name → look up the `component` declaration in `Files`.
  Covers **D2**.

- **D1 — Go symbol → real `.go`** (`{user.Name}`'s `Name` field, a type, a
  package func, a stdlib/dep symbol): `Fset.Position` yields the real `.go`
  file. Return as-is. Exact.
- **D2 — component tag `<Card/>` → `component Card`**: resolved via path (b) —
  the gsx AST gives the tag name, and we return the `.gsx` `Location` of the
  matching `component` declaration found in `Files`. Exact, same-package. (A
  component imported from another package is referenced in the skeleton as a Go
  identifier, so it falls under path (a)/D1 and resolves to that package's
  generated `.x.go`, which §4's decl `//line` maps back to its `.gsx`.)
- **D3 — component param** (`user` in `{user.Name}` → the `user` param): needs a
  **skeleton `//line`** before each synthesized `name := _gsxp.Name` binding,
  pointing at the param's `.gsx` declaration position. Then the param's local
  object resolves through `Fset.Position` uniformly. (Small `analyze.go` change;
  binding lines rarely carry diagnostics, so the position shift is benign.)

All three funnel through `obj.Pos() → Fset.Position`: `.go` paths pass through,
`.gsx` paths (via `//line`) are returned. When the resolved filename is neither a
real file nor a `.gsx` (a bare skeleton overlay path with no `//line`), we return
no definition rather than point into synthetic code.

## 4. `.go → .gsx` enablement — declaration `//line` in emitted code

The emitter (`internal/codegen/emit.go`) currently writes `//line` only inside
component bodies. Add directives at the two declaration sites so gopls (and the
Go compiler) attribute them to `.gsx`:

1. **Before each component `func` declaration** → `//line card.gsx:<componentLine>:<col>`,
   so go-to-def on `Card` from a `.go` file lands on `component Card`, and any
   compiler error in the synthesized function wrapper points at the component.
2. **Before each props-struct field** → `//line card.gsx:<paramLine>:<paramCol>`,
   so go-to-def on a prop field (`CardProps.Title`) lands on the `.gsx` param,
   and prop-type errors point at the param.

This is **emit-only** — it works with plain gopls, no `gsx lsp` required. It
changes generated output, so the codegen golden fixtures
(`internal/corpus/testdata/...`) must be **rebaselined**; the new `//line` lines
are the only expected delta. Verify the directives survive `go/format` (already
proven for body `//line`s; confirm for struct-field position).

## 5. Component boundaries

- `internal/codegen`: extend `harvest` to retain `_gsxuse` arg nodes; expose a
  batch result carrying `Fset` + per-package `*types.Info` + the
  `gsxast.Node → ast.Expr` map + parsed `.gsx` files; add skeleton `//line` for
  param bindings (D3); add emit `//line` at func decls + props fields (§4).
- `gen`: `lspAnalyzer` implements `Analyze` returning `*lsp.Package`.
- `internal/lsp`:
  - `mapping` — cursor `(uri,pos)` → skeleton `*ast.Ident` (the reverse mapper,
    §3.2); pure `go/ast`/`gsxast`, unit-testable in isolation.
  - `definition` — `textDocument/definition` handler: reverse map → `Info.Uses` →
    `Fset.Position` → `Location`, plus the gsx-AST direct path for component tags
    (`<Card/>` resolves via the `Files` AST when the tag is not a Go ident in the
    skeleton).
  - server: advertise `definitionProvider`; dispatch `textDocument/definition`.

## 6. Scope & testing

**In:** retention refactor; reverse mapper; `textDocument/definition` for
D1/D2/D3; skeleton `//line` for params; emit declaration `//line` (§4) + golden
rebaseline.

**Deferred:** find-references, hover (a near-free follow-on — same request-side
mapping, response is `Info.Types[node]` as a string), document symbols,
formatting; column-perfect positions; the `//line` column-accuracy TODO
(`analyze.go:624`) beyond what §4 needs.

**Testing:**
- Reverse mapper unit tests: cursor offsets across a multi-node selector
  (`a.b.c`), a call (`f(x)`), and a param ident — assert the resolved skeleton
  `*ast.Ident` is the right one.
- `definition` end-to-end (txtar / temp-module, mirroring slice-1's e2e): one
  case each for D1 (field/type in `.go`), D2 (`<Card/>` → `card.gsx`), D3 (param)
  — assert the returned `Location` file + line.
- Emit `//line`: a codegen test asserting the generated `.x.go` carries `//line`
  immediately before the component `func` and before each props field, mapping to
  the component/param `.gsx` line; plus the golden-corpus rebaseline.
- Optional manual check: in Neovim/gopls, open a `.go` caller of a generated
  component and confirm go-to-def opens the `.gsx`.

## 7. Risks

- **Golden churn (§4):** declaration `//line`s touch every component's generated
  output. Mitigation: rebaseline is mechanical; assert the delta is `//line`-only.
- **`//line` inside a struct decl:** must survive `go/format`. Mitigation:
  verify early; fall back to a per-field-comment form if needed.
- **Anchoring at the Go-expression start (§3.2):** the parser must expose the Go
  expression's start position distinct from the `{` delimiter. Mitigation:
  confirm the gsx AST field used; the reverse-mapper unit tests catch a wrong
  anchor immediately.
- **Enumeration-order coupling:** `ExprMap` relies on `componentExprs`/`emitProbes`
  staying in lockstep (already an invariant `harvest` depends on). No new risk;
  the existing tests guard it.

## 8. What ships in Slice 2a

In an editor: in a `.gsx` file, go-to-definition on a struct field/type jumps to
its `.go`; on `<Card/>` jumps to `component Card`; on a component param jumps to
the param. And from a `.go` file, go-to-def on a gsx component jumps into the
`.gsx` (via gopls + the new declaration `//line`s). Both directions, line-accurate.
