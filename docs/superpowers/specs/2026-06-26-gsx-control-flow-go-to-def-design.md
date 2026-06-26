# gsx LSP — go-to-definition in control-flow clauses + code blocks

**Status:** approved design (brainstormed 2026-06-26), ready for an implementation plan.

**One-line:** Extend the gsx LSP's go-to-definition so a cursor on an identifier inside a `{ for … }` / `{ if … }` clause or a `{{ … }}` code block resolves — using the same in-memory-skeleton bridge that value interpolations (`{ expr }`) already use, with **zero dependence on generated `.x.go`**.

## 1. Goal & non-goals

### Goal
Go-to-definition works for identifiers in:
- **`ForMarkup.Clause`** — e.g. `{ for _, post := range props.Posts { … } }`: `props` → the props param, `Posts` → the field, `post` → its loop-variable binding.
- **`IfMarkup.Cond`** — e.g. `{ if props.Featured { … } }`.
- **`Code`** (`{{ … }}`) — e.g. `{{ total := sum(items) }}`: `sum` → the func, `items` → its declaration, `total` → its binding.

Resolution is identical in spirit to the existing `Interp` path: bridge the cursor into the in-memory skeleton, resolve via `go/types` `Info`, and map the target back to `.gsx` via `//line`.

### Non-goals
- **`.x.go` dependence.** Resolution flows through the warm `Module`'s in-memory skeleton + `//line`-mapped positions, never the on-disk generated `.x.go` (consistent with the whole warm-core design). The existing `.x.go`-suffix guard (`definition.go:172`) stays and rejects any position that would land in generated code.
- **Find-references / hover** in control-flow regions — out of scope here (go-to-def only). Hover may follow once the bridge exists.
- **Pipeline-staged expressions** inside clauses — the existing `hasPipeStages` deferral applies (a clause is a raw Go fragment; it has no gsx pipeline syntax, so this is moot for clauses, but the LSP must not assume otherwise).

## 2. Background — why it doesn't work today

`exprNodeAtOffset` (`internal/lsp/definition.go`) — the cursor→node finder for go-to-def — only matches `*gsxast.Interp` and `*gsxast.ExprAttr` (their `[ExprPos, ExprPos+len(Expr))` span). Control-flow constructs are different node types:
- `ForMarkup{ Clause string; Body }`, `IfMarkup{ Cond string; Then; Else }`, `Code{ Code string }` — each holds a **raw Go string** and only a whole-node `span` (no position for the clause/cond/code *text*).

The skeleton (`emitProbes`, `internal/codegen/analyze.go`) **does** emit these byte-faithfully — `for %s {` (Clause), `if %s {` (Cond), `WriteString(t.Code)` — so the type-checker resolves their identifiers; the info exists in `Info.Uses/Defs`. But:
1. There is no position field to tell the LSP "the cursor is inside this clause."
2. `harvest` builds `ExprMap` by k-ordering `_gsxuse(expr)` calls; clauses aren't wrapped in `_gsxuse`, so they have no `ExprMap` entry to bridge through.
3. The skeleton emits no `//line` before a clause, so positions *inside* it (e.g. a loop-variable declaration) don't map back to `.gsx`.

The skeleton string is parsed **verbatim** (`goparser.ParseFile(fset, …, skel, …)`, `module_importer.go:254`) — no gofmt — so skeleton **byte offsets are stable**, which makes emission-offset tracking viable.

## 3. Design

Three layers, each a small extension of an existing mechanism.

### 3.1 AST + parser — clause/cond/code positions
Add, mirroring `Interp.ExprPos`:
- `ForMarkup.ClausePos token.Pos` — first char of the (trimmed) `Clause` text in source.
- `IfMarkup.CondPos token.Pos` — first char of `Cond`.
- `Code.CodePos token.Pos` — first char of `Code`.

The parser sets each where it already extracts the string (`parser/markup.go` `ForMarkup{Clause:…}` at ~259, `IfMarkup{Cond:…}` at ~300, and the `{{ }}` Code production). `ClausePos`/`CondPos`/`CodePos` point at the first char of the *trimmed* text, so `[Pos, Pos+len(text))` is the exact source span and is byte-identical to the skeleton emission.

**Position-field normalization:** any new AST position field must be zeroed by `internal/printer/corpus_test.go`'s `zeroSpans` (the faithfulness test normalizes positions) — same treatment `CloseNamePos` required.

### 3.2 Codegen — `CtrlMap` + clause `//line`
Two additions to skeleton generation, both `.gsx`-source-only:

**(a) Emission-offset tracking → `CtrlMap`.** While `emitProbes` emits a control-flow node, record the skeleton byte-offset where the clause/cond/code text begins (e.g. `sb.Len()` after writing `"for "`, after `"if "`, or before `WriteString(Code)`). `buildSkeleton` returns these offsets (per gsx node). After the skeleton string is parsed into the `token.FileSet`, convert each byte offset to a skeleton `token.Pos` via the skeleton file's `token.File.Pos(offset)`, and record:

```
CtrlMap[gsxNode] = ctrlRef{ clauseStart token.Pos; node goast.Node }
```

where `clauseStart` is the skeleton position of the clause text start, and `node` is the smallest skeleton AST node containing it (the `*goast.ForStmt`/`*goast.IfStmt`, or the enclosing statement for a code block) — used to scope `innermostIdent`. `CtrlMap` is retained in `PackageResult` (and `lsp.Package`) parallel to `ExprMap`.

**(b) Compensated `//line` before each clause.** Emit `//line <file>:<line>:<col>` anchored to `ClausePos`/`CondPos`/`CodePos` (compensated for the `"for "`/`"if "` prefix, exactly like `emitProbes`'s `//line` compensation for `_gsxuse(`), so positions *inside* the clause map back to `.gsx`. This is what makes go-to-def **to** a loop-variable binding land on its `.gsx` declaration, and makes any type-error in a clause report at the right `.gsx` column. After the clause body, reset the `//line` (as the existing control-flow emission already establishes a baseline) so the rest of the skeleton is unaffected.

Both additions are LSP-facing; they do not change generated `.x.go` output, so the **Phase-0 corpus equivalence gate stays green** (it compares `Files`, and the skeleton is not emitted output — but the `//line` change to the skeleton could shift a clause-line *diagnostic* column; if any `diagnostics.golden` moves, verify the new column points into the clause and regenerate).

### 3.3 LSP — extend the bridge
- **`exprNodeAtOffset`**: in addition to `Interp`/`ExprAttr`, return a control-flow node when the byte offset is in `[ClausePos, ClausePos+len(Clause))` (and Cond/Code). Return the node plus the gsx-source start offset of its clause text.
- **`handleDefinition`**: for a control-flow node, bridge identically to `Interp`:
  ```
  skelPos := CtrlMap[node].clauseStart + token.Pos(off - clauseGsxStartOffset)
  id := innermostIdent(CtrlMap[node].node, skelPos)
  obj := Info.Uses[id]  (or Info.Defs[id] for a binding like `post`)
  dp := pkg.Fset.Position(obj.Pos())   // //line → .gsx
  if dp.Filename == "" || strings.HasSuffix(dp.Filename, ".x.go") { return nil }  // existing guard
  return locationFor(dp)
  ```

## 4. Data flow (the reported bug)

`home.gsx:33` `{ for _, post := range props.Posts {` — cursor on `Posts`:
`exprNodeAtOffset` → the `ForMarkup` (cursor ∈ clause span) → `skelPos` via `CtrlMap` relative offset → `innermostIdent` finds skeleton `Posts` → `Info.Uses[Posts]` = the `Posts` field of the props struct → `pkg.Fset.Position` → the field's `.gsx` declaration. Cursor on `props` → the props param; cursor on `post` → `Info.Defs[post]` → the loop-var binding's `.gsx` position (via the clause `//line`).

## 5. Components & boundaries

| Unit | Responsibility | Depends on |
|---|---|---|
| `parser` clause positions | set `ClausePos`/`CondPos`/`CodePos` | source byte positions it already tracks |
| `emitProbes` emission-offset + clause `//line` | record skeleton offsets; emit clause `//line` | `sb` offsets; `ClausePos`/etc. |
| `harvest`/`Module.Package` `CtrlMap` build | offsets → `token.Pos` + containing node; retain in `PackageResult` | parsed skeleton file + fset |
| `exprNodeAtOffset` extension | recognize control-flow nodes at a cursor | the new AST positions |
| `handleDefinition` extension | bridge via `CtrlMap` → `innermostIdent` → `Info` | `CtrlMap`, `pkg.Info`, `pkg.Fset` |

## 6. Invariants

- **`.x.go`-independent:** resolution uses the in-memory skeleton's `Info` + `//line`-mapped positions; the `.x.go`-suffix guard rejects generated-code positions.
- **Byte-faithful:** gsx `Clause`/`Cond`/`Code` text == its skeleton emission (verbatim); the relative-offset bridge requires it. A regression test asserts the skeleton emits each clause byte-identically.
- **Additive to codegen output:** `Files` (generated `.x.go`) unchanged; corpus equivalence gate stays green.
- **Consistent with `Interp`:** the same `exprNodeAtOffset` → bridge → `innermostIdent` → `Info` → `.x.go`-guard → `Location` pipeline; `CtrlMap` is the control-flow analogue of `ExprMap`.

## 7. Testing (per [[gsx-syntax-change-test-coverage]])

- **Parser** (`parser/position_test.go`): `ClausePos`/`CondPos`/`CodePos` point at the first char of the clause/cond/code, including leading-whitespace and multi-line cases.
- **Codegen** (`internal/codegen`): a component with a `for`/`if`/`{{ }}` — assert `CtrlMap` has an entry per control-flow node whose `clauseStart` maps (via the skeleton fset/`//line`) to the `.gsx` clause start, and that the emitted clause is byte-identical to the source clause.
- **LSP unit** (`internal/lsp/definition_test.go`): go-to-def on `props` (→ param), `Posts` (→ field), `post` (→ its binding) in a for-clause; an identifier in an `if` cond; an identifier and a binding in a `{{ }}` code block.
- **e2e** (`gen/…_e2e_test.go`): the reported `{ for _, post := range props.Posts {` case end-to-end through `runLSP`, asserting resolution to the props field — with no `.x.go` on disk.
- **No regression:** corpus gate green; existing go-to-def/hover/references tests green; full `go test ./...` green.

## 8. Out of scope / follow-ups
- Hover and find-references in control-flow regions (the bridge enables them later).
- Go-to-def on the `else` keyword / structural tokens (only identifiers resolve).
