# Getting started

Create a gsx app, start live reload, and make your first change.

## Prerequisites

- Go 1.24 or newer
- Node.js 18 or newer
- npm, or another Node package manager such as pnpm or Yarn

## Create a project

Install gsx and scaffold the starter:

```sh
go install github.com/gsxhq/gsx/cmd/gsx@latest
gsx init hello-gsx --yes
cd hello-gsx
```

If another program named `gsx` is installed, run `gsx version` before
scaffolding to check which binary your shell found.

`--yes` also adds gsx as a Go tool, tidies the module, and installs the Vite
dependencies.

## Start the development server

```sh
npm run dev
```

Open the URL printed in the terminal. The starter runs `go tool gsx dev`, so
you do not need a separate code generator or file watcher.

Using pnpm, Yarn, or another package manager? Run its equivalent of
`npm run dev`; use the [`[dev]` configuration](./config.md#dev-development-loop)
if you also want to replace the default `npx vite` command.

## Make the first change

Open `app.gsx`, change the text inside `<h1>`, and save. The server rebuilds
and the browser reloads with the new text.

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

The resulting server embeds the built Vite assets and does not run Vite.

## Next steps

- Follow [Learn gsx](./learn.md) for the normal component patterns.
- Keep the [syntax reference](./syntax.md) open while writing `.gsx`.
- Use the [playground](/playground) for quick experiments.
- See the [CLI reference](./cli.md#gsx-dev) and
  [`gsx.toml` reference](./config.md) when you need customization.
