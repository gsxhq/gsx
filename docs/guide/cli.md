# CLI

The `gsx` command-line tool generates Go from `.gsx` files, formats them, and
reports the resolved pipeline configuration.

```bash
go install github.com/gsxhq/gsx/cmd/gsx@latest
```

The stock `gsx` binary uses the hardcoded standard codegen. A project can build
its own binary that calls `gen.Main` with options to register custom pipeline
filters and attribute-classification rules — that binary exposes the *same*
commands and flags described here.

## Usage

```
gsx [global flags] <command> [arguments]
```

| Command | What it does |
|---------|--------------|
| `generate [paths...]` | generate `.x.go` from `.gsx` files (default: `.`) |
| `fmt [paths...]` | format `.gsx` files (canonical, idempotent) |
| `clean --cache` | remove the gsx cache directory |
| `info` | list the resolved pipeline filters and attribute rules |
| `lsp` | run the language server over stdio (JSON-RPC) |
| `version` | print the gsx version |
| `help` | show usage |

### Global flags

Global flags apply to any command and **must come before the command name**
(`gsx -v generate`, not `gsx generate -v`):

| Flag | Effect |
|------|--------|
| `-C dir` | change to `dir` before resolving path arguments |
| `-q` | quiet: suppress success output |
| `-v` | verbose: list each written file |

`-q` / `-v` only affect `generate`'s success output; other commands ignore them.

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

These subcommand flags may appear before or after the path arguments.

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
```

| Flag | Effect |
|------|--------|
| (none) | write each file's formatted source to stdout |
| `-w` | rewrite each changed file in place |
| `-l` | list the paths of files whose formatting differs |
| `-d` | write a unified diff of the changes to stdout |

Path arguments are `.gsx` files or directories (walked recursively, skipping
`.git`, hidden dirs, `vendor`, `node_modules`, and `testdata`). No arguments
formats `.` recursively.

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
lowering — and any configured attribute-classification rules. Useful for seeing
which filters are in scope and, when packages shadow each other, which one wins
(last package wins).

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

The server analyzes each open package with the stock (std-filter) codegen
pipeline and publishes diagnostics — it never writes `.x.go` to disk. It is an
early slice: editing existing `.gsx` files is analyzed live, but a new buffer
that has never been saved to disk is not yet picked up.

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

> **Alpha.** `generate`, `fmt`, `info`, `clean`, `version`, and `lsp` (an early
> diagnostics-only slice) are implemented. A checker (`vet`) and richer
> language-server features are on the
> [roadmap](https://github.com/gsxhq/gsx/blob/main/docs/ROADMAP.md).
