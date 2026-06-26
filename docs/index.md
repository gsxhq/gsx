# gsx documentation

gsx is a templating language for Go: **templ-style `component` declarations** with
a **JSX-style markup body**, compiled to plain Go.

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

> **Status — alpha.** gsx is runnable end-to-end: `gsx generate` compiles
> `.gsx` → `.x.go` (plus `gsx fmt` and `gsx info`). Codegen covers interpolation,
> control flow, attributes with contextual escaping, the `|>` pipeline + filters,
> components/props/`{children}`, method components, named slots, and attribute
> fallthrough. Still in progress: some CLI commands (`vet`/`lsp`), `style`
> composition, and structured diagnostics. See the [roadmap](./ROADMAP.md).

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
