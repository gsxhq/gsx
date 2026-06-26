# gsx config model: layers, env overrides, declarative minify

**Date:** 2026-06-26
**Status:** Approved (design); plan + implementation to follow
**Scope:** gsx code (`gen` package) + user docs (`docs/guide/config.md`) + contributor practice (`CLAUDE.md`)

## Problem

gsx already has two configuration layers — a declarative `gsx.toml` and
func-valued `gen.With*` options — and `mergeConfig` already layers options on
top of the file. But three things are missing or implicit:

1. **No env-var layer.** Env vars exist only ad hoc and internal (`GSXCACHE` in
   `gen/cachestore.go`, `GSX_PERF` in tests). There is no user-facing way to make
   dev and prod differ without editing the file or writing Go.
2. **Minification is always-on.** The only control is replacing the minifier
   function wholesale (code-only). There is no declarative way to turn it off for
   a fast dev loop or to choose a level.
3. **The layering is an unstated practice.** `docs/guide/config.md` documents the
   pieces but never names the model or gives a rule for *where a new knob goes*.
   "Prefer config; fall back to options" is the intended guideline but is nowhere
   written down — for users *or* for contributors.

This design names the config model, adds a curated environment-override layer,
makes minification declarative (the concrete driver), and documents the model
for both audiences.

## Goals

- A **named three-layer config model** with an explicit rule for which layer a
  new knob belongs in, written for both contributors ("us") and users.
- A **curated, opt-in environment-override layer** (`GSX_*`) so dev/prod differ
  with no file edit and no recompile.
- **Declarative minification** with a per-asset enum level, env-overridable,
  preserving today's always-on behavior as the default.
- Correct **incremental-cache invalidation** when the resolved minify level
  changes (via file or env).

## Non-goals

- A generic key-path env scheme (`GSX_CONFIG__a__b=…`). Rejected: verbose and
  silently typo-prone, and over-delivers vs. the curated need.
- Migrating the existing internal knobs (`GSXCACHE`, `GSX_PERF`) into the
  user-facing registry. They stay internal; we only document the naming
  convention so they are not mistaken for user config.
- Granular per-asset env vars (`GSX_MINIFY_CSS`/`GSX_MINIFY_JS`) in this slice.
  The coarse `GSX_MINIFY=off|on` switch covers the dev/prod case; granular vars
  can be added later under the same registry.
- A global / `$HOME` config. Unchanged: discovery stays bounded by the repo root.

## The config model (the named practice)

Three layers. Each has a single rule for what belongs in it.

| Layer | Mechanism | Holds | A knob belongs here when… |
|---|---|---|---|
| 1. Declarative | `gsx.toml` | data-expressible knobs | **default** — anything expressible as data |
| 2. Programmatic | `gen.With*` options | func-valued knobs | a Go *function* cannot be named in TOML |
| 3. Env override | `GSX_*` env vars | a curated subset of Layer-1 knobs | the knob legitimately varies dev↔prod |

**Decision rule (for contributors — "where does my new knob go?"):**

1. Can it be expressed as data? → it lives in `gsx.toml` (Layer 1). This is the
   default; start here.
2. Is its value a Go *function* (a minifier, a predicate, a matcher)? → it is a
   `gen.With*` option (Layer 2). Functions cannot be named in TOML.
3. Does it legitimately differ between dev and prod, and benefit from being
   changed without editing a file or recompiling? → *additionally* register it in
   the env layer (Layer 3). A knob is **never** env-only: Layer 3 always overrides
   a value that also has a Layer-1 home.

**Per-knob precedence:** `option > env > config-file`.

- Code (`gen.With*`) is the most deliberate statement and always wins.
- Env overrides the file's default (the dev/prod switch).
- The file is the base.

A knob that is unset at a higher layer falls through to the next. Layers compose
per-knob, not all-or-nothing: setting `GSX_MINIFY` does not disturb filters read
from the file.

## Architecture

Config code keeps its current three-file shape in `gen`:

- `configfile.go` — declarative layer: `tomlConfig` schema, `loadConfig`,
  `mergeConfig`. Gains the `[minify]` schema and the env-application pass.
- `options.go` — programmatic layer: `With*` option funcs. Gains
  `WithMinifyLevel`.
- `main.go` — the resolved internal `config` struct. Gains the minify level
  fields.

### Resolution order

The single resolution path becomes:

```
base   = loadConfig(file)        // Layer 1: file defaults
base   = applyEnvOverrides(base) // Layer 3: env overrides file
merged = mergeConfig(base, opts) // Layer 2: options win (existing last-wins)
```

Because `mergeConfig` already lets `opts` win over `base`, applying env to `base`
*before* the merge yields `option > env > config` with no change to merge
semantics. This order is documented in a comment at the resolution site so the
precedence is discoverable from the code.

### Env registry

A single table in `gen` (e.g. `envOverrides`), each entry holding:

- the env var name (`GSX_*`),
- a one-line human description (surfaced by `gsx info`),
- a typed parser/applier `func(raw string, cfg *config) error` that validates the
  value and writes the corresponding field.

`applyEnvOverrides(cfg *config) error` iterates the registry; for each var that is
**present** (`os.LookupEnv`), it parses and applies. An invalid value is a hard
error naming the var and the accepted values — consistent with strict TOML
decoding rejecting unknown keys. A var that is absent leaves the file value
untouched.

This keeps the *mechanism* general (one table, one pass, uniform error handling)
while coverage stays *selective* (only registered knobs get a var).

### `gsx info`

`gsx info` (and `--json`) gains an **Environment** section listing every
registered var, its accepted values / description, its current raw value (or
"unset"), and whether it is actively overriding the resolved config. This makes
the env layer as inspectable as the rest of the config — answering "is my
`GSX_MINIFY` actually taking effect?".

## Declarative minification (the driver)

### Schema

New `[minify]` table in `tomlConfig`, enum level per asset, default `"safe"`
(preserves today's always-on behavior — an absent `[minify]` table is identical
to current output):

```toml
[minify]
css = "safe"   # "safe" | "none"
js  = "none"
```

The level is a small typed enum in `gen` (e.g. `minifyLevel` with `safe` and
`none`), not a bare string, so validation is centralized and adding levels later
is a closed change. Strict decoding plus enum validation makes an unknown level
(`css = "agressive"`) a hard error naming the key and the accepted values.

### Internal wiring

- `config` gains `cssMinLevel` and `jsMinLevel` (the enum). The zero value maps
  to `"safe"` so an absent `[minify]` table and the explicit default agree.
- `internal/codegen/emit.go` gates the existing minify passes on the level:
  `none` → skip the pass entirely (verbatim/pass-through output); `safe` → run
  the built-in minifier, or the custom `WithCSSMinifier`/`WithJSMinifier` func if
  one is installed. **The level is the gate; the custom func is the
  implementation** — `level = none` means the custom func is not called at all.
- The resolved css/js level is threaded into `codegen` alongside the existing
  `cssMin`/`jsMin` parameters (or a small carrier) so `emit.go` can consult it.

### Env override

- `GSX_MINIFY=off` sets **both** css and js levels to `none`; `GSX_MINIFY=on`
  sets both to `safe`. This is the coarse dev/prod switch (`off` for a fast dev
  loop, `on`/default for prod). Accepted values: `on`, `off` (case-insensitive);
  anything else is a hard error.
- Per the precedence rule, `WithMinifyLevel` (code) beats `GSX_MINIFY` (env)
  beats `[minify]` (file).

### `WithMinifyLevel` option

`gen.WithMinifyLevel(css, js minifyLevel)` (exact signature TBD in the plan —
likely accepting the exported level constants) lets a `cmd/gsx` binary pin the
level so code can force-enable minification in a prod build regardless of the
environment. Included so the `option > env` half of the precedence rule applies
to this knob, keeping the model uniform.

### Cache invalidation (hard requirement)

`computeKey` (`gen/cachekey.go`) currently folds in filters, aliases, the
classifier fingerprint, and field-matcher presence — but **not** any minify
state. Today that is safe because a custom minifier lives in the project's
`cmd/gsx` binary, which is already pinned via `codegenID` (the binary hash). But
a **declarative** minify level read from `gsx.toml`, or overridden by
`GSX_MINIFY`, is *not* a source file in the package dir and is *not* in the key —
so without action, switching the level would serve stale cached output.

**Requirement:** the resolved `cssMinLevel`/`jsMinLevel` must be folded into
`computeKey` (e.g. `minify=css:none,js:safe`). Because env resolution happens
before key computation, an env-driven level change changes the key and correctly
invalidates the cache. A regression test pins this for both the file path and the
env path.

## Audit findings (current state)

- **Config code is already well-separated** across `configfile.go` (declarative),
  `options.go` (programmatic), and `main.go` (resolved struct); this design adds
  to that structure rather than reshaping it.
- **`mergeConfig` already implements `option > config`** with last-wins
  semantics; the env layer slots cleanly in between.
- **Latent gap (now fixed by this design):** minify state is absent from the
  cache key. Harmless today (custom funcs ride the binary hash) but a correctness
  bug the moment minify becomes declarative.
- **Env-var naming is inconsistent:** existing internal vars are `GSXCACHE` (no
  underscore) and `GSX_PERF`. We keep `GSXCACHE` as-is for back-compat but fix
  the convention going forward: **user-facing env vars are `GSX_<THING>`**, and
  internal/test knobs are explicitly documented as not user config.

## Documentation

### For users — `docs/guide/config.md`

- A short **"Three layers"** intro: declarative `gsx.toml` (preferred),
  programmatic `gen.With*` (func-valued fallback), and `GSX_*` environment
  overrides (dev/prod) — with the precedence line `option > env > config`.
- `[minify]` added to the **Options** reference (levels, default, per-asset).
- A new **Environment overrides** section: the curated var list (`GSX_MINIFY`
  to start), precedence, and a dev/prod example (`GSX_MINIFY=off go run ...` for
  the dev loop; default/`on` for prod).
- A pointer to `gsx info` as the way to confirm what is active, including env.

### For us — contributor practice

- The **decision rule** ("where does a new knob go?", the three numbered steps)
  recorded in this spec and summarized as a short note in `CLAUDE.md` under a
  Configuration heading, so future knobs follow the same model.

## Testing

Per `CLAUDE.md`, every syntax/codegen change ships a corpus case, one per
context.

- **Corpus** (`internal/corpus/testdata/cases/**`): `[minify] css = "none"` and
  `[minify] js = "none"` cases, each pinning a `render.golden` that differs from
  the minified baseline, proving the level actually gates the pass. Goldens
  regenerated with `-update`; `coverage.golden` kept in sync.
- **Unit (`gen`):**
  - env resolution applies a present var and ignores an absent one;
  - precedence is `option > env > config` for the minify level (table-driven
    across the three layers set in combination);
  - invalid values are rejected with a clear error (`GSX_MINIFY=banana`,
    `[minify] css = "agressive"`);
  - `gsx info` reports each registered var, its value, and active/override state.
- **Cache (`gen`):** changing the resolved minify level — once via `gsx.toml`,
  once via `GSX_MINIFY` — changes `computeKey`; an unchanged level does not.

## Open questions

- Exact `WithMinifyLevel` signature (level constants vs. strings) — settle in the
  plan.
- Whether `gsx info --json` nests env under a new top-level key or extends the
  existing config object — settle in the plan against the current JSON shape.
