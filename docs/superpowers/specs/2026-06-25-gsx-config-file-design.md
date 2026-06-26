# gsx.toml — Project Configuration File — Design

**Status:** implemented. This is the original design record; the schema keys were
renamed after implementation for clarity — the named-filter table `[aliases]` →
**`[filters]`** and the bulk package list `filters` → **`filterPackages`**. The
authoritative, current reference is the user guide: [Configuration](../../guide/config.md).
Key names below reflect the original design, not the shipped schema.

**Goal:** Let a project configure gsx codegen declaratively in a `gsx.toml` file
read by the **stock `gsx` binary**, so the common cases (filter packages, filter
aliases like `url`/`id`/`target`, attribute-classification rules, built-in
minifiers) need NO per-project `cmd/gen` program. A custom `cmd/` calling
`gen.Main` remains the advanced escape hatch for the few options that are Go
functions (custom minifier, attribute-classifier predicate, field matcher).

**Why:** Today any project that needs a custom filter or alias must ship its own
`cmd/gen/main.go` that calls `gen.Main(gen.WithFilter(...))` and build that binary
to generate. The structpages rewrite proved how restricting this is: every one
of six examples grew an identical `cmd/gen`. Worse, the LSP and the Vite plugin
shell out to the **stock** `gsx` binary, which knows none of a project's filters
— so a config-via-code project must also repoint its editor and build tooling at
the custom binary ([[gsx-init-dev-loop-scaffold]] consequence). A config file the
stock binary reads dissolves all of this: one `gsx.toml`, and `gsx generate`,
`gsx lsp`, and the Vite plugin all agree with zero per-project Go code.

---

## 1. What is declarative vs what stays code

The `gen.With*` options split cleanly:

| Option | Value kind | Config-able? |
|---|---|---|
| `WithFilters(pkgs…)` | package import paths | ✅ `filters = [...]` |
| `WithFilter(name, fn)` | name → `pkg.Func` | ✅ `[aliases] name = "pkgpath.Func"` |
| `WithJSAttrs` / `WithURLAttrs` / `WithCSSAttrs` | `Rule{Name|Prefix}` data | ✅ array-of-tables |
| built-in CSS/JS minify | enable flag | ✅ `[css] minify = true` / `[js] minify = true` |
| `WithCSSMinifier(fn)` / `WithJSMinifier(fn)` | `func(string)(string,error)` | ❌ code-only |
| `WithAttrClassifier(label, fn)` | predicate func | ❌ code-only |
| `WithFieldMatcher(fn)` | matcher func | ❌ code-only |

**Key enabler:** `WithFilter`'s reflection only extracts `(PkgPath, FuncName)`
strings from the func value, then resolves the *signature* via go/types at
harvest time (producing a `codegen.FilterAlias{Name, PkgPath, FuncName}`). A
config string `url = "github.com/.../structpages.URLFor"` supplies the identical
`(PkgPath, FuncName)` — so config aliases reuse the **exact same**
`FilterAlias` → go/types harvest → validation path (including the curried-shape
migration diagnostic). No reflection, no compiled binary, no new validation
logic.

## 2. File format & location

- **Format:** TOML. **Filename:** `gsx.toml`. **Parser:** `github.com/BurntSushi/toml`
  (canonical Go TOML lib, minimal transitive deps). gsx is otherwise
  dependency-light; this is the one new direct dep, accepted as the cost of a
  hand-editable config (a hand-rolled TOML subset is explicitly rejected — it
  would be the "fake-simple, not a real implementation" anti-pattern).
- **Discovery — first-found walking up, bounded by the git repo root:** gsx
  walks **up** from the target directory (after any `-C` chdir) and uses the
  **first `gsx.toml` it finds**. The walk is bounded by the **git repo root**
  (the nearest ancestor containing `.git`); if none is in the repo, no config
  (today's std-only behavior). There is **no merging across files** — the
  nearest `gsx.toml` wins wholesale — and a per-module `gsx.toml` therefore
  overrides an ancestor one.
- **Typically at the repo root.** Because the walk crosses module boundaries
  (it stops at the git repo, not the module's `go.mod`), one `gsx.toml` at the
  repo root serves **every module in the repo** — e.g. a monorepo of example
  modules (`examples/todo`, `examples/blog`, …) shares one config, while any
  single example can drop its own `gsx.toml` to override. Resolution is still
  per-module: a config alias like `structpages.URLFor` is harvested against the
  module being generated, which must `require` that package (each example does).
  A repo-root `gsx.toml` also gives editor tooling a stable project-root anchor.
- **No global / `$HOME` config.** Every config key is a Go import path
  (project-specific by nature), so a user-global config would be wrong for
  unrelated projects; the walk never escapes the git repo. (If gsx ever gains a
  genuinely user-level setting — none exist — revisit.)
- **Bounding when not in a git repo:** fall back to the module root (`go.mod`)
  as the stop, so a non-repo checkout still resolves a config beside its
  `go.mod` without walking to the filesystem root.

```toml
# gsx.toml — at the module root, beside go.mod

# Filter packages whose exported funcs are harvested by contract (WithFilters).
# std is always available; list it only to be explicit, or to set precedence.
filters = ["github.com/gsxhq/gsx/std"]

# Explicit filter aliases (WithFilter): template name → "<pkgPath>.<Func>".
[aliases]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"

# Attribute-classification rules (WithJSAttrs / WithURLAttrs / WithCSSAttrs).
# Each rule has exactly ONE of name (exact, case-insensitive) or prefix.
[[urlAttrs]]
name = "data-href"
[[jsAttrs]]
prefix = "data-on-"
```

## 3. Schema (v1)

| Key | Type | Maps to |
|---|---|---|
| `filters` | array of strings (import paths) | `WithFilters` |
| `[aliases]` | table `name → "pkgPath.Func"` | one `FilterAlias` each (`WithFilter`) |
| `[[jsAttrs]]` / `[[urlAttrs]]` / `[[cssAttrs]]` | array-of-tables, each `{name?, prefix?}` | `WithJSAttrs`/`WithURLAttrs`/`WithCSSAttrs` |

**Minify flags deferred (not v1):** there is currently no string-based bundled
minifier to enable — `WithCSSMinifier`/`WithJSMinifier` take a custom
`func(string)(string,error)`, and the internal `cssmin`/`jsmin` operate on an
AST, not a string transform. A `[css].minify`/`[js].minify` flag therefore has
no existing target; exposing a bundled string minifier is a separate feature.
Until it exists, minify stays a code-only option (`cmd/` + a custom func). See
§10.

**Alias string parsing:** split `"github.com/jackielii/structpages.URLFor"` at the
**last `.` that follows the last `/`** → `pkgPath="github.com/jackielii/structpages"`,
`funcName="URLFor"`. (Splitting at the final `.` naively would break on
dotted path segments like `gopkg.in/x`.) `funcName` must be a valid exported Go
identifier, else a positioned config error.

**Rule validation:** exactly one of `name`/`prefix` per rule (mirrors the existing
`attrclass.Rule` validation in `WithJSAttrs` etc.); both-or-neither is a config
error.

**Strictness:** decode with `toml.MetaData.Undecoded()` checked — an unknown key
(typo like `filteres` or `alias`) is a config error, not silently ignored.

## 4. Loading & precedence

- **Stock binary path:** `runConfig` currently receives a zero `config{}` from
  `run`. Insert config loading: resolve the module root from the target dir,
  load `gsx.toml` if present into a `config`, and pass that to `runConfig`.
  Config is loaded for the subcommands that consume it — **`generate` and
  `info`** — each via the shared `resolveConfig` (discover → load → merge).
  Config-agnostic commands (`version`, `help`, `clean`, `fmt`, `lsp`) do **not**
  load `gsx.toml`, so a malformed config can't break them. **`gsx lsp` config
  integration is deferred to the separate LSP work** (so it is NOT a divergence
  here).
- **`gen.Main(opts…)` (custom cmd/) — MERGE:** `Main` loads `gsx.toml` as the
  base config first, then applies the programmatic `opts` on top. Filters and
  aliases from opts **append** to the config's (the existing last-wins table
  resolves name collisions; an opt alias can intentionally override a config
  alias of the same name). Func-valued opts (minifier/classifier/field-matcher)
  simply set fields the config can't. So a cmd/ project writes simple things in
  `gsx.toml` and Go only for the func it actually needs.
- **Resolution order for the filter table** (unchanged mechanism): std →
  `filters` packages (config, in order) → `aliases` (config) → opt filters →
  opt aliases, last-wins on name collisions. (Document the exact order in the
  plan; it must be deterministic for the cache key.)

## 5. Caching

The config's resolved values populate the same `config`/`filterPkgs`/`aliases`
fields that already fold into the build-cache key (`computeKey`). So a `gsx.toml`
change that alters filters/aliases/rules invalidates the cache through the
existing mechanism — no separate config hash is needed, provided the RESOLVED
set (post-merge) is what feeds the key. The plan must verify a config edit busts
the cache (a test).

## 6. Tooling impact (the payoff)

- **`gsx lsp` (deferred):** wiring the language server to load the same
  `gsx.toml` — so it resolves a project's `url`/`id`/`target` filters for
  diagnostics + go-to-definition with no custom binary — is a **separate LSP
  effort**, not part of this slice. Until then the prior "point the editor at the
  project's binary" caveat ([[pipeline-forward-application]] §4) still applies to
  the language server.
- **Vite plugin** shells `go tool gsx generate` (stock) which now reads the
  config — zero plugin change, no per-project generator.
- **`gsx info` is the single source of truth.** It prints the **discovered
  `gsx.toml` path** (or "none") and the **fully-resolved** configuration after
  merging config + any opts: filters, aliases, and attr rules. This is the
  authoritative answer to "which config is in effect, from where" and the
  debugging surface for "why isn't my `url` filter found." (Extends the existing
  `info` output.)

## 7. Migration of the structpages examples (validation)

All six example modules' `cmd/gen/main.go` collapse to **one** `gsx.toml` at the
structpages repo root (each example finds it by walking up past its own `go.mod`
to the repo root):

```toml
# structpages/gsx.toml — shared by every example module
[aliases]
url    = "github.com/jackielii/structpages.URLFor"
id     = "github.com/jackielii/structpages.ID"
target = "github.com/jackielii/structpages.IDTarget"
```

Then generation is plain `go tool gsx generate ...` (or `gsx generate`) with no
`cmd/`. This is the end-to-end proof the feature works; it lands as a follow-up
on the examples PR, not in the gsx change itself.

## 8. Errors & edges

- **Missing file:** not an error — std-only behavior (back-compat).
- **Malformed TOML / unknown key / bad alias string / both name+prefix:** a clear
  error naming the `gsx.toml` path (and key where possible), failing generation
  before any work — mirrors how a bad `WithFilters` marker fails today.
- **Alias func not found / wrong shape:** surfaces from the existing go/types
  harvest (`FilterAlias` path) — same diagnostics as the code path.
- **`-C <dir>`:** discovery walks up from the post-chdir dir, so `-C` selects
  which module's config applies.
- **No go.mod (not in a module):** no module root → no config lookup (gsx codegen
  already requires a module for type resolution, so this is the existing failure
  mode, not a new one).
- **Multiple modules under one tree:** each module root has its own `gsx.toml`;
  the one for the target dir's module wins. No cross-module inheritance in v1.

## 9. Testing (per [[gsx-syntax-change-test-coverage]])

- **config loading unit tests:** parse each schema key; alias-string split
  (incl. dotted path segments like `gopkg.in/x.F`); unknown-key rejection;
  both-name+prefix rejection; missing file → empty config.
- **discovery unit tests:** first-found wins walking up from a nested target dir
  across a module boundary (a repo-root `gsx.toml` found from a sub-module dir);
  a nearer `gsx.toml` overrides an ancestor one; the walk stops at the git repo
  root (a `gsx.toml` outside the repo is NOT used); absent → std-only; `-C`
  redirects discovery; non-repo checkout falls back to the `go.mod` stop.
- **end-to-end (go-run) test:** a temp module with a `gsx.toml` aliasing a
  ctx-injecting filter, generated via the STOCK run path (no opts), asserting the
  lowered call + render — proving the stock binary honors config.
- **merge test:** `gen.Main` with both a `gsx.toml` and a programmatic opt;
  assert both take effect and an opt overrides a same-named config alias.
- **cache test:** editing `gsx.toml` (changing an alias) regenerates (cache
  busted); an unrelated edit does not.
- **info test:** `gsx info` prints the resolved config.

## 10. Out of scope (v1)

- **Minify flags** (`[css].minify`/`[js].minify`): no string-based bundled
  minifier exists to enable (see §3). Requires first exposing a built-in
  `func(string)(string,error)` minifier — a separate feature. Until then minify
  is code-only.
- Func-valued options in config (custom minifier/classifier/field-matcher) —
  stay `cmd/`-only.
- Config inheritance / monorepo configs above the module root.
- A `gsx init` that writes a starter `gsx.toml` (natural follow-up via
  [[gsx-init-dev-loop-scaffold]]).
- Per-package config overrides within one module.
