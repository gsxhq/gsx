# gsx

A templating language for Go: **templ-style `component` declarations** with a
**JSX-style markup body**, compiled to plain Go.

> **Status — alpha.** Language design is stable; the parser, runtime, and codegen
> phase 1 are done. The CLI is a work in progress, so gsx is **not yet runnable
> end-to-end**. See the [roadmap](docs/ROADMAP.md).

## What is gsx

`.gsx` files hold ordinary Go (imports, types, funcs) plus `component`
declarations. A generator lowers each component to plain Go in a `.x.go` file the
Go compiler type-checks and builds:

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

- **Type-safe by construction** — components become plain Go; props are generated
  structs, so gsx owns the field names (no symbol-resolver guesswork).
- **Close to HTML and to Go** — JSX-style markup for templates; ordinary Go for
  everything else. Capitalization decides component-vs-element (`<div>` vs `<Card>`).
- **templ-compatible** — `gsx.Node` has the identical method set to
  `templ.Component`, so gsx output drops into the templ ecosystem without importing
  templ. The runtime is **standard-library only**.

## A taste

```gsx
component Card(title string, featured bool) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
	</section>
}
```

*(Illustrative — `.gsx` files are not yet buildable; the CLI is WIP.)*

## Learn more

- **Docs** — [Why gsx](docs/guide/vision.md) ·
  [Principles](docs/guide/principles.md) · [Syntax](docs/guide/syntax.md)
- **Examples** — the [`examples/`](examples/) corpus is the canonical syntax
  reference.
- **Roadmap & status** — [docs/ROADMAP.md](docs/ROADMAP.md).
- **Design docs** — [docs/superpowers/specs/](docs/superpowers/specs/).

## Documentation site

The public docs site is built with VitePress in the separate
[`gsxhq/website`](https://github.com/gsxhq/website) repo, which renders the
Markdown in [`docs/guide/`](docs/guide/).

## Contributing

Issues and discussion welcome. Runtime code must stay standard-library only; the
generator/CLI may use `golang.org/x/tools`.

## License

[MIT](LICENSE) © 2026 Jackie Li
