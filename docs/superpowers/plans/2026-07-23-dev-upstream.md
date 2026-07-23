# Dev Upstream Single-Source Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One authority for the dev backend upstream: `gsx.toml [dev].upstream` (env-expanded) drives gsx dev's health probe, the panel's server row, and — via injected `GSX_DEV_UPSTREAM` — the vite plugin's `devFallback` target.

**Architecture:** gsx dev resolves the upstream once per env cycle (startup + `.env`-dirty), derives `healthURL` and `status.server`, and injects `GSX_DEV_UPSTREAM` into the front-door child env (same direction as `GSX_DEV_TOKEN`). The plugin's `devFallback` defaults its `target` to that env var. Defaults reproduce today's behavior exactly (zero migration).

**Tech Stack:** Go stdlib (gen/), TypeScript + vitest (vite-plugin-gsx).

**Spec:** `docs/superpowers/specs/2026-07-23-dev-upstream-single-source-design.md` — authoritative; read fully first.

## Global Constraints

- gsx work in a worktree on branch `dev-upstream` **created after `vite-url-port` merges** (both touch the env-resolution area of `gen/dev.go`; rebase if it lands mid-flight). Plugin work in `/Users/jackieli/personal/gsxhq/vite-plugin-gsx` branch `dev-upstream`. Verify `git branch --show-current` before every commit.
- Expansion: only `${VAR}` is special; bare `$VAR` stays literal; no escape mechanism. Unset var → error naming the variable. Expansion env = the same merged env runDev already builds (`mergeDotEnv(os.Environ(), loadDotEnv(...))` — shell wins).
- Resolved upstream must parse as an absolute http/https URL; stored as origin only (strip any trailing slash and path — a path in the config is an error, not silently dropped). Default when absent: `http://localhost:` + `GO_PORT` (default `7777`).
- `[dev].health` path, default `/healthz`; `healthURL = upstream + health`.
- `status.server` gains `"upstream"` (the origin string); `"port"` stays for one release, derived from the resolved upstream's URL port, empty string when it has none — never from `GO_PORT`.
- Env contract: `GSX_DEV_UPSTREAM=<origin>` injected into the FRONT-DOOR child env only.
- `upstream` is observational — nothing in this plan changes how/where the Go server listens.
- Plugin: `devFallback` `opts.target` default = `process.env.GSX_DEV_UPSTREAM`; explicit option wins; neither → exactly current behavior. Panel renders `server.upstream ?? ":" + server.port`.
- No plugin package.json version bump (release workflow self-bumps).
- Gates per repo as in prior tasks: gsx `go test ./gen -count=1` full (quiet machine — check ports 7799/7811/7813/7821/7823/7825 free and no orphan gsx binaries), `gofmt -l gen/` empty, `go vet ./gen`; plugin `npx vitest run`, `npx tsc --noEmit`, `npm run build`. ALL commands FOREGROUND.

---

### Task 1: ${VAR} expansion (gen)

**Files:** Create logic in `gen/devserver.go` (alongside `mergeDotEnv`/`envPort`); Test: `gen/devserver_test.go`.

**Interfaces:**
- Produces: `func expandEnvRefs(s string, env []string) (string, error)` — replaces every `${NAME}` with the env value (last occurrence wins, matching child-process semantics of the merged slice — but note the merged slice has no duplicates post-mergeDotEnv, so any lookup is fine; use `envLookup` if one exists, else scan); unset/empty-name → `error` containing the literal variable name; bare `$` and `$VAR` untouched; `${` without `}` → error.

- [ ] **Step 1: failing tests** — table: `"http://localhost${ADDR}"` + `ADDR=:8890` → `http://localhost:8890`; multi-var `"${SCHEME}://x:${P}"`; unset `${NOPE}` → error mentioning `NOPE`; bare `$ADDR` literal; `${}` → error; unterminated `${X` → error; no refs → unchanged.
- [ ] **Step 2:** run → FAIL (undefined).
- [ ] **Step 3:** implement with a simple scanner (index of `${`, find `}`, lookup, append) — no regexp needed, no recursion (expanded values are NOT re-expanded).
- [ ] **Step 4:** run → PASS.
- [ ] **Step 5:** commit `feat(gen): ${VAR} env expansion for dev config`.

---

### Task 2: upstream resolution (gen)

**Files:** Modify `gen/devserver.go` (or a natural sibling); read `gen/dev.go` + the `tomlDev` struct (find it — it's the parsed `[dev]` table) first. Test: `gen/devserver_test.go`.

**Interfaces:**
- Consumes: `expandEnvRefs` (Task 1); `tomlDev` gains `Upstream string` + `Health string` toml fields (match the struct's existing tag style).
- Produces: `func resolveUpstream(upstream, health string, env []string) (origin, healthURL, port string, err error)` — empty `upstream` → `http://localhost:`+`envPort(env,"GO_PORT","7777")` (port = that value); non-empty → expand, `url.Parse`, require http/https + host, reject a non-empty path/query/fragment with a clear error, origin = `scheme://host[:port]`, port = `u.Port()` (may be empty); health defaults `/healthz`, must start with `/`; `healthURL = origin + health`.

- [ ] **Step 1: failing tests** — table: absent upstream + no GO_PORT → `http://localhost:7777` + port `7777`; absent + GO_PORT=8081 → 8081; `http://localhost${ADDR}` + `ADDR=:8890` → origin/port 8890; explicit no-port upstream `http://mstudio` → port `""`; path in upstream → error; non-http scheme → error; unset var → error; custom health `/live` → healthURL suffix.
- [ ] **Steps 2-4:** RED → implement → GREEN.
- [ ] **Step 5:** commit `feat(gen): [dev] upstream/health resolution`.

---

### Task 3: wire into runDev + status + env injection (gen)

**Files:** Modify `gen/dev.go` (startup resolution site where `goPort`/`healthURL` are computed today, the `.env`-dirty branch that recomputes them, the front-door spawn env, and the `devStatus` init/updates), `gen/devstatus.go` (`serverStat` gains `Upstream string \`json:"upstream"\``); Tests: `gen/devstatus_test.go`, `gen/dev_test.go`.

**Interfaces:**
- Consumes: `resolveUpstream` (Task 2), existing `status devStatus`, front-door spawn closure (which already appends `GSX_DEV_TOKEN`).
- Produces: wire shape `"server":{"healthy":bool,"port":"8890","upstream":"http://localhost:8890"}` (Task 4's plugin rendering consumes it); `GSX_DEV_UPSTREAM` in the vite child env.

- [ ] **Step 1: failing tests** — (a) unit: `TestStatusEventShape` extended for `upstream`; (b) integration (recorder pattern from `TestDevPanelCommands`, skip `-short`): project with `.env` `ADDR=:<freeport>` + `gsx.toml` `[dev] upstream = "http://localhost${ADDR}"`, scaffold server listening on `ADDR` (adapt `devTestMainGo` — it reads GO_PORT today; give the test its own main.go variant reading ADDR), assert gsx dev's status events report the resolved upstream + healthy (proving the probe followed `upstream`, not GO_PORT), and assert the front-door env: `--web "sh -c 'echo $GSX_DEV_UPSTREAM > marker; sleep 600'"` writes the resolved origin.
- [ ] **Step 2:** RED for the right reasons (no `upstream` field; probe hits 7777 and reports unhealthy; marker empty).
- [ ] **Step 3:** implement: resolution at both sites (startup + `.env`-dirty — on the dirty path a resolution ERROR is logged + overlay-posted like the existing env error handling, not fatal), `healthURL`/`status.Server.{Port,Upstream}` from the resolution, spawn env gains `GSX_DEV_UPSTREAM=`+origin next to the token line.
- [ ] **Step 4:** GREEN; full gen suite; gofmt/vet.
- [ ] **Step 5:** commit `feat(gen): single-source dev upstream — probe, status, GSX_DEV_UPSTREAM injection`.

---

### Task 4: plugin — devFallback default + panel rendering

**Files:** Modify `src/dev-fallback.ts` (read it fully first — note the current target option semantics), `src/client-logic.ts` (`renderStatus` server row); Tests: `test/dev-fallback.test.ts`, `test/client-logic.test.ts`.

**Interfaces:**
- Consumes: `GSX_DEV_UPSTREAM` env contract; status wire shape from Task 3.
- Produces: `devFallback()` with no target works under gsx dev; panel shows the origin.

- [ ] **Step 1: failing tests** — devFallback: `GSX_DEV_UPSTREAM` set + no opts.target → uses env value (assert via whatever the module exposes for its target — read the file; if the target is closure-private, test through observable behavior as the existing dev-fallback tests do); explicit opts.target wins over env; neither → current behavior byte-identical (existing tests unchanged are the assertion). renderStatus: `{server:{upstream:"http://localhost:8890",healthy:true}}` renders the origin; upstream absent → falls back to `:port` (old gsx dev); both absent → degrades sanely.
- [ ] **Steps 2-4:** RED → implement (read `process.env.GSX_DEV_UPSTREAM` at devFallback call time, not module load time — vitest sets env per test) → GREEN; full suite, tsc, build.
- [ ] **Step 5:** commit `feat: devFallback defaults to GSX_DEV_UPSTREAM; panel renders the upstream origin`.

---

### Task 5: scaffold + docs (gsx)

**Files:** Modify `gen/templates/init/simple/vite.config.ts` (devFallback loses its explicit target; proxy target becomes `process.env.GSX_DEV_UPSTREAM ?? "http://localhost:" + (env.GO_PORT || "7777")` — read the template's actual shape first and keep its style), `gen/init_test.go` if it pins the template, `docs/guide/config.md` (`[dev]` section: `upstream`/`health` keys, expansion rule, one example), `docs/guide/dev-loop.md` only if it states the old GO_PORT-only rule. CONCISE — behavior plainly, no rationale.

- [ ] **Step 1:** template change + test adjustment (RED if pinned), verify `TestInit` green.
- [ ] **Step 2:** docs; check no literal `{{ }}` introduced (VitePress).
- [ ] **Step 3:** commit `feat(gen): scaffold reads GSX_DEV_UPSTREAM; [dev] upstream docs`.

---

## Final gate

- [ ] gsx: `make ci` (quiet machine), read the propagated exit code before any merge action — never chain a merge on it.
- [ ] plugin: `npx vitest run && npx tsc --noEmit && npm run build`.
- [ ] Live smoke (coordinator): scaffold app with `ADDR`-style config, panel shows `up http://localhost:<port>`; also plain scaffold (no upstream) behaves exactly as today.
- [ ] Task reviews per task; final adversarial whole-branch review before PRs.
- [ ] Release order: plugin PR → release (self-bumps) → gsx PR (guide cites the released version if a floor is needed for `devFallback` env pickup).
