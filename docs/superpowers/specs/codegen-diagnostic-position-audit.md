# Codegen Diagnostic Position Audit

**Date:** 2026-06-22
**Author:** audit produced for testing-foundation increment (Task 3)
**Referenced by:** `2026-06-22-testing-foundation-p0-design.md` §4 R3; `2026-06-22-testing-architecture-review.md` G3/R3

## Gap statement

Parser diagnostics already carry `.gsx` line:col positions — the parser's `token.FileSet` maps
every AST node to a source location, and `diagnostics.golden` files under
`internal/corpus/testdata/cases/parser/` confirm this (e.g. `e01_mismatched_close.txtar` renders
`3:24: mismatched close tag </span>, expected </div>`). Codegen diagnostics carry **no** `.gsx`
position today. Every `fmt.Errorf("codegen: …")` site in `internal/codegen/` emits a bare
`codegen: …` message with no `line:col` prefix — users see which package and what went wrong,
but not the offending `.gsx` line. This is the known gap (G3 in the review doc). Threading
positions requires plumbing the relevant AST node's `.Pos()` into each error site — a real codegen
change deferred to the next increment. This document is the backlog artifact: it enumerates every
site, classifies it, and identifies which AST node should supply the position when positions are
threaded.

## Site table

All sites confirmed by reading the source file at the given line. The "has position" column
answers: does the emitted error message include a `.gsx` `line:col` derived from a
`token.Pos`/`fset.Position()`? None of the codegen error sites do — they produce bare
`codegen: …` strings. The "node for position" column names the AST node (from `ast/ast.go`)
whose `.Pos()` should be passed to `fset.Position()` when positions are threaded.

### analyze.go

| file:line | current message shape | has position | node for position |
|---|---|---|---|
| `analyze.go:83` | `codegen: load package: %w` | no | n/a — package-level error, no AST node |
| `analyze.go:86` | `codegen: no package found in %s` | no | n/a — package-level error |
| `analyze.go:90` | `codegen: type resolution failed: %s` | no | n/a — package-level error |
| `analyze.go:191` | `codegen: probing overlay path %s: %w` | no | n/a — filesystem error, no AST node |
| `analyze.go:1086` | `codegen: empty method-component receiver` | no | `*ast.Component` (caller has `c`; pass `c.Pos()`) |
| `analyze.go:1091` | `codegen: parse method-component receiver %q: %w` | no | `*ast.Component` |
| `analyze.go:1095` | `codegen: invalid method-component receiver %q` | no | `*ast.Component` |
| `analyze.go:1099` | `codegen: method component receiver must be named, e.g. (p T) — got %q` | no | `*ast.Component` |
| `analyze.go:1118` | `codegen: method-component receiver var %q is reserved (ambient context)` | no | `*ast.Component` |
| `analyze.go:1121` | `codegen: method-component receiver var %q uses the reserved _gsx prefix` | no | `*ast.Component` |
| `analyze.go:1124` | `codegen: method-component receiver var %q is reserved (shadows a generated import)` | no | `*ast.Component` |
| `analyze.go:1141` | `codegen: parse params %q: %w` | no | `*ast.Component` |
| `analyze.go:1167` | `codegen: param name %q is reserved (ambient context)` | no | `*ast.Component` (param text position is not tracked in AST; use component pos as best available) |
| `analyze.go:1170` | `codegen: param name %q is reserved (implicit children slot)` | no | `*ast.Component` |
| `analyze.go:1173` | `codegen: param name %q is reserved (implicit fallthrough attributes)` | no | `*ast.Component` |
| `analyze.go:1176` | `codegen: param name %q uses the reserved _gsx prefix` | no | `*ast.Component` |
| `analyze.go:1184` | `codegen: param name %q is reserved (shadows a generated import)` | no | `*ast.Component` |
| `analyze.go:1216` | `codegen: invalid Go in pass-through block: %w` | no | `*ast.GoBlock` (caller has the `GoBlock` in scope at call site in `buildSkeleton`) |

### emit.go

| file:line | current message shape | has position | node for position |
|---|---|---|---|
| `emit.go:505` | `codegen spike: unsupported markup node %T` | no | the `ast.Markup` `n` — call `n.Pos()` |
| `emit.go:528` | `codegen: could not resolve type of pipeline %q` | no | `*ast.Interp` `n` — `n.Pos()` |
| `emit.go:533` | `codegen spike: \`?\` try-marker not supported yet` | no | `*ast.Interp` `n` — `n.Pos()` |
| `emit.go:537` | `codegen spike: could not resolve type of interpolation %q` | no | `*ast.Interp` `n` — `n.Pos()` |
| `emit.go:542` | `codegen: interpolation %q returns %s; only (T, error) is supported` | no | `*ast.Interp` `n` — `n.Pos()` |
| `emit.go:594` | `codegen: interpolation %q has type %s; not a renderable type` | no | `*ast.Interp` `n` (caller is `genInterp` which has `n`) |
| `emit.go:614` | `codegen: <style> body may contain only text and ${ } interpolations, got %T` | no | the `ast.Markup` `n` in `genStyleChild` — `n.Pos()` |
| `emit.go:621` | `codegen: \`?\` try-marker not supported in <style> interpolation yet` | no | `*ast.Interp` `n` in `emitCSSInterp` — `n.Pos()` |
| `emit.go:624` | `codegen: pipeline stages not supported in <style> interpolation yet` | no | `*ast.Interp` `n` — `n.Pos()` |
| `emit.go:628` | `codegen: could not resolve type of <style> interpolation %q` | no | `*ast.Interp` `n` — `n.Pos()` |
| `emit.go:633` | `codegen: <style> interpolation %q returns %s; only (T, error) is supported` | no | `*ast.Interp` `n` — `n.Pos()` |
| `emit.go:666` | `codegen: value of type %s not renderable in CSS context (need string/number/Stringer or gsx.RawCSS)` | no | `*ast.Interp` `n` (caller `emitCSSInterp` has it) |
| `emit.go:723` | `codegen: unknown attribute %T` | no | the `ast.Attr` `a` in `emitAttr` — `a.Pos()` |
| `emit.go:835` | `codegen: expr value in JS/event-handler context (%q) is unsafe; …` | no | `*ast.ExprAttr` `a` — `a.Pos()` |
| `emit.go:837` | `codegen: expr value in CSS context (%q) is unsafe; …` | no | `*ast.ExprAttr` `a` — `a.Pos()` |
| `emit.go:841` | `codegen: \`?\` try-marker in attribute %q not supported yet` | no | `*ast.ExprAttr` `a` — `a.Pos()` |
| `emit.go:860` | `codegen: could not resolve type of attribute %q value %q` | no | `*ast.ExprAttr` `a` — `a.Pos()` |
| `emit.go:924` | `codegen: attribute value type %s not supported (string/number/bool/Stringer only)` | no | the `ast.ExprAttr` in scope at `emitAttrValue` call site |
| `emit.go:1280` | `codegen: pipeline/\`?\` on child-component prop %q (<%s>) not supported yet` | no | `*ast.ExprAttr` `t` in `childPropsLiteral` — `t.Pos()` |
| `emit.go:1285` | `codegen: pipeline/\`?\` on child-component fallthrough attr %q (<%s>) not supported yet` | no | `*ast.ExprAttr` `t` — `t.Pos()` |
| `emit.go:1313` | `codegen: class attribute on a component (<%s>) not supported yet` | no | `*ast.ClassAttr` `t` — `t.Pos()` |
| `emit.go:1315` | `codegen: spread attribute on a component (<%s>) not supported yet` | no | `*ast.SpreadAttr` `t` — `t.Pos()` |
| `emit.go:1317` | `codegen: conditional attribute on a component (<%s>) not supported yet` | no | `*ast.CondAttr` `t` — `t.Pos()` |
| `emit.go:1319` | `codegen: unknown attribute %T on component (<%s>)` | no | the `ast.Attr` `a` — `a.Pos()` |
| `emit.go:1371` | `codegen: non-identifier attribute %q on component %s (attribute fallthrough not supported yet)` | no | `*ast.ExprAttr`/`*ast.StaticAttr` in scope at call site |

### filters.go

| file:line | current message shape | has position | node for position |
|---|---|---|---|
| `filters.go:37` | `codegen: \`?\` try-marker on filter %q not supported yet` | no | the containing `*ast.Interp` or `*ast.ExprAttr` (callers `genInterp`/`emitExprAttr` have the node; `lowerPipe` itself has no AST node — must be threaded from callers) |
| `filters.go:41` | `codegen: unknown filter %q` | no | same as above |
| `filters.go:47` | `codegen: filter %q takes no arguments` | no | same as above |
| `filters.go:52` | `codegen: filter %q requires arguments` | no | same as above |
| `filters.go:182` | `codegen: load filter packages: %w` | no | n/a — package-level error |
| `filters.go:196` | `codegen: filter package %q not found in %s` | no | n/a — package-level error |
| `filters.go:199` | `codegen: filter package %q type resolution failed: %s` | no | n/a — package-level error |
| `filters.go:202` | `codegen: filter package %q has no type information` | no | n/a — package-level error |

### batch.go

| file:line | current message shape | has position | node for position |
|---|---|---|---|
| `batch.go:42` | `codegen: abs(%s): %w` | no | n/a — filesystem/path error |
| `batch.go:110` | `codegen: load filter table: %w` | no | n/a — package-level error |
| `batch.go:177` | `codegen: load packages: %w` | no | n/a — package-level error |
| `batch.go:212` | `codegen: type resolution failed: %s` | no | n/a — package-level error |

## Summary counts

- **Total diagnostic sites:** 55
- **Has position (yes):** 0
- **No position:** 55
  - User-facing (`.gsx` source location is meaningful and should be threaded): **39** (all analyze.go component/param/recv validation sites + all emit.go AST-node-aware sites + filters.go pipeline/filter sites)
  - Infrastructure errors (package-level, filesystem; no AST node exists): **16** (batch.go, filters.go package-loader, analyze.go package-loader sites)

## Spot-check evidence

**Row analyze.go:1167** (`codegen: param name "ctx" is reserved`): confirmed in
`internal/corpus/testdata/cases/diagnostics/reserved_param_ctx.txtar` — `diagnostics.golden`
reads `codegen: param name "ctx" is reserved (ambient context)` with no `line:col`. The function
`checkReservedParams` (analyze.go:1164) receives only `params []param` (name+type strings), with
no `*ast.Component` or `fset` in scope. Threading the position requires the caller `genComponent`
(emit.go:134, which has `c *ast.Component` and `fset *token.FileSet`) to pass position info down,
or `checkReservedParams` to accept a component pos argument.

**Row emit.go:835** (`codegen: expr value in JS/event-handler context`): confirmed in
`internal/corpus/testdata/cases/security/js_rejected_onclick.txtar` — `diagnostics.golden` reads
`cases/security_js_rejected_onclick/input.gsx: codegen: expr value in JS/event-handler context
("onclick") is unsafe; needs a safe type via \`|> js\`…` — note the *path* prefix is added by the
corpus harness's error renderer, not by the `fmt.Errorf` itself. The error carries no `line:col`.
`emitExprAttr` (emit.go:829) has `a *ast.ExprAttr` in scope with `a.Pos()` available; threading
requires also accepting `fset *token.FileSet` (currently absent from `emitExprAttr`'s signature)
or computing position at the call site in `emitAttr`.

**Row emit.go:614** (`codegen: <style> body may contain only text …`): `genStyleChild`
(emit.go:606) already accepts `fset *token.FileSet` and the `ast.Markup` node `n`, both in scope.
The position is immediately available — this is the simplest site to thread.

**Row analyze.go:83** (`codegen: load package: %w`): infrastructure error from `packages.Load`
at the go/packages level — no `.gsx` AST node exists at this point. Classified "n/a" for
position, confirmed by reading `analyze.go:75–91`: error fires before any gsx parsing occurs.

## Confirmed: diagnostics.golden format

Parser `diagnostics.golden` format: `line:col: message` (bare, relative) — e.g. `3:24: mismatched
close tag </span>`.

Codegen `diagnostics.golden` format: `codegen: message` (bare, no position) — confirmed for all 9
cases under `internal/corpus/testdata/cases/diagnostics/`.

Security cases prepend the path via corpus harness, but still no `line:col`.

## Next increment: threading positions

For the **next increment** (codegen-position-threading):

1. **Thread positions into the 39 user-facing "no" rows.** Each `fmt.Errorf("codegen: …")` site
   that has an AST node in scope needs its message changed to
   `fmt.Errorf("%s: codegen: …", fset.Position(node.Pos()))`. Sites where `fset` is not currently
   in scope (e.g. `emitAttr`, `emitExprAttr`, `checkReservedParams`) need `fset` added to their
   signatures or position threaded via an error wrapper at the call site.

2. **Regenerate affected `diagnostics.golden` files.** Every corpus case under
   `testdata/cases/diagnostics/` and `testdata/cases/security/` that exercises a user-facing
   codegen error will need its golden regenerated via `-update` once messages gain `line:col`.
   Codegen unit tests in `internal/codegen/*_test.go` that assert on error substrings will also
   need updating.

3. **Build the `//~ ERROR` inline-annotation harness as consumer.** Per the design doc
   (`2026-06-22-testing-foundation-p0-design.md` §7):
   - The harness must scan the **raw input text**, not the parsed AST — error-case inputs are
     deliberately malformed and may not parse cleanly.
   - The annotation check must coexist with `diagnostics.golden` without double-pinning the same
     fact (e.g. use annotations for line-accuracy, golden for exact message text, or drop the
     golden in favour of annotations for annotated cases).
   - `PipeStage` has no `span` (it is a plain value struct in `ast.go:189`); filter-site positions
     must be threaded from the containing `Interp`/`ExprAttr` node at `lowerPipe`'s call sites.
