# Editor support

gsx ships two editor integrations: a **language server** (`gsx lsp`) for
diagnostics and navigation, and a **tree-sitter grammar**
([tree-sitter-gsx](https://github.com/gsxhq/tree-sitter-gsx)) for syntax
highlighting. Both are editor-agnostic — anything that speaks LSP or hosts
tree-sitter can use them today. A dedicated VS Code extension is
[planned](#vs-code-planned).

## Language server — `gsx lsp`

`gsx lsp` runs an in-process language server over stdio, speaking the
[Language Server Protocol](https://microsoft.github.io/language-server-protocol/)
as JSON-RPC. It analyzes each open package through the real codegen pipeline
(parse → type-check → harvest) **without writing `.x.go` to disk**, so a `.gsx`
file is checked exactly the way `gsx generate` would build it.

It is launched by your editor, not run by hand:

```bash
gsx lsp                 # blocks, reading LSP messages on stdin
```

### What it does

| Feature | LSP method | Notes |
|---|---|---|
| **Diagnostics** | `publishDiagnostics` | positioned parse + type errors (with severity, code, help) from the shared diagnostics engine; re-analyzed on every change, with multi-error and component-boundary recovery |
| **Go-to-definition** | `textDocument/definition` | jump from a Go symbol in a `{ }`/attribute expression to its `.go` definition; from a `<Card/>` tag to its `component` declaration; and from a `.go` component reference back to the `.gsx` |
| **Hover** | `textDocument/hover` | gopls-style type/signature for an identifier or expression; a component tag shows its signature (answered from the AST even mid-edit when type-checking can't complete) |
| **Find references** | `textDocument/references` | `.go` call sites and `.gsx` tag sites for a component, within the package |
| **Formatting** | `textDocument/formatting` | canonical `gsx fmt` form, including unused-import removal — wire it to format-on-save |

**Not yet:** completion, pipeline-aware definition/hover (an expression behind a
`|>` returns nothing today), cross-package references, and dotted/cross-package
component tags (`<ui.Button/>`). See the
[roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md) for status.

### Configure your editor

The server needs three things: the command `gsx lsp`, the `gsx` filetype mapped
to `.gsx`, and a project root (the directory holding `gsx.toml` or `go.mod`).

**Neovim** (≥ 0.10, built-in LSP client) — no plugin required:

```lua
-- Treat .gsx files as filetype "gsx".
vim.filetype.add({ extension = { gsx = "gsx" } })

-- Start `gsx lsp` for every gsx buffer, rooted at the project.
vim.api.nvim_create_autocmd("FileType", {
  pattern = "gsx",
  callback = function(args)
    vim.lsp.start({
      name = "gsx",
      cmd = { "gsx", "lsp" },
      root_dir = vim.fs.root(args.buf, { "gsx.toml", "go.mod" }),
    })
  end,
})
```

**Any LSP client** — the equivalent settings are: command `gsx lsp`, language id
`gsx`, document selector `*.gsx`, root markers `gsx.toml` / `go.mod`. The server
reads `gsx.toml` in-process, so the filters and attribute rules you
[configure](./config.md) are reflected in its analysis.

> The `gsx` binary must be on the editor's `PATH` (`go install
> github.com/gsxhq/gsx/cmd/gsx@latest`), or point `cmd` at an absolute path /
> `go tool gsx`.

## Syntax highlighting — tree-sitter

[tree-sitter-gsx](https://github.com/gsxhq/tree-sitter-gsx) is a tree-sitter
grammar that highlights gsx markup **and** the languages embedded in it: Go
(the file-level pass-through plus every `{ }` / `@{ }` hole and each `|>`
pipeline segment), JavaScript (`<script>` and JS attributes), and CSS
(`<style>` bodies) are highlighted by their own parsers via tree-sitter
*injection*. So a Go expression inside an interpolation is colored like Go, not
like a string.

### Neovim

Neovim 0.10+ has built-in tree-sitter, so the grammar installs the **native**
way — a compiled parser on `runtimepath` plus query files, with **no
`nvim-treesitter` dependency** (it was archived in 2026). The grammar's
[README](https://github.com/gsxhq/tree-sitter-gsx#install-in-neovim) has a
self-healing `lazy.nvim` recipe (rebuilds the parser when it's stale) and a
manual install.

**Prerequisites:** Neovim ≥ 0.10, a C compiler, and the
[tree-sitter CLI](https://github.com/tree-sitter/tree-sitter), plus the Go /
JavaScript / CSS parsers installed for the embedded-language injection (most
configs already have them).

> Pair tree-sitter (highlighting) with `gsx lsp` (diagnostics + navigation) for
> the full Neovim experience — they are independent and complementary.

## VS Code (planned)

A dedicated VS Code extension — bundling syntax highlighting and the `gsx lsp`
language client so it works out of the box — is on the roadmap. Until it ships,
VS Code users can run `gsx lsp` through a generic LSP client extension using the
settings above (command `gsx lsp`, language `gsx`, `*.gsx` selector).
