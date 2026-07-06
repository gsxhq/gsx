# `data:` image resource URLs (tag-aware navigational/resource split)

**Status:** design / awaiting review
**Date:** 2026-07-06
**Branch:** TBD (feature worktree)

## Motivation

gsx's URL sanitizer (`escape.go:38`, `urlSanitize`) is a single flat allow-list:
`http`/`https`/`mailto`/`tel` pass; every other scheme — including every `data:` —
is rewritten to `about:invalid#gsx`. Every attribute in `builtinURL`
(`internal/attrclass/attrclass.go:159`) gets the same treatment regardless of the
element it sits on.

That is safe but too blunt for a real and common need: **inline images as data URLs**.
An author who has image bytes in hand cannot write

```gsx
<img src=`data:image/png;base64,@{imageBytes |> base64}` />
```

— today the assembled value starts with `data:`, so it is blocked outright.

The reason gsx (and `html/template`) block `data:` wholesale is the *navigational*
danger: `data:text/html,<script>…` in an `<a href>` or `<iframe src>` is a live
document and executes. But that danger is **contextual**. A `data:image/png` in an
`<img src>` is just pixels. The fix is to stop treating "URL attribute" as one
context and split it into **navigational** and **resource** sinks — the same line
every serious HTML sanitizer eventually draws — and, in resource sinks only, allow
`data:` narrowed to an image-MIME allow-list.

This goes deliberately **beyond `html/template`**, which has no resource/navigational
split and blocks `data:` everywhere. gsx's escaping is otherwise a faithful port; this
is a considered, security-reasoned extension, not an approximation.

## The security model

The load context — not the attribute name alone — determines whether a `data:` URL
is dangerous. gsx already knows the **element tag** at codegen time, so the split is
**tag + attribute aware**, not attribute-name-only.

Browser behavior that the allow-list encodes:

| Sink | raster (`png/jpeg/gif/webp/avif`) | `image/svg+xml` |
|---|---|---|
| `<img src>`, `<picture><source srcset\|src>`, `<video poster>`, CSS `background`/`background-image` | **safe** (pixels) | **safe** — `<img>`/CSS load SVG in *restricted mode*: no script, no external fetch |
| `<iframe src>`, `<object data>`, `<embed src>` | safe (renders the image) | **UNSAFE** — SVG becomes a live document; `<script>` inside runs |
| `<script src>` | **UNSAFE** | **UNSAFE** — classic scripts ignore the response MIME; an attacker-controlled base64 payload executes as JS |
| navigational: `<a href>`, `<form action>`, `formaction`, `ping`, `cite`, `<link href>` (non-image `rel`) | blocked (unchanged) | blocked (unchanged) |

Two rules fall out:

1. **Raster image MIMEs** (`image/png`, `image/jpeg`, `image/gif`, `image/webp`,
   `image/avif`) are safe on every **resource** sink *except `<script src>`* — a
   browser will not execute a decoded PNG, but a classic `<script>` ignores the MIME
   and will execute an attacker's base64 payload, so `script` stays blocked.
2. **`image/svg+xml`** is safe only on the **image-rendering** sinks (`img`,
   `picture source`, `poster`, CSS background). It is excluded from
   `iframe`/`object`/`embed` (SVG-as-document → script) and `script`.

**Nothing is trusted by type.** Both authoring forms below re-run the resource-context
sanitizer on the fully assembled value. There is no `gsx.SafeURL`/trusted-value
smuggling: the scheme + MIME are re-validated at the sink no matter how the string
was produced. The one explicit opt-out remains `gsx.RawURL` (`node.go:39`) — the
author-vouches escape hatch that skips the scheme check but is still attribute-escaped.
That is the workaround for anything the allow-list refuses (an exotic MIME, SVG in an
`<object>`, a non-image `data:` you have validated yourself).

### Non-goals / explicitly out of scope

- No relaxation of **navigational** sinks. `data:` in `href`/`action`/… stays blocked.
- No `data:` for non-image MIME (`text/*`, `application/*`, fonts, media) in the
  built-in allow-list. Users who need those use `gsx.RawURL` or configure `urlAttrs`.
- No new trusted runtime type. We are not adding `gsx.DataImageURL` or similar — a user
  who wants a typed constructor writes their own filter (Form B is the blessed path).
- SVG in `iframe`/`object`/`embed` is **not** made ergonomic; `gsx.RawURL` covers it.

## Authoring forms

Both forms produce the same class of value and are re-validated by the same resource
sanitizer. They coexist; neither is deprecated by the other.

### Form A — static-prefix literal with a payload hole

```gsx
<img src=`data:image/png;base64,@{imageBytes}` />
```

The scheme + MIME + `base64,` marker are **static source text** the compiler reads at
codegen time. Because the dangerous, context-pinning part is fixed and author-written,
the compiler classifies this statically as a resource data-image URL and only has to
guarantee the **hole** is base64-charset-safe. This is the same "static literal prefix
pins the security context" property gsx already relies on for `` js`…` ``/`` css`…` ``
literals and for the whole-value URL sanitize of attr-interp literals (PR #33).

**Payload encoding is driven by the hole's Go type:**

| Hole type | Behavior |
|---|---|
| `[]byte` | **auto base64-encode** the raw bytes (`base64.StdEncoding`) |
| `string` | treated as **already-encoded** base64 text: charset-validated, emitted as-is (no double-encoding) |

So a user with raw bytes in a `string` who wants them *encoded* writes `@{[]byte(s)}`;
a user who already holds a base64 `string` drops it in directly. The type is the
switch between encode and pass-through — no magic beyond Go's own types.

The static prefix must match `data:<allowed-mime>;base64,` for the auto-classification
and the type-driven encoding to engage. A literal whose static prefix is not a
recognized image `data:` prefix falls back to the existing whole-value URL sanitize
(and is therefore blocked if it is a non-allow-listed scheme). Placing this literal on
a **navigational** or excluded sink (`href`, `script src`, `object data`, …) is a
**codegen error** with a message pointing at `gsx.RawURL` — the author asked for a
data-image URL in a place the split forbids.

Charset safety: the base64 payload lives inside a quoted, attribute-escaped value, so
it cannot break out of the attribute. Charset validation of a `string` hole is a
**correctness** guard (a malformed payload yields a broken image, not an injection).

### Form B — the `dataURL` std filter

```gsx
<img src={imageBytes |> dataURL("image/png")} />
```

A new **std** filter, Go-standard-library only:

```go
// std.DataURL assembles a data: URL from raw bytes and a MIME type:
//   dataURL(mime) : data:<mime>;base64,<base64.StdEncoding(subject)>
// The result is a plain string; the sink's resource sanitizer decides whether the
// scheme+MIME are permitted, so DataURL grants no privilege by itself.
func DataURL(subject []byte, mime string) string
```

Seed-first contract (`subject` first), matching every other std filter. Output is a
plain `string`; because **URL attributes sanitize AFTER the pipe** (established by the
whole-literal-pipe work, `FuzzURLWholeLiteralPipeSchemeSafety`), the assembled
`data:image/png;base64,…` is re-checked by the resource sanitizer at the sink. So:

- `{ bytes |> dataURL("image/png") }` on `<img src>` → passes (raster, resource sink).
- `{ bytes |> dataURL("text/html") }` on any sink → blocked (not an image MIME).
- `{ bytes |> dataURL("image/png") }` on `<a href>` → blocked (navigational sink).

`dataURL` is a convenience for assembly, **not** a trust grant — safety stays at the
sanitizer. It is Go-std-only, so there is no dependency cost to shipping it in `std`.

## std as the lowest-precedence filter base

Prerequisite behavior change so `dataURL` (and every other built-in) is overridable
without dropping the rest of std.

Today `dedupFilterPkgs` (`internal/codegen/codegen.go:21`) folds in `std` **only when
the filter list is empty**; a non-empty `WithFilters(myPkg)`/`filterPackages=["myPkg"]`
that omits `std` loses all of std. Change: **`std` is always present as the first
(lowest-precedence) package**, deduped, whether or not the user lists it.

Resulting precedence, low → high:

```
std  <  WithFilters / filterPackages (in listed order, last-wins)  <  WithFilter / [filters] aliases
```

So a user shadows an individual std name (`base64`, `dataURL`, …) by:

- `gen.WithFilter("dataURL", myFn)` / `[filters] dataURL = "mymod/pkg.MyDataURL"` — alias, always wins; or
- listing a package after std via `WithFilters`/`filterPackages` — last-wins;

…and never loses the rest of std. No new config surface: the existing `gsx.toml`
`[filters]` / `filterPackages` (`gen/configfile.go:28`) already express both.

This changes generated import/dispatch output for any project that used a non-empty
non-std filter list, so it **must fold into `computeKey`** (`gen/cachekey.go`) or the
incremental cache serves stale output. (The effective filter-package set is already a
cache-key input; the change is that the set now always includes std.)

## Implementation surface

1. **Context split — `internal/attrclass`.** Introduce a resource vs. navigational
   distinction. `Classify` must become **tag-aware** for the resource decision (it
   currently keys on attribute name only). Concretely: a resource-URL classification
   that carries which sink class the attr+tag pair is (image-render / generic-resource /
   script / navigational). `WithURLAttrs` / `[[urlAttrs]]` user rules extend the
   navigational or resource set explicitly (schema addition: a rule gains a
   context/sink field; default preserves today's navigational behavior).

2. **Resource sanitizer — `escape.go`.** Add a resource-context sanitizer alongside
   `urlSanitize`: same allow-list for http(s)/relative, **plus** `data:` narrowed to the
   image-MIME allow-list, parameterized by sink class (raster-everywhere-but-script;
   svg only on image-render sinks). Navigational `urlSanitize` is unchanged. A shared
   MIME parser reads `data:<mime>[;base64],` conservatively (case-insensitive MIME,
   require `;base64` for the built-in allowance).

3. **Codegen — `internal/codegen`.** (a) Static-prefix recognition for Form A: when a
   URL-attr embedded literal's static prefix is `data:<allowed-mime>;base64,` and the
   sink permits it, lower the hole with type-driven encoding (`[]byte` → base64 encode
   emit; `string` → charset-validated passthrough) instead of whole-value sanitize;
   otherwise fall back to existing whole-value sanitize. (b) Codegen error when a
   data-image literal lands on an excluded sink, message points at `gsx.RawURL`.

4. **std filter — `std` package.** Add `DataURL([]byte, string) string`. Corpus/std
   filter registration is automatic (whole-package harvest).

5. **std-as-base — `internal/codegen/codegen.go` + `gen`.** Always include
   `stdImportPath` as the first filter package; fold into `computeKey`.

## Testing

Per project convention, every syntax/codegen change ships a txtar corpus case per
context, plus runtime unit tests and fuzz coverage for the security core.

- **Corpus (`internal/corpus/testdata/cases/`):**
  - `url/data_image_literal_raster` — Form A, `<img src>`, `[]byte` hole (auto-encode).
  - `url/data_image_literal_string` — Form A, `string` hole (passthrough).
  - `url/data_image_literal_svg_img` — Form A svg on `<img>` (allowed).
  - `url/data_image_literal_rejected_href` — Form A on `<a href>` (codegen error).
  - `url/data_image_literal_rejected_script` — Form A on `<script src>` (codegen error).
  - `url/data_url_filter` — Form B, `<img src>`.
  - `url/data_url_filter_blocked_mime` — Form B `text/html` → sanitized to blocked.
  - `url/data_url_filter_blocked_nav` — Form B on `<a href>` → blocked.
- **Runtime unit tests (root `gsx`):** resource sanitizer allow/deny matrix across sink
  classes and MIME set; `RawURL` still bypasses; navigational sanitizer unchanged.
- **Fuzz:** extend the URL fuzz targets (`internal/codegen/url_fuzz_test.go`,
  `FuzzURLWholeLiteralPipeSchemeSafety`) to assert no resource-sink input yields an
  executable navigational scheme, and no `image/svg+xml` reaches an
  iframe/object/embed/script sink.
- **Filter precedence:** unit test that a user `dataURL`/`base64` alias shadows std
  while the rest of std stays available; cache-key test that the std-as-base change
  invalidates.

## Docs & siblings

- `docs/guide/syntax.md` / `syntax/attributes.md` — data-image literal form.
- `docs/guide/escaping.md` — the navigational/resource split, the sink matrix, the
  `RawURL` escape hatch, why gsx diverges from `html/template` here.
- `docs/guide/pipelines.md` — `dataURL` filter.
- `docs/guide/config.md` — std-as-lowest-precedence base; overriding a built-in filter.
- ROADMAP: check the `data:image` resource-URL item; note the navigational/resource
  split shipped.
- Sibling grammars (`../tree-sitter-gsx`, `../vscode-gsx`, `gsxhq.github.io`
  CodeMirror): no new *token* — Form A reuses the existing backtick-with-`@{}` attr
  literal (already a pending highlight item), Form B is an ordinary pipeline. No grammar
  change is strictly required; the existing attr-literal highlight backlog covers it.

## Open questions

- Should `srcset` (comma/space-separated candidate list) get per-candidate data-image
  support, or only the single-URL `src`/`poster`? (Lean: `src`/`poster` first; `srcset`
  candidate parsing is a separable follow-up.)
- CSS `background: url(data:image/…)` lives in the CSS value filter, not the URL attr
  path. In scope now, or a separate CSS-context slice? (Lean: separate slice — the
  sink is a different sanitizer; ship the attribute path first.)
