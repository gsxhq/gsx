# gsx documentation

gsx is a templating language for Go: **templ-style `component` declarations** with
a **JSX-style markup body**, compiled to plain Go.

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

> **Status — alpha.** gsx is usable end to end, but the language and APIs may
> change before a stable release. See [Status](./guide/status.md) and
> [Roadmap](./ROADMAP.md).

## Start here

- **[Why gsx](./guide/vision.md)** — where gsx fits and what it avoids.
- **[Principles](./guide/principles.md)** — the design commitments.
- **[Syntax](./guide/syntax.md)** — the topic hub. The
  [test corpus](https://github.com/gsxhq/gsx/tree/main/internal/corpus/testdata/cases)
  pins accepted syntax with parse, codegen, and render goldens.
- **[Configuration](./guide/config.md)** — `gsx.toml` for filters and attribute rules.
- **[Extensions](./guide/extensions.md)** — code-based setup for function-valued options.

## Reference

- [Roadmap & status](./ROADMAP.md)
