# Single source of truth for the dev backend upstream

`gsx dev`'s health probe and the dev panel's "server" row key off the `GO_PORT`
env var (default 7777), while the vite side independently learns the backend
address through `devFallback({target})` in the app's `vite.config.js`. Apps
that configure their listen address under any other name silently split the two:
the proxy/fallback works, but the panel reports "server down :7777".

Real instance (one-learning, 2026-07-23): `.env` has `ADDR=:8890`;
`vite.config.js` builds `target` from `env.ADDR`; `gsx dev` probed
`localhost:7777/healthz` and the fresh dev panel showed the server down while
the app served traffic fine.

## Problem

Two processes hold the same fact in different shapes:

- `gen/dev.go` computes `healthURL` from `GO_PORT` (default `7777`) and pushes
  `status.server.port` to the panel.
- `devFallback` in the vite process gets the true upstream via its `target`
  option, computed by hand in every app's `vite.config.js` from whatever env
  var that app happens to use.

There is no channel between them, so they drift. The fix should not be "report
the plugin's target back to gsx dev" — that adds a reverse channel, only works
while the front door is alive, and leaves the app's `vite.config.js` as the
authority for something the dev loop owns. `gsx dev` already owns the env it
spawns both children with (it resolves and injects `VITE_PORT`/`VITE_DEV_URL`
the same way); the upstream should flow the same direction.

## Design

One authority, flowing downhill:

```
gsx.toml [dev].upstream (env-expanded, default http://localhost:${GO_PORT|7777})
        │
        ▼
   gsx dev resolves once per env cycle
        ├── healthURL = upstream + health path   (probe + panel status)
        └── GSX_DEV_UPSTREAM=<upstream> injected into the vite child env
                 │
                 ▼
        devFallback() defaults target to process.env.GSX_DEV_UPSTREAM
        (scaffold vite.config proxy target reads it too)
```

### Config

New `[dev]` keys in `gsx.toml`:

```toml
[dev]
upstream = "http://localhost${ADDR}"   # ${VAR} expanded from merged env
health = "/healthz"                    # path probed on upstream (default)
```

- `${VAR}` expansion resolves against the merged env (`os.Environ()` +
  `.env`, shell wins — same env `resolveViteDevEnv` sees). Concatenation makes
  `:port`-shaped vars work naturally: `ADDR=:8890` →
  `http://localhost:8890`.
- Only `${VAR}` is special: a bare `$VAR` stays literal, and there is no
  escape mechanism in v1.
- Unset var in the expansion is a startup error naming the variable — never a
  silent empty string.
- `upstream` is observational: it retargets probing and reporting only.
  `gsx dev` never sets the server's listen address — the app owns where it
  listens.
- Default when `upstream` is absent: `http://localhost:${GO_PORT}` with
  `GO_PORT` defaulting to `7777` — exactly today's behavior, zero migration.
- The result must parse as an absolute http/https URL; error otherwise.
- Re-resolved on the existing `.env`-dirty path in `gen/dev.go` (where
  `goPort`/`healthURL` are recomputed today), so an `.env` edit retargets the
  probe live.

### Env contract (gsx dev → vite child)

`gsx dev` injects `GSX_DEV_UPSTREAM=<resolved upstream>` into the front door's
env, alongside `VITE_PORT`/`VITE_DEV_URL`/`GSX_DEV_TOKEN`. This is the
cross-repo contract line; the value is the origin only (no trailing slash, no
path).

### Plugin (`vite-plugin-gsx`, separate repo)

- `devFallback(opts?)`: `opts.target` now defaults to
  `process.env.GSX_DEV_UPSTREAM`. Explicit `opts.target` still wins. Neither
  set → keep today's behavior (whatever the current required-option error or
  default is), so standalone `npx vite` without `gsx dev` is unchanged.
- Scaffold template simplifies: `devFallback()` with no target, and the proxy
  target becomes `process.env.GSX_DEV_UPSTREAM ?? "http://localhost:" +
  (env.GO_PORT || "7777")` (fallback keeps standalone vite working).

### Status / panel

`status.server` gains the resolved origin so the panel can say
`up http://localhost:8890` instead of a bare port. Keep `port` populated for
one release so an older plugin's panel still renders — derived from the
resolved upstream's URL port (empty when the upstream has none), never from
`GO_PORT`. The plugin renders `server.upstream ?? ":" + server.port`.

## Why top-down beats plugin-reports-back

- Works when the front door is down, external (`--no-web`), or mid-respawn —
  precisely when the panel's server row matters most.
- No new reverse channel or pairing payload; `GSX_DEV_TOKEN` injection is the
  established precedent for this direction.
- The app's `vite.config.js` stops being the authority for a fact the dev loop
  needs; per-app env-var naming (`ADDR`, `GO_PORT`, `PORT`) becomes a one-line
  `gsx.toml` mapping visible in committed config instead of convention.

## Testing

- `gen`: expansion (multi-var, `:port` concatenation, unset-var error, invalid
  URL error), default fallback to `GO_PORT`/7777, `.env`-dirty re-resolution
  retargets `healthURL`, `GSX_DEV_UPSTREAM` present in the spawned front-door
  env (extend the existing devcmd/devserver e2e).
- plugin: `devFallback()` picks up `GSX_DEV_UPSTREAM`; explicit target wins;
  absence keeps current behavior.
- scaffold: generated project boots with no `GO_PORT` anywhere and a
  `[dev] upstream` line, panel shows the right origin.
