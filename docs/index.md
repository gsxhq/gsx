# gsx documentation

gsx is a templating language for Go: **templ-style `component` declarations** with
a **JSX-style markup body**, compiled to plain Go.

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

> **Status — alpha.** gsx is runnable end-to-end. `gsx init` scaffolds a Go and
> Vite application, `gsx dev` runs the warm generate/build/reload loop, and
> `gsx lsp` provides diagnostics, navigation, hover, references, and formatting.
> The language and APIs are usable but may still change before a stable release.
> See the [status](./guide/status.md) and [roadmap](./ROADMAP.md).

## Start here

- **[Why gsx](./guide/vision.md)** — the problem it solves and the bet behind it.
- **[Principles](./guide/principles.md)** — the design commitments.
- **[Syntax](./guide/syntax.md)** — a quick tour; the
  [test corpus](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases)
  is the canonical, always-current reference (every accepted form is a case that
  parses, generates Go, and pins its rendered output).
- **[Configuration](./guide/config.md)** — the `gsx.toml` file: pipeline filters, filter packages, and attribute rules read by the stock binary.
- **[Extensions](./guide/extensions.md)** — the code escape hatch (`cmd/gsx` + `gen.Main`) for function-valued options: custom minifier, classifier predicate, field matcher.

## Reference

- [Roadmap & status](./ROADMAP.md)
- [Design docs](./superpowers/specs/) — the internal specifications.
