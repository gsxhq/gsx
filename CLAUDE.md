# gsx

JSX-like Go templating language + codegen. `.gsx` → generated `.x.go` → `go build` → streamed HTML.
Runtime (root package) is **standard-library only** — keep it dependency-free; tooling (`gen`, CLI, LSP) may use `golang.org/x/tools`.

`gsx` binary conflicts with another system tool — run `go run ./cmd/gsx …`, or `gsx version` to verify.

## Before merging to main

Run `make ci` — it mirrors `.github/workflows/ci.yml` (build/vet/test both modules, examples drift, `gofmt` + `gsx fmt`).
It is the authoritative, uncached run (`-count=1`); GitHub CI runs the same. `make check` is the same checks plus `lint` with the test cache left on — use it as a **pre-merge dry run**, not as an inner loop. For the inner loop run the single affected test; see **Test performance** below.

Pin Go to `GO_VERSION` in `ci.yml` (currently 1.27.1): your PATH-local `go` must be that exact version — a different minor re-introduces gofmt drift, and the `internal/typebundle` parity test fails closed until its reader caps and the regenerated `playground.typebundle` match. Never add a `toolchain` directive to `go.mod`: codegen refuses toolchain switching (`go_overlay.go`), so it would break every module load for anyone whose PATH `go` differs. The `go 1.26.1` directive is the *dependents'* minimum and moves separately.
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

`internal/gsxfmt/testdata/cases/*.txtar` pins **layout** (`input.gsx` + `fmt.golden`); the semantic corpus above pins meaning. Keep them apart. Regenerate with `go test ./internal/gsxfmt -run TestFmtCorpus -update`, then verify without `-update`. **Any formatter change ships a fmt-corpus case.**

## Test performance — run the narrow test, not the suite

The full suite is **~230s**; a single test is **~1.5s**. Never run `make ci` / `make check` / `go test ./...` as your inner loop.

**While building a feature, run only what you changed:**

```
go test ./internal/codegen -run 'TestMyNewThing' -count=1     # one test
go test ./internal/corpus  -run 'TestCorpus/my_case' -count=1 # one corpus case
go test ./gen -run 'TestDev' -count=1                          # one cluster
```

Run the full `make ci` **once, at final review before merging** — that is the authoritative gate and its exit code must reach the merge decision.

**Why it's slow, and the rule that follows.** `gen` (~264s) and `internal/codegen` (~260s) run concurrently and *are* the suite; every other package finishes in under a second. Both are dominated by `packages.Load`: the gsx runtime closure is **85 packages / 667 files / ~205k lines**, and it is re-parsed and re-type-checked **once per test Module** — 803 loads per run, ~0.25–0.31s each. In the CPU profile that is `parseFiles` 96s + `checkFiles` 61s + 106s of resulting GC.

So the unit of test cost is **a `codegen.Module` open, not a test function.** Adding 50 table rows to an existing Module-backed test is ~free; adding one more test that opens its own Module costs ~0.3s forever. Prefer extending an existing fixture's table over writing a new module-opening test.

Do **not** try to speed the suite up by merging fast tests: in `gen`, 344 of 552 tests take <1s and total 46s combined. Merging only pays when it removes a `packages.Load`.

Test parallelism is capped at `-parallel 4` in the Makefile (`PARALLEL`). This is deliberate — the default `GOMAXPROCS` fan-out is *slower* (292s vs 232s) because each `go list` internally saturates every core. Don't raise it without measuring.

## Conventions

- **Branches:** feature work in a **git worktree** (use the `superpowers:using-git-worktrees` skill).
- **Process:** brainstorm → spec → plan → subagent-driven execution with per-task reviews → one **independent adversarial reviewer** (builds throwaway probe programs, not just reads the diff) before merging a subsystem.
- **No "simple heuristics" in core logic** — real implementations only. Security escaping (HTML/URL/JS/CSS) is a faithful port of `html/template`, never an approximation.

Three layers, precedence **option > env > config**. Design lives in `docs/superpowers/specs/`. `docs/ROADMAP.md` should be reviewed and updated.

Performance is important: we thrive to keep generation fast, and dev experience smooth.

No workarounds, when we see somethings looks odd, flag it and discuss. Don't just "fix it" with a hack. We want to avoid technical debt.

If you need a paragraph-long comment to justify why the workaround is OIK, the code is wrong - fix the code.

`golang.org/x/tools/go/packages.Load` is expensive — ~0.25–0.31s, and it governs test wall time (see **Test performance**). Make sure you understand the performance implications before calling it, in production code *or* in a test.

## Neighboring repos (siblings under `~/personal/gsxhq/`)

`gsxhq.github.io` (VitePress docs, local dir `website`) · `tree-sitter-gsx` (grammar) · `vite-plugin-gsx` (`@gsxhq/vite-plugin-gsx`) · `vite` (`github.com/gsxhq/vite`). The dev loop (`gsx init` scaffold + Vite plugin + `vite` Go helper) is shipped/closed. Playground backend (`playground/server/`) deploys to Fly.io (`playground/server/fly.toml`); docs site to GitHub Pages via `deploy-docs.yml`.
