# reload-probe

A standalone, repeatable local test of `gsx dev`'s **browser-reload** behavior.
It is intentionally **not** part of `go test` / `make ci` — it spawns a live dev
loop and drives real file edits.

## Run

```sh
make reload-probe              # reuses a cached scaffolded app (fast)
make reload-probe FRESH=--fresh   # re-scaffold the throwaway app
# or directly:
bash dev/reload-probe/run.sh [--fresh]
```

## What it checks

It runs `gsx dev --no-web` against a scaffolded app, with `recorder.go` standing
in for Vite to capture the `/__gsx/event` and `/__reload` POSTs that `gsx dev`
makes. For both a `.gsx` and a `main.go` error it asserts:

1. introducing the error posts an `ok:false` event (the browser error overlay), and
2. fixing it posts a `/__reload` (the recovery reload).

Assertion 2 for `.gsx` is the regression this guards: a fixed `.gsx` often
regenerates **byte-identical** `.x.go`, so the hash-gated writer skips the write
(`wrote=false`) and — without the `overlayUp` recovery path in `gen/dev.go` — no
reload fired and the overlay never cleared.

## Files

- `run.sh` — driver: builds gsx, scaffolds/caches the app under
  `$TMPDIR/gsx-reload-probe`, runs the dev loop, edits files, asserts, tears down.
- `recorder.go` — tiny HTTP recorder (`//go:build ignore`; excluded from the
  package build).

The scaffolded app is cached under `$TMPDIR/gsx-reload-probe` and reused across
runs; `--fresh` rebuilds it. Ports are chosen free per run, so it won't collide
with other dev servers.
