# gsx

JSX-like Go templating language + codegen. `.gsx` → generated `.x.go` → `go build` → streamed HTML.
Two Go modules: root `github.com/gsxhq/gsx` and `playground/server` (`gsxplayground`, `replace` → root).
Runtime (root package) is **standard-library only** — keep it dependency-free; tooling (`gen`, CLI, LSP) may use `golang.org/x/tools`.

In this repo the `gsx` binary on PATH is usually Homebrew **Ghostscript**, not the compiler — run `go run ./cmd/gsx …` here, never bare `gsx`. (Editor integrations must verify `gsx version` prints `gsx v…`.)

## Before every commit to main / merge

Run `make ci` — it mirrors `.github/workflows/ci.yml` (build/vet/test both modules, examples drift, gofmt + `gsx fmt`).
Pin Go to `GO_VERSION` in `ci.yml` (currently 1.26.1); a different minor re-introduces gofmt drift.
The CI `docs` job (VitePress, clones `gsxhq/gsxhq.github.io`) isn't in `make ci` — only matters when editing `docs/guide/**`.

## What we keep getting wrong (from git history)

These are the recurring cleanup commits. Pre-empt them:

- **Format-output changes drift the generated artifacts.** Any change to `internal/printer`/`cssfmt`/`rawfmt`/`gsxfmt` output reformats committed `.gsx` AND the examples gallery. After such a change run `make examples` and commit `docs/examples.json`, `docs/guide/examples.md`, `playground/server/examples.json`; re-run `gsx fmt -w` over committed `.gsx` templates (e.g. `gen/templates/init/simple/`). (`chore(format): restore gofmt/gsx-fmt + regenerate examples…`)
- **Codegen-output changes need a cache-version bump.** Changing emitted Go (`.x.go`) without bumping the version in `internal/codegen/version.go` serves stale cached output (`gen/cache.go` keys on it). Bump it and cite the causing commit. (`fix(codegen): bump cache version 20→21 (gw.S coalescing in 5aed1ba)`)
- **`go.sum` drift in BOTH modules.** `go run`/`go build` can add transitive entries; `go mod tidy` prunes them. Tidy root *and* `playground/server`. (`fix(ci): tidy playground/server go.sum…`)
- **Printer breaks significant whitespace.** Multi-line attribute wrapping / structural breaking must preserve significant spaces and gofmt nesting indentation — guard the edges. The printer asserts **faithfulness + idempotence** (`fmt(fmt(s))==fmt(s)`, `parse(fmt(s))≈parse(s)`); add a corpus case that exercises the break. (`fix(printer): cfBody edge-safety guard…`)
- **`gsx fmt` must not hard-fail on config.** Programmatic/`gsx.toml` formatter options (e.g. CSS) thread best-effort; bad config falls back to verbatim, never errors. (`fix(gen): gsx fmt threads programmatic cssFmt without hard-failing…`)
- **Unused-var/import analysis ignores conditional-attribute conditions.** An identifier/import used *only* inside `<el { if COND { attr } }>` is falsely flagged (`declared and not used`). Build COND from idents already used elsewhere in the component (proper fix: walk COND in the analysis).

## Testing — the txtar corpus is canonical

`internal/corpus/testdata/cases/**/*.txtar` is the authoritative syntax reference (parsed → generated → rendered → goldens pinned). Learn syntax from there, not from prose; `examples/` once rotted because it was only parsed (a non-existent `gsx.JSON` survived).

- **Every syntax/codegen change ships a corpus case** pinning `input.gsx` + `generated.x.go.golden` + `render.golden`. New syntax valid in multiple contexts (text/attr/style/script/JS/child-prop) needs a case **per context**.
- Regenerate goldens: `go test ./internal/corpus -run TestCorpus -update` (also rewrites `coverage.golden`; a forgotten manifest bump fails the suite). Then verify without `-update`.
- Runtime behavior gets unit tests in the root `gsx` package.
- **Don't hand-edit `.x.go` or golden files** — they're generated; change the `.gsx`/source and regenerate.

## Conventions

- **Commits:** Conventional Commits, lowercase, no trailing period: `type(scope): subject`. Types: feat/fix/docs/test/perf/refactor/chore/ci. Scope = subsystem (`lsp`, `codegen`, `gen`, `printer`, `cssfmt`, `rawfmt`, `playground[/server]`, `init`, `parser`, `config`, `std`, `corpus`). Trailer `Claude-Session: <url>`; never `Co-authored-by`.
- **Branches:** feature work in a **git worktree** (use the `superpowers:using-git-worktrees` skill → `.claude/worktrees/<name>`; don't hand-make sibling dirs). Finish a branch with a `Merge <branch>: <summary>` merge commit.
- **Process:** brainstorm → spec → plan → subagent-driven execution with per-task reviews → one **independent adversarial reviewer** (builds throwaway probe programs, not just reads the diff) before merging a subsystem. This consistently catches bugs the tests miss.
- **No "simple heuristics" in core logic** — real implementations only. Security escaping (HTML/URL/JS/CSS) is a faithful port of `html/template`, never an approximation.

## Specs

Design lives in `docs/superpowers/specs/` (the "why") paired with `docs/superpowers/plans/` (the "how"), dated. `docs/ROADMAP.md` indexes shipped/deferred. Read the relevant spec+plan pair before changing a subsystem. In-flight direction: a single warm in-memory module-analysis core (`internal/codegen`) shared by LSP/generate/watch/fmt/playground — spec `2026-06-26-gsx-warm-module-analysis-core-design.md`.

## Neighboring repos (siblings under `~/personal/gsxhq/`)

`gsxhq.github.io` (VitePress docs, local dir `website`) · `tree-sitter-gsx` (grammar) · `vite-plugin-gsx` (`@gsxhq/vite-plugin-gsx`) · `vite` (`github.com/gsxhq/vite`). The dev loop (`gsx init` scaffold + Vite plugin + `vite` Go helper) is shipped/closed. Playground backend (`playground/server/`) deploys to Cloud Run; docs site to GitHub Pages via `deploy-docs.yml`.
