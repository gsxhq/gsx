# Getting started

Create a gsx app, start live reload, and make your first change.

## Prerequisites

- Go 1.24 or newer — the only requirement for gsx itself.
- Node.js 18 or newer with npm — only for the starter's Vite asset pipeline,
  which this page uses. See [Do I need Node.js?](#do-i-need-node-js).

## Do I need Node.js? {#do-i-need-node-js}

Not for gsx, and never to run a gsx server in production.

gsx is a Go program that turns `.gsx` into `.x.go`. Building and serving an app
is `gsx generate && go build` — one Go binary, nothing installed from npm.

Node enters only because the starter below pairs gsx with
[Vite](https://vite.dev) to bundle CSS and JavaScript, and only while you
develop and when you build those assets. The compiled server embeds the built
files and runs neither Vite nor Node. Skip it by writing your own `main.go` with
[`gsx generate`](./cli.md#generate), or run the loop with
[`gsx dev --no-web`](./cli.md#gsx-dev).

## Create a project

Install gsx and scaffold the starter:

```sh
go install github.com/gsxhq/gsx/cmd/gsx@latest
gsx init hello-gsx --yes
cd hello-gsx
```

If another program named `gsx` is installed, run `gsx version` before
scaffolding to check which binary your shell found. `@latest` is the newest
tagged release; to pin one, use its tag instead (see
[Releases and versioning](./status.md#releases-and-versioning)).

`--yes` also adds gsx as a Go tool, tidies the module, and installs the Vite
dependencies.

## Start the development server

```sh
npm run dev
```

Open the URL printed in the terminal. That npm script is a one-line wrapper
around `go tool gsx dev` — the watching, generation, and rebuilding are all
done by the Go binary, so you do not need a separate code generator or file
watcher. Run `go tool gsx dev` directly if you prefer.

After scaffolding, you can switch to pnpm, Yarn, or another package manager.
Run its equivalent of `npm run dev`; use the
[`[dev]` configuration](./config.md#dev-development-loop) if you also want to
replace the default `npx vite` command.

## Make the first change

Open `app.gsx`, change the text inside `<h1>`, and save. The server rebuilds
and the browser reloads with the new text.

`app.gsx` also shows reusable markup bound to a package-level `var` — inferred
as `gsx.Node` and interpolated like any other node:

```gsx
var footer = <><hr/><small>Built with gsx</small></>

component Page(children gsx.Node) {
	<main>{children}</main>
	{footer}
}
```

Generated `*.x.go` files are ignored by the starter. Do not edit or commit
them; gsx recreates them from the `.gsx` source.

For save behavior and build failures, see the [development loop](./dev-loop.md).

## Build for production

From a clean checkout with dependencies installed, build the assets, generate
Go, and compile the server:

```sh
npm run build
go tool gsx generate
go build -o app
./app
```

`npm run build` (`vite build`) bundles assets into `dist/`; the Go steps
generate and compile. The binary embeds those files, so deployment is the binary
alone — no Node.js, npm install, or Vite on the server. Only the bundling step
needs a JS toolchain, and only on the build machine.

## Next steps

- Follow [Learn gsx](./learn.md) for the normal component patterns.
- Keep the [syntax reference](./syntax.md) open while writing `.gsx`.
- Use the [playground](/playground) for quick experiments.
- See the [CLI reference](./cli.md#gsx-dev) and
  [`gsx.toml` reference](./config.md) when you need customization.
