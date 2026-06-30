# Getting started

This guide creates a small server-rendered gsx application, starts its development
loop, and makes the first live change.

## Prerequisites

- Go 1.24 or newer
- Node.js 18 or newer
- npm, or another Node package manager such as pnpm or Yarn

The starter uses npm in the commands below. Vite needs Node.js and a package
manager, but npm itself is not required.

## Create a project

Install the gsx CLI and scaffold the starter:

```sh
go install github.com/gsxhq/gsx/cmd/gsx@latest
gsx init hello-gsx --yes
cd hello-gsx
```

If another program named `gsx` is installed on your system, run `gsx version` to
verify the binary before scaffolding.

The `--yes` flag runs the starter's setup commands without prompting: it adds gsx
as a Go tool, tidies the Go module, and installs the Vite dependencies.

## Start the development server

```sh
npm run dev
```

Open `http://localhost:5173`.

The package script runs the project-local tool:

```sh
go tool gsx dev
```

`gsx dev` watches `.gsx`, `.go`, and `.env` files. On each relevant change it
regenerates Go with a warm compiler, builds and safely swaps the Go server, and
asks Vite to reload the browser. If generation or compilation fails, the browser
shows the error while the last working server keeps running.

## Make the first change

Open `app.gsx`, change some visible text, and save. The browser reloads with the
new output.

The generated `app.x.go` beside it is ordinary Go consumed by `go build`. Keep it
in source control, but edit `app.gsx` rather than the generated file.

Try introducing an invalid expression in `app.gsx`. The browser error overlay
shows the diagnostic. Fix it and save again; the overlay clears and the new
server replaces the last working one.

## Use another package manager

You can replace the npm install and script commands with the equivalents from
pnpm, Yarn, or another compatible package manager. By default, `gsx dev` starts
Vite with `npx vite`. Configure a different front-door command in `gsx.toml`:

```toml
[dev]
web = ["pnpm", "vite"]
```

Then run `go tool gsx dev` directly, or update the `dev` script in `package.json`
to use your preferred setup.

## Build for production

Build the Vite assets, then compile and run the Go server:

```sh
npm run build
go build -o app
./app
```

The production binary embeds the generated `dist/` assets and does not run Vite.

## Where to go next

- Read the [syntax overview](./syntax) and [basic syntax](./syntax/basic-syntax).
- See the [`gsx dev` CLI reference](./cli#gsx-dev) for custom build, run, log,
  and front-door commands.
- Configure filters, asset processing, and the dev loop in
  [`gsx.toml`](./config).
