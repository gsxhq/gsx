# gsx LSP — hover parity for attribute names + cross-package component tags

**Status:** approved design (brainstormed 2026-06-26), ready for an implementation plan.

## 1. Goal & non-goals

**Goal.** Bring `textDocument/hover` to parity with the go-to-definition cases
shipped this session. Today `handleHover` (`internal/lsp/hover.go`) returns null
on the two positions gd now resolves:

- **H1** — hover on a component-invocation **attribute name** → the component
  parameter's type (e.g. `comments` on `<CommentsList comments={…}/>` →
  `comments []store.Comment`). Same-package *and* cross-package.
- **H2** — hover on a **dotted / cross-package component tag** (`<components.Input>`,
  `<layout.PublicShell>`) → the component's signature (e.g.
  `component Input(name, label, value, errMsg string)`).

The work reuses the gd resolvers (`componentAttrParamAt`'s walk,
`resolveCrossPkgComponent`, `findComponentDecl`, `splitDottedTag`,
`renderComponentSig`) and tidies them via a small shared-helper refactor.

**Non-goals.**
- **Custom `FieldMatcher`** attr→param mapping (the default `firstUpper` rule
  only, as in gd).
- **Hover content richness beyond the param decl.** H1 shows `name type`
  (gopls-style); no enclosing-component prefix.
- **`.x.go` reliance** — hover resolves from in-memory `.gsx`/Params, never the
  generated `.x.go` (same as gd). Cross-package (H2 + cross-pkg H1) needs the
  dependency to be importable (generated), like any Go import — only to
  type-resolve the qualifier; the rendered signature/type comes from the dep
  `.gsx`/its `Params` string.

## 2. Background — current hover dispatch

`handleHover` (`hover.go:16`) dispatches, in order:
1. `.go` → null (gopls owns it).
2. **Simple component tag** (`componentAtTag`, hover.go:141) → `renderComponentSig`.
   `componentAtTag` excludes dotted tags (hover.go:159:
   `strings.Contains(t, ".")`).
3. `exprNodeAtOffset` → `{…}` expression / pipeline hover (via `pipedTarget` or
   the skeleton ident/type).

So an attribute name hits no case (H1 gap), and a dotted tag is excluded at
step 2 (H2 gap). Existing reusable helpers: `renderComponentSig(c)` (hover.go:183,
renders `component [recv] Name(params)`), `markdownGo(s)`, `rangeForSpan(text,
start, end, enc)`.

The gd side (this session) provides: `componentAttrParamAt` (the attr-detection
walk + same/cross-package param resolution), `resolveCrossPkgComponent(pkg,
qualifier, name) (*Component, *token.FileSet, bool)`, `findComponentDecl(pkg,
name) *Component`, `splitDottedTag(tag) (qual, name string, ok bool)`,
`paramOffsetIn(params, attr) (int, bool)`, `firstUpper`.

## 3. Shared-helper refactor (gd + hover)

All in `internal/lsp`. The refactor is behavior-preserving for gd (its e2e
tests must stay green).

**3.1 `componentAttrAtOffset(pkg *Package, path string, off int) (tag, attr string, attrStart int, ok bool)`.**
The element/attr-detection walk currently inlined in `componentAttrParamAt`:
walk `pkg.Files[path]` for an `*Element` whose tag is a component
(`isComponentTag`) and whose named attr's span (`attr.Pos()` … `+len(name)`)
covers `off`. Returns the tag, the attr name, and the attr-name byte start (in
the edited file's `GSXFset` offset space). `componentAttrParamAt` is rewritten to
call this for detection.

**3.2 `resolveTagComponent(pkg *Package, tag string) (*gsxast.Component, *token.FileSet, bool)`.**
Unifies same-package and cross-package component resolution:

```go
func resolveTagComponent(pkg *Package, tag string) (*gsxast.Component, *token.FileSet, bool) {
    if qualifier, name, ok := splitDottedTag(tag); ok {
        return resolveCrossPkgComponent(pkg, qualifier, name) // dep .gsx + its FileSet
    }
    c := findComponentDecl(pkg, tag)
    if c == nil {
        return nil, nil, false
    }
    return c, pkg.GSXFset, true // same-package: positions in the package GSXFset
}
```

`componentAttrParamAt` collapses to: detect via `componentAttrAtOffset` → resolve
via `resolveTagComponent` → `paramOffsetIn` → `fset.Position(comp.ParamsPos +
rel)`. This removes its current duplicated same/cross-package branches (a net
simplification verified by the existing gd e2e tests).

## 4. H1 — hover on an attribute name → param type

Add `paramDeclIn(params, attr string) (string, bool)`: parse the raw `Params`
with `go/parser` (as `paramOffsetIn` does), find the field whose name matches
`attr` (`firstUpper(name) == firstUpper(attr)`), and return `name + " " +
types.ExprString(field.Type)` — e.g. `"comments []store.Comment"`,
`"featured bool"`, grouped `"b string"`. Parse failure / no match → `false`.

In `handleHover`, after the tag block (§5) and before the expression block, add:

```go
if tag, attr, attrStart, ok := componentAttrAtOffset(pkg, path, off); ok {
    if comp, _, ok := resolveTagComponent(pkg, tag); ok {
        if decl, ok := paramDeclIn(comp.Params, attr); ok {
            rng := rangeForSpan(text, attrStart, attrStart+len(attr), s.enc)
            return s.reply(f.ID, Hover{Contents: markdownGo(decl), Range: &rng})
        }
    }
}
```

This needs only the AST (no `pkg.Info`), so it answers even when type-checking
fails mid-edit — placed before the `pkg.Info == nil` guard.

## 5. H2 — hover on a dotted/cross-package tag → component signature

Generalize `componentAtTag` (hover.go:141) to also accept dotted component tags
and resolve them cross-package:

- In the walk, match an `*Element` whose tag is a component tag — simple
  (`isSimpleComponentTag`) **or** dotted (`splitDottedTag` ok) — with the cursor
  on the tag-name span (`el.Pos()+1` … `+len(tag)`); record `tag`, `nameStart`.
- Resolve the component via `resolveTagComponent(pkg, tag)` (replacing the
  inline same-package `findComponentDecl` loop). Return `(comp, nameStart, ok)`.

The existing hover call site (hover.go:38) is unchanged; `renderComponentSig(c)`
already renders any `*Component` (including a cross-package one — it reads only
`Recv`/`Name`/`Params` strings). The hover range stays the tag-name span in the
edited file. Result: `<components.Input>` hover →
`component Input(name, label, value, errMsg string)`.

## 6. Dispatch order (final `handleHover`)

1. `.go` → null.
2. **Component tag** (`componentAtTag`, now simple + dotted) → signature.
3. **Attribute name** (H1, new) → param type.
4. `pkg.Info == nil` guard → null.
5. Expression / pipeline (unchanged).

Steps 2–3 need only the AST (answer mid-edit); step 5 needs type info. The three
cursor regions (tag name / attr name / `{…}` value) are disjoint, so ordering is
unambiguous.

## 7. Invariants

- `internal/lsp` only; no `internal/codegen` import.
- `.x.go`-independent: signatures/types render from in-memory `.gsx` and the raw
  `Params` string; the dep `.x.go` is used (via gd's `resolveCrossPkgComponent`)
  only to locate the dep directory and to type-resolve the qualifier.
- Best-effort, never panics: every miss → null hover.
- gd behavior unchanged — the refactor (§3) is exercised by the existing gd e2e
  tests, which must stay green.

## 8. Testing (per [[gsx-syntax-change-test-coverage]])

`internal/lsp` unit tests (fast) for the new pure helper, plus `gen` e2e
(`testing.Short`-guarded) driving `textDocument/hover` over JSON-RPC like the
existing `gen/hover_e2e_test.go`.

- **`paramDeclIn` unit** (`internal/lsp`): `"comments []store.Comment"`/`comments`
  → `"comments []store.Comment"`; `"title string, featured bool"`/`featured` →
  `"featured bool"`; grouped `"a, b string"`/`b` → `"b string"`; firstUpper
  `"Title string"`/`title` → `"Title string"`; no-match → false; malformed
  `"]["` → false (no panic).
- **H1 e2e same-package** (`gen`, Short): hover on the `comments` attribute of
  `<CommentsList comments={…}/>` → hover contains `comments []store.Comment`,
  range on the attr name.
- **H1 e2e cross-package** (`gen`, Short): a two-package module (dep generated
  via `Generate`); hover on `name` of `<components.Input name=…/>` → contains
  `name string`.
- **H2 e2e cross-package** (`gen`, Short): hover on `<components.Input>` (the
  tag) → contains `component Input(`.
- **No regression**: existing `gen/hover_e2e_test.go` (simple tag, expr,
  pipeline hover) and the gd e2e (`TestDefinitionAttrParam`,
  `TestDefinitionCrossPkg*`) stay green — the refactor preserves gd behavior.
- Full `go test ./...` green.
