# gsx LSP — Hover (`textDocument/hover`)

## 1. Goal & non-goals

**Goal.** Hovering a symbol in a `.gsx` file shows its Go type/signature, rendered
like gopls (a fenced `go` code block): a variable, field, func, or component
declaration, or — when the cursor is not on a named symbol — the type of the
expression under it.

**Non-goals (v1).**
- `.go` files — gopls owns hover there (gsx returns null, same stance as
  diagnostics/formatting).
- Doc comments (the slim analysis retains no Go ASTs) — a follow-up.
- Raw-Go symbols inside a `GoChunk`, and the param *name* in a component
  signature — follow-ups.
- Dotted component tags (`<ui.Button/>`) and cross-package components — follow-ups
  (same boundary as the shipped go-to-definition D2).

## 2. Background — what hover reuses

Hover is the go-to-definition reverse-mapper with a different payload: instead of
the symbol's *location*, return its *type*. It reads only already-retained
analysis (`internal/lsp.Package`): `Info *types.Info`, `ExprMap map[gsxast.Node]ast.Expr`
(gsx `Interp`/`ExprAttr` → skeleton Go expression), `Files map[string]*gsxast.File`
(parsed gsx ASTs), `GSXFset`. The existing helpers in `internal/lsp/definition.go`
are reused verbatim:
- `exprNodeAtOffset(pkg, path, off) (gsxast.Node, token.Pos)` — the `Interp`/`ExprAttr`
  whose Go-expression span covers the byte offset.
- `hasPipeStages(node) bool` — whether the node carries `|>` stages (the
  byte-identical bridge does not hold for these).
- `innermostIdent(skelExpr, skelPos) *ast.Ident` — the identifier at a skeleton
  position.
- `componentTagDeclAt`-style tag detection — for the component-tag case.

No new analysis, no codegen change beyond exposing one already-computed value
(§4).

## 3. What hover renders

A gopls-style fenced block. Given the cursor:

| Cursor on | Source | Rendered (markdown) |
|---|---|---|
| an identifier resolving to an object (`u`, `Name` in `u.Name`, `Greeting`) | `types.ObjectString(obj, qf)` | ` ```go\nvar u User\n``` ` / ` ```go\nfield Name string\n``` ` / ` ```go\nfunc Greeting(name string) string\n``` ` |
| an expression with no named symbol at the exact offset | `types.TypeString(Info.Types[skelExpr].Type, qf)` | ` ```go\nstring\n``` ` |
| a component tag `<Card/>` | the gsx `Component` AST node | ` ```go\ncomponent Card(title string)\n``` ` |

`qf` is a `types.Qualifier` that renders the analyzed package's own types
unqualified and imported types by package name: `func(p *types.Package) string {
if p == pkgTypes { return "" }; return p.Name() }` → `User` (same package),
`store.User` (imported).

Component signature is rendered from the `Component` node directly:
`"component " + recvClause + Name + "(" + Params + ")"`, where `recvClause` is
`Recv + " "` when `Recv != ""` (e.g. `component (p UsersPage) Row(...)`).

## 4. Components & changes

- **`internal/lsp/protocol.go`** — add:
  - `Hover struct { Contents MarkupContent json:"contents"; Range *Range json:"range,omitempty" }`
  - `MarkupContent struct { Kind string json:"kind"; Value string json:"value" }` (Kind = `"markdown"`).
  - `HoverProvider bool json:"hoverProvider"` on `serverCapabilities`.
  - Hover request params reuse the existing `textDocumentPositionParams`.
- **`internal/lsp/server.go`** — dispatch `case "textDocument/hover": return s.handleHover(f)`;
  set `HoverProvider: true` in `handleInitialize`.
- **`internal/lsp/hover.go`** (new) — `handleHover` (§5) plus small helpers:
  `componentAtTag(pkg, path, off) (*gsxast.Component, bool)` (tag detection →
  matching `Component` node by name across `pkg.Files`), `renderObject`,
  `renderType`, `renderComponentSig`, and `markdownGo(s string) MarkupContent`.
- **`internal/lsp/analysis.go`** — add `Types *types.Package` to `Package`
  (the analyzed package, for `qf`).
- **`internal/codegen/batch.go`** — add `Types *types.Package` to `PackageResult`
  and set `res.Types = pkg.Types` next to `res.Info = pkg.TypesInfo`.
- **`gen/lsp.go`** — copy `pr.Types` onto the returned `lsp.Package`.

## 5. `handleHover` flow

```
parse params → path = uriToPath(uri)
if path ends ".go"          → reply null         (gopls owns .go)
text, ok := docs.text(uri)  ; if !ok → null
pkg := s.pkgs[dir(path)]    ; if pkg==nil || pkg.Info==nil → null
off := byteOffsetForPosition(text, line, char, enc)

// component tag
if c, ok := componentAtTag(pkg, path, off):
    return Hover{ markdownGo(renderComponentSig(c)), Range: tag-name span }

// expression
node, exprPos := exprNodeAtOffset(pkg, path, off) ; if node==nil → null
if hasPipeStages(node) → null                       // mirror definition (v1)
skel := pkg.ExprMap[node] ; if skel==nil → null
skelPos := skel.Pos() + (off - GSXFset.Position(exprPos).Offset)

if id := innermostIdent(skel, skelPos); id != nil:
    obj := Info.Uses[id]; if obj==nil { obj = Info.Defs[id] }
    if obj != nil → return Hover{ markdownGo(renderObject(obj)), Range: ident span }

// fall back to the whole expression's type
if t := Info.Types[skel].Type; t != nil → return Hover{ markdownGo(renderType(t)), Range: expr span }
return null
```

`Range` (the `.gsx` span the editor highlights), per case:
- **identifier:** the identifier's own `.gsx` span — start byte
  `exprStart + (id.Pos() - skel.Pos())`, length `len(id.Name)` (`exprStart` is the
  gsx-fset offset of `exprPos`; the gsx and skeleton expression texts are
  byte-identical, so the delta is exact).
- **whole-expression fallback:** the node's expression span
  `[exprPos, exprPos+len(Expr))`.
- **component tag:** the tag-name span `[elemOff+1, +len(Tag))`.

`Range` is optional in LSP — if computing it is ever ambiguous, omit it rather
than emit a wrong span.

**No `.x.go` guard** (unlike definition): hover shows a type and never navigates,
so a synthesized binding (`ctx`, a param local) resolving to a real type is
correct and useful, not a leak.

## 6. Error handling / edge cases

- No package, no `Info`, no expr/tag at cursor, nil object, nil type → null.
- Piped expression (`{ x |> f }`) → null in v1: the node lowers to a wrapped call,
  so the byte-identical relative-offset bridge that maps the cursor to the right
  skeleton identifier does not hold (the same reason definition returns null).
- A component tag whose component is not found in the package (dotted/cross-package)
  → fall through to the expression path (which yields null for a bare tag) → null.
- Hover must never panic on a malformed/mid-edit buffer: every lookup is
  nil-checked and returns null on miss.

## 7. Testing

End-to-end through the real server (`gen` package, `runLSP`, mirroring the
existing definition/formatting e2e tests; module-resolution tests guarded by
`testing.Short()`):

- **Field:** hover on `Name` in `{ u.Name }` → contains ` ```go ` and `field Name string`.
- **Func:** hover on `Greeting` in `{ Greeting(u.Email) }` → `func Greeting(name string) string`.
- **Var/param:** hover on `u` in `{ u.Name }` → `var u User`.
- **Whole-expression type:** hover on a position inside an expression that is not an
  identifier (e.g. a string literal / operator) → the expression's type (`string`).
- **Component tag:** hover on `Card` in `<Card title="hi"/>` → `component Card(title string)`.
- **`.go` file → null**; **non-expression position (plain markup text) → null**;
  **piped expression `{ x |> upper }` → null** and never leaks a `.x.go` path.
- **Capability:** `initialize` response advertises `"hoverProvider":true`.
- Unit: `renderComponentSig` for a function component and a method component
  (`Recv != ""`); `renderObject`/`renderType` qualifier — current-package type
  unqualified, imported type as `pkg.Name`.

## 8. Risks

- **Qualifier needs the analyzed `*types.Package`.** Exposing `pkg.Types` is a
  one-line addition mirroring the existing `pkg.TypesInfo` retention; it does not
  enlarge the slim cross-index (it is already loaded for the analysis).
- **Relative-offset bridge** assumptions are identical to definition's (already
  proven); piped nodes are excluded for the same reason. No new bridge risk.
- **Column/encoding:** `Range` is encoded against the `.gsx` text via the existing
  `byteOffsetForPosition`/`charForByteCol` helpers; an off span only mislocates the
  editor highlight, never the content. Omit `Range` rather than emit a wrong one.

## 9. What ships

In an editor, hovering a Go expression or a component tag in a `.gsx` file shows
its type/signature — `var u User`, `field Name string`, `func Greeting(name
string) string`, `component Card(title string)` — purely from the retained
in-memory analysis, with `.go` hover left to gopls.
