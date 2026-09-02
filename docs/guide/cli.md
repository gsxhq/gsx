# CLI

Use `gsx` to scaffold a project, run the development loop, generate Go, and
format `.gsx` files.

```text
gsx [global flags] <command> [arguments]
```

## Choose a command

| Task | Command |
|------|---------|
| Create a new project directory | `gsx new <dir>` |
| Scaffold a starter into the current directory | `gsx init` |
| Generate, build, and reload while editing | `gsx dev [dir]` |
| Generate `.x.go` files | `gsx generate [paths...]` |
| Format `.gsx` files | `gsx fmt [paths...]` |
| Inspect the resolved project setup | `gsx info` |
| Remove the generation cache | `gsx clean --cache` |
| Start the editor language server | `gsx lsp` |
| Show version and build information | `gsx version` |
| List commands | `gsx help` |

## Global flags

`-C` is the only true global flag. It must appear before the command name and
sets the base directory for commands that resolve project paths or configuration:

```bash
gsx -C ./web generate .
```

| Flag | Effect |
|------|--------|
| `-C dir` | Use `dir` as the base for project paths and configuration. |

`-q` and `-v` may also appear before or after `generate`, but they affect that
command only:

```bash
gsx -q generate
gsx generate -v ./views
```

## `gsx new` {#gsx-new}

Create a new project directory — the fastest way to start a gsx project:

```bash
gsx new myapp
```

```text
gsx new <dir> [flags]
```

| Flag | Effect |
|------|--------|
| `--template name` | Select the starter template by name. Omit it in a terminal to get the interactive picker. |
| `--module path` | Set the Go module path. The default is the target directory name. |
| `--from dir-or-module` | Fetch the template from a local checkout or a module path instead of the registry. Overrides `--template`. |
| `--force` | Overwrite an existing `go.mod` or `package.json`. |
| `--yes`, `-y` | Run setup commands without prompting. |

`dir` is required (positionally, or via the interactive prompt below); a
non-interactive `gsx new` with no directory is a usage error — creating `.`
is [`gsx init`](#gsx-init)'s job.

### Templates and the picker {#new-templates}

Two templates ship today: `simple` is a stock `net/http` + gsx + Vite
starter, embedded in the `gsx` binary. `saas` is the flagship full-stack
starter — auth, dashboard, CRUD, SQLite, htmx — fetched from
`github.com/gsxhq/saas-template` on first use.

In a terminal, running `gsx new` with no directory prompts for a project name
and, unless `--template` (or `--from`) was given explicitly, then shows a
numbered picker:

```bash
gsx new
```

```text
Project name [gsx-app]: myapp
Select a template:
  1) saas          Full-stack SaaS starter: auth, dashboard, CRUD, SQLite, htmx (fetched from github.com/gsxhq/saas-template)
  2) simple        Stock net/http ServeMux + gsx + Vite dev loop.
Select a template [simple]:
```

Press Enter to accept the default, type a number, or type a template name
directly. An invalid answer reprints the list and reprompts once, then falls
back to the default.

### Fetching a template {#new-from}

A template backed by a module path (like `saas`) is fetched from the module
proxy (`GOPROXY`, default `https://proxy.golang.org`) at its latest version
and personalized in place: the module path is rewritten in `go.mod`, in
`.go`/`.gsx` imports and `gsx.toml`, and `package.json`'s `name` is set to
the new module's basename. `GOPROXY=off` (or one with no `http(s)://` entry)
disables fetching — use `--from` with a local directory instead.

`--from` overrides `--template`'s source and is told apart the way the `go`
command tells a local path from an import path:

```bash
gsx new myapp --from ../my-template          # ./, ../ or absolute ⇒ local directory (must exist)
gsx new myapp --from example.com/org/template # anything else ⇒ module path, fetched
```

A local directory is read the way `go` would publish it as a module: `.git`
and other VCS directories, nested modules, vendored packages and symlinks are
omitted. Anything else a checkout carries that shouldn't ship — `node_modules/`,
a local `.env` — belongs in the manifest's `strip` list.

#### `gsx-template.json` {#template-manifest}

An optional manifest at the template root (never copied itself):

```json
{
  "strip": ["docs/", ".github/", "node_modules/", "*.md"],
  "env": { "APP_SECRET": "secret-hex-32", "APP_ENV": "literal:dev" }
}
```

`strip` patterns are `path.Match` globs tested against each file's path and
every ancestor directory, so `docs/`, `docs` and `docs/*` all remove the
subtree; `*.md` removes root-level markdown only. `env` entries are appended
to the scaffold's `.env` (keys already present are left alone):
`secret-hex-32` generates a fresh 32-byte hex secret, `literal:<v>` writes
`<v>`.

The target module path (the directory basename, or `--module`) is validated
like `go mod init` does, so a bare `myapp` is accepted; pass `--module` for a
publishable path.

`new` exits `0` on success, `2` for invalid usage (a missing or extra
directory argument, a bad `--from` value, an invalid target module path) or an
existing protected project file, and `1` when fetching, scaffolding, or a
setup command fails.

## `gsx init` {#gsx-init}

Scaffold the `simple` gsx + Vite starter into the **current directory**:

```bash
gsx init
```

```text
gsx init [flags]
```

`init` never takes a directory argument — it always scaffolds into the
directory you run it from (or `-C`'s target). Use [`gsx new <dir>`](#gsx-new)
to create a new project directory instead; passing one to `init` is a usage
error that names the redirect:

```text
$ gsx init myapp
gsx: init scaffolds into the current directory; use 'gsx new <dir>' to create a project directory
```

| Flag | Effect |
|------|--------|
| `--template simple` | Select the starter template. `simple` is the only template `init` can scaffold — `init` never fetches; see [`gsx new`](#new-from) for fetched templates. |
| `--module path` | Set the Go module path. The default is the current directory's name. |
| `--force` | Overwrite an existing `go.mod` or `package.json`. |
| `--yes`, `-y` | Run setup commands without prompting. |

### Interactive setup {#interactive-mode-terminal}

In a terminal, `gsx init` scaffolds the project immediately — there is no
project-name prompt, since the target is always the current directory — then
asks before running each setup command.

Press Enter, `y`, or `yes` to run a step. Skipping one step does not skip the
remaining steps.

### Non-interactive setup {#non-interactive-mode-ci--redirected-stdin}

Use `--yes` when a script should scaffold and run every setup command:

```bash
gsx init --module example.com/acme/myapp --yes
```

When standard input is a pipe or regular file (not a character device) and
`--yes` is omitted, `init` scaffolds the files, then prints the setup commands
instead of running them.

`init` exits `0` on success, `2` for invalid usage (including a positional
directory argument, which is `new`'s job) or an existing protected project
file, and `1` when scaffolding or a setup command fails.

## `gsx dev` {#gsx-dev}

Run the development loop from the project directory:

```bash
gsx dev
```

Pass an optional project directory with `gsx dev [dir]`.

Relevant source changes regenerate as needed, build, swap the server, and reload
the browser. After the first successful build, later generation or build
failures leave the last working server running. A `.env` change only restarts
the backend with fresh environment values; it does not regenerate or build.
See the [development loop](./dev-loop.md) for the full file-by-file behavior.

| Flag | Effect |
|------|--------|
| `--web command` | Set the front-door command. The default is `npx vite`. |
| `--no-web` | Run generation and the Go server without the front door. |
| `--build command` | Set the server build command. |
| `--run command` | Set the built-server command. |
| `--log` | Copy backend output to the default per-project log file. |
| `--log-file path` | Copy backend output to `path`. |
| `--no-log` | Disable backend file logging, including logging from `gsx.toml`. |

### Common customizations

Run without Vite when another tool provides the front door:

```bash
gsx dev --no-web
```

Override the build and run commands for one session:

```bash
gsx dev --build "go build -o ./tmp/app ./cmd/site" --run "./tmp/app"
```

Command flag values are split on whitespace. For arguments that need exact
boundaries, use arrays in the [`[dev]` configuration](./config.md#dev-development-loop).

Before the first successful build there is no previous server to keep alive.
Fix the startup error and save again. A clean signal-driven shutdown exits `0`;
invalid flags exit `2`, and fatal startup errors exit `1`.

## `gsx generate` {#generate}

Generate a sibling `.x.go` file for every `.gsx` file under the selected paths:

```bash
gsx generate
gsx generate ./views ./email
```

With no path, the command uses `.`. Directory paths are searched recursively.

| Flag | Effect |
|------|--------|
| `--no-cache` | Regenerate without reading or writing cached results. |
| `--json` | Write diagnostics as one JSON array to stdout. |
| `--watch` | Keep running and regenerate when source files change. |
| `--format=ndjson` | In watch mode, write one machine-readable event per line. |
| `-q` | Suppress the success summary. |
| `-v` | List each written or removed file before the summary. |

Generate flags may appear before or after path arguments.

### Diagnostics and exit status {#when-generation-fails}

Normal diagnostics go to stderr. Use JSON when another program consumes them:

```bash
gsx generate --json ./views
```

| Exit | Meaning |
|------|---------|
| `0` | Generation succeeded, including when everything was already current. |
| `1` | A source diagnostic or operational error prevented generation. |
| `2` | The command, configuration, or a path was invalid. |

When a `.gsx` file has a generation error, gsx replaces its previous generated
file with a deliberately non-compiling marker. This prevents `go build` from
silently using stale output. Fix the `.gsx` file and run `generate` again.

Deleting a `.gsx` file removes its generated sibling on the next generation
run. Only files with gsx's generated-file header are removed; a hand-written
file with the same name is left alone. I/O and project-loading failures do not
replace files with error markers.

### Watch mode

Use watch mode when an integration needs generation without the full dev loop:

```bash
gsx generate --watch
```

For a machine-readable stream, use newline-delimited JSON:

```bash
gsx generate --watch --format=ndjson
```

Human watch output goes to stderr. In NDJSON mode, stdout contains only event
objects; diagnostic fields use the same shape as `--json`.

## `gsx fmt` {#fmt}

Rewrite `.gsx` files in place before committing:

```bash
gsx fmt -w .
```

Paths may be files or directories. Directories are searched recursively; with
no path, the command formats `.`. Hidden directories, `.git`, `vendor`,
`node_modules`, and `testdata` are skipped.

| Flag | Effect |
|------|--------|
| none | Write formatted source to stdout. |
| `-w` | Rewrite changed files in place. |
| `-l` | List files whose source would change. |
| `-d` | Show a unified diff. |
| `-imports=goimports` | Remove unused imports and normalize import declarations. This is the default. |
| `-imports=gofmt` | Format existing imports without removing, merging, or regrouping them. |
| `-no-imports` | Alias for `-imports=gofmt`. |
| `-stdin-filename=PATH` | Read the source from stdin and treat it as `PATH`. |

The `goimports` mode cannot add a missing import; it organizes imports already
present in the `.gsx` file. A CLI import mode overrides the
[`[formatter]` setting](./config.md#formatter--gsx-fmt--editor-formatting).

`gsx fmt` also formats CSS in `<style>` and JavaScript in executable `<script>`
bodies. Interpolation holes are preserved. If an embedded body cannot be
formatted safely, that body is left unchanged.

`` js` ``/`` css` `` **attribute** values are re-indented the same way as
`<script>`/`<style>` bodies. Plain `"…"` string attributes are left verbatim.

### Text flow

Inline elements (`code`, `a`, `span`, `em`, `b`, `img`, and other
phrasing-content tags) stay in the surrounding text flow: the formatter never
breaks their children open to meet the width budget. If a line has no other
legal break point, it stays over budget rather than exploding an inline
element.

Long prose wraps between words instead: a newline between two words collapses back to a single space, a newline where two pieces of markup touch adds nothing, but a newline against a space next to a tag would delete that space — so the formatter only ever breaks where no space can be lost.

`{" "}` sticks to the word before it and never starts a line.

Author layout still wins: breaking the line right after an opening tag's `>`
keeps that element block-formatted, even if its content would otherwise fit
inline.

### Formatting stdin

`-stdin-filename` formats content that is not on disk. `PATH` names the file
in `-l`/`-d` output and error messages and selects its `gsx.toml`,
`.editorconfig`, and package for import analysis — nothing is read from it,
and it need not exist. Path arguments and `-w` are rejected in this mode.

A pre-commit hook uses it to check the **staged** blob rather than the working
copy:

```bash
git show ":$f" | gsx fmt -l -stdin-filename "$f"
```

### Check formatting in CI

`-l` and `-d` exit `1` when any file differs, so either works as a CI check:

```bash
gsx fmt -l .
```

| Exit | Meaning |
|------|---------|
| `0` | Nothing failed; with `-l`/`-d`, nothing differs. |
| `1` | With `-l`/`-d` only: at least one file differs and nothing failed. |
| `2` | Something failed: a usage error (invalid flag, import-mode combination, or path) or a read, parse, analysis, or write failure on any file. |

A failure wins over a difference, so a script can tell "run `-w`" from "this
file is broken".

## `gsx info` {#info}

Inspect the configuration that gsx resolves for the current project:

```bash
gsx info
```

The human view shows the active config path and resolved filters, renderers,
attribute rules, minification, formatter width, and environment overrides.
Use JSON for automation:

```bash
gsx info --json
```

Human and JSON output are different inspection views, not identical encodings
of the same fields. Scripts should consume `--json` rather than parse the human
table.

Resolution failures exit `1`. Invalid arguments or project configuration exit
`2`; a successful inspection exits `0`.

## `gsx clean` {#clean}

Remove the generation cache:

```bash
gsx clean --cache
```

The command exits successfully when the cache is disabled or absent. It refuses
to remove a directory that does not contain gsx's cache marker, which protects
against an unsafe `GSXCACHE` value. A refusal or removal failure exits `1`;
invalid flags exit `2`.

## `gsx lsp` {#lsp}

Editors start the language server over standard input and output:

```bash
gsx lsp
```

You normally do not run this command yourself. See [Editor setup](./editor.md)
for VS Code, Neovim, and generic client configuration.

## `gsx version` {#version}

Print the installed version:

```bash
gsx version
```

When available, the output also includes the commit revision, commit time,
dirty-tree state, and Go toolchain version. Local builds without an embedded
module version report `(devel)`.

## Environment variables {#environment}

| Variable | Effect |
|----------|--------|
| `GSX_MINIFY=none|full` | Override [`[minify]`](./config.md#minify--asset-minification-level) for both `<style>` and `<script>`. |
| `GSXCACHE=off` | Disable the generation cache. |
| `GSXCACHE=path` | Use `path` instead of the operating-system user cache directory. |

For minification, a programmatic option in a [custom gsx binary](./extensions.md)
wins over `GSX_MINIFY`, which wins over `gsx.toml`. Use `--no-cache` for a
single uncached generation run.

## Stability {#status}

The CLI is alpha. This page lists the commands that are available now; see
[Status](./status.md) for the broader shipped surface.
