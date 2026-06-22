# gsx documentation

gsx is a templating language for Go: **templ-style `component` declarations** with
a **JSX-style markup body**, compiled to plain Go.

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

> **Status — alpha.** Language design is stable; the parser, runtime, and codegen
> phase 1 are done. The CLI is a work in progress, so gsx is **not yet runnable
> end-to-end**. See the [roadmap](./ROADMAP.md).

## Start here

- **[Why gsx](./guide/vision.md)** — the problem it solves and the bet behind it.
- **[Principles](./guide/principles.md)** — the design commitments.
- **[Syntax](./guide/syntax.md)** — a quick tour; the [`examples/`](../examples/)
  corpus is the canonical reference.

## Reference

- [Roadmap & status](./ROADMAP.md)
- [Design docs](./superpowers/specs/) — the internal specifications.
