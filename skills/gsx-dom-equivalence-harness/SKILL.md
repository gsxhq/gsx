---
name: gsx-dom-equivalence-harness
description: Use when verifying a templ→gsx migration with DOM-level equivalence tests — covers the domtest harness API, Normalize semantics, why string-diff fails, how to render templ components with children in tests, and how to inject a structpages URL context.
---

# gsx DOM-equivalence test harness

Learned while migrating one-learning's button layer from templ to gsx.
The harness lives in `internal/domtest` of the consuming app.

## Philosophy

This is a loose regression **guardrail**, not a byte-exact oracle.
The goal is **behaviour parity** — not identical raw HTML strings.
Intended improvements (e.g. removing a trailing space, deduplicated attrs) are
re-baselined rather than treated as regressions.

## API

```go
// Render a component and return its HTML string.
func Render(t *testing.T, n Renderable) string

// Render with an explicit context (e.g. one carrying URL state).
func RenderCtx(t *testing.T, ctx context.Context, n Renderable) string

// Normalize an HTML fragment for comparison.
func Normalize(t *testing.T, html string) string

// Assert two HTML fragments are DOM-equivalent.
func AssertEqual(t *testing.T, want, got string)

// Assert DOM-equivalence with an explicit context.
func AssertEqualCtx(t *testing.T, ctx context.Context, want, got string)
```

`Renderable` is any value with `Render(ctx context.Context, w io.Writer) error` —
both `templ.Component` and `gsx.Node` satisfy it without a wrapper.

## What `Normalize` does

1. Parses the fragment with `atom.Body` as the context node (correct HTML5 fragment parsing).
2. Sorts attributes by `(namespace, key)` — order-independent comparison.
3. Collapses text-node whitespace via `strings.Fields` — insignificant whitespace differences vanish.
4. Canonicalises token-list attributes (`class`, `rel`) only — splits on whitespace, sorts tokens,
   rejoins. Other attribute values keep significant whitespace intact.

## Why string-diff fails here

templ has two known quirks that produce correct HTML but non-identical raw strings:

1. **Duplicate attributes:** a `type` attribute emitted both explicitly and via rest-spread
   (`{ attrs... }`) appears twice in raw output; the HTML5 parser merges to the first occurrence.
2. **Trailing space in empty class merge:** `class="base "` when the caller class is empty.

Both are absorbed by `Normalize` + the HTML5 parser. A raw `strings.Compare` would
falsely fail on both.

## Rendering a templ component with children in a test

templ children are threaded through `context`, so you need `templ.WithChildren`:

```go
// Render a templ component that accepts children, passing literal HTML as the child.
comp.Render(templ.WithChildren(ctx, templ.Raw(childHTML)), w)
```

Wrap this in a `domtest.Renderable`-compatible helper (`wrapTemplChild`) so it fits
the same `AssertEqual` call as the gsx version.

## Injecting a structpages URL context

`structpages.URLFor` (the `url` filter target) reads page context from the request
context via an unexported key (`pcCtx`). You cannot inject it directly.

**Pattern: mount a minimal one-page structpages tree and capture the context.**

```go
var capturedCtx context.Context

router := structpages.New(/* minimal route tree */)
router.WithMiddlewares(func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        capturedCtx = r.Context()   // captured AFTER structpages applies withPcCtx
        next.ServeHTTP(w, r)
    })
})

req := httptest.NewRequest("GET", "/", nil)
rw := httptest.NewRecorder()
router.ServeHTTP(rw, req)

// Now use capturedCtx for both templ and gsx renders:
domtest.AssertEqualCtx(t, capturedCtx, templHTML, gsxHTML)
```

**Assumption:** structpages applies `withPcCtx` before calling user middlewares.
If that ordering ever changes, this pattern breaks — document it as a known coupling.

## When DOM-equivalence is not enough

Alpine.js and other client-side behaviours (`x-data`, `@click`, `:class`) cannot
be verified by static HTML comparison. Add a **Playwright behavioural check** for
components whose correctness depends on runtime interactivity.
