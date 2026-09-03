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

The [CLI](./cli.md) provides `init`, `generate`, `fmt`, and `info` commands, plus
the [development loop](./dev-loop.md). Vite is an optional front door.

### Editor support

The [language server](./editor.md) provides diagnostics, navigation, references,
symbols, formatting, code actions, completion, completion documentation, and
auto-imports.

## Releases and versioning

gsx ships tagged releases, starting at `v0.1.0`. Tags follow semantic
versioning as Go modules read it: before 1.0 a **minor** bump (`v0.2.0`) may
change syntax or APIs and may require source migrations, and a **patch** bump
(`v0.1.1`) does not. Each release is listed in the
[changelog](https://github.com/gsxhq/gsx/blob/main/CHANGELOG.md).

Pin the runtime and the CLI to the same tag:

```sh
go get github.com/gsxhq/gsx@v0.1.0
go get -tool github.com/gsxhq/gsx/cmd/gsx@v0.1.0
```

`@latest` resolves to the newest tag, which is what `gsx init` pins. To follow
unreleased work use `@main`. The runtime and the CLI live in one module, so one
tag covers both; `gsx version` prints the tag the binary was built from.

Releases require the Go version in `go.mod` (currently 1.26).

## Durable boundaries

- Generation produces `.x.go` files that are compiled with the application.
- Find-references is project-scoped and excludes external package references.
- Pre-1.0 minor releases may require source migrations.

## Roadmap

See [Roadmap & Status](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md)
for changing engineering details and planned work.
