# Status

## Maturity

gsx is alpha software and is used in production. The compiler, runtime, and
toolchain work end to end, but language and API compatibility are not guaranteed
before 1.0.

## Shipped surfaces

### Language and rendering

[Typed authored signatures](./syntax/props.md), [components, children, and
slots](./syntax/composition.md), [control flow](./syntax/control-flow.md),
[pipelines](./syntax/pipelines.md), and [attribute forwarding](./syntax/composition.md)
are available. Rendering has [contextual escaping and explicit trust
boundaries](./syntax/escaping.md).

### Toolchain

The [CLI](./cli.md) provides init, generate, format, and info commands, plus the
[development loop](./dev-loop.md). Vite is an optional front door.

### Editor support

The [language server](./editor.md) provides diagnostics, navigation, references,
symbols, formatting, code actions, completion, completion documentation, and
auto-imports.

## Durable boundaries

- Generation produces `.x.go` files that are compiled with the application.
- Find-references is project-scoped and excludes external package references.
- Pre-1.0 releases may require source migrations.

## Roadmap

See [Roadmap & Status](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md)
for changing engineering details and planned work.
