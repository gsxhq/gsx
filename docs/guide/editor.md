# Editor support

Install the VS Code extension for the shortest setup. Other editors can run
`gsx lsp` and use the tree-sitter grammar separately.

## VS Code

Install the [gsx extension](https://marketplace.visualstudio.com/items?itemName=gsxhq.gsx):

```bash
code --install-extension gsxhq.gsx
```

The extension supplies highlighting and starts the language server when it can
find the gsx compiler. It checks `gsx.server.path`, then `PATH`, `GOBIN`, and
`GOPATH/bin`, and verifies each candidate before using it.

If the compiler is missing, run **gsx: Install/Update Language Server**. Wait
for the terminal command to finish, then run **gsx: Restart Language Server**.
Set `gsx.server.path` to an absolute path when automatic discovery is not right
for your setup.

## Neovim

Use Neovim's built-in LSP client for language features.

### Language server

```lua
vim.filetype.add({ extension = { gsx = "gsx" } })

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

### Syntax highlighting

Use the [tree-sitter-gsx grammar and source](https://github.com/gsxhq/tree-sitter-gsx).
Neovim needs a compiled parser registered as `gsx` on `runtimepath`, plus the
grammar's highlight and injection queries under `queries/gsx/`. Building the
parser from source requires a C compiler and the tree-sitter CLI.

The grammar includes Go directly. Only JavaScript and CSS regions are injected,
so install those two parsers to highlight `<script>`, `<style>`, `js` literals,
and `css` literals.

## Other editors

Configure any LSP client with these values:

| Setting | Value |
|---|---|
| Command | `gsx lsp` |
| Language ID | `gsx` |
| Document selector | `*.gsx` |
| Root markers | `gsx.toml`, `go.mod` |

For highlighting, use
[tree-sitter-gsx](https://github.com/gsxhq/tree-sitter-gsx) when your editor
supports tree-sitter.

## Language features

| Feature | What you get |
|---|---|
| Diagnostics | Parse, type, and component errors. |
| Hover | Go types and component signatures. |
| Go to definition | Go symbols and components across `.gsx` and `.go`. |
| Find references | Project component calls across `.gsx` and `.go`. |
| Formatting | Canonical `gsx fmt` output and project settings. |
| Document symbols | File components and top-level Go declarations. |
| Workspace symbols | Module components and top-level Go declarations. |
| Code actions | Organize imports and choose missing imports. |
| Completion | Go identifiers and members, pipe filters, component tags and attributes, HTML tags/attributes/values, and `hx-*` attributes when htmx is enabled. |

Completion returns plain text edits, not snippets, and does not add imports
yet; use the missing-import quick fix for that. References do not include
external packages. See [Status](./status.md) for the current limits.

## Organize imports {#organize-imports-on-save}

Enable formatting and import organization on save in VS Code:

```json
"[gsx]": {
  "editor.formatOnSave": true,
  "editor.codeActionsOnSave": {
    "source.organizeImports": "explicit"
  }
}
```

The organize-imports action removes unused imports, sorts them, and adds a
missing import when there is one unambiguous match. When several packages match,
use the **Add import** quick fix to choose one. Run `go get` first when the
package is not in your module.

Import organization still runs when
[`[formatter].imports`](./config.md#formatter--gsx-fmt--editor-formatting) is
`"gofmt"`; that setting controls formatting, not the explicit code action.

## Troubleshooting the `gsx` binary

Ghostscript can also install a command named `gsx`. Check what your editor will
launch:

```bash
gsx version
```

The compiler prints a line beginning with `gsx `. If you see a Ghostscript
banner, find the Go binary directories:

```bash
go env GOBIN GOPATH
```

Both outputs are directories. When `GOBIN` is non-empty, use `<GOBIN>/gsx`.
Otherwise use `<GOPATH>/bin/gsx`. Replace the bracketed value with the command's
output and enter that concrete executable path in your editor setting. The VS
Code extension performs the compiler check itself and skips unrelated binaries;
generic LSP clients do not.
