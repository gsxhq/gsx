# Patterns

Patterns are copyable userland conventions built from ordinary Go and gsx.

## Available patterns

- **[Package renderers](./patterns/package-renderers.md)** — keep third-party
  value policy in an application-owned `.gsx` package.
- **[Render once](./patterns/render-once.md)** — emit a dialog container,
  inline style or script, or another singleton once per request.
- **[Translation keys](./patterns/i18n-keys.md)** — write i18n keys in
  markup; a context-aware renderer translates them for the request's locale.
- **[Streaming flush](./patterns/streaming-flush.md)** — flush the response
  mid-page so the browser paints above-the-fold markup before the slow tail.
