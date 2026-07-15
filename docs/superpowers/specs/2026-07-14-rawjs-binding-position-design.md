# `gsx.RawJS` holes in JS binding/lvalue position — design

**Goal:** Let a `@{ … }` hole whose Go type is `gsx.RawJS` appear in a JavaScript
binding/lvalue position inside a `js` literal or `<script>` block (assignment
target, declaration name, member name after `.`, property key), splicing its
bytes verbatim. Bare (non-`RawJS`) holes in those positions stay rejected.

## Motivation

Expression-valued `js`/`css` literals were introduced so authors write embedded
JS with holes inline in Go blocks instead of hand-rolling
`gsx.RawJS(fmt.Sprintf(…))`. But the JS-context classifier
(`internal/jsx/jsx.go`) rejects a hole in any position it does not recognize as
a *value* position — including the left side of an assignment. So an Alpine
handler that writes to a dynamic model path cannot be expressed as a `js`
literal today:

```gsx
// Real code in one-learning-gsx/ui/common_edit_components.gsx — the only
// way to write it today, because `xModelPath = foundId` puts the path on the
// LHS of an assignment:
{Key: "@change", Value: gsx.RawJS(fmt.Sprintf(`
const val = $event.target.value;
…
%s = foundId;
hasError = false;
`, xModelPath))},
```

The value-position uses in the same file (`getLabel(@{gsx.RawJS(xModelPath)})`,
a bare `@{gsx.RawJS(xModelPath)}` `:value`) already convert cleanly to `js`
literals and render byte-identically. Only the assignment-target hole is
blocked. This design closes that gap.

## Current behavior (grounding)

`classifyHole` (`internal/jsx/jsx.go`) runs at *classify* time — during
`jsx.ResolveScripts` / `jsx.ResolveEmbedded`, before Go types are resolved. For
a hole that is its own token (`ownToken`), it classifies by the previous
significant JS token `prevSig`:

- `isValuePosition(prevSig)` → `ast.JSCtxValue` (accepted).
- otherwise → **rejected immediately** with diagnostic `jsx-identifier-position`
  ("… not a safe JavaScript value position (it looks like an
  identifier/binding) …").

`isValuePosition` enumerates value-introducing tokens: binary/unary operators
(except `++`/`--`/`?.`), `(`, `[`, `,`, `:`, `?`, `=>`, `…`, template-substitution
start, and expression-introducing keywords (`return`, `typeof`, `new`, `case`,
`throw`, …). Everything else — statement start (after `;`, `{`, newline),
after `.`, after `let`/`const`/`var`/`function`/`class` — is "binding".

At *emit* time the hole's resolved Go type **is** available:
`embeddedHoleExpr(…) (expr string, typ types.Type, ok bool)` already returns it,
and `embeddedJSValueExpr` switches on `s.JSCtx` to pick the escaper
(`EscapeJSVal` for `JSCtxValue`; `EscapeJSStr`/`Tmpl`/`Regexp` for the literal
contexts). `gsx.RawJS` is a named type (`type RawJS string`) in
`github.com/gsxhq/gsx`; `EscapeJSVal`'s runtime `jsValEscaper` already emits a
`RawJS` value verbatim. Static named-type detection has direct precedents:
`isRawCSS` (`analyze.go`) and `isRawURL` (`emit.go`).

## Design

**Defer the binding-position decision from classify time to emit time, and gate
it on the hole's Go type.**

### 1. New JS context: `ast.JSCtxBinding`

Add `JSCtxBinding` to the `JSCtx` enum. It means "this hole is its own token in a
non-value position; whether it is legal depends on its Go type, decided at
emit."

### 2. Classifier defers instead of rejecting

In `classifyHole`, the `ownToken` branch, when `!isValuePosition(prevSig)`:
set `h.ctx = ast.JSCtxBinding`, `h.resolved = true`, and return `true` (no
diagnostic). The substring branches (string/template/regex/comment) are
unchanged. No hole is rejected at classify time anymore for position reasons;
position legality is now always adjudicated where the type is known.

### 3. Static `isRawJS`

Add `isRawJS(t types.Type) bool` to `internal/codegen`, mirroring `isRawCSS`
exactly (`types.Unalias` → `*types.Named` → `Obj().Name() == "RawJS"` &&
`Obj().Pkg().Path() == "github.com/gsxhq/gsx"`).

### 4. Emit handles `JSCtxBinding` (both JS emit paths)

Wherever a JS hole is lowered — the attribute / expression-literal path
(`embeddedJSValueExpr`) **and** the `<script>`-block interp path — add a
`case ast.JSCtxBinding`:

- Resolve `(expr, typ)` via `embeddedHoleExpr`.
- If `isRawJS(typ)` → splice **verbatim**: emit `string(expr)` into the concat
  (RawJS's underlying type is `string`; no escaping — the author has vouched for
  the bytes, exactly as a value-position `RawJS` already renders verbatim
  through `jsValEscaper`).
- Else → `bag.Errorf(…, "jsx-binding-position", …)` with the reworded diagnostic
  (below). Fails the build; nothing is spliced.

The `exprPos` / per-hole-temp materialization logic that already wraps
`JSCtxValue` applies unchanged (a `JSCtxBinding` RawJS splice is a plain string
concat term, same shape as the value path).

### 5. Diagnostic wording

Non-`RawJS` hole in a binding position, emitted at emit time:

> `jsx: @{ } here is a JavaScript binding/lvalue position (assignment target,`
> `declaration or member name); only a gsx.RawJS value may be spliced here —`
> `wrap it as gsx.RawJS(...) if the bytes are trusted, or use it where a value`
> `is expected.`

The old classify-time `jsx-identifier-position` diagnostic is removed (its
scenario now surfaces from emit with the wording above).

## Scope

- **Uniform:** a `RawJS`-typed hole is allowed in **any** position the
  classifier currently rejects — assignment LHS, member name (`foo.@{…}`),
  declaration name (`let @{…}`), property key. `RawJS` means "trust these
  bytes," independent of position; one code path, no per-position allowlist.
- **JS only.** CSS has no lvalue/binding concept — every `css` hole is a value
  filtered through `FilterCSS`; `RawCSS` is untouched. `f`` (EmbeddedText) has
  no JS context.
- **Type gate preserved.** Bare strings, numbers, Stringers, etc. in a binding
  position still error. The only way in is an explicit `gsx.RawJS`, keeping the
  trust decision visible at the call site — the same guarantee the classifier
  gave, now type-aware instead of position-blind.

## Contexts requiring corpus coverage (per project rule)

New syntax/semantics valid in multiple contexts needs a case per context:

1. `<script>` block: `<script>@{gsx.RawJS(path)} = 1;</script>`.
2. `js` attribute: `@change=js\`@{gsx.RawJS(path)} = foundId;\``.
3. Expression-valued `js` literal in a Go block (the `displaySyncAttrs`
   shape): `{Key: "@change", Value: js\`… @{gsx.RawJS(path)} = foundId; …\`}`.
4. Uniform-scope positions: member name `foo.@{gsx.RawJS(p)}`, declaration name
   `let @{gsx.RawJS(n)} = 1`.
5. Rejection: a **bare string** hole in binding position → `jsx-binding-position`
   diagnostic (at least one context; the negative is the guard's whole point).
6. Update `internal/corpus/testdata/cases/script/interp_identifier_rejected.txtar`
   — the rejection is now emit-time and reworded, and fires only for non-RawJS.

## Verification points

- **Differential:** each accepted case renders byte-identical to the
  `gsx.RawJS(concat)` form it replaces (mirrors `rawjs_passthrough_value`).
- **LSP:** the `jsx-binding-position` diagnostic must surface in the language
  server. It is now an emit-time `bag.Errorf`; confirm the analyzer's JS-emit
  path is exercised for `<script>`/attr/expression holes so LSP reports it
  (many gsx diagnostics are already emit-time — verify this one joins them).
- **No sibling grammar changes:** `@{ … }` syntax is unchanged; tree-sitter-gsx
  and vscode-gsx need no update. Docs (the `js` literal holes section) and this
  spec do.

## Non-goals

- No new hole syntax (no `@!{ }` raw variant) — the `RawJS` type is the opt-in.
- No relaxation for CSS or non-JS embedded contexts.
- No change to value-position or string/template/regex-position escaping.
