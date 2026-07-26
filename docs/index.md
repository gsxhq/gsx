# gsx documentation

gsx is a templating language for Go: **templ-style `component` declarations** with
a **JSX-style markup body**, compiled to plain Go.

```
.gsx → parser → AST → codegen → .x.go → go build → HTML
```

That pipeline is Go end to end: **no Node.js, npm, or JavaScript runtime** is
needed to build or serve a gsx app. The default starter adds Vite to bundle
front-end assets — optional, and never part of the running server. See
[Do I need Node.js?](./guide/getting-started.md#do-i-need-node-js).

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
