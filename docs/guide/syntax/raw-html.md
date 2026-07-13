# Raw HTML

Plain `{ expr }` values are HTML-escaped. Use `gsx.Raw` only when a trusted or
sanitized string must render as markup.

## Rendering trusted HTML

<!--@include: ./_generated/raw-html/010-raw-html.md-->

## Security

`gsx.Raw` writes the supplied HTML without escaping. The value must already be
trusted or sanitized; never pass it unvalidated user input. See
[Escaping](./escaping.md#trusted-value-helpers) for the other explicit trust
boundaries.
