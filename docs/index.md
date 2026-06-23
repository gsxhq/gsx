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
- **[Syntax](./guide/syntax.md)** — a quick tour; the [`examples/`](../examples/)
  corpus is the canonical reference.
- **[Extensions](./guide/extensions.md)** — custom attribute classification, filter packages, and the `cmd/gsx` registration pattern.

## Reference

- [Roadmap & status](./ROADMAP.md)
- [Design docs](./superpowers/specs/) — the internal specifications.
