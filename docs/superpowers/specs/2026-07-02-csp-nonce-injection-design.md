# CSP nonce threading — runtime context + codegen injection

**Date:** 2026-07-02
**Status:** approved (supersedes the earlier doc-only decision; ROADMAP item 7)

## Decision

gsx ships a *minimal* nonce facility, framed strictly as a templating feature:

1. **Runtime context API** (root package, stdlib-only):
   - `gsx.WithNonce(ctx context.Context, nonce string) context.Context`
   - `gsx.NonceFromContext(ctx context.Context) string` (returns `""` when absent)
   - `(*gsx.Writer).Nonce(ctx context.Context)` — writes ` nonce="<attr-escaped>"`
     when the context carries a non-empty nonce, otherwise writes nothing.
2. **Codegen injection**: generated code automatically adds the nonce attribute to
   every `<script>` and `<style>` open tag it emits, honoring the precedence rules
   below.

Explicitly **out of scope** (server concerns, not templating):

- nonce *generation* (crypto/rand is the app's job),
- `Content-Security-Policy` header/policy construction,
- middleware or `NonceAttr`-style server helpers,
- `<link rel="stylesheet">` nonce injection (possible follow-up; scope is the two
  tags gsx already special-cases for body context).

Rationale: "given render context, emit correct attributes on generated markup" is
a templating responsibility. Everything upstream of that (choosing the nonce,
writing the header) stays with the server. Naming: `WithNonce`, not
`WithCSPNonce` — docs scope it to CSP nonce attributes.

## Semantics

- **Which tags**: native `<script>` and `<style>` elements (tag matched
  case-insensitively), in *both* live element-emission paths:
  plain elements (`genNode`) and elements carrying the `{ attrs... }` bag
  spread (`emitManualSpreadElement` → `emitFallthroughAttrs`).
  (`emitRootElement` — the old AUTO fallthrough — is dead code since the
  explicit-forwarding redesign and is left untouched.) *All* `<script>` tags
  qualify — external `src=…`, `type="application/json"` data islands, modules —
  adding a nonce is harmless and consistent; CSP simply ignores it where
  irrelevant.
- **Injection point**: after all authored attributes, immediately before the
  tag-closing `>` (or `/>`).
- **No nonce in context / empty nonce**: `Writer.Nonce` writes zero bytes; output
  is byte-identical to today.
- **Escaping**: the nonce value is written via `AttrValue` (HTML attribute
  entity escaping), like any quoted attribute value.
- **Components**: `<Component/>` tags are unaffected; a component's own
  `<script>`/`<style>` tags get injection inside *its* generated code, and the
  render context flows through `Render(ctx, w)` unchanged.
- **Raw HTML**: `gsx.RawHTML` (and any markup gsx does not itself emit) is
  untouched — gsx only decorates tags it owns.

### Precedence — author always wins

1. **Explicit attr (compile time)**: if the element carries *any* authored
   attribute named `nonce` (case-insensitive; `StaticAttr`, `ExprAttr`,
   `BoolAttr`, or one appearing in **any branch** of a `CondAttr`, recursively),
   auto-injection is **disabled for that element entirely**. Even the branch
   where a conditional nonce is absent gets no auto value — predictable and
   simple; an author writing `nonce` anywhere has taken ownership.
2. **Spread bags (runtime)**: if the element has spread attributes — inline
   `{ expr... }` spreads (including ones nested in `CondAttr` branches) or the
   manual `{ attrs... }` bag — injection is guarded at runtime:

   ```go
   if !attrs.Has("nonce") && !_gsxv0.Has("nonce") {
       _gsxgw.Nonce(ctx)
   }
   ```

   Inline spread expressions are captured in **hoisted temporaries**
   (`var _gsxvN gsx.Attrs` declared before the attr emission, assigned at the
   spread's emission point) so each expression is evaluated exactly once and
   the guard also works for a spread inside a cond-attr branch: an untaken
   branch leaves the temp `nil`, and a nil `Attrs.Has` is false. The manual
   bag (`attrs`) is a side-effect-free identifier and is used directly.
   Note the guard uses `Attrs.Has` (exact key match), consistent with `Spread`'s
   own key handling; `Spread` drops structurally invalid keys, but "nonce" is
   always valid, so `Has` and the spread's emission agree.

## Generated-code shape

Plain script tag:

```go
_gsxgw.S("<script")
_gsxgw.Nonce(ctx)
_gsxgw.S(">…")
```

With an inline spread:

```go
_gsxgw.S("<script")
var _gsxv0 gsx.Attrs
_gsxv0 = someBagExpr()
_gsxgw.Spread(ctx, _gsxv0)
if !_gsxv0.Has("nonce") {
    _gsxgw.Nonce(ctx)
}
_gsxgw.S(">…")
```

`<script { attrs... }>` (MANUAL fallthrough):

```go
_gsxgw.S("<script")
// …emitFallthroughAttrs (class/style merge, guarded scalars, bag spread)…
if !attrs.Has("nonce") {
    _gsxgw.Nonce(ctx)
}
_gsxgw.S(">…")
```

## Costs

- Static coalescing (`coalesceStaticWrites`) no longer merges across a
  `<script>`/`<style>` open tag: the run splits into
  `S("<script") · Nonce(ctx) · S(">…")`. One extra writer call plus one
  `ctx.Value` lookup per script/style tag per render. Pages carry few such
  tags; no config knob is added (the check is a no-op branch without a nonce).
- Every generated file with a script/style tag changes → bump
  `internal/codegen/version.go` so the incremental cache invalidates
  (`codegenIdentity` in `gen/cachekey.go` folds it in).

## Testing

Corpus (`internal/corpus/testdata/cases/nonce/`) — the ctx is supplied per-case
via a support-file helper (no harness change):

```go
-- support.go --
func withTestNonce(n gsx.Node) gsx.Node {
	return gsx.Func(func(ctx context.Context, w io.Writer) error {
		return n.Render(gsx.WithNonce(ctx, "r4nd0m"), w)
	})
}
-- invoke --
withTestNonce(Page(PageProps{…}))
```

Cases (each pins `generated.x.go.golden` + `render.golden`):

- `script_basic`, `style_basic` — plain injection.
- `no_nonce_ctx` — same template rendered with `context.Background()`:
  attribute absent, output byte-identical to pre-feature.
- `script_src`, `script_json_island` — non-inline scripts still injected.
- `explicit_static_wins`, `explicit_expr_wins` — authored `nonce` suppresses
  injection (no duplicate attribute).
- `cond_attr_nonce` — `{ if c { nonce="…" } }` disables injection on both
  branches.
- `spread_with_nonce`, `spread_without_nonce` — runtime guard behavior; the
  spread-expr temp evaluates once (case uses a counting helper).
- `spread_in_cond_attr` — `{ if c { { extra... } } }` on a `<script>`: hoisted
  temp guard works on both branches.
- `manual_spread`, `manual_spread_with_nonce` — `<script { attrs... }>`,
  caller passing and not passing a `nonce` key.
- A non-script/style control (`<div>`) showing no injection.

Runtime unit tests (root package, `nonce_test.go`): `WithNonce`/
`NonceFromContext` round-trip and absence, empty-string nonce is a no-op,
`Writer.Nonce` escapes a hostile value (`" onload=…`), works after a prior
writer error (standard `gw.err` short-circuit).

## Docs

- Rewrite the "CSP nonces" section of `docs/guide/syntax/escaping.md` around
  `gsx.WithNonce` + automatic injection + precedence; keep the framing that
  nonce generation and the CSP header remain app-owned (middleware example
  calls `gsx.WithNonce` instead of a hand-rolled context key).
- `docs/ROADMAP.md` item 7 and `docs/guide/status.md` gap line updated to
  "shipped" wording.
- No grammar/syntax change → no tree-sitter-gsx / vscode-gsx / CodeMirror
  updates required.
