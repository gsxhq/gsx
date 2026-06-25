# gsx LSP — Go-to-definition & Hover inside Pipelines (seed-first)

**Status:** approved design (brainstormed 2026-06-25; retargeted to the seed-first
lowering after `pipeline-forward-application` landed), ready for an
implementation plan.

## 1. Goal & non-goals

**Goal.** Make go-to-definition and hover work on every meaningful cursor region
of a piped interpolation/attribute expression
`{ postPage{} |> url("slug", p.Slug) |> upper }`:
- the **seed** (`postPage{}`),
- each **filter name** (`url`, `upper`) → its registered Go filter func,
- symbols inside **filter args** (`p.Slug`).

Today a piped node returns null for both — `handleDefinition`/`handleHover` bail
on `hasPipeStages(node)` (the lowered expression is not byte-identical to source).
This is the single most common gap in real structpages code, where `|> url`,
`|> id`, `|> target` appear constantly.

**Non-goals.**
- No new navigation for non-pipeline code (unchanged).
- Hover's whole-expression type fallback (cursor in the seed but not on an
  identifier) is NOT extended to piped nodes here — `pipedTarget` resolves to a
  `types.Object`; no object → null. Minor follow-up.
- The `?` failable marker, pipeline-as-filter-argument, and `mapEach` are not gsx
  features and are out of scope.

## 2. Background — the seed-first lowering

`pipeline-forward-application` replaced the old curried lowering with a single
seed-first call per stage (`internal/codegen/filters.go` `lowerPipe`):

```
acc := "(" + seed + ")"
for each stage:
    args := []
    if filter wantsCtx: args += pipeCtxIdent   // "ctx", at position 0
    args += acc                                  // the subject (prior acc)
    if stage has args:   args += stage.Args      // user stage args, after subject
    acc := <alias>.<Func>(args…)
```

So a pipeline lowers to nested single calls:

```
{ greeting() |> truncate(80) |> upper }
  → _gsxstd.Upper( _gsxstd.Truncate( (greeting()), 80 ) )
{ page{} |> url("id", x) }
  → structpages.URLFor( ctx, (page{}), "id", x )   // ctx injected at args[0]
```

The analysis already type-checks that lowered expression and retains it:
`Package.ExprMap[node]` is the lowered `ast.Expr`, and `Package.Info` covers its
sub-nodes — so `Info.Uses[Upper-ident]` is the `std.Upper` func object,
`Info.Uses[URLFor-ident]` is `structpages.URLFor`, and the seed/arg identifiers
are in `Info.Uses` too. `internal/lsp` already has the reverse-mapper
(`exprNodeAtOffset`, `innermostIdent`) and the hover renderers
(`types.ObjectString`/`TypeString`, `qualifierFor`, `markdownGo`, `rangeForSpan`).

**Gap:** `ast.PipeStage{Name, Args, HasArgs}` carries no source position, so the
cursor cannot be located on `url` or inside `truncate(n)`.

## 3. Architecture

Two pieces — one parser change, one LSP router. **No codegen change.**

### 3.1 Parser/AST — `PipeStage` source positions

Add `NamePos token.Pos` (first char of the filter name) and `ArgsPos token.Pos`
(first char of the args, after `(`; `NoPos` when `!HasArgs`). Purely for cursor
detection in source; resolution still comes from `go/types`.

The inner interpolation text is contiguous from `Interp.ExprPos` (the position of
`inner[0]`). Thread that base position into pipe parsing:
- `splitPipe(src)` returns each segment's byte offset within `src` (today it
  returns only the strings).
- `parsePipe(inner string, base token.Pos)` — for each segment at offset
  `segOff`, the name starts after the segment's leading whitespace and the args
  after the `(` + leading whitespace; set
  `NamePos = base + segOff + leadingWS(seg)` and
  `ArgsPos = base + (offset of the first args char)`.
- `parsePipeStage` returns/sets the within-segment name/args offsets.
- `parser/markup.go` (the `Interp` call site, ~line 23) passes `exprPos`. The
  `ExprAttr` path reuses the already-positioned `Interp.Stages`, so only the one
  call site changes.
- `internal/printer/corpus_test.go` `zeroSpans` zeros `Stages[i].NamePos`/
  `ArgsPos` (mirroring the existing `ExprPos`/`NamePos`/`ParamsPos` handling) so
  printer faithfulness/idempotence holds. The printer prints `Name`/`Args` text
  and is unaffected by the new positions.

### 3.2 LSP — the piped router (Approach A: walk the lowered AST)

`internal/lsp/pipe.go`: `pipedTarget(pkg *Package, node gsxast.Node, exprPos
token.Pos, off int) (obj types.Object, span [2]int, ok bool)`. Both
`handleDefinition` and `handleHover` call it in place of the `hasPipeStages → null`
guard: definition → `s.locationForPos(pkg.Fset.Position(obj.Pos()))` (guarded:
real source only, never `.x.go`); hover → `markdownGo(types.ObjectString(obj,
qf))` with the region span as `Range`.

The lowered shape is fixed by `lowerPipe`, so the N filter layers peel
deterministically (N = `len(node.Stages)`):

```
cur := ExprMap[node]                                // outermost stage's call
selSel  := make([]*ast.Ident, N)                    // stage i → its filter Sel ident
selArgs := make([][]ast.Expr, N)                    // stage i → its user stage args
for i := N-1; i >= 0; i-- {
    call := cur.(*ast.CallExpr)
    selSel[i] = call.Fun.(*ast.SelectorExpr).Sel
    subjIdx := 0
    if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == ctxIdent {
        subjIdx = 1                                 // ctx injected at args[0]
    }
    selArgs[i] = call.Args[subjIdx+1:]              // user stage args follow the subject
    cur = call.Args[subjIdx]                        // descend into the subject
}
seedExpr := unwrapParens(cur)                       // innermost ((seed)) → seed
```

A user stage arg can never occupy `args[0]` (the subject is always there, or the
injected ctx ident is), so the ctx check is unambiguous. The loop peels exactly N
layers, so the seed — even if it is itself a call — is never mis-peeled. Every
type assertion is guarded (comma-ok); any shape mismatch yields `ok=false`
(→ null), never a panic (§6).

### 3.3 Region detection

Given the cursor byte offset `off` (gsx-fset positions):
- **seed** — `off ∈ [seedStart, seedStart+len(node.Expr))`, `seedStart =
  GSXFset.Position(exprPos).Offset`. Map into `seedExpr` by relative offset
  (`seedExpr.Pos() + (off - seedStart)`), then `innermostIdent` → `Info.Uses`/`Defs`.
- **filter name i** — `off ∈ [nameStart, nameStart+len(stage.Name))`,
  `nameStart = GSXFset.Position(stage.NamePos).Offset`. Target = `Info.Uses[selSel[i]]`.
- **filter args i** — `off ∈ [argsStart, argsStart+len(stage.Args))`,
  `argsStart = GSXFset.Position(stage.ArgsPos).Offset`. Find the sub-expression of
  `selArgs[i]` covering the relative offset (its text is byte-identical to
  `stage.Args`), then `innermostIdent` → `Info.Uses`.
- otherwise → `ok=false`.

The `.gsx` `Range` for hover is the region span (the identifier span for an
ident; the seed/args span otherwise), via `rangeForSpan`.

### 3.4 The reserved ctx ident (no codegen change)

`internal/lsp` is deliberately decoupled from `internal/codegen` (the analyzer is
injected via `gen`; the LSP imports none of codegen). So the walk does **not**
import codegen for the ctx-ident name — it declares a local
`const ctxIdent = "ctx"` in `internal/lsp/pipe.go` with a comment noting it must
match codegen's reserved `pipeCtxIdent`. The value is fundamental and stable (the
ambient render-context binding), and the **ctx-injected e2e test** (§5) fails
loudly if the two ever diverge. Hence this slice touches only the parser and the
LSP — no codegen change.

## 4. Error handling / edges

- **Fully defensive router:** every assertion on the lowered AST is comma-ok;
  any mismatch / nil sub-node / out-of-range → `ok=false` (null def, null hover),
  never a panic.
- **Unknown filter:** `lowerPipe` errors, so the node never reaches `ExprMap`
  (the component skeleton was skipped) → `ExprMap[node] == nil` → null. (Within
  the type-probe an unknown filter falls back to the bare seed, so seed nav may
  still resolve even when a later filter is unregistered.)
- **`.x.go` guard (definition only):** a resolved object pointing at the
  synthetic overlay path is suppressed; hover has no such guard (it shows a type,
  never navigates).
- **Cursor on `|>` / whitespace / `(` / `)`** — in no region → null.
- **Non-piped nodes** are unaffected: `pipedTarget` is consulted only when
  `hasPipeStages(node)` is true; the existing byte-identical path handles the rest.
- **Walk ↔ `lowerPipe` coupling:** the walk depends on `lowerPipe`'s output shape.
  A round-trip unit test (lower known pipelines, walk, assert recovered
  selectors/seed) guards drift; if `lowerPipe` changes, that test fails loudly.

## 5. Testing (per [[gsx-syntax-change-test-coverage]])

Every parser/codegen change ships txtar corpus + unit coverage; LSP behavior is
covered end-to-end through the real server.

**Parser (unit, `parser/`):**
- `PipeStage.NamePos`/`ArgsPos` point at the exact source bytes for `{ x |> upper }`,
  `{ x |> truncate(5) }`, and a multi-stage pipeline with surrounding/interior
  whitespace (`{ x |>  upper |> truncate( 5 ) }`).
- Existing pipe-parse tests still pass (the new return shape of `splitPipe`/
  `parsePipe` is internal).

**Codegen / printer (corpus + unit):**
- Printer faithfulness + idempotence over the whole corpus still holds with the
  new `PipeStage` position fields (zeroSpans updated) — run the corpus property
  test.
- A corpus case (codegen + render) confirming the seed-first lowering the walk
  relies on is **stable**: a bare filter (`upper`), a filter with args
  (`truncate(5)`), and a **ctx-injected** filter — asserting the lowered call
  places `ctx` at `args[0]` and the subject immediately after. This guards the
  `lowerPipe` shape (and the `ctx`-at-`args[0]` placement) the walk depends on. (Add to
  the existing pipeline corpus rather than a bespoke harness.)

**LSP e2e (`gen`, via `runLSP`; module-resolution guarded by `testing.Short()`),
both definition and hover**, on `{ greeting() |> truncate(5) |> upper }` with a
local `greeting`, std `truncate`/`upper`, plus a ctx-injected alias case
`{ page{} |> url("id", x) }` (a local filter `func URL(ctx, page any, args ...any)
(string, error)` registered via the cache manifest, mirroring the
filter-manifest tests):
- **def on seed** `greeting` → its declaration; never `.x.go`.
- **def on filter** `upper`/`truncate` → the std func; `url` → the alias func.
- **def on arg** — `{ x |> truncate(n) }` with a param `n` → `n`'s declaration.
- **hover on seed** → the seed's signature; **hover on filter** `upper` →
  `func Upper(s string) string`; **hover on arg** `n` → `var n int`.
- **ctx-injected**: def/hover on `url` → `structpages`-style `URLFor`; def on the
  subject and the `"id"`/`x` args resolve at the right positions (proves the
  `subjIdx` ctx-offset handling).
- **cursor on `|>`** → null (def and hover).
- A non-piped regression check still passes (existing def/hover suites).
- **Walk round-trip unit:** recovered `selSel`/`selArgs`/seed match the stages for
  bare, arg, ctx-injected, and multi-stage pipelines.

## 6. Risks

- **Walk ↔ `lowerPipe` coupling** — mitigated by the round-trip unit test and the
  corpus lowering case (§5), and by bounding the walk to exactly N layers.
- **ctx-detection** relies on the reserved ctx ident at `args[0]`; the LSP's
  local `ctxIdent` must match codegen's `pipeCtxIdent` ("ctx"), which the
  ctx-injected e2e test guards. A user stage arg can never sit at `args[0]`.
- **Position arithmetic** for `NamePos`/`ArgsPos` reuses the contiguous-slice
  reasoning the existing `ExprPos` relies on; covered by exact-byte parser tests.
- **Seed/args mapping** reuses the proven byte-identical relative-offset technique
  (their text is copied verbatim into the lowered call).

## 7. What ships

In an editor, go-to-definition and hover work everywhere in a piped expression:
jump from `|> url` to `structpages.URLFor`, hover it for its signature, jump to
the seed and to any symbol inside filter arguments — all from the already
type-checked lowered expression, with only a parser-position addition (no codegen change).
