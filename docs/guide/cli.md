# CLI

Use `gsx` to scaffold a project, run the development loop, generate Go, and
format `.gsx` files.

```text
gsx [global flags] <command> [arguments]
```

## Choose a command

| Task | Command |
|------|---------|
| Create a starter project | `gsx init [dir]` |
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

## `gsx init` {#gsx-init}

Create the `simple` gsx + Vite starter in a new directory:

```bash
gsx init myapp
```

```text
gsx init [dir] [flags]
```

| Flag | Effect |
|------|--------|
| `--template simple` | Select the starter template. `simple` is the only current template. |
| `--module path` | Set the Go module path. The default is the target directory name. |
| `--force` | Overwrite an existing `go.mod` or `package.json`. |
| `--yes`, `-y` | Run setup commands without prompting. |

### Interactive setup {#interactive-mode-terminal}

In a terminal, `gsx init` asks for a project name when `dir` is omitted. It
then scaffolds the project and asks before running each setup command.

```bash
gsx init
```

Press Enter, `y`, or `yes` to run a step. Skipping one step does not skip the
remaining steps.

### Non-interactive setup {#non-interactive-mode-ci--redirected-stdin}

Use `--yes` when a script should scaffold and run every setup command:

```bash
gsx init myapp --module example.com/acme/myapp --yes
```

With redirected input and no `--yes`, `init` scaffolds the files but only
prints the setup commands for you to run.

`init` exits `0` on success, `2` for invalid usage or an existing protected
project file, and `1` when scaffolding or a setup command fails.

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

The `goimports` mode cannot add a missing import; it organizes imports already
present in the `.gsx` file. A CLI import mode overrides the
[`[formatter]` setting](./config.md#formatter--gsx-fmt--editor-formatting).

`gsx fmt` also formats CSS in `<style>` and JavaScript in executable `<script>`
bodies. Interpolation holes are preserved. If an embedded body cannot be
formatted safely, that body is left unchanged.

### Check formatting in CI

`-l` and `-d` exit `1` when any file differs, so either works as a CI check:

```bash
gsx fmt -l .
```

Parse, analysis, and write failures also exit `1`. Invalid flags, import-mode
combinations, or paths exit `2`; otherwise formatting exits `0`.

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
