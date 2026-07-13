# Status

gsx is alpha software. It works end to end, but the language and APIs may change
before a stable release.

## Ready to use

- Start a project, generate and format templates, inspect configuration, and run
  the Vite-backed development loop through the [CLI](./cli.md).
- Build typed components with generated or user-owned props, children, slots,
  control flow, pipelines, and attribute forwarding.
- Render escaped HTML, URLs, CSS, and JavaScript values, with explicit trust
  boundaries where automatic encoding is not enough.
- Use diagnostics, go-to-definition, hover, references, symbols, and formatting
  through the [language server](./editor.md).

## Current limits

- LSP completion is not implemented.
- Find-references covers project components, not external or non-project
  references.
- Composable `style={...}` works on elements, but not on component invocations;
  pass a static style or compose it inside the component instead.
- Class composition accepts individual string contributions, but not a
  `[]string` as one class part.

## Roadmap

See [Roadmap & Status](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md)
for planned work and detailed engineering progress.
