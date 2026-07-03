# (R, error) Filters at Any Pipeline Stage — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A filter returning `(R, error)` works at any pipeline stage in every pipeline-legal context, halting the chain at the failing stage and returning the error out of the render closure; corpus gains per-case filter packages and a full coverage matrix.

**Architecture:** Harvest records `hasErr` per filter statically. `lowerPipe` gains a `wrap` callback applied to error-returning non-final stages: emit contexts pass a `hoistTuple`-based closure (statement hoist + `return _gsxerr`), probe contexts pass an `_gsxunwrap(...)` wrapper (single-expression skeleton probe) — both derive from the same stage lowering, preserving emit ≡ probe. Component cond-attrs lower to a statement-form `if/else` when (and only when) a branch needs hoisting. The final stage keeps flowing through the existing per-context `(T, error)` tuple machinery unchanged.

**Tech Stack:** Go 1.26.1 (pin: `GO_VERSION` in ci.yml), stdlib-only runtime, `golang.org/x/tools` in tooling, txtar corpus.

**Spec:** `docs/superpowers/specs/2026-07-03-pipe-error-any-stage-design.md`

## Global Constraints

- Execute in a **git worktree** (create at execution start via `superpowers:using-git-worktrees`), branch `pipe-error-any-stage`.
- Runtime (root package) stays **standard-library only**. No runtime API change in this feature.
- **No syntax change**: parser/AST/formatter/tree-sitter/vscode-gsx/CodeMirror untouched. The `?` try-marker stays rejected.
- Never hand-edit `.x.go` or `*.golden` — regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`. `-update` also rewrites `coverage.golden`.
- Inner loop: `make check`. Before merge: `make ci` + independent adversarial review (per CLAUDE.md process).
- Existing goldens must stay **byte-identical** except where a task explicitly says otherwise.
- Error semantics everywhere: failing stage halts the chain (later stages never run), `return _gsxerr` out of the render closure — identical to existing `(T, error)` auto-unwrap.

## Reference: key existing code (read before starting a task)

- `internal/codegen/filters.go:46` `lowerPipe(seed, stages, table)` — textual nesting, the thing we extend.
- `internal/codegen/filters.go:74` `filterEntry{funcName, wantsCtx, alias, pkgPath}`.
- `internal/codegen/filters.go:197` `harvestFilters`, `:365` `classifyFilter`, `:394` `validFilterResults` (already accepts 2-result-with-error).
- `internal/codegen/emit.go:1487` `hoistTuple(b, expr, interpTemp)` — emits `tmp, _gsxerr := expr; if _gsxerr != nil { return _gsxerr }`, returns temp name. The emit wrap reuses this exact helper.
- `internal/codegen/emit.go:1435` `genInterp` (text context), `:1741/:1820/:1898` attr paths, `:2071/:2105/:2352/:2420` style, `:2212` `lowerClassPartSeed`, `:3629` `childPropsLiteral` (pipe lowering at `:3656/:3701/:3743/:3798`), `:3922` `classEntryExpr`, `:4066` `condAttrsExpr`, `:4090` `condBranchAttrs`.
- `internal/codegen/analyze.go:1434` `probeExpr`, `:2187` `walkLivenessAttrExprs`.
- `internal/codegen/module_importer.go:840` skeleton helpers incl. `_gsxunwrap[T any](v T, _ ...any) T`.
- `internal/corpus/loader.go:30` `caseToml` (per-case `gsx.toml`, parsed, never written to disk), `:15` `caseDoc`.
- `internal/corpus/codegen.go:63` `codegenDirs` (hardcodes `FilterPkgs: [std]`), `caseImportRoot`.
- `internal/corpus/batch.go:231` render dispatch — currently `_ = pkg.GsxEntryRender(...)`, discards render errors.

---

### Task 1: Corpus infra — per-case `filterPackages` + render-error capture

**Files:**
- Modify: `internal/corpus/loader.go` (caseToml, caseDoc, loadCase)
- Modify: `internal/corpus/codegen.go` (codegenDirs signature)
- Modify: `internal/corpus/batch.go` (custom-options rerun set at ~:95–110; render dispatch at ~:231, generated main imports at ~:236)
- Test: `internal/corpus/loader_test.go`; new case `internal/corpus/testdata/cases/pipeerr/final_stage_custom_filter.txtar`

**Interfaces:**
- Consumes: existing `caseImportRoot(c) == "corpustest/cases/" + c.dir`.
- Produces: `caseDoc.filterPkgs []string` (resolved absolute import paths); `codegenDirs(moduleDir string, dirs []string, merger *codegen.ClassMergerRef, filterPkgs []string)`; batch main prints `\n[render error] <err>` to stdout when `GsxEntryRender` returns non-nil, so `render.golden` pins partial output + error text.

- [x] **Step 1: Write the failing loader test**

In `internal/corpus/loader_test.go` add:

```go
func TestLoadCaseFilterPackages(t *testing.T) {
	dir := t.TempDir()
	src := `-- gsx.toml --
filterPackages = ["./filters", "github.com/gsxhq/gsx/std"]
-- filters/filters.go --
package filters
-- input.gsx --
package views

component C() { <p>hi</p> }
`
	path := filepath.Join(dir, "testdata", "cases", "pipeerr", "fp.txtar")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCase(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"corpustest/cases/pipeerr_fp/filters", "github.com/gsxhq/gsx/std"}
	if !slices.Equal(c.filterPkgs, want) {
		t.Fatalf("filterPkgs = %v, want %v", c.filterPkgs, want)
	}
	if _, hasToml := c.files["gsx.toml"]; hasToml {
		t.Fatal("gsx.toml must not be written to disk")
	}
}
```

(Match the existing test file's import style; the case-name→dir mapping `pipeerr/fp` → `pipeerr_fp` follows `loadCase`'s `strings.ReplaceAll(name, "/", "_")`.)

- [x] **Step 2: Run it to verify it fails**

Run: `go test ./internal/corpus -run TestLoadCaseFilterPackages -v`
Expected: FAIL — `c.filterPkgs undefined`.

- [x] **Step 3: Implement loader support**

In `internal/corpus/loader.go`:

```go
type caseToml struct {
	ClassMerger    string   `toml:"class_merger"`
	FilterPackages []string `toml:"filterPackages"`
}
```

Add `filterPkgs []string` to `caseDoc` (comment: resolved import paths; `"./x"` entries resolve against the case import root). In the `case f.Name == "gsx.toml":` branch of `loadCase`, after the class_merger block:

```go
for _, p := range tc.FilterPackages {
	if strings.HasPrefix(p, "./") {
		p = caseImportRoot(c) + strings.TrimPrefix(p, ".")
	}
	c.filterPkgs = append(c.filterPkgs, p)
}
```

NOTE: `caseImportRoot` uses `c.dir`, which is set before the file loop — verify that ordering holds; if not, resolve after the loop.

- [x] **Step 4: Thread into codegen + batch**

`internal/corpus/codegen.go` — extend `codegenDirs`:

```go
func codegenDirs(moduleDir string, dirs []string, merger *codegen.ClassMergerRef, filterPkgs []string) (map[string]codegen.DirResult, error) {
	return codegen.GenerateDirs(moduleDir, dirs, codegen.Options{
		FilterPkgs:  append([]string{codegen.StdImportPath}, filterPkgs...),
		CSSMinify:   true,
		JSMinify:    true,
		ClassMerger: merger,
	}, nil)
}
```

`internal/corpus/batch.go` — update both call sites (default run passes `nil, nil`); extend the existing per-case custom-options rerun (the class-merger path at ~:103) to also trigger when `len(cs.c.filterPkgs) > 0`, passing `cs.c.classMerger, cs.c.filterPkgs`. Grep for any other `codegenDirs(` callers (`coverage.go`, tests) and update.

- [x] **Step 5: Capture render errors in the batch main**

At `batch.go:~231` replace the discard with an error print, and add `"fmt"` to the generated main's imports (~:236):

```go
fmt.Fprintf(&dispatch, "\tos.Stdout.WriteString(%q)\n\tif err := %s.GsxEntryRender(ctx, os.Stdout); err != nil {\n\t\tfmt.Fprintf(os.Stdout, \"\\n[render error] %%v\", err)\n\t}\n",
	marker, qual) // keep the SAME two args the current Fprintf at batch.go:231 passes
	// (the per-case output marker string and the entry package qualifier) —
	// only the format string changes.
```

- [x] **Step 6: Add the first custom-filter corpus case (final-stage error — works TODAY, proves infra)**

Create `internal/corpus/testdata/cases/pipeerr/final_stage_custom_filter.txtar`:

```
# A custom filter package wired via per-case gsx.toml filterPackages. The final
# stage returns (string, error); the existing tuple auto-unwrap handles it —
# this case pins the ALREADY-SHIPPED behavior end-to-end for the first time.
-- gsx.toml --
filterPackages = ["./filters"]
-- filters/filters.go --
package filters

import "errors"

// Shout upcases-ish by appending "!"; fails on empty input.
func Shout(s string) (string, error) {
	if s == "" {
		return "", errors.New("shout: empty input")
	}
	return s + "!", nil
}
-- input.gsx --
package views

component Hi(name string) {
	<p>{ name |> shout }</p>
}
-- invoke --
Hi(HiProps{Name: "ada"})
-- diagnostics.golden --
-- render.golden --
<p>ada!</p>
```

- [x] **Step 7: Regenerate goldens, verify, inspect**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. The new case gains a `generated.x.go.golden`; **diff the whole tree** — no other `render.golden` may change (if one does, an existing case has a silently-erroring render: STOP and surface it to the user rather than accepting the new golden).

- [x] **Step 8: Run corpus + loader tests, commit**

Run: `go test ./internal/corpus` — PASS.
```bash
git add internal/corpus docs/superpowers/plans
git commit -m "test(corpus): per-case filterPackages + render-error capture"
```

---

### Task 2: Harvest `hasErr` on `filterEntry`

**Files:**
- Modify: `internal/codegen/filters.go` (filterEntry, harvestFilters, classifyFilter callers)
- Test: `internal/codegen/filters_test.go`

**Interfaces:**
- Produces: `filterEntry.hasErr bool` — true iff the harvested func's results are `(R, error)`. Task 3 reads it in `lowerPipe`.

- [x] **Step 1: Write the failing test**

In `internal/codegen/filters_test.go` add a test that harvests a tiny in-memory package (follow the file's existing harvest-test fixture pattern — there are existing tests loading filter packages from a temp module; copy that setup):

```go
func TestHarvestHasErr(t *testing.T) {
	// Fixture package source:
	//   func Plain(s string) string                  → hasErr=false
	//   func Fallible(s string) (string, error)      → hasErr=true
	//   func CtxFallible(ctx context.Context, s string) (int, error) → hasErr=true, wantsCtx=true
	//   func Generic[T any](s []T) (T, error)        → hasErr=true
	table := harvestFixture(t, `package f

import "context"

func Plain(s string) string { return s }
func Fallible(s string) (string, error) { return s, nil }
func CtxFallible(ctx context.Context, s string) (int, error) { return 0, nil }
func Generic[T any](s []T) (T, error) { var z T; return z, nil }
`)
	for name, want := range map[string]bool{
		"plain": false, "fallible": true, "ctxFallible": true, "generic": true,
	} {
		e, ok := table.lookup(name)
		if !ok {
			t.Fatalf("filter %q not harvested", name)
		}
		if e.hasErr != want {
			t.Errorf("%s: hasErr = %v, want %v", name, e.hasErr, want)
		}
	}
}
```

(`harvestFixture` = whatever helper the existing tests use to build a `filterTable` from source; write a small one against `loadFilterTableMulti` + a temp module if none exists.)

- [x] **Step 2: Run to verify it fails**

Run: `go test ./internal/codegen -run TestHarvestHasErr -v`
Expected: FAIL — `e.hasErr undefined`.

- [x] **Step 3: Implement**

`filterEntry` gains `hasErr bool` (comment: the filter returns `(R, error)` and needs stage-hoisting when non-final). In `harvestFilters`, where each entry is built from the classified signature, set `hasErr: sig.Results().Len() == 2` (the contract already validated result shapes in `classifyFilter`). Set it on the `WithFilter` alias path too (same function handles both — verify).

- [x] **Step 4: Run tests, commit**

Run: `go test ./internal/codegen -run 'TestHarvest|TestFilter' && make check`
Expected: PASS (pure addition, no golden changes).
```bash
git add internal/codegen
git commit -m "feat(codegen): record hasErr on harvested filter entries"
```

---

### Task 3: Stage-aware `lowerPipe` + text context (emit & probe) — the first E2E mid-stage case

**Files:**
- Modify: `internal/codegen/filters.go` (`lowerPipe`)
- Modify: `internal/codegen/emit.go` (`genInterp` at :1435; a shared emit-wrap helper next to `hoistTuple`)
- Modify: `internal/codegen/analyze.go` (`probeExpr` at :1434 and its callers)
- Test: `internal/codegen/filters_test.go` (lowering unit tests), `internal/codegen/` skeleton snapshot test, corpus case `pipeerr/text_mid_stage.txtar` + `pipeerr/text_mid_stage_error.txtar`

**Interfaces:**
- Consumes: `filterEntry.hasErr` (Task 2).
- Produces: `lowerPipe(seed string, stages []ast.PipeStage, table filterTable, wrap func(call string) string) (expr string, usedPkgs map[string]string, err error)`. `wrap` is applied to each error-returning **non-final** stage's call; the final stage is returned as-is (its tuple flows through existing per-context machinery). `wrap == nil` → a mid-pipeline `hasErr` stage returns the error `codegen: filter %q returns (R, error); a failing stage is not supported in this position` (callers position it). Also produces two canonical wraps used by every later task: `emitPipeWrap(b, interpTemp)` and the probe literal `probePipeWrap`.

- [x] **Step 1: Write failing lowering unit tests**

```go
func TestLowerPipeMidStageErr(t *testing.T) {
	table := filterTable{
		"parse": {funcName: "Parse", alias: "_gsxf0", pkgPath: "m/f", hasErr: true},
		"join":  {funcName: "Join", alias: "_gsxstd", pkgPath: "github.com/gsxhq/gsx/std"},
	}
	stages := []ast.PipeStage{{Name: "parse"}, {Name: "join", HasArgs: true, Args: `" "`}}

	// probe-style wrap
	got, _, err := lowerPipe("csv", stages, table, func(c string) string { return "_gsxunwrap(" + c + ")" })
	if err != nil {
		t.Fatal(err)
	}
	want := `_gsxstd.Join(_gsxunwrap(_gsxf0.Parse((csv))), " ")`
	if got != want {
		t.Errorf("probe form:\n got %s\nwant %s", got, want)
	}

	// final-stage hasErr is NOT wrapped
	got2, _, _ := lowerPipe("csv", []ast.PipeStage{{Name: "parse"}}, table, func(c string) string { return "WRAPPED" })
	if got2 != "_gsxf0.Parse((csv))" {
		t.Errorf("final stage must stay unwrapped, got %s", got2)
	}

	// nil wrap + mid-stage hasErr → friendly error
	_, _, err = lowerPipe("csv", stages, table, nil)
	if err == nil || !strings.Contains(err.Error(), `filter "parse" returns (R, error)`) {
		t.Errorf("nil-wrap error = %v", err)
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/codegen -run TestLowerPipeMidStageErr -v`
Expected: FAIL — wrong argument count to `lowerPipe`.

- [x] **Step 3: Implement the new `lowerPipe`**

Replace the body (`filters.go:46`), keeping the doc comment updated:

```go
func lowerPipe(seed string, stages []ast.PipeStage, table filterTable, wrap func(call string) string) (expr string, usedPkgs map[string]string, err error) {
	acc := "(" + strings.TrimSpace(seed) + ")"
	usedPkgs = map[string]string{}
	for i, st := range stages {
		e, ok := table.lookup(st.Name)
		if !ok {
			return "", nil, fmt.Errorf("codegen: unknown filter %q", st.Name)
		}
		usedPkgs[e.alias] = e.pkgPath
		args := make([]string, 0, 3)
		if e.wantsCtx {
			args = append(args, pipeCtxIdent)
		}
		args = append(args, acc)
		if st.HasArgs && strings.TrimSpace(st.Args) != "" {
			args = append(args, st.Args)
		}
		acc = e.alias + "." + e.funcName + "(" + strings.Join(args, ", ") + ")"
		if e.hasErr && i < len(stages)-1 {
			if wrap == nil {
				return "", nil, fmt.Errorf("codegen: filter %q returns (R, error); a failing stage is not supported in this position", st.Name)
			}
			acc = wrap(acc)
		}
	}
	return acc, usedPkgs, nil
}
```

Next to `hoistTuple` in `emit.go`, add the canonical emit wrap and probe wrap:

```go
// emitPipeWrap returns the lowerPipe wrap for emit contexts: each error-returning
// non-final stage hoists through hoistTuple (temp + `if _gsxerr != nil { return
// _gsxerr }`), halting the chain at the failing stage. Statements land in b
// before the statement that consumes the pipeline's final expression.
func emitPipeWrap(b *bytes.Buffer, interpTemp *int) func(string) string {
	return func(call string) string { return hoistTuple(b, call, interpTemp) }
}

// probePipeWrap is the lowerPipe wrap for skeleton probes: _gsxunwrap keeps the
// probe a single expression while preserving the stage's result type and any
// positioned go/types errors inside the user's args (emit ≡ probe).
func probePipeWrap(call string) string { return "_gsxunwrap(" + call + ")" }
```

(`probePipeWrap` is needed from `analyze.go` too — both files are package `codegen`, fine.)

- [x] **Step 4: Mechanically update ALL existing `lowerPipe` callers to compile**

Every call site gains a 4th arg. In THIS task set the correct wraps for the **text/probe** paths and `nil` everywhere else (later tasks upgrade them):
- `emit.go:1445` (`genInterp`): `emitPipeWrap(b, interpTemp)` — verify `interpTemp` is in scope (it's a `genInterp` param).
- `analyze.go:1438` (`probeExpr`): `probePipeWrap`.
- `analyze.go:2199` (`walkLivenessAttrExprs`): `probePipeWrap` (a probe-side path — safe to set correctly now).
- All other emit sites (`emit.go:1741, 1820, 1898, 2071, 2105, 2216, 2352, 2420, 3656, 3701, 3743, 3798`): `nil` for now. Where the caller reports errors via `bag.Errorf`/`attrError`, the nil-wrap error surfaces positioned automatically through the existing `unresolved-pipeline` handling — confirm each site funnels the error into its existing diagnostic path (they all already handle a lowerPipe error).

Run: `go build ./... && gopls check internal/codegen/*.go` — clean.

- [x] **Step 5: Verify zero regressions**

Run: `go test ./internal/corpus -run TestCorpus && go test ./internal/codegen`
Expected: PASS with **zero golden changes** (no existing case uses a mid-stage error filter).

- [x] **Step 6: Add the two E2E corpus cases**

`internal/corpus/testdata/cases/pipeerr/text_mid_stage.txtar`:

```
# A (R, error) filter as a NON-FINAL pipeline stage in text context: the stage
# hoists to `_gsxvN, _gsxerr := ...; if _gsxerr != nil { return _gsxerr }` and
# the next stage consumes the temp. Success path.
-- gsx.toml --
filterPackages = ["./filters"]
-- filters/filters.go --
package filters

import (
	"errors"
	"strings"
)

// Parse splits a comma-separated list; fails on empty input.
func Parse(s string) ([]string, error) {
	if s == "" {
		return nil, errors.New("parse: empty input")
	}
	return strings.Split(s, ","), nil
}
-- input.gsx --
package views

component Tags(csv string) {
	<p>{ csv |> parse |> join(" ") }</p>
}
-- invoke --
Tags(TagsProps{Csv: "a,b"})
-- diagnostics.golden --
-- render.golden --
<p>a b</p>
```

`pipeerr/text_mid_stage_error.txtar`: same filters + component, but `-- invoke --` is `Tags(TagsProps{Csv: ""})` and `render.golden` pins the partial output + `[render error] parse: empty input` line (exact bytes come from `-update`; INSPECT them — the error must be present and later stages must not have run).

- [x] **Step 7: Regenerate, verify, and INSPECT the generated code (user checkpoint)**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Open `text_mid_stage.txtar`'s new `generated.x.go.golden` and confirm it matches the approved Approach-A shape (hoisted `_gsxv0, _gsxerr := _gsxf0.Parse((csv))` before `_gsxgw.Text(...)`). **This golden is the artifact the user asked to see — surface it in the task report.**

- [x] **Step 8: Skeleton snapshot test (the probe form, second user checkpoint)**

Add a unit test in `internal/codegen` that generates the skeleton for the `text_mid_stage` source and asserts the probe line contains `_gsxuse(_gsxstd.Join(_gsxunwrap(_gsxf0.Parse((csv))), " "))`. Follow the existing skeleton-dumping test pattern in `analyze_test.go` (there are tests that build skeletons from source; reuse their harness). Name it `TestSkeletonProbeMidStageErrFilter`. Include the skeleton snippet in the task report for user inspection.

- [x] **Step 9: Run the full inner loop, commit**

Run: `make check`
Expected: PASS.
```bash
git add internal/codegen internal/corpus
git commit -m "feat(codegen): (R, error) filters at non-final pipeline stages (text context)"
```

---

### Task 4: Thread the emit wrap through the remaining value contexts

**Files:**
- Modify: `internal/codegen/emit.go` — attr paths (:1741, :1820, :1898), style (:2071, :2105, :2352, :2420), `lowerClassPartSeed` (:2212, gains a `wrap` param) + `classEntryExpr` (:3922) + `hoistValueCF` arm lowering, `childPropsLiteral` (:3656, :3701, :3743, :3798)
- Modify: `internal/codegen/analyze.go` — the `classEntryExpr(probeWrap=true)` skeleton path and child-prop probes (~:930–1110) pass `probePipeWrap`
- Test: corpus cases per context under `pipeerr/`

**Interfaces:**
- Consumes: `lowerPipe(..., wrap)`, `emitPipeWrap(b, interpTemp)`, `probePipeWrap` (Task 3).
- Produces: every pipeline-legal value context accepts mid-stage error filters, except cond-attr branches (Task 5). `lowerClassPartSeed(p ast.ClassPart, table filterTable, wrap func(string) string)`.

- [x] **Step 1: Add failing corpus cases (one per context; same `filters/filters.go` as Task 3's `Parse` + a `Pick(s []string, i int) (string, error)` filter)**

Create under `internal/corpus/testdata/cases/pipeerr/` (each with `gsx.toml` + `filters/filters.go` as in Task 3, plus `Pick`):

```go
// Pick returns element i; fails when out of range.
func Pick(s []string, i int) (string, error) {
	if i < 0 || i >= len(s) {
		return "", fmt.Errorf("pick: index %d out of range", i)
	}
	return s[i], nil
}
```

- `attr_mid_stage.txtar` — `<a href={ csv |> parse |> pick(0) |> lower }>x</a>`, invoke `Csv: "B,C"`, render `<a href="b">x</a>`. (Mid stages `parse` AND `pick` both hoist; `lower` is std.)
- `class_part_mid_stage.txtar` — `<div class={ csv |> parse |> pick(0) }>x</div>`, render `<div class="B">x</div>`.
- `class_cf_arm_mid_stage.txtar` — value-form CF arm: `class={ if ok { csv |> parse |> pick(0) } else { "z" } }` (hoists must land INSIDE the if/case block — this exercises the `hoistValueCF`/`armExpr` path at emit.go:3936).
- `style_mid_stage.txtar` — `<div style={ color: csv |> parse |> pick(0) }>x</div>` with invoke `Csv: "red,blue"` (match the existing style-pipeline case shape in `cases/style/block_pipeline.txtar` — copy its syntax exactly).
- `child_prop_mid_stage.txtar` — `<Label text={ csv |> parse |> pick(0) }/>` (upgrades the deferred case; ALSO delete the stale deferral note from `cases/tuple/child_prop_pipeline.txtar`'s header comment).
- `ordered_attrs_mid_stage.txtar` — two expr attrs where one is a mid-stage-error pipe, pinning hoist order relative to the ordered-attrs temp hoisting (read `emit.go:2932–3030` first; the case pins whatever the correct interleaving is).

- [x] **Step 2: Run to verify they fail with the friendly nil-wrap diagnostic**

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | grep pipeerr`
Expected: each new case fails; diagnostics contain `returns (R, error); a failing stage is not supported in this position` (NOT raw go/types noise). If any context shows raw go/types errors instead, its probe path is reached before emit — fix that context's probe to `probePipeWrap` first.

- [x] **Step 3: Upgrade each context to `emitPipeWrap`**

For each site: the enclosing function already has (or is already passed) `b *bytes.Buffer` + `interpTemp *int` for `hoistTuple` — pass `emitPipeWrap(b, interpTemp)`. For `lowerClassPartSeed`, add the `wrap` param and thread from `classEntryExpr`: emit mode (`probeWrap=false`, `b != nil`) → `emitPipeWrap(b, interpTemp)`; skeleton mode (`probeWrap=true`) → `probePipeWrap`; cond-attr branch mode (`b == nil`) → `nil` (Task 5 lifts it). In `hoistValueCF`/`armExpr`, the wrap must capture the CURRENT emit position so hoists land inside the arm's block (the existing `hoistTuple` call there at emit.go:3961 shows the correct `b`). Update the child-prop probe path in `analyze.go` (~:930–1110) to `probePipeWrap`.

- [x] **Step 4: Regenerate, verify all cases render**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus && go test ./internal/codegen`
Expected: PASS; new goldens only. Inspect each new `generated.x.go.golden`: hoists land before the consuming statement (and inside if/case blocks for the CF-arm case).

- [x] **Step 5: `make check`, commit**

```bash
git add internal/codegen internal/corpus
git commit -m "feat(codegen): mid-stage (R, error) filters in attr/class/style/child-prop contexts"
```

---

### Task 5: Cond-attr statement form (branch-level hoisting)

**Files:**
- Modify: `internal/codegen/emit.go` — `condAttrsExpr` (:4066) call site(s) + new `condAttrsStmt`; `condBranchAttrs` (:4090) gains `b/interpTemp/wrap`
- Test: corpus cases `pipeerr/cond_attr_branch_mid_stage.txtar`, `pipeerr/cond_attr_branch_final_stage.txtar`

**Interfaces:**
- Consumes: `emitPipeWrap`, `lowerPipe(..., wrap)`.
- Produces: `condAttrsStmt(b *bytes.Buffer, interpTemp *int, t *ast.CondAttr, rtPkg, tag, mergeExpr string, table filterTable, resolved map[ast.Node]types.Type) (tempName string, usedPkgs map[string]string, err error)` — emits `var _gsxvN gsx.Attrs; if cond { ...hoists...; _gsxvN = gsx.Attrs{...} } else { ... }` and returns `_gsxvN`; plus `condBranchNeedsHoist(attrs []ast.Attr, table filterTable, resolved map[ast.Node]types.Type) bool`.

- [x] **Step 1: Add the failing corpus case**

`pipeerr/cond_attr_branch_mid_stage.txtar` (component target; filters as Task 4):

```
# A mid-stage (R, error) filter inside a component conditional-attr branch.
# The branch lowers to a statement-form if/else (NOT AttrsCond thunks) so the
# hoist's `return _gsxerr` is legal; the untaken branch still never evaluates.
-- gsx.toml --
filterPackages = ["./filters"]
-- filters/filters.go --
package filters

import (
	"errors"
	"fmt"
	"strings"
)

// Parse splits a comma-separated list; fails on empty input.
func Parse(s string) ([]string, error) {
	if s == "" {
		return nil, errors.New("parse: empty input")
	}
	return strings.Split(s, ","), nil
}

// Pick returns element i; fails when out of range.
func Pick(s []string, i int) (string, error) {
	if i < 0 || i >= len(s) {
		return "", fmt.Errorf("pick: index %d out of range", i)
	}
	return s[i], nil
}
-- input.gsx --
package views

component Card(title gsx.Node, attrs gsx.Attrs) { <div {attrs}>{title}</div> }

component Page(hot bool, csv string) {
	<Card title={"Hi"} { if hot { data-pick={ csv |> parse |> pick(0) } } }/>
}
-- invoke --
Page(PageProps{Hot: true, Csv: "a,b"})
-- diagnostics.golden --
-- render.golden --
<div data-pick="a">Hi</div>
```

(FIRST read `cases/components/component_conditional_attr.txtar` and mirror its exact component/attr syntax — the sketch above must be adjusted to the real cond-attr syntax pinned there.)

- [x] **Step 2: Verify it fails with the friendly diagnostic**

Run: `go test ./internal/corpus -run TestCorpus 2>&1 | grep cond_attr_branch`
Expected: FAIL with the nil-wrap diagnostic from Task 3.

- [x] **Step 3: Implement `condBranchNeedsHoist` + `condAttrsStmt`**

`condBranchNeedsHoist`: true when any branch attr (then/else, recursively over its class parts / expr attrs) is a pipeline with a `hasErr` non-final stage (walk `Stages` against `table`), OR hits the existing `b == nil` edges (tuple class part / ordered part / value-form CF — consult the same conditions that produce the three "not supported yet" attrErrors at emit.go:3930/:4008/:4013).

`condAttrsStmt`: emit the statement form —

```go
tmp := fmt.Sprintf("_gsxv%d", *interpTemp)
*interpTemp++
fmt.Fprintf(b, "\t\tvar %s %s.Attrs\n\t\tif %s {\n", tmp, rtPkg, strings.TrimSpace(t.Cond))
// inside the block: build the branch's Attrs literal with condBranchAttrs,
// now passing b/interpTemp/emitPipeWrap so hoists land INSIDE the if block:
fmt.Fprintf(b, "\t\t%s = %s\n", tmp, thenLit)
fmt.Fprintf(b, "\t\t} else {\n")   // only when t.Else non-empty; else leave zero Attrs
fmt.Fprintf(b, "\t\t%s = %s\n\t\t}\n", tmp, elseLit)
```

At the `condAttrsExpr` call site(s) (grep `condAttrsExpr(` — find the caller that builds the component Attrs merge chain): when `condBranchNeedsHoist(...)`, call `condAttrsStmt` and splice `tmp` into the merge expression where the `AttrsCond(...)` call would sit; otherwise keep `condAttrsExpr` byte-identical. Indentation of emitted statements must match the surrounding generated code (compare against an `-update` diff).

- [x] **Step 4: Regenerate + verify + inspect**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus`
Expected: PASS. Verify (a) the new case's golden shows `var _gsxvN gsx.Attrs; if hot { ... }`, (b) **every pre-existing cond-attr golden is byte-identical** (`git diff --stat internal/corpus/testdata` shows only `pipeerr/`), (c) the untaken-branch guarantee: add `pipeerr/cond_attr_branch_untaken.txtar` — same source, `Hot: false`, `Csv: ""` (parse would fail!) — render.golden shows the div WITHOUT the attr and WITHOUT a render error, proving the untaken branch never ran.

- [x] **Step 5: `make check`, commit**

```bash
git add internal/codegen internal/corpus
git commit -m "feat(codegen): statement-form cond-attr lowering when a branch needs hoisting"
```

Note: lifting the three pre-existing "not supported yet" cond-attr edges via this same path is IN scope only if it falls out of `condBranchNeedsHoist` naturally; if any resists, keep its diagnostic and note it in the task report rather than growing this task.

---

### Task 6: Generics, ctx filters, halt semantics, diagnostics polish

**Files:**
- Test: corpus cases under `pipeerr/`; `internal/codegen/filters_test.go`

**Interfaces:** consumes everything above; produces no new API.

- [x] **Step 1: Generic filter cases**

Shared `-- filters/filters.go --` for these cases:

```go
package filters

import (
	"errors"
	"fmt"
	"strings"
)

// First returns the first element; fails on an empty slice.
func First[T any](s []T) (T, error) {
	var zero T
	if len(s) == 0 {
		return zero, errors.New("first: empty")
	}
	return s[0], nil
}

// Rev reverses a slice (no error) — a generic pass-through stage.
func Rev[T any](s []T) []T {
	out := make([]T, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

// Parse splits a comma-separated list; fails on empty input.
func Parse(s string) ([]string, error) {
	if s == "" {
		return nil, errors.New("parse: empty input")
	}
	return strings.Split(s, ","), nil
}

var _ = fmt.Sprintf
```

- `generic_mid_stage.txtar` — `{ csv |> parse |> rev |> first |> upper }` (generic error filter `first` mid-chain, instantiation inferred through the chain; `parse` also mid-stage error). Invoke `Csv: "a,b"` → `B`.
- `generic_final_stage.txtar` — `{ csv |> parse |> first }` → `a`.
- `ctx_err_filter.txtar` — `func Who(ctx context.Context, s string) (string, error)` appending `"!"`; `{ name |> who }` → pins `wantsCtx` + `hasErr` together (ctx arg injected before the subject, tuple unwrapped).

- [x] **Step 2: Halt-semantics case (later stages never run)**

`halt_on_error.txtar` — filters:

```go
// Fail always errors.
func Fail(s string) (string, error) { return "", errors.New("fail: boom") }

// Detonate panics if ever invoked — proves the chain halted at Fail.
func Detonate(s string) string { panic("detonate: stage ran after error") }
```

`{ name |> fail |> detonate }`; render.golden pins `[render error] fail: boom`. If codegen ever evaluated later stages after an error, the batch `go run` would panic and the whole corpus run fails loudly.

- [x] **Step 3: Negative / diagnostics cases**

- `js_attr_still_rejected.txtar` — an error filter piped in a JS attr position; pins that the existing pipeline rejection message is unchanged (copy the shape of `cases/pipelines/attr_js_rejected.txtar`). ACTUAL outcome: renders cleanly (no rejection) — post-JS-unlock, a plain `onclick={…}` ExprAttr has no special JS-context routing (only the explicit `js\`…\`` literal form does), so it goes through the same already-shipped `emitPipeWrap` ExprAttr path as `attr_mid_stage.txtar`. Pinned as such; the filename is a historical label, not a live rejection.
- `try_marker_still_rejected.txtar` — `{ csv |> parse? |> join(" ") }` with a REAL error filter; pins the existing `?` diagnostic text (fires at parse time, before filter resolution, so identical regardless of the named filter's error-ness).
- If Task 5 left any nil-wrap position, add one case per remaining position pinning the friendly `returns (R, error); a failing stage is not supported in this position` diagnostic. None added: the one remaining nil-wrap position (a component cond-attr branch pipeline with NO error-returning stage) is pre-existing, out of this feature's matrix per controller guidance.
- Extra case requested by the Task 5 review, `cond_attr_branch_combined.txtar` (component cond-attr branch with BOTH an error-stage pipe `data-pick={ csv |> parse |> pick(0) }` AND a composable `class={ cls(csv) }` where `cls` is a local `(string, error)` helper): NOT pinned. It surfaces a raw Go compiler diagnostic leaking through as corpus "diagnostics" (`8:279: too many arguments in call to _gsxrt.Class \n\thave (string, error)\n\twant (string)`) instead of either a clean render or a friendly positioned gsx diagnostic — see task-6-report.md for the full repro and root-cause analysis. Left OUT of this commit per instructions (raw compile-error noise is not a valid pin); flagged as DONE_WITH_CONCERNS for the controller.

- [x] **Step 4: Regenerate, verify, full suite, commit**

Run: `go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus && make check`
Expected: PASS.
```bash
git add internal/corpus internal/codegen
git commit -m "test(corpus): generics, ctx, halt-semantics and diagnostics coverage for pipe error stages"
```

---

### Task 7: Docs + roadmap + CI

**Files:**
- Modify: `docs/guide/syntax/pipelines.md`, `docs/ROADMAP.md`
- Verify: full `make ci`

- [ ] **Step 1: Document error-returning filters in `docs/guide/syntax/pipelines.md`**

Read the page first and match its voice/structure. Add a section (adjust heading level to the page):

```markdown
## Filters that can fail

A filter may return `(R, error)` — at any stage of a pipeline:

​```go
func Parse(s string) ([]string, error) { … }
​```

​```gsx
<p>{ csv |> parse |> join(" ") }</p>
​```

When a stage returns a non-nil error, the pipeline halts at that stage
(later filters never run), rendering stops, and the error is returned
from the component's render — the same auto-unwrap gsx applies to any
`(T, error)` interpolation. To handle the error instead, use the
explicit form: `{ if v, err := parse(csv); err != nil { … } }`.
```

Check whether `docs/guide/syntax/_generated` content comes from corpus `-- doc --` sections (see `internal/corpus/docmeta.go`); if pipelines examples are generated, add a `-- doc --` section to `pipeerr/text_mid_stage.txtar` following an existing documented case's format instead of hand-writing the example. Remember: literal `{{ }}` in guide prose needs `::: v-pre`.

- [ ] **Step 2: Update `docs/ROADMAP.md`**

Mark the error-filter/pipeline item (grep ROADMAP for `filter` / `pipeline` / `error`) as shipped with a one-line pointer to the spec.

- [ ] **Step 3: Full CI + commit**

Run: `make ci`
Expected: PASS (build/vet/test both modules, examples drift, gofmt + gsx fmt).
```bash
git add docs
git commit -m "docs: pipeline filters returning (R, error) at any stage"
```

- [ ] **Step 4: Adversarial review + merge prep**

Per CLAUDE.md: one independent adversarial reviewer (builds throwaway probe programs — e.g. a scratch module with exotic error filters: generic ctx filter, filter whose R is a named error-ish type, two error stages back-to-back, error filter under `-race`) before merging. Then `superpowers:finishing-a-development-branch`.

---

## Self-review notes

- Spec §1–§7 each map to Tasks 2, 3, 5, 3–4, 1, 3–6, 7 respectively; probe-form open question is answered by Task 3 Steps 7–8 (explicit user-inspection checkpoints).
- Line numbers are anchors from 2026-07-03 main (`eac2410`); re-grep if drifted.
- Task 4 Step 1's style/cond-attr case syntax must be copied from existing pinned cases, not invented — called out inline.
