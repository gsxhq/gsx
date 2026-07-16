# gsx

A templating language for Go: **templ-style `component` declarations** with a
**JSX-style markup body**, compiled to plain Go.

> **Status — alpha.** gsx is usable end to end, but the language and APIs may
> change before a stable release. See [Status](docs/guide/status.md) and
> [Roadmap](docs/ROADMAP.md).

## What is gsx

`.gsx` files hold ordinary Go (imports, types, funcs) plus `component`
declarations. A generator lowers each component to plain Go in a `.x.go` file the
Go compiler type-checks and builds:

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

- **Checked by Go** — each component keeps its authored Go signature; markup
  binds parameters by exact name and direct Go callers use the same function.
- **Close to HTML and to Go** — JSX-style markup for templates; ordinary Go for
  everything else. Tags resolve against package symbols; an unresolved lowercase
  tag remains an HTML element.
- **templ-compatible** — `gsx.Node` has the identical method set to
  `templ.Component`, so gsx output drops into the templ ecosystem without importing
  templ. The runtime is **standard-library only**.

## A taste

```gsx
import "github.com/gsxhq/gsx"

component Card(title string, featured bool, children gsx.Node) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
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
