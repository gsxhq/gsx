# `gsx dev` Go-port selection

## Problem

`gsx dev` auto-picks a free Vite port but never picks a Go port, so two
projects cannot run at once: the second one's backend fails to bind.

- `resolveViteDevEnv` (`gen/devserver.go`) resolves the Vite port as
  `VITE_PORT` > port in `VITE_DEV_URL` > auto-pick upward from `5173`.
- `resolveUpstream` (`gen/devserver.go`) reads `GO_PORT` (default `7777`) only
  to compute the health-probe origin. It never checks availability, and the
  value is never injected back into the spawned server's environment.
- `gsx init` writes `.env` with `GO_PORT=7777`
  (`gen/templates/init/simple/dot-env`), so every scaffolded project pins the
  same backend port while leaving the Vite port unpinned.

A second, latent bug of the same family: on an `.env` change
(`gen/dev.go:459`) the loop re-runs `resolveViteDevEnv` against a freshly
merged `os.Environ() + .env`, which no longer carries the previously
auto-picked `VITE_PORT`. Our own Vite child still holds `5173`, so
`portAvailable` reports it busy and the resolver picks `5174` — `viteURL` then
addresses a port nothing listens on. Any Go-side auto-pick inherits the
identical trap.

## Rule

One rule for both ports:

- **Unset** — auto-pick upward from the default (`7777` for Go, `5173` for
  Vite).
- **Set** — strict. A busy port is a startup error naming the variable, never
  a silent relocation.

`[dev].upstream` is unchanged and takes precedence: when it is set, the user
places the backend, and `gsx dev` neither picks nor injects a Go port.

## Design

### `resolveGoPort` — single owner of `GO_PORT`

New function in `gen/devserver.go`:

```go
func resolveGoPort(env []string, upstreamSet bool, held string) (newEnv []string, port string, err error)
```

| Condition | Result |
|---|---|
| `upstreamSet` | `(env, "", nil)` — no pick, no injection |
| `GO_PORT` set, non-numeric | error `invalid GO_PORT %q` |
| `GO_PORT` set, empty | error: set but empty — unset it or give it a port number |
| `GO_PORT` set, busy | error `GO_PORT %s is already in use — unset it (or comment it out in .env) to let gsx dev pick a free port` |
| `GO_PORT` set, free or `held` | `(env, port, nil)` |
| `GO_PORT` absent | `held` if still free, else `nextAvailablePort("7777")`; injected via `setEnvValue(env, "GO_PORT", port)` |

Injection is what makes the pick effective: the scaffold's `main.go` binds
`cmp.Or(os.Getenv("GO_PORT"), "7777")`, and `gen/dev.go` passes this `env` to
the spawned server (`srv.env`).

`resolveUpstream` stops reading `GO_PORT` and takes the resolved port as a
parameter:

```go
func resolveUpstream(upstream, health string, env []string, goPort string) (origin, healthURL, port string, err error)
```

Its `env` parameter remains for `${VAR}` expansion. Its set-but-empty
`GO_PORT` guard moves to `resolveGoPort`; there is exactly one reader of the
variable's semantics.

### Held ports — stickiness and self-conflict

`resolveViteDevEnv` gains the same `held string` parameter. In both resolvers:

- A requested port equal to `held` counts as **free**. Our own child's
  listener is never a conflict — otherwise adding `GO_PORT=7777` to `.env`
  while the loop already picked `7777` would hard-error against ourselves.
- The auto-pick path **prefers `held`** when it is still free, so a
  re-resolution returns the same port instead of drifting upward. This is the
  fix for the `5173`→`5174` bug above.

A small helper expresses both: `portFree(port, held string) bool` returns true
when `port == held` (and `held != ""`), else `portAvailable(port)`.

`nextAvailablePort` is shared by both resolvers but its messages name Vite
(`invalid VITE_PORT %q`, `choose Vite dev port: …`). It gains a caller-supplied
label so the Go-side failure reads `choose Go server port: no free port at or
above 7777`; the invalid-start message becomes generic, since a non-numeric
`VITE_PORT`/`GO_PORT` is already rejected by each resolver before this call.

### Wiring in `gen/dev.go`

Startup (around `gen/dev.go:64`), in order:

1. `resolveViteDevEnv(env, dc.host, "")` → `env`, `viteURL`.
2. `resolveGoPort(env, tdUpstream != "", "")` → `env` (with `GO_PORT`
   injected), `goPort`.
3. `resolveUpstream(tdUpstream, tdHealth, env, goPort)` → `origin`,
   `healthURL`, status port.

The `.env`-fire path (`gen/dev.go:459`) performs the same three steps, passing
the currently-bound ports as `held`, and updates the tracked ports only after
all three succeed. The existing discipline — a resolution failure logs,
overlays, and leaves `env`/`viteURL`/`healthURL`/`status` exactly as they were
— is unchanged.

The front door already receives the resolved origin as `GSX_DEV_UPSTREAM`,
derived from `resolveUpstream`, so the Vite proxy follows an auto-picked Go
port with no further plumbing.

### Scaffold and docs

- `gen/templates/init/simple/dot-env` and `dot-env.example`: `GO_PORT=7777`
  becomes a commented knob, so a new project floats by default and
  uncommenting opts into strict behavior.
- `docs/guide/config.md`: state the shared rule for both ports — unset floats
  from the default, set is strict — in the `[dev]` and `upstream` sections.
- Sibling repo `gsx-examples/streaming-partial/.env`: comment out its
  `GO_PORT` so the examples run side by side.

## Testing

Unit (`gen/devserver_test.go`):

- `resolveGoPort` table: absent → `7777` when free, injected into env; absent
  with `7777` busy → `7778`; set and busy → error naming `GO_PORT`; set and
  busy but equal to `held` → accepted; set but empty → error; non-numeric →
  error; `upstreamSet` → no port, env untouched.
- `resolveViteDevEnv`: an explicit port equal to `held` is accepted; the
  auto-pick path returns `held` rather than the next port above it.
- `resolveUpstream` tests adapt to the new `goPort` parameter; the
  `GO_PORT`-derived rows move to `resolveGoPort`.

Integration (`gen/dev_test.go`, recorder pattern, skipped under `-short`):

- Two dev loops over two scratch projects with port-less `.env` files both
  reach healthy on distinct Go ports — the reported bug.
- An `.env` edit during a run leaves both the Go and the Vite port unchanged
  (pins the stickiness fix).

Existing tests that pin the Vite "already in use" hard error
(`gen/dev_test.go:1712`) and the `.env`-fire error branches stay green
unmodified.
