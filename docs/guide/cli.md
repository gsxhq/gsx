# CLI

The `gsx` command-line tool generates Go from `.gsx` files, formats them, and
reports the resolved pipeline configuration.

```bash
go install github.com/gsxhq/gsx/cmd/gsx@latest
```

The stock `gsx` binary uses the hardcoded standard codegen. A project can build
its own binary that calls `gen.Main` with options to register custom pipeline
filters and URL attribute rules — that binary exposes the *same*
commands and flags described here.

## Usage

```
gsx [global flags] <command> [arguments]
```

| Command | What it does |
|---------|--------------|
| `generate [paths...]` | generate `.x.go` from `.gsx` files (default: `.`) |
| `dev [dir]` | run the warm generate, Go server, and browser-reload loop |
| `fmt [paths...]` | format `.gsx` files (canonical, idempotent) |
| `init [dir]` | scaffold a gsx + Vite starter app |
| `clean --cache` | remove the gsx cache directory |
| `info` | list the resolved pipeline filters and URL attribute rules |
| `lsp` | run the language server over stdio (JSON-RPC) |
| `version` | print the gsx version |
| `help` | show usage |

### Global flags

These flags are accepted before the command name. `generate` also accepts `-q`
and `-v` after the path arguments:

| Flag | Effect |
|------|--------|
| `-C dir` | change to `dir` before resolving path arguments |
| `-q` | quiet: suppress success output |
| `-v` | verbose: list each written file |

`-q` / `-v` only affect `generate`'s success output.

## gsx init

```
gsx init [dir] [--template simple] [--module path] [--force] [--yes|-y]
```

Scaffolds a new gsx + Vite starter app into `dir` (default: `.`).
The `simple` template creates a project with a stock `net/http` ServeMux server,
a `.gsx` component, and a Vite dev-server config. Its `npm run dev` script runs
the complete development loop through the project-local gsx tool.

| Flag | Effect |
|------|--------|
| `--template` | starter template to use (default: `simple`) |
| `--module` | Go module path (default: basename of the target directory) |
| `--force` | overwrite an existing `go.mod` or `package.json` |
| `--yes`, `-y` | run all setup steps without prompting |

### Interactive mode (terminal)

When run on a terminal (`gsx init`), the command:

1. Prompts for the project name if no `dir` argument is given (default: `gsx-app`).
2. Scaffolds the starter files.
3. Confirms and runs each setup step in order:
   - `go get -tool github.com/gsxhq/gsx/cmd/gsx@latest`
   - `go mod tidy`
   - `npm install`

Each step shows the command and asks `[Y/n]`; pressing Enter or typing `y`/`yes`
runs it. Type `n` to skip a step and continue with the rest.

On success it prints:

```
✓ Done!
  cd <dir>
  npm run dev
```

`npm run dev` runs `go tool gsx dev`, which starts the Go server and Vite
front door. Open `http://localhost:5173`.

### Non-interactive mode (CI / redirected stdin)

When stdin is not a terminal (pipe, redirect, CI environment), `gsx init`
scaffolds the project and **prints the setup steps** instead of running them:

```
Scaffolded a gsx + Vite app. Next steps:
  cd <dir>
  go get -tool github.com/gsxhq/gsx/cmd/gsx@latest
  go mod tidy
  npm install
  npm run dev
```

Use `--yes`/`-y` to run all setup steps unattended without prompting (useful in
CI scripts or when you want a one-shot setup):

```bash
gsx init myapp --yes
```

**Exit codes:** `0` on success; `2` on a usage error (unknown template, or an
existing `go.mod`/`package.json` without `--force`); `1` on a step failure or
I/O error. When a step fails the remaining commands are printed to stderr.

> **Planned template:** `structpages` — a struct-based routing starter built on
> the [structpages](https://github.com/jackielii/structpages) framework
> (htmx + templ). Not yet available; only `simple` is currently implemented.

## gsx dev

```
gsx dev [dir] [--web command] [--no-web] [--build command] [--run command]
              [--log] [--log-file path] [--no-log]
```

`gsx dev` owns the application development loop in one foreground process. It
keeps gsx's type information warm, regenerates `.x.go` files, builds and safely
swaps the Go server, supervises Vite, and drives Vite's browser error overlay and
reload. A failed generation or build leaves the last working server running.

The generated starter exposes it as:

```sh
npm run dev        # runs go tool gsx dev
go tool gsx dev    # equivalent direct command
```

Vite requires Node.js and a package manager. The default front-door command is
`npx vite`; npm can be replaced with pnpm, Yarn, or another compatible tool by
setting `web` in [`gsx.toml`](./config#dev-development-loop), or with `--web`.

| Flag | Effect |
|------|--------|
| `--web command` | front-door command (default: `npx vite`) |
| `--no-web` | manage generation and the Go server without starting Vite |
| `--build command` | Go server build command |
| `--run command` | built server run command |
| `--log` | write a backend log in gsx's per-project cache directory |
| `--log-file path` | write the backend log to an explicit path |
| `--no-log` | disable logging, including a log configured in `gsx.toml` |

Command flags are whitespace-split and executed directly, without a shell. Use
the array form in `gsx.toml` when arguments need precise boundaries.

By default the built server and optional bare `--log` output live in the
per-project operating-system cache, so the loop does not add temporary artifacts
to the working tree. The generated `.x.go` files remain beside their `.gsx`
sources.

## generate

Compiles each `.gsx` file to a sibling `.x.go` file that the Go compiler
type-checks and builds:

```bash
gsx generate            # the current directory
gsx generate ./web ./mail
gsx -v generate         # also list every written file
```

Path arguments are files or directories; with none, `generate` defaults to `.`.

| Flag | Effect |
|------|--------|
| `-no-cache` | bypass the content-hash cache; regenerate everything |
| `-json` | emit diagnostics as a JSON array to stdout |
| `-watch` | stay running and regenerate on every `.gsx`/`.go` change (see below) |
| `-format` | `ndjson` makes `-watch` stream machine-readable events on stdout |

These subcommand flags may appear before or after the path arguments.

### Watch mode

`gsx generate --watch` runs as a long-lived process: it watches your `.gsx`
(and non-generated `.go`) sources and regenerates on each change, keeping the
type-resolution environment **warm** so each rebuild is in-process — no fresh
`go/packages.Load` per save. On a small package this takes a warm regenerate
from ~140 ms (a cold one-shot `gsx generate`) down to **~1–2 ms**. A change to a
`.go`/`go.mod`/`go.sum` file rebuilds the warm resolver first (one reload), then
regenerates; pure `.gsx` edits always take the fast path. Generated `*.x.go`,
and the `tmp/`, `dist/`, `node_modules/`, `.git/` directories, are never watched.

```bash
gsx generate --watch                 # human output on stderr; Ctrl-C to stop
gsx generate --watch --format=ndjson # one JSON event per line on stdout
```

With `--format=ndjson`, stdout is a clean newline-delimited JSON stream (all
logs go to stderr). This supports integrations that run the standalone watcher;
`gsx dev` performs generation in-process instead:

- `{"event":"start","root":"<abs>","watching":["<dir>",…]}`
- `{"event":"generated","ok":true,"durationMs":2,"written":["page.x.go"],"diagnostics":[]}`
- `{"event":"generated","ok":false,"durationMs":1,"written":[],"diagnostics":[ … ]}` — `diagnostics` is the same shape as `gsx generate --json`
- `{"event":"error","message":"<text>"}` — an operational failure (not a compile diagnostic)

**Diagnostics** are rendered in one of three forms:

- **rich** — rustc-style with a source snippet and caret, when stderr is a terminal
- **compact** — one line per diagnostic (`file:line:col: severity[code]: message`), when stderr is redirected
- **JSON** — a machine-readable array, with `-json`

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | success — files written (or nothing to do) |
| `1` | one or more error-severity diagnostics, or an I/O / module-graph error |
| `2` | usage error (e.g. a path that does not exist) |

## fmt

Formats `.gsx` files to their canonical, idempotent form. The flag surface is
faithful to `gofmt`:

```bash
gsx fmt                 # print formatted "." to stdout
gsx fmt -w ./web        # rewrite files in place
gsx fmt -l              # list files whose formatting differs
gsx fmt -d file.gsx     # show a unified diff of the changes
gsx fmt -w -no-imports  # rewrite, but keep unused imports
```

| Flag | Effect |
|------|--------|
| (none) | write each file's formatted source to stdout |
| `-w` | rewrite each changed file in place |
| `-l` | list the paths of files whose formatting differs |
| `-d` | write a unified diff of the changes to stdout |
| `-no-imports` | keep unused imports (skip the module analysis that finds them) |

Path arguments are `.gsx` files or directories (walked recursively, skipping
`.git`, hidden dirs, `vendor`, `node_modules`, and `testdata`). No arguments
formats `.` recursively.

**Line width.** The formatter wraps at 80 columns by default; set
[`[formatter]` `print_width`](./config.md#formatter--gsx-fmt--editor-formatting)
in `gsx.toml` to change it. The language server reads the same setting, so
`gsx fmt` and format-on-save always agree.

**Unused imports.** Like `goimports`, `gsx fmt` **removes unused imports** from a
`.gsx` file's pass-through Go by default — detected via the type-checker, so it
runs a module analysis (`go list` + type-check). Pass `-no-imports` to format
whitespace only and skip that analysis (faster, and works outside a resolvable
module). The language server's format action drops unused imports too, so
format-on-save in an editor keeps imports tidy.

**Embedded CSS & JS.** `gsx fmt` also formats the languages embedded in your
markup — the CSS inside `<style>`, and (in a follow-up) the JavaScript inside
`<script>` — so a whole `.gsx` file has one formatter. CSS lands first with a
small built-in formatter; JS follows. `@{ }` interpolation holes inside a body
are preserved exactly across formatting.

Formatting embedded code is **correct-or-verbatim**: if a body can't be parsed
cleanly, gsx leaves it byte-for-byte untouched rather than risk mangling it —
the same safety rule as Go-fragment formatting. The built-in CSS formatter is
deliberately minimal and **replaceable** with your own (e.g. a Prettier
shell-out) — see [Extending gsx](./extensions.md#custom-cssjs-formatter).

> **In development.** Embedded `<style>` CSS formatting is being built now
> (`<script>` JS after it). On the current release, `<style>` and `<script>`
> bodies are still emitted verbatim. The `gsx fmt` flags above are stable.

**Exit codes:** `0` on success; `1` on a parse error, or — with `-l` / `-d` —
when any file differs. The non-zero-on-difference behavior of `-l` / `-d` is
deliberately CI-friendly: it lets a build fail when sources are not canonically
formatted, the way `gofmt -l` is used as a check.

```bash
# CI gate: fail if anything is unformatted
gsx fmt -l . && echo "formatted"
```

## info

Prints the resolved pipeline filters — the table that drives `{ x |> filter }`
lowering — and any configured URL attribute rules. Useful for seeing which
filters are in scope and, when packages shadow each other, which one wins (last
package wins).

```bash
gsx info
gsx info --json         # machine-readable manifest
```

```
gsx v0.0.0-…

Filter packages (last-wins):
  github.com/gsxhq/gsx/std

Filters (6):
  default   std.Default   param
  join      std.Join      param
  lower     std.Lower     bare
  trim      std.Trim      bare
  truncate  std.Truncate  param
  upper     std.Upper     bare

minify: css=none js=none

Environment:
  GSX_MINIFY  unset  minify <style>/<script>: none|full (overrides [minify])
```

Each filter is `bare` (`{ x |> upper }`) or `param` (`{ x |> truncate(10) }`).
A `(shadows …)` note marks a filter whose name overrides an earlier package's.

## clean

```bash
gsx clean --cache       # remove the gsx cache directory
```

Removes the cache directory used by `generate`. As a safety measure it refuses
to delete a directory that is not a gsx cache (the directory must contain the
standard `CACHEDIR.TAG` sentinel), so pointing `GSXCACHE` at a non-cache path
will not nuke it.

## lsp

Runs the gsx language server over stdin/stdout, speaking the
[Language Server Protocol](https://microsoft.github.io/language-server-protocol/)
as JSON-RPC. It is launched by an editor, not invoked by hand:

```bash
gsx lsp                 # blocks, reading LSP messages on stdin
```

The server analyzes each open package using the resolved configuration and
provides diagnostics, definition, hover, references, and formatting without
writing `.x.go` to disk. Existing saved `.gsx` files are analyzed live;
brand-new unsaved buffers are not yet picked up.

## version

```bash
gsx version
```

Prints the version embedded by `go install` (a pseudo-version like
`v0.0.0-20260624…`), or `(devel)` for a local `go run` / `go build`.

## Environment

| Variable | Effect |
|----------|--------|
| `GSXCACHE` | `off` disables the cache; a path overrides the cache directory. Unset uses the OS user cache dir (`os.UserCacheDir()/gsx`). |

`generate` caches output keyed on a content hash so unchanged files are not
regenerated. The cache is bypassed by `-no-cache`, by `GSXCACHE=off`, or when a
custom binary configures a CSS/JS minifier (functions are not hashable).

## Status

> **Alpha.** The shipped command set is listed above. Planned commands and
> deferred diagnostics work are tracked in [Status](./status.md) and the
> [Roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
