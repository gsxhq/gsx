# URL Hardening: Meta Refresh and Base Href

## Context

GSX already treats normal URL attributes as URL contexts. `href`, `src`,
`action`, `formaction`, `poster`, `cite`, `ping`, `data`, `background`,
`manifest`, `xlink:href`, and htmx URL attributes route dynamic values through
`Writer.URL`, which applies `urlSanitize` before HTML attribute escaping.

The remaining high-value gap is not a plain URL attribute. A `<meta>` element
with `http-equiv="refresh"` interprets its `content` attribute as a timed reload
or redirect. Browser parsing extracts a URL from values such as
`0; URL=/next`. Today GSX treats `content` as a plain attribute, so dynamic
refresh destinations are not scheme-sanitized.

`<base href>` is already covered by the global `href` URL classification, but
it deserves explicit tests because it changes how relative URLs in the document
resolve.

## Goals

- Sanitize dynamic URLs embedded in `<meta http-equiv="refresh" content={...}>`.
- Preserve valid refresh prefixes such as `0`, `5; url=`, whitespace, comma, and
  semicolon separators.
- Keep static template-authored refresh content unchanged; template authors are
  trusted by the GSX security model.
- Add explicit coverage proving dynamic `<base href={...}>` values use the URL
  sanitizer.
- Add adversarial URL tests seeded from the existing stdlib differential corpus
  and OWASP-style scheme-obfuscation vectors.
- Update the roadmap after implementation to say `data:image` allowance depends
  on splitting navigational and resource URL contexts.

## Non-Goals

- Do not allow `data:image` in this slice.
- Do not add a broad `data:` allow-list.
- Do not introduce `TrustedResourceURL` or split navigational/resource URL types
  yet.
- Do not reject or rewrite static template-authored `javascript:` URLs.
- Do not build a general HTML state machine for all tag/attribute interactions.

## Design

### Refresh Content Helper

Add an unexported runtime helper that sanitizes the URL segment of a meta refresh
content value:

```go
func refreshContentSanitize(s string) string
```

The helper implements the refresh-content grammar GSX needs for output
sanitization:

1. Skip leading ASCII whitespace.
2. Parse the leading non-negative integer and optional decimal suffix.
3. If the value contains no redirect URL, return the original string unchanged.
4. Accept the browser separators after the time: ASCII whitespace, `;`, or `,`.
5. Accept optional case-insensitive `url`, optional ASCII whitespace, `=`, and
   optional ASCII whitespace.
6. If the URL is quoted with `'` or `"`, sanitize only the bytes inside the
   matching quote and preserve the quote and trailing suffix.
7. If unquoted, sanitize the URL substring through the end of the value.

If parsing fails before a URL segment is identified, return the original string.
Once a URL segment is identified, pass only that segment through `urlSanitize`.
This preserves the refresh delay and syntax while applying the same scheme
policy as ordinary URL attributes.

The helper is intentionally in the runtime package so generated code stays
small and all edge-case tests live beside `urlSanitize`.

### Codegen Routing

When emitting an expression attribute named `content` on a `meta` element whose
`http-equiv` attribute is statically `refresh` case-insensitively, route the
dynamic value through a new writer method:

```go
func (gw *Writer) RefreshContent(s string)
```

`RefreshContent` writes `refreshContentSanitize(s)` with normal HTML attribute
escaping. `gsx.RawURL` does not affect refresh content in this slice because the
expression is not directly a URL value; callers that need a trusted refresh
string can write static template content or build an already-safe string that
passes the normal allow-list.

If `http-equiv` is dynamic, keep `content` as a plain attribute. GSX cannot know
the context at compile time without a larger runtime attribute-state mechanism.
This limitation will be documented in the tests and roadmap.

### Base Href Coverage

Add corpus coverage for:

```gsx
component Base(u string) {
	<base href={u}/>
}
```

The generated golden must show the existing URL path (`_gsxgw.URL(u)`), and a
render case must prove `javascript:` becomes `about:invalid#gsx`.

### Adversarial Tests

Add runtime tests for `refreshContentSanitize`:

- `0` and `5` reload-only values stay unchanged.
- `0;url=/next`, `0; URL=https://example.com`, and `0, url='?q=a:b'` preserve
  safe destinations.
- `0;url=javascript:alert(1)`, mixed-case schemes, leading whitespace before the
  scheme, and control characters in the scheme become `about:invalid#gsx`.
- Quoted URLs only rewrite the quoted segment and preserve trailing content.

Add corpus coverage for generated refresh content:

```gsx
component Refresh(to string) {
	<meta http-equiv="refresh" content={ "0;url=" + to }/>
}
```

The generated code must route through `RefreshContent`; the render golden must
prove an unsafe scheme is blocked.

## Tradeoffs

This design deliberately handles only the statically-known refresh case. That
keeps codegen explicit and avoids a runtime classifier that would need to reason
about multiple attributes as they stream. The cost is that
`<meta http-equiv={kind} content={value}>` remains plain attribute escaping even
when `kind` evaluates to `refresh`.

The refresh parser is a real implementation of the relevant browser grammar, not
a substring heuristic. It still stops short of building a full URL parser; URL
scheme policy stays centralized in `urlSanitize`.

Deferring `data:image` avoids silently allowing dangerous `data:` uses in
navigational contexts. A later resource-URL split can allow narrowly-scoped image
data URLs for `img src` without changing `href`, `base`, forms, or refresh.

## Verification

- `go test ./...` for the root runtime and generator packages touched by the
  helper and codegen routing.
- `go test ./internal/corpus -run TestCorpus -update`, then rerun without
  `-update`, for generated and render goldens.
- `make check` after the implementation slice is complete.
