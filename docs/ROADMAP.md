# gsx Roadmap & Status

Living high-level status. Update as subsystems land. Detailed design lives in
`docs/superpowers/specs/`, plans in `docs/superpowers/plans/`.

Module: `github.com/gsxhq/gsx` · runtime is **standard-library only**; the
generator/CLI may use `golang.org/x/tools`.

## Pipeline at a glance

`.gsx` → **parser** → **AST** → **codegen** (`go/packages` resolution) → `.x.go` → `go build` → renders HTML via the **runtime**.

| Stage | Status |
|---|---|
| Parser + AST | ✅ done (Part 2 grammar + pipeline parsing) |
| Runtime (`gsx`) | ✅ done |
| Codegen | 🟡 phase 1 done (foundation + interpolation); feature phases pending |
| CLI / `gen.Main` | ⬜ not started |
| Pipeline `|>` end-to-end | 🟡 parsed only — **codegen + filters not done** |

## Done

**Parser / grammar** (`parser/`, `ast/`) — elements, fragments, text, interpolation
(`{ expr }`, `?` try), attributes (static / expr / bool / spread / markup),
control flow (`{ if/for/switch }`), `{{ }}` Go blocks, conditional attributes,
composable `class`/`style`, comments, `<!DOCTYPE>`, `<!-- -->`, raw-text
`<script>`/`<style>`, **pipeline `|>` parsing** (`Interp.Stages` / `ExprAttr`
stages). Public go/ast-parity API; fuzz-hardened (no crashers). 11/12 examples
parse (see Debts: example 02).

**Runtime** (`gsx`, module root) — `Node`/`Func`/`Raw`, error-threading `Writer`
with streaming text/attr/URL escapers, class/style compose + pluggable
`ClassMerger`, `Attrs` bag + deterministic `Spread`. Independent-review SHIP.

**Codegen phase 1** (`internal/codegen`) — `GeneratePackage(dir)`: `go/packages`
+ `Overlay` skeleton type resolution (cross-file, cross-component); arity-safe
`_gsxuse` probe; components+params → props + used-param local-binding; full §5
type-aware interpolation (string / []byte / numeric / bool / `gsx.Node` /
`[]gsx.Node` / `fmt.Stringer`; `gsx.Raw` via Node); `(T,error)` auto-unwrap;
child components (no props yet); GoChunk import hoisting; `//line` maps;
identifier hygiene + pointer-`Render` + overlay-collision hardening.
Tested by source golden + ~21 compile-and-render goldens.

## Codegen phase 2 — feature phases (next)

Each is a spec/plan → SDD slice that graduates more of the example corpus to
render goldens. Suggested order:

1. ⬜ **Guard pipeline silent-drop** — make codegen ERROR on non-empty
   `Interp.Stages`/`ExprAttr` stages until the pipeline is lowered (prevents
   silently dropping filters). *Do first — it's a correctness hole today.*
2. ⬜ **Control flow** — `{ if/for/switch }`, `{{ }}` → plain Go around writes.
3. ⬜ **Attributes** — static / expr (type+context-aware: `AttrValue` vs `URL`) /
   bool / composable `class`+`style` / spread / conditional `{ if … { attr } }`.
4. ⬜ **Pipeline `|>` + filters** — lower `Stages` to nested (generic) filter
   calls; `gen`-registered filter resolution via `go/types` harvest; ship a
   starter `std` filter package. (Ergonomically load-bearing for numerics.)
5. ⬜ **Child-component props + `{children}`** — attr→field mapping, children/slot
   closures.
6. ⬜ **Method components** — `component (r T) X()` → method.
7. ⬜ **Auto-fallthrough attrs + diagnostics** — single-root fallthrough +
   compile-time ambiguity errors.

## Tracked debts / deferrals

- ⬜ **Pipeline codegen + filters/`std`/`gen`** — designed
  (`2026-06-19-gsx-pipeline-and-extensions-design.md`), not implemented (phase-2 #4).
- ⬜ **Example 02 `02_text_escaping.gsx`** stays red (`47:35`): a `//` line
  comment in markup-content position. Separate parser gap; decision pending.
- ⬜ **`_gsx`-alias generator-emitted imports** — robust form of the import-shadow
  guard (currently `gsx`/`strconv` are reserved param names as a stopgap).
- ⬜ **Structured diagnostics** (`internal/diag`: GSXnnnn codes, ranges, JSON) —
  designed in the CLI-skeleton spec; not built.
- ⬜ **CLI / `gen.Main`** (`generate`/`fmt`/`vet`/`lsp`/`render`), file discovery,
  `//go:generate`, incremental/watch — designed, not built.
- ⬜ **Codegen niceties** — coalesce adjacent `gw.S` static writes; `//line`
  trailing-state reset; `data:image` URL allowance.

## Design docs (reference)

- `2026-06-18-gsx-templating-design.md` — the language.
- `2026-06-18-gsx-codegen-walkthrough.md` — hand-written generated code / runtime model.
- `2026-06-19-gsx-runtime-design.md` — runtime package.
- `2026-06-19-gsx-codegen-design.md` — codegen architecture + lowering rules.
- `2026-06-19-gsx-pipeline-and-extensions-design.md` — `|>` + filters + `gen.Main`.
- `2026-06-18-gsx-cli-skeleton-design.md` — CLI, exit codes, diagnostics model.
