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
| **Formatting** | `textDocument/formatting` | canonical `gsx fmt` form, honoring the configured [`[formatter] imports`](./config.md#formatter--gsx-fmt--editor-formatting) mode — wire it to format-on-save |
| **Organize imports** | `textDocument/codeAction` (`source.organizeImports`) | always removes unused imports, reorders, and adds an unambiguous missing import, regardless of the configured mode — see [below](#organize-imports-on-save) |
| **Add import (quickfix)** | `textDocument/codeAction` (`quickfix`) | one `Add import: <path>` action per candidate package, for an import `source.organizeImports` couldn't add unambiguously — see [below](#organize-imports-on-save) |

**Deferred:** completion. References cover project components discovered during
module analysis; external/non-project references are deferred. See [Status](./status.md) and the
[roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md) for current scope.

### Organize imports on save

The gsx language server offers the standard `source.organizeImports` code
action. It always organizes — remove unused, merge, dedup, group, sort — even
when [`[formatter] imports`](./config.md#formatter--gsx-fmt--editor-formatting)
is `"gofmt"`, exactly like gopls, where formatting can be plain gofmt while the
action still organizes.

```json
"[gsx]": {
  "editor.formatOnSave": true,
  "editor.codeActionsOnSave": { "source.organizeImports": "explicit" }
}
```

Because gsx has no partial formatter, the action's edit spans the whole
document: applying it also canonicalizes the rest of the file.

**It also adds a missing import — but only when unambiguous.** If an
unresolved qualifier like `rand.IntN` maps to exactly one package that could
supply it, `source.organizeImports` adds that import on save, right alongside
removing unused ones. If more than one package could supply the name —
`rand.Read` exists in both `crypto/rand` and `math/rand` — nothing is added on
save; a wrong import written unattended on every save is worse than none.

For an ambiguous name, use the **`quickfix`** code action instead. It offers
one `Add import: <path>` action per surviving candidate — `Add import:
crypto/rand` and `Add import: math/rand` for `rand.Read` — deduplicated by
resolved import path, so you choose. It shows up wherever your editor surfaces
quickfixes for the `undefined: rand` diagnostic.

Candidates come from two sources, both plain map lookups — **never a scan of
the module cache**:

- the standard library, from a table baked into the `gsx` binary at build time; and
- every package already in your module's dependency graph, which the analyzer
  has already type-checked as part of ordinary analysis.

A package that is not yet in your `go.mod` is **not** offered — run `go get`
first. Real `goimports` resolves this case by scanning the module cache,
measured at roughly 1.4 seconds per unresolved identifier — the normal state
while you're still mid-edit — so gsx deliberately does not.

Ambiguity is resolved **by symbol**, not just by package name: `rand.IntN`
offers only `math/rand/v2` (the only one of the three `rand` packages that
exports `IntN`), and `template.HTML` offers only `html/template`
(`text/template` has no `HTML` type). Go's `internal` visibility rule is
honored exactly — your own `myapp/internal/db` is offered from anywhere under
`myapp`, but `myapp/x/internal/db` is not offered from `myapp/y`, and a
standard-library `internal` package is never offered at all.

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
