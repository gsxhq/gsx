# Dev loop

`gsx dev` watches the project, keeps the Go server current, and reloads the
browser. The generated starter runs it with `npm run dev`.

## Run it

```sh
npm run dev
```

Open the URL printed in the terminal and leave the command running while you
edit the project.

## What happens on save

On a normal `.gsx` save, gsx runs this sequence:

1. Generate the affected Go code.
2. Build a new server binary.
3. Replace the running server after the build succeeds.
4. Reload the browser.

Other project files have slightly different behavior:

- A `.go`, `go.mod`, or `go.sum` change refreshes affected generation, then
  rebuilds and reloads.
- A `.env` change restarts the existing backend with fresh environment values,
  then reloads. It does not regenerate or rebuild.

## When a build fails

After the server has built successfully once, generation and build errors from
later save cycles appear in the browser overlay, and the last working server
remains active. Fix the error and save again to build and reload the new version.

### The first build

Before the first successful build, there is no last working server. Keep
`gsx dev` running, fix the startup error, and save again.

## Customize the commands

Use the [`gsx dev` flags](./cli.md#gsx-dev) for one-off changes to the front
door, build, run, or logging commands. Put persistent settings in the
[`[dev]` section of `gsx.toml`](./config.md#dev-development-loop).
