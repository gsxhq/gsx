# srcset URL-list sanitization

**Status:** approved
**Date:** 2026-07-11
**Closes (in part):** #78 (srcset item)

## Problem

`srcset` is currently classified `CtxPlain`: it is HTML-escaped but never
URL-sanitized, on both the static-element path and the spread path. It is a
comma-separated list of image candidates (`url [descriptor]`), so a `javascript:`
(or other disallowed-scheme) URL in any candidate is emitted verbatim.

This is not a live XSS in a modern browser (`srcset` candidate URLs are image
resources and a `javascript:` candidate does not execute), but it is the last
gap in gsx's "every URL sink is sanitized by context" story. The static-element
path and the spread path emit `srcset` identically today, so the fix must cover
both uniformly.

Security escaping in gsx is a **faithful port of `html/template`, never an
approximation** (CLAUDE.md). `html/template` handles `srcset` with a dedicated
`srcsetFilterAndEscaper` + `filterSrcsetElement`; this design ports that
structure.

## Principle

A `srcset` value is a comma-separated list of image-candidate strings. **Each
candidate URL is sanitized exactly as `<img src>` is** — `urlSanitizeImage`
(scheme allow-list `http`/`https`/`mailto`/`tel` + `data:image/*`) — using
`html/template`'s exact candidate-splitting, metadata-validation, and failsafe
structure. A disallowed scheme in any candidate yields gsx's `blockedURL`
(`about:invalid#gsx`) for that whole candidate; safe candidates pass through
unchanged (gsx does scheme-filtering only, no percent-normalization — a
deliberate gsx-wide divergence from `html/template`, not introduced here).

## Architecture

Decision (locked in brainstorming): **extend the existing sink split**, do not
add a new `Context`. The nav-vs-image sink distinction already lives in codegen
(`urlWriterMethod(tag, name)`), not in `attrclass`; `srcset` becomes a third
arm of that same seam. This keeps `attrclass`'s `Context` enum unchanged and
routes the static and spread paths through one point.

### Runtime port — `escape.go`

Mirrors the established `refreshContentSanitize` port (meta-refresh grammar).

- `srcsetSanitize(s string) string` — port of `html/template`'s
  `srcsetFilterAndEscaper` plain-string path: walk `s`, split on `,`, call
  `filterSrcsetElement` for each element, re-join with `,`.
- `filterSrcsetElement(s string, left, right int, b *strings.Builder)` — port of
  the same in `html/template`: skip leading HTML space; the URL runs to the next
  HTML space; the remainder is descriptor metadata. The candidate passes iff
  **`urlSanitizeImage(url) != blockedURL`** *and* the metadata bytes are only
  HTML-space / ASCII-alnum (`html/template`'s exact envelope). On pass, write
  `s[left:start]` + the sanitized URL + `s[end:right]`. On fail (bad scheme *or*
  bad metadata), write `blockedURL` for the whole candidate.
- Reuse gsx's existing `isASCIIWhitespaceByte` (its byte set — space, `\t`,
  `\n`, `\f`, `\r` — matches `html/template`'s `isHTMLSpace`). Add a small
  `isASCIIAlnumByte` helper for the metadata check.
- `writeSrcset(w io.Writer, s string) error = writeHTML(w, srcsetSanitize(s))` —
  HTML-escapes the sanitized list, like every other gsx sink.

### Sinks — `writer.go`

- `func (gw *Writer) Srcset(s string)` — the static/string sink.
- `func (gw *Writer) SrcsetVal(v any)` — the bag/dynamic sink; a `gsx.RawURL`
  passes verbatim (author's whole-value vouch, still attribute-escaped), any
  other value is stringified then `srcsetSanitize`d. Mirrors
  `URLVal`/`URLImageVal`.

### Classifier — `attrclass.go`

Add `srcset` and `imagesrcset` to the builtin URL-exact name set (both become
`CtxURL`). `imagesrcset` is a real srcset URL-list carrier
(`<link rel=preload imagesrcset>`). No new `Context`.

### Codegen — `emit.go`

- `urlWriterMethod(tag, name)` gains a leading arm:
  `case name == "srcset" || name == "imagesrcset": return "Srcset"`.
  Placed before the `SinkImage` check so it wins. This single change routes the
  **static path** (`<img srcset={x}>` and `srcset=f"…"` already dispatch through
  `urlWriterMethod` at emit.go:2773/2782) to `gw.Srcset`.
- `emitSpreadCall` splits `cls.URLExactNames()` into three sets — nav, image,
  **srcset** — by `urlWriterMethod(tag, name)`. `gw.Spread`'s signature gains a
  `srcsetNames []string` parameter; the runtime routes those keys through
  `SrcsetVal`. All `gw.Spread` call sites are codegen-emitted, so the signature
  change is internal.

## Blast radius

- `gw.Spread` gains one parameter (`srcsetNames`) — codegen-only callers.
- One new runtime file section in `escape.go`, two sinks in `writer.go`, two
  builtin names + no `Context` change in `attrclass.go`, one `urlWriterMethod`
  arm + three-way split in `emit.go`.
- Runtime stays stdlib-only. No surface-syntax change → tree-sitter-gsx /
  vscode-gsx / CodeMirror / `gsx fmt` unaffected.

## Testing

- **Corpus, per context** (`internal/corpus/testdata/cases/srcset-sanitize/`):
  static `srcset={expr}`, static `srcset=f"…"` literal, bag spread, and
  cond-nested spread. Each pins `input.gsx` + `generated.x.go.golden` +
  `render.golden`. Vectors: multi-candidate with descriptors, a `javascript:`
  candidate (→ `blockedURL`), a bad-metadata descriptor (→ `blockedURL`), a
  `data:image/png;base64,…` candidate (passes), and `imagesrcset` on `<link>`.
- **Unit** (root `gsx` package): a `srcsetSanitize` table mirroring
  `html/template`'s `url_test.go` srcset cases, adapted to gsx's scheme-only /
  image-sink semantics and `blockedURL` failsafe.
- `docs/guide/syntax/attributes.md`: document `srcset` as a sanitized URL-list
  sink. `docs/ROADMAP.md`: flip `srcset` from **Deferred**.

## Companion cleanups (this batch, no design needed)

These ship alongside srcset as discrete plan tasks (mechanical — bug fixes /
refactors / a small LSP addition):

From #78:
- **Nested-cond one-spread count.** `<a { if c { {x…} } } { if d { {y…} } }>`
  (two spreads each nested in separate cond-attrs) slips past the "at most one
  spread per element" count, which sees top-level spreads only. Both are
  sanitized, but duplicate keys across the two bags can double-render (HTML
  first-wins). Extend the spread count via `walkSpreadAttrs`, or add a
  `composition.md` footnote. Decide during planning; a regression case pins it
  either way.
- **`[]byte` URL value renders as a Go decimal array** — a `toStr` vs
  `anyRenderString` divergence in the runtime, pre-existing and rendering-wide
  (fail-safe garble, not exploitable). Align the value-stringify path.

From #71 (attrs-only test debt):
- `TestAttrsOnlySig`: add a `TypeParams`-bearing signature rejection case; move
  the named-sig-underlying test wrapper out of the fabricated `gsx` package.
- Extract a shared `parseGsxTypeDecls` helper (`gsxChunkTypeNames` duplicates
  ~30 lines of `gsxStructDecls` scan/walk); add an explicit defined-type
  (`type X int`) case to `TestTypeNames`.
- **LSP hover** on attrs-only component-value tags (go-to-def shipped; hover
  deferred — ROADMAP known-gap). Add hover parity.

## Non-goals

- CSS `url()` inside `style` (separate follow-up, tracked).
- Percent-normalization of URLs (gsx does scheme-filtering only, everywhere).
- `<meta http-equiv=refresh>` content (already handled by `RefreshContent`).
