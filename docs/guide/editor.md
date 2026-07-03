# Editor support

gsx ships two editor integrations: a **language server** (`gsx lsp`) for
diagnostics and navigation, and a **tree-sitter grammar**
([tree-sitter-gsx](https://github.com/gsxhq/tree-sitter-gsx)) for syntax
highlighting. Both are editor-agnostic — anything that speaks LSP or hosts
tree-sitter can use them today. A dedicated [VS Code extension](#vs-code) bundles
the language client and highlighting into one install.

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
| **Go-to-definition** | `textDocument/definition` | jump from a Go symbol anywhere Go appears — `{ }` interpolations, attribute expressions, spreads, class/style parts and their `: cond` guards, `{ if … }` conditional-attribute conditions, value-form `if`/`switch` control expressions and arms, `for`/`if`/`switch` clauses, and `{{ }}` blocks — to its definition; from a `<Card/>` tag to its `component` declaration; and from a `.go` component reference back to the `.gsx` |
| **Hover** | `textDocument/hover` | gopls-style type/signature for an identifier or expression in all the same positions as go-to-definition; a component tag shows its signature (answered from the AST even mid-edit when type-checking can't complete) |
| **Find references** | `textDocument/references` | `.go` call sites and `.gsx` tag sites for project components discovered by module analysis; external/non-project packages are skipped |
| **Formatting** | `textDocument/formatting` | canonical `gsx fmt` form, including unused-import removal — wire it to format-on-save |

**Deferred:** completion. References cover project components discovered during
module analysis; external/non-project references are deferred. See [Status](./status.md) and the
[roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md) for current scope.

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
reads `gsx.toml` in-process, so the filters and URL attribute rules you
[configure](./config.md) are reflected in its analysis.

> The `gsx` binary must be on the editor's `PATH` (`go install
> github.com/gsxhq/gsx/cmd/gsx@latest`), or point `cmd` at an absolute path /
> `go tool gsx`.

> **Name collision with Ghostscript.** Ghostscript also installs a binary named
> `gsx` (Homebrew symlinks it into `/opt/homebrew/bin`), which can shadow the gsx
> compiler on `PATH`. If `gsx version` prints a Ghostscript banner instead of
> `gsx v…`, your LSP client is launching the wrong binary — point `cmd` at the
> real one's absolute path (e.g. `$(go env GOPATH)/bin/gsx`). The
> [VS Code extension](#vs-code) detects and skips the impostor automatically.

## Syntax highlighting — tree-sitter

[tree-sitter-gsx](https://github.com/gsxhq/tree-sitter-gsx) is a tree-sitter
grammar that highlights gsx markup **and** the languages embedded in it: Go
(the file-level pass-through plus every `{ }` / `@{ }` hole and each `|>`
pipeline segment), JavaScript (`<script>` bodies and `` js`...` `` attribute
literals), and CSS (`<style>` bodies and `` css`...` `` attribute literals) are
highlighted by their own parsers via tree-sitter *injection*. `@{expr}` holes
inside those embedded languages are injected as Go, so an interpolation is
colored like Go, not like a string.

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

## VS Code

The [vscode-gsx](https://github.com/gsxhq/vscode-gsx) extension bundles syntax
highlighting and the `gsx lsp` language client, so diagnostics, navigation, and
formatting work out of the box — no generic LSP client to wire up.

- **Highlighting** uses a TextMate grammar (VS Code has no public tree-sitter
  highlighting API), with Go / JavaScript / CSS colored inside their embedded
  regions — the same grammar that highlights the gsx blocks in these docs.
- **Language features** come from `gsx lsp`, spawned automatically. The extension
  resolves the binary from the `gsx.server.path` setting, then `PATH`, `GOBIN`,
  and `GOPATH/bin`, **verifying each candidate is really the gsx compiler** — so a
  shadowing Ghostscript `gsx` is skipped rather than launched. The commands
  **gsx: Install/Update Language Server** and **gsx: Restart Language Server**
  help when it's missing or stale.

Install it from the
[VS Code Marketplace](https://marketplace.visualstudio.com/items?itemName=gsxhq.gsx):

```bash
code --install-extension gsxhq.gsx
```

To test an unreleased build, package the `.vsix` from the repo:

```bash
git clone https://github.com/gsxhq/vscode-gsx && cd vscode-gsx
npm ci && npm run package           # produces gsx-<version>.vsix
code --install-extension gsx-*.vsix
```
