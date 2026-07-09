# gsx

JSX-like Go templating language + codegen. `.gsx` → generated `.x.go` → `go build` → streamed HTML.
Runtime (root package) is **standard-library only** — keep it dependency-free; tooling (`gen`, CLI, LSP) may use `golang.org/x/tools`.

`gsx` binary conflicts with another system tool — run `go run ./cmd/gsx …`, or `gsx version` to verify.

## Before merging to main

Run `make ci` — it mirrors `.github/workflows/ci.yml` (build/vet/test both modules, examples drift, `gofmt` + `gsx fmt`).
It is the authoritative, uncached run (`-count=1`); GitHub CI runs the same. For the **inner dev loop**, use `make check`: the same checks, but it drops `-count=1` 

Pin Go to `GO_VERSION` in `ci.yml` (currently 1.26.1); a different minor re-introduces gofmt drift.
The CI `docs` job (VitePress, clones `gsxhq/gsxhq.github.io`) isn't in `make ci` — only matters when editing `docs/guide/**`.
Literal `{{ }}` in `docs/guide/**` prose must be wrapped in a `::: v-pre` block — VitePress parses `{{ }}` as a Vue interpolation and the build fails otherwise.

Any syntax change should be accompanied by rigorous tests, documentation and sibling project updates:

- ../tree-sitter-gsx
- ../vscode-gsx
- ../gsxhq.github.io/ CodeMirror & VitePress syntax

Run `make lint`

## Testing — the txtar corpus is canonical

`internal/corpus/testdata/cases/**/*.txtar` is the authoritative syntax reference (parsed → generated → rendered → goldens pinned). Learn syntax from there, not from prose; Also `examples/*.txtar`

- **Every syntax/codegen change ships a corpus case** pinning `input.gsx` + `generated.x.go.golden` + `render.golden`. New syntax valid in multiple contexts (text/attr/style/script/JS/child-prop) needs a case **per context**.
- Regenerate goldens: `go test ./internal/corpus -run TestCorpus -update` (also rewrites `coverage.golden`; a forgotten manifest bump fails the suite). Then verify without `-update`.
- Runtime behavior gets unit tests in the root `gsx` package.
- **Don't hand-edit `.x.go` or golden files** — they're generated; change the `.gsx`/source and regenerate.

### The fmt corpus is separate

`internal/gsxfmt/testdata/cases/*.txtar` pins **layout** (`input.gsx` + `fmt.golden`); the semantic corpus above pins meaning. Keep them apart: a codegen golden must not churn when line-breaking changes, and a layout golden must not churn when generated code changes. Regenerate with `go test ./internal/gsxfmt -run TestFmtCorpus -update`, then verify without `-update`.

**Any formatter change ships a fmt-corpus case.** The property tests in `internal/printer` (faithfulness, idempotence, re-parse safety, no-verbatim-fallback) cannot catch a layout regression — a formatter that reflows the author's source is still faithful, still idempotent, and still re-parses. Only a pinned golden catches that.

## Conventions

- **Branches:** feature work in a **git worktree** (use the `superpowers:using-git-worktrees` skill).
- **Process:** brainstorm → spec → plan → subagent-driven execution with per-task reviews → one **independent adversarial reviewer** (builds throwaway probe programs, not just reads the diff) before merging a subsystem.
- **No "simple heuristics" in core logic** — real implementations only. Security escaping (HTML/URL/JS/CSS) is a faithful port of `html/template`, never an approximation.

Three layers, precedence **option > env > config**. Design lives in `docs/superpowers/specs/`. `docs/ROADMAP.md` should be reviewed and updated.

Performance is important: we thrive to keep generation fast, and dev experience smooth.

No workarounds, when we see somethings looks odd, flag it and discuss. Don't just "fix it" with a hack. We want to avoid technical debt.

If you need a paragraph-long comment to justify why the workaround is OIK, the code is wrong - fix the code.

## Neighboring repos (siblings under `~/personal/gsxhq/`)

`gsxhq.github.io` (VitePress docs, local dir `website`) · `tree-sitter-gsx` (grammar) · `vite-plugin-gsx` (`@gsxhq/vite-plugin-gsx`) · `vite` (`github.com/gsxhq/vite`). The dev loop (`gsx init` scaffold + Vite plugin + `vite` Go helper) is shipped/closed. Playground backend (`playground/server/`) deploys to Cloud Run; docs site to GitHub Pages via `deploy-docs.yml`.
