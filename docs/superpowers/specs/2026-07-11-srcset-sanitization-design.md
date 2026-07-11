# srcset URL-list sanitization

**Status:** approved (revised — WHATWG grammar)
**Date:** 2026-07-11
**Closes (in part):** #78 (srcset item)
**Files follow-ups:** ping (space-separated URL list), CSS `url()` in `style`

## Problem

`srcset` is currently classified `CtxPlain`: it is HTML-escaped but never
URL-sanitized, on both the static-element path and the spread path. It is a
comma-separated list of image candidates (`url [descriptor]`), so a `javascript:`
(or other disallowed-scheme) URL in any candidate is emitted verbatim. This is
not a live XSS in a modern browser (`srcset` candidates are image resources; a
`javascript:` candidate does not execute), but it is the last gap in gsx's
"every URL sink is sanitized by context" story, and the static and spread paths
emit `srcset` identically today, so the fix must cover both.

### Why not a faithful `html/template` port

CLAUDE.md mandates that security escaping is a **faithful port of
`html/template`**. For `srcset` specifically, that target is wrong: probing
`html/template` (Go 1.26.1) shows its `srcset` sanitizer is a conservative
over-approximation that is **broken for valid inputs**:

| input | `html/template` output |
|---|---|
| `a.jpg 1.5x` | `#ZgotmplZ` (blocks a valid fractional density descriptor) |
| `data:image/png;base64,iVBOR 1x` | `#ZgotmplZ,iVBOR 1x` (mangles the data URL) |

The root cause: `html/template` splits candidates on **every** comma *before*
parsing (so a `data:` URL's mandatory `;base64,` comma guillotines it), and its
`metadataOk` gate rejects any descriptor byte that is not HTML-space/ASCII-alnum
(so `.` in `1.5x` fails). That gate exists to guard `html/template`'s URL
**normalization fast-path** — which gsx does not have (gsx scheme-filters only
and HTML-escapes the whole attribute value). Porting it faithfully would import
false-positives that break real `srcset` for zero security benefit in gsx.

So `srcset` is sanitized by porting the **actual WHATWG `srcset` grammar** (the
one browsers use), not `html/template`'s `srcset` code. The security-critical
part — per-URL scheme sanitization — remains a faithful port
(`urlSanitizeImage`). Only the candidate **split** follows WHATWG.

### Structured-carrier principle (the general rule this establishes)

gsx already contains the precedent: `refreshContentSanitize` (meta-refresh
`content`) is a bespoke **WHATWG-grammar** port, not an `html/template` copy.
Generalizing:

> **Single-value URL/JS/CSS/HTML attributes are faithful `html/template` ports.
> Structured URL carriers (a list or an embedded URL with its own grammar) are
> faithful ports of that grammar (WHATWG), sanitizing each URL with gsx's
> scheme-allow-list sink.**

The structured URL carriers in HTML are a small, closed set:

| carrier | grammar | status |
|---|---|---|
| `srcset` / `imagesrcset` | comma-list of `url [descriptor]` | this spec (WHATWG) |
| `<meta http-equiv=refresh content>` | `time; url=URL` | done (`refreshContentSanitize`) |
| `ping` | space-separated URL list | follow-up — see below |
| CSS `url()` in `style` | CSS context | follow-up (separate subsystem) |

Everything else in gsx's URL set (`href, src, action, formaction, poster, cite,
data, background, manifest, xlink:href`, htmx) is a single URL, correctly
handled by the existing single-URL sinks. This is **not** a reason to rethink
the URL model — the model is sound and already beats `html/template` (data:image
support); it just needs the structured carriers named and handled consistently.

**`ping` follow-up (non-security):** `ping` is a space-separated URL list that
gsx (like `html/template`) sanitizes as a single URL — a hostile URL mid-list
can slip past scheme-checking. This is a robustness gap, **not** a
vulnerability: browsers fire `ping` only to http/https on click and never
execute a `javascript:` ping. Tracked as a follow-up issue; give it space-split
per-URL sanitization there.

## Principle

A `srcset` value is a comma-separated list of image candidates. Parse it with
the **WHATWG `srcset` grammar**: each candidate's URL is the run of
non-whitespace bytes (so commas *inside* a URL — a data URL's `;base64,`, a
query's `?a=1,2` — stay part of the URL); a run's trailing commas are candidate
boundaries; the remainder up to the next comma is an **inert descriptor**. Each
parsed URL is sanitized as an image resource (`urlSanitizeImage`: scheme
allow-list `http`/`https`/`mailto`/`tel` + `data:image/*`); a blocked URL
collapses its whole candidate to gsx's `blockedURL` (`about:invalid#gsx`). The
descriptor needs no validation — the entire result is HTML-escaped by
`writeSrcset`, so descriptor content can never break out of the attribute.

**Parser-differential safety:** because the candidate split matches the browser's
own grammar, gsx never misclassifies a URL as a descriptor (or vice-versa). Any
divergence from WHATWG's descriptor-parenthesis handling (not implemented — an
unused feature) can only cause gsx to sanitize *more* text as a URL, never less;
over-blocking is safe.

## Architecture

Decision (locked in brainstorming): **extend the existing sink split**, do not
add a new `Context`. The nav-vs-image sink distinction already lives in codegen
(`urlWriterMethod(tag, name)`), not in `attrclass`; `srcset` becomes a third arm
of that same seam. `attrclass`'s `Context` enum is unchanged.

### Runtime — `escape.go`

- `srcsetSanitize(s string) string` — the WHATWG candidate parser (below).
  Reuses the existing `urlSanitizeImage`, `blockedURL`, and
  `isASCIIWhitespaceByte`. It does **not** reuse `html/template`'s
  `filterSrcsetElement` or any `metadataOk`/alnum gate.
- `writeSrcset(w io.Writer, s string) error = writeHTML(w, srcsetSanitize(s))` —
  HTML-escapes the sanitized list, like every other gsx sink.

The parser:

```go
func srcsetSanitize(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		// 1. Candidate separators (leading whitespace + commas) copied verbatim.
		sep := i
		for i < len(s) && (isASCIIWhitespaceByte(s[i]) || s[i] == ',') {
			i++
		}
		b.WriteString(s[sep:i])
		if i >= len(s) {
			break
		}
		// 2. URL = run of non-whitespace bytes (commas inside a URL stay).
		urlStart := i
		for i < len(s) && !isASCIIWhitespaceByte(s[i]) {
			i++
		}
		run := s[urlStart:i]
		url := strings.TrimRight(run, ",") // trailing commas are boundaries
		i -= len(run) - len(url)           // re-consume them as separators
		// 3. Descriptor: rest up to next comma, only if the URL had no
		//    trailing-comma boundary. Inert (HTML-escaped downstream).
		descStart := i
		if len(url) == len(run) {
			for i < len(s) && s[i] != ',' {
				i++
			}
		}
		desc := s[descStart:i]
		// 4. A blocked URL collapses the whole candidate; else URL + descriptor.
		if urlSanitizeImage(url) == blockedURL {
			b.WriteString(blockedURL)
		} else {
			b.WriteString(url)
			b.WriteString(desc)
		}
	}
	return b.String()
}
```

### Sinks — `writer.go`

- `func (gw *Writer) Srcset(s string)` — the static/string sink.
- `func (gw *Writer) SrcsetVal(v any)` — the bag/dynamic sink; a `gsx.RawURL`
  passes verbatim (author's whole-value vouch, still attribute-escaped), any
  other value is stringified then `srcsetSanitize`d. Mirrors
  `URLVal`/`URLImageVal`.

### Classifier — `attrclass.go`

Add `srcset` and `imagesrcset` to the builtin URL-exact name set (both become
`CtxURL`). `imagesrcset` is a real srcset carrier (`<link rel=preload
imagesrcset>`). No new `Context`.

### Codegen — `emit.go`

- `urlWriterMethod(tag, name)` gains a leading arm:
  `case strings.ToLower(name) == "srcset" || … == "imagesrcset": return "Srcset"`,
  before the `SinkImage` check. This routes the **static path**
  (`<img srcset={x}>`, `srcset=f"…"`, which already dispatch through
  `urlWriterMethod`) to `gw.Srcset`.
- `emitSpreadCall` splits `cls.URLExactNames()` into nav/image/**srcset** sets;
  `gw.Spread` gains a `srcsetNames []string` parameter routing those keys
  through `SrcsetVal`. All `gw.Spread` call sites are codegen-emitted.

## Behavior table (pinned by tests)

| input | output |
|---|---|
| `a.jpg 1x, b.jpg 2x` | `a.jpg 1x, b.jpg 2x` |
| `a.jpg 1.5x` | `a.jpg 1.5x` (fixed vs html/template) |
| `s-320.jpg 320w, s-640.jpg 640w` | unchanged |
| ` a.jpg 1x , b.jpg 2x ` | unchanged (whitespace preserved) |
| `javascript:alert(1) 1x` | `about:invalid#gsx` |
| `ok.jpg 1x, javascript:alert(1) 2x` | `ok.jpg 1x, about:invalid#gsx` |
| `data:image/png;base64,iVBOR 1x` | `data:image/png;base64,iVBOR 1x` (intact) |
| `data:image/png;base64,iVBOR 1x, x.jpg 2x` | unchanged (intact) |
| `data:text/html,<script> 1x` | `about:invalid#gsx` (one clean block) |
| `a.jpg,b.jpg` (no spaces) | `a.jpg,b.jpg` (WHATWG single-URL misparse; safe) |
| `` (empty) | `` |

## Blast radius

- `gw.Spread` gains one parameter (`srcsetNames`) — codegen-only callers.
- `escape.go` gains `srcsetSanitize` + `writeSrcset`; `writer.go` two sinks;
  `attrclass.go` two builtin names (no `Context` change); `emit.go` one
  `urlWriterMethod` arm + three-way split in `emitSpreadCall`.
- Runtime stays stdlib-only. No surface-syntax change → tree-sitter-gsx /
  vscode-gsx / CodeMirror / `gsx fmt` unaffected.

## Testing

- **Unit** (root `gsx` package): a `srcsetSanitize` table covering every row of
  the Behavior table above.
- **Corpus, per context** (`internal/corpus/testdata/cases/srcset-sanitize/`):
  static `srcset={expr}`, static `srcset=f"…"` literal, bag spread, cond-nested
  spread. Vectors: multi-candidate with descriptors, a `javascript:` candidate,
  a `data:image` candidate (intact), a `data:text/html` candidate (blocked), and
  `imagesrcset` on `<link>`. Each pins `input.gsx` + `generated.x.go.golden` +
  `render.golden`.
- `docs/guide/syntax/attributes.md`: document `srcset` as a WHATWG-parsed,
  per-candidate-sanitized URL-list sink. `docs/ROADMAP.md`: flip `srcset` from
  **Deferred**; note the structured-carrier principle and the ping / CSS `url()`
  follow-ups.

## Companion cleanups (this batch, no design needed)

Ship alongside srcset as discrete plan tasks (mechanical):

From #78:
- **Nested-cond one-spread count.** `<a { if c { {x…} } } { if d { {y…} } }>`
  slips past the "at most one spread per element" count (top-level spreads only).
  Extend it to descend into cond-attr branches (`walkSpreadAttrs`). A regression
  corpus case pins the diagnostic.
- **`[]byte` URL value renders as a Go decimal array** — a `toStr` vs
  `anyRenderString` divergence, pre-existing and rendering-wide (fail-safe
  garble, not exploitable). Align `toStr`'s `[]byte` case.

From #71 (attrs-only test debt):
- `TestAttrsOnlySig`: add a `TypeParams`-bearing signature rejection case; move
  the named-sig-underlying test wrapper out of the fabricated `gsx` package.
- Extract a shared `parseGsxTypeDecls` helper (`gsxChunkTypeNames` duplicates
  ~30 lines of `gsxStructDecls`); add an explicit defined-type (`type X int`)
  case to `TestTypeNames`.
- **LSP hover** on attrs-only component-value tags (go-to-def shipped; hover
  deferred). Add hover parity.

## Non-goals / tracked follow-ups

- **`ping`** space-separated URL-list sanitization — separate issue (non-security).
- **CSS `url()` in `style`** — separate issue (CSS context, separate subsystem).
- Percent-normalization of URLs (gsx scheme-filters only, everywhere — deliberate).
- WHATWG descriptor **parenthesis** handling (unused feature; omission only
  over-blocks, never under-blocks).
