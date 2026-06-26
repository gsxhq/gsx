# gsx

JSX-like Go templating language + codegen. `.gsx` → generated `.x.go` → `go build` → streamed HTML.
Runtime (root package) is **standard-library only** — keep it dependency-free; tooling (`gen`, CLI, LSP) may use `golang.org/x/tools`.

`gsx` binary conflicts with another system tool — run `go run ./cmd/gsx …`, or `gsx version` to verify.

## Before every commit to main / merge

Run `make ci` — it mirrors `.github/workflows/ci.yml` (build/vet/test both modules, examples drift, `gofmt` + `gsx fmt`).
Pin Go to `GO_VERSION` in `ci.yml` (currently 1.26.1); a different minor re-introduces gofmt drift.
The CI `docs` job (VitePress, clones `gsxhq/gsxhq.github.io`) isn't in `make ci` — only matters when editing `docs/guide/**`.

## Testing — the txtar corpus is canonical

`internal/corpus/testdata/cases/**/*.txtar` is the authoritative syntax reference (parsed → generated → rendered → goldens pinned). Learn syntax from there, not from prose; Also `examples/*.txtar`

- **Every syntax/codegen change ships a corpus case** pinning `input.gsx` + `generated.x.go.golden` + `render.golden`. New syntax valid in multiple contexts (text/attr/style/script/JS/child-prop) needs a case **per context**.
- Regenerate goldens: `go test ./internal/corpus -run TestCorpus -update` (also rewrites `coverage.golden`; a forgotten manifest bump fails the suite). Then verify without `-update`.
- Runtime behavior gets unit tests in the root `gsx` package.
- **Don't hand-edit `.x.go` or golden files** — they're generated; change the `.gsx`/source and regenerate.

## Conventions

- **Branches:** feature work in a **git worktree** (use the `superpowers:using-git-worktrees` skill).
- **Process:** brainstorm → spec → plan → subagent-driven execution with per-task reviews → one **independent adversarial reviewer** (builds throwaway probe programs, not just reads the diff) before merging a subsystem.
- **No "simple heuristics" in core logic** — real implementations only. Security escaping (HTML/URL/JS/CSS) is a faithful port of `html/template`, never an approximation.

### Configuration — where a new knob goes

Three layers, precedence **option > env > config**. To add a config knob:

1. **Can it be data?** → put it in `gsx.toml` (`tomlConfig` in `gen/configfile.go`)
   and the resolved `config` struct. This is the default.
2. **Is it a Go function?** → add a `gen.With*` option in `gen/options.go`
   (functions can't be named in TOML).
3. **Does it vary dev↔prod?** → *also* register a `GSX_<THING>` var in
   `gen/envconfig.go` (`envOverrides`). A knob is never env-only.

Any knob that changes generated output MUST be folded into `computeKey`
(`gen/cachekey.go`), or the incremental cache will serve stale output. Document
user-facing knobs in `docs/guide/config.md`. (Internal knobs like `GSXCACHE` are
not user config.)

Design lives in `docs/superpowers/specs/`. `docs/ROADMAP.md` should be reviewed and updated.

## Neighboring repos (siblings under `~/personal/gsxhq/`)

`gsxhq.github.io` (VitePress docs, local dir `website`) · `tree-sitter-gsx` (grammar) · `vite-plugin-gsx` (`@gsxhq/vite-plugin-gsx`) · `vite` (`github.com/gsxhq/vite`). The dev loop (`gsx init` scaffold + Vite plugin + `vite` Go helper) is shipped/closed. Playground backend (`playground/server/`) deploys to Cloud Run; docs site to GitHub Pages via `deploy-docs.yml`.
