# gsx

[![Go Reference](https://pkg.go.dev/badge/github.com/gsxhq/gsx.svg)](https://pkg.go.dev/github.com/gsxhq/gsx)
[![CI](https://github.com/gsxhq/gsx/actions/workflows/ci.yml/badge.svg)](https://github.com/gsxhq/gsx/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/gsxhq/gsx)](https://goreportcard.com/report/github.com/gsxhq/gsx)
[![Release](https://img.shields.io/github/v/release/gsxhq/gsx)](https://github.com/gsxhq/gsx/releases)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gsxhq/gsx)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A JSX-like templating language for Go, compiled to plain Go that streams HTML.

> **Status — alpha, tagged.** gsx is used in production today and ships tagged
> releases from `v0.1.0`; language and API compatibility may change before 1.0.
> See [Status](docs/guide/status.md), the [changelog](CHANGELOG.md), and the
> [Roadmap](docs/ROADMAP.md).

## What is gsx

`.gsx` files hold ordinary Go (imports, types, funcs) plus `component`
declarations. A generator lowers each component to plain Go in a `.x.go` file the
Go compiler type-checks and builds:

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

- **Checked by Go** — each component keeps its exact authored Go signature, and
  markup binds parameters by name.
- **HTML-shaped markup with ordinary Go** — use JSX-like markup for templates
  and ordinary Go for everything else.
- **Safe by context** — contextual HTML, URL, CSS, and JavaScript escaping with
  a **standard-library-only** runtime.
- **Go-native tooling** — generation and builds are Go; Node.js is needed only
  when your application chooses frontend tooling such as Vite.

## A taste

```gsx
import "github.com/gsxhq/gsx"

// Markup can bind to a package-level var — inferred as gsx.Node.
var footer = <><hr/><small>Built with gsx</small></>

component Card(title string, featured bool, children gsx.Node) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
		{footer}
	</section>
}
```

*Run `gsx generate` to compile this to plain Go (`.x.go`), then `go build`.*

## Learn more

- **Docs** — [Why gsx](docs/guide/vision.md) ·
  [Principles](docs/guide/principles.md) · [Syntax](docs/guide/syntax.md) ·
  [CLI](docs/guide/cli.md)
- **Examples** — the [test corpus](internal/corpus/testdata/cases) is the
  canonical syntax reference (every case parses, generates Go, and pins its
  rendered output).
- **Roadmap & status** — [docs/ROADMAP.md](docs/ROADMAP.md).

## Documentation site

The public docs site — <https://gsxhq.github.io/> — is built with VitePress in the
separate [`gsxhq.github.io`](https://github.com/gsxhq/gsxhq.github.io) repo, which
renders the Markdown in [`docs/guide/`](docs/guide/).

## Contributing

Issues and discussion welcome. Runtime code must stay standard-library only; the
generator/CLI may use `golang.org/x/tools`.

## License

[MIT](LICENSE) © 2026 Jackie Li
