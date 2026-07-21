# Runtime Render Audit Baseline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore `gsx-bench` against current GSX, add representative attribute-forwarding workloads, and produce a reproducible ranked runtime/codegen hotspot audit that determines which optimisations deserve implementation.

**Architecture:** The sibling worktrees are paired so `gsx-bench`'s existing `replace github.com/gsxhq/gsx => ../gsx` resolves to the audited core branch. `gsx-bench` owns external user workloads and output equivalence; the core repository owns exact-path microbenchmarks and the audit report. This plan stops at the evidence boundary: the follow-up optimisation plan is written only after profiles and repeated benchmark results identify concrete targets.

**Tech Stack:** Go 1.26.1, GSX generator/runtime, a-h/templ v0.3.1020, Go benchmark/profile tooling, `golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d`, gopls, Make.

## Global Constraints

- Work only in `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx` and `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench`.
- Measure absolute GSX performance first; templ and `html/template` are context.
- Do not use or modify `~/work/one-learning`.
- Do not add compatibility wrappers or preserve obsolete generated/runtime APIs.
- Do not hand-edit generated `.x.go` files; regenerate from authored `.gsx` or `.templ` sources.
- Keep the root GSX runtime standard-library only.
- Preserve exact escaping, URL sanitisation, attribute ordering, duplicate resolution, class/style precedence, writer error semantics, and concurrency behavior.
- Keep raw benchmark output and profiles under `/tmp/gsx-runtime-audit`, not in either repository.
- A large ABI experiment is not authorised by this plan; it requires the follow-up optimisation plan and double-digit representative evidence.

---

## File Map

### `gsx-bench`

- `gsxr/escape.gsx`, `gsxr/nums.gsx`, `gsxr/page.gsx`, `gsxr/scenarios.gsx`, `tw/buttons.gsx`: migrate authored components from the removed implicit `attrs` binding to explicit `gsx.Attrs` parameters.
- `gsxr/*.x.go`, `tw/*.x.go`: regenerated current GSX output; never edit directly.
- `bench_test.go`, `document_test.go`, `scenarios_test.go`, `nums_test.go`, `tw_test.go`, `conc_test.go`: call the current verbatim generated signatures.
- `gsxr/attrs.gsx`: GSX-owned forwarded-attribute and folded-attribute workloads.
- `templr/attrs.templ`: templ counterpart for the shared forwarding workload.
- `templr/attrs_templ.go`: generated templ output; never edit directly.
- `attrs_test.go`: shared inputs, equivalence tests, folded-output assertions, and pooled/discard benchmarks.
- `Makefile`, `README.md`: reproducible repeated-benchmark commands and accurate scenario documentation.

### `gsx`

- `docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md`: environment, commands, baseline tables, profile attribution, escape analysis, ranked candidates, and rejected targets.
- Existing `*_bench_test.go` files: read and run during baseline; modify only in the follow-up optimisation plan when a measured candidate needs an exact regression benchmark.

---

### Task 1: Restore `gsx-bench` on the current authored and generated ABI

**Files:**
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsxr/escape.gsx`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsxr/nums.gsx`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsxr/page.gsx`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsxr/scenarios.gsx`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/tw/buttons.gsx`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/bench_test.go`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/document_test.go`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/scenarios_test.go`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/nums_test.go`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/tw_test.go`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/conc_test.go`
- Regenerate: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsxr/*.x.go`
- Regenerate: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/tw/*.x.go`

**Interfaces:**
- Consumes: current component syntax `component Name(arg T, attrs gsx.Attrs)` and current generated verbatim Go signatures.
- Produces: compilable benchmark components called as `Render(person)`, `List(rows, nil)`, `Table(rows, nil)`, `Piped(rows, nil)`, `Page(rows, nil)`, `Comments(comments, nil)`, `Stats(rows, nil)`, and `Buttons(btnLabels, btnOver, nil)`.

- [ ] **Step 1: Reproduce and preserve the failing baseline**

```bash
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-audit-cache go test -count=1 ./...
```

Expected: FAIL because committed generated files call the obsolete two-argument `Writer.Spread` form.

- [ ] **Step 2: Make every forwarded bag explicit in authored GSX**

Add the runtime import to files that do not already import it, using an import block where the file also imports benchmark data:

```go
import (
	"github.com/gsxhq/gsx"
	"github.com/gsxhq/gsx-bench/data"
)
```

Change only components that use `{ attrs... }`:

```go
component Comments(items []data.Comment, attrs gsx.Attrs)
component Stats(rows []data.Row, attrs gsx.Attrs)
component Page(rows []data.Row, attrs gsx.Attrs)
component UserCard(r data.Row, attrs gsx.Attrs)
component List(rows []data.Row, attrs gsx.Attrs)
component Card(r data.Row, attrs gsx.Attrs)
component Table(rows []data.Row, attrs gsx.Attrs)
component Piped(rows []data.Row, attrs gsx.Attrs)
component Button(label string, attrs gsx.Attrs)
component Buttons(labels []string, override string, attrs gsx.Attrs)
```

Do not add an attrs parameter to `Render`; it has no spread.

- [ ] **Step 3: Update Go benchmark callers to the verbatim signatures**

Replace generated props construction with direct calls:

```go
gsxRender(gsxr.Render(person))
gsxRender(gsxr.List(rows, nil))
gsxRender(gsxr.Table(rows, nil))
gsxRender(gsxr.Piped(rows, nil))
gsxRender(gsxr.Page(rows, nil))
gsxRender(gsxr.Comments(comments, nil))
gsxRender(gsxr.Stats(rows, nil))
gsxRender(tw.Buttons(btnLabels, btnOver, nil))
```

Update `BenchmarkPageGSXParallel` to receive `gsxr.Page(rows, nil)`.

- [ ] **Step 4: Regenerate GSX output from the paired core worktree**

```bash
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-audit-cache make generate-gsx
```

Expected: PASS; generated functions use direct parameters and the current seven-argument `Writer.Spread` call.

- [ ] **Step 5: Verify semantics and the complete package graph**

```bash
GOCACHE=/tmp/gsx-runtime-audit-cache make test
GOCACHE=/tmp/gsx-runtime-audit-cache go test -count=1 ./...
gopls check -severity=hint bench_test.go document_test.go scenarios_test.go nums_test.go tw_test.go conc_test.go
git diff --check
```

Expected: all tests PASS, no gopls diagnostics, and no whitespace errors.

- [ ] **Step 6: Commit the benchmark ABI restoration**

```bash
git add gsxr/*.gsx gsxr/*.x.go tw/*.gsx tw/*.x.go bench_test.go document_test.go scenarios_test.go nums_test.go tw_test.go conc_test.go
git commit -m "chore: migrate benchmarks to current gsx ABI"
```

### Task 2: Add representative forwarding and folded-attribute workloads

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsxr/attrs.gsx`
- Create: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/templr/attrs.templ`
- Create: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/attrs_test.go`
- Regenerate: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsxr/attrs.x.go`
- Regenerate: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/templr/attrs_templ.go`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/Makefile`
- Modify: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/README.md`

**Interfaces:**
- Consumes: `data.Row`, `gsx.Attrs`, `templ.OrderedAttributes`, existing `pooled`, `discard`, `renderString`, and `canonical` helpers.
- Produces: `gsxr.ForwardedLinks(rows []data.Row, linkAttrs gsx.Attrs) gsx.Node`, `gsxr.FoldedTabs(rows []data.Row, attrs gsx.Attrs) gsx.Node`, templ `ForwardedLinks`, `BenchmarkForwardedAttrs*`, and `BenchmarkFoldedAttrsGSX*`.

- [ ] **Step 1: Write failing external behavior and benchmark tests**

Create `attrs_test.go`:

```go
package bench

import (
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/gsxhq/gsx"
	"github.com/gsxhq/gsx-bench/gsxr"
	"github.com/gsxhq/gsx-bench/templr"
)

var forwardedAttrs = gsx.Attrs{
	{Key: "id", Value: "member-link"},
	{Key: "class", Value: "wide"},
	{Key: "style", Value: "margin: 0"},
	{Key: "href", Value: "/members"},
	{Key: "disabled", Value: true},
	{Key: "data-kind", Value: "member"},
}

func asTemplAttrs(attrs gsx.Attrs) templ.OrderedAttributes {
	out := make(templ.OrderedAttributes, 0, len(attrs))
	for _, attr := range attrs {
		out = append(out, templ.KV[string, any](attr.Key, attr.Value))
	}
	return out
}

func forwardedGSX() render {
	return gsxRender(gsxr.ForwardedLinks(rows, forwardedAttrs))
}

func forwardedTempl() render {
	return templRender(templr.ForwardedLinks(rows, asTemplAttrs(forwardedAttrs)))
}

func TestForwardedAttrsAgree(t *testing.T) {
	g, tp := renderString(forwardedGSX()), renderString(forwardedTempl())
	if canonical(g) != canonical(tp) {
		t.Fatalf("forwarded attrs differ:\n gsx:   %s\n templ: %s", g, tp)
	}
}

func TestFoldedAttrsOutput(t *testing.T) {
	got := renderString(gsxRender(gsxr.FoldedTabs(rows[:2], forwardedAttrs)))
	for _, want := range []string{`class="tab wide active"`, `class="tab wide inactive"`, `href="/members"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("folded output missing %q: %s", want, got)
		}
	}
	if strings.Count(got, `href="/members"`) != 2 {
		t.Fatalf("folded output should render one href per tab: %s", got)
	}
}

func BenchmarkForwardedAttrsGSXPooled(b *testing.B)   { pooled(b, forwardedGSX()) }
func BenchmarkForwardedAttrsTemplPooled(b *testing.B) { pooled(b, forwardedTempl()) }
func BenchmarkForwardedAttrsGSXDiscard(b *testing.B)  { discard(b, forwardedGSX()) }

func BenchmarkFoldedAttrsGSXPooled(b *testing.B) {
	pooled(b, gsxRender(gsxr.FoldedTabs(rows, forwardedAttrs)))
}

func BenchmarkFoldedAttrsGSXDiscard(b *testing.B) {
	discard(b, gsxRender(gsxr.FoldedTabs(rows, forwardedAttrs)))
}
```

- [ ] **Step 2: Run the tests to prove the workloads do not exist yet**

```bash
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run 'Test(ForwardedAttrsAgree|FoldedAttrsOutput)' .
```

Expected: FAIL to compile because `gsxr.ForwardedLinks`, `gsxr.FoldedTabs`, and `templr.ForwardedLinks` do not exist.

- [ ] **Step 3: Add the authored GSX workloads**

Create `gsxr/attrs.gsx`:

```go
package gsxr

import (
	"github.com/gsxhq/gsx"
	"github.com/gsxhq/gsx-bench/data"
)

component ForwardedLink(row data.Row, attrs gsx.Attrs) {
	<a { attrs... }>{ row.Name }</a>
}

component ForwardedLinks(rows []data.Row, linkAttrs gsx.Attrs) {
	<nav>{ for _, row := range rows {
		<ForwardedLink row={row} { linkAttrs... }/>
	} }</nav>
}

component FoldedTabs(rows []data.Row, attrs gsx.Attrs) {
	<nav>{ for _, row := range rows {
		<a class="tab" { attrs... } { if row.Active { class="active" } else { class="inactive" } }>{ row.Name }</a>
	} }</nav>
}
```

- [ ] **Step 4: Add the templ counterpart for the shared forwarding workload**

Create `templr/attrs.templ`:

```go
package templr

import (
	"github.com/a-h/templ"
	"github.com/gsxhq/gsx-bench/data"
)

templ ForwardedLink(row data.Row, attrs templ.OrderedAttributes) {
	<a { attrs... }>{ row.Name }</a>
}

templ ForwardedLinks(rows []data.Row, attrs templ.OrderedAttributes) {
	<nav>
		for _, row := range rows {
			@ForwardedLink(row, attrs)
		}
	</nav>
}
```

- [ ] **Step 5: Regenerate both engines and run the focused tests**

```bash
GOCACHE=/tmp/gsx-runtime-audit-cache make generate
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run 'Test(ForwardedAttrsAgree|FoldedAttrsOutput)' -count=1 -v .
```

Expected: generation succeeds and both tests PASS. The folded class order is
`tab wide active` / `tab wide inactive`, matching the source-order aggregation
pinned by `internal/corpus/testdata/cases/condmerge/all_forms.txtar`.

- [ ] **Step 6: Make repeated benchmark collection reproducible**

In `Makefile`, add:

```make
BENCH ?= .
COUNT ?= 10

bench: ## run repeated render benchmarks suitable for benchstat
	go test -bench '$(BENCH)' -benchmem -run '^$$' -count=$(COUNT) .
```

Document these commands in `README.md`:

```bash
make bench
make bench BENCH='Pooled|Parallel' COUNT=10
make bench BENCH='ForwardedAttrs|FoldedAttrs' COUNT=10
```

Add `ForwardedAttrs` and `FoldedAttrs` rows to the scenario table. Identify the first as shared with templ and the second as GSX-only. Remove performance claims whose committed numbers predate the regenerated current baseline; keep methodology and historical explanation only when current results support it.

- [ ] **Step 7: Verify and commit the expanded benchmark suite**

```bash
GOCACHE=/tmp/gsx-runtime-audit-cache make test
GOCACHE=/tmp/gsx-runtime-audit-cache go test -count=1 ./...
GOCACHE=/tmp/gsx-runtime-audit-cache go test -race -run 'Test(ForwardedAttrsAgree|FoldedAttrsOutput|ScenariosAgree|StatsAgree|ButtonsAgree)' .
gopls check -severity=hint attrs_test.go
git diff --check
```

Expected: all commands PASS with no diagnostics.

```bash
git add gsxr/attrs.gsx gsxr/attrs.x.go templr/attrs.templ templr/attrs_templ.go attrs_test.go Makefile README.md
git commit -m "bench: cover current attribute render paths"
```

### Task 3: Capture the baseline and rank actual hotspots

**Files:**
- Create: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx/docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md`
- Create outside repositories: `/tmp/gsx-runtime-audit/baseline.txt`
- Create outside repositories: `/tmp/gsx-runtime-audit/*.cpu`, `/tmp/gsx-runtime-audit/*.mem`, `/tmp/gsx-runtime-audit/*.top`, `/tmp/gsx-runtime-audit/escape.txt`

**Interfaces:**
- Consumes: restored benchmark suite and existing core microbenchmarks.
- Produces: ranked candidate list naming exact functions/generated shapes, measured baseline tables, and an explicit keep-investigating/reject decision for each candidate family.

- [ ] **Step 1: Record the environment and repository revisions**

Copy the exact output into the audit note's `Environment` section:

```bash
go version
uname -m
sysctl -n machdep.cpu.brand_string
git -C /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx rev-parse HEAD
git -C /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench rev-parse HEAD
```

- [ ] **Step 2: Collect ten-sample external baselines**

```bash
mkdir -p /tmp/gsx-runtime-audit
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench . -benchmem -count=10 . | tee /tmp/gsx-runtime-audit/baseline.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d /tmp/gsx-runtime-audit/baseline.txt
```

Expected: all benchmarks complete; benchstat reports distributions for time, bytes, and allocations.

- [ ] **Step 3: Collect focused core microbenchmarks**

```bash
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench 'Benchmark(Class|Style|Root|Forwarding|Cond|Without)' -benchmem -count=10 .
```

Expected: stable results covering empty and non-empty root machinery, class/style merge, spread, and folded conditional paths.

- [ ] **Step 4: Capture CPU and allocation profiles for representative paths**

```bash
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
for name in Page Table ForwardedAttrs FoldedAttrs Comments; do
  GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench "^Benchmark${name}GSXPooled$" -benchtime=5s -cpuprofile "/tmp/gsx-runtime-audit/${name}.cpu" -memprofile "/tmp/gsx-runtime-audit/${name}.mem" -memprofilerate=1 .
  go tool pprof -top -nodecount=30 "/tmp/gsx-runtime-audit/${name}.cpu" > "/tmp/gsx-runtime-audit/${name}.cpu.top"
  go tool pprof -top -alloc_space -nodecount=30 "/tmp/gsx-runtime-audit/${name}.mem" > "/tmp/gsx-runtime-audit/${name}.mem.top"
done
```

Expected: every profile exists and each `.top` report attributes costs to named runtime/generated functions rather than only harness setup.

- [ ] **Step 5: Capture compiler escape decisions and trace hot symbols**

```bash
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -gcflags='all=-m=2' . 2> /tmp/gsx-runtime-audit/escape.txt
rg -n 'escapes to heap|does not escape|Writer|Spread|ClassMerged|StyleMerged|Forwarded|Folded|Table|Page' /tmp/gsx-runtime-audit/escape.txt

cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
gopls references -d attrs.go:326:20
gopls references -d writer.go:187:20
```

Expected: the audit can distinguish per-child closures, bag materialisation, map/slice/string allocations, and stack-only writer work.

- [ ] **Step 6: Write the evidence-backed audit report**

Create `docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md` with completed content under every heading:

```markdown
# Runtime Render Performance Audit

## Environment
## Benchmark Repositories and Commands
## External Baseline
## Core Microbenchmarks
## CPU Profile Attribution
## Allocation and Escape Attribution
## Ranked Candidate 1
## Ranked Candidate 2
## Ranked Candidate 3
## Paths Already Fast or Inherent
## Rejected Designs
## Recommended Optimisation Slices
```

For each ranked candidate, state the exact function/generated call shape, measured share or allocation count, proposed single-variable experiment, deciding benchmark, and correctness tests. Explicitly record why empty bags, escaping, numeric interpolation, pipeline user transforms, or the old struct-node experiment are not targets when the evidence says so.

- [ ] **Step 7: Verify and commit the audit report**

```bash
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
rg -n 'TBD|TODO|FIXME|placeholder' docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md
git diff --check
git status --short
```

Expected: the placeholder scan prints nothing; only the audit note is uncommitted in the GSX worktree.

```bash
git add docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md
git commit -m "docs(perf): record runtime render baseline"
```

### Task 4: Independently challenge the benchmark and hotspot conclusions

**Files:**
- Modify when findings require correction: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx/docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md`
- Modify when a coverage flaw is proven: the smallest relevant file under `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/`
- Create outside repositories only: throwaway probe programs under `/tmp/gsx-runtime-audit/review/`

**Interfaces:**
- Consumes: benchmark restoration commits, full baseline, profiles, and ranked report.
- Produces: an independent adversarial verdict that the suite exercises real generated paths and every recommended optimisation is evidence-backed.

- [ ] **Step 1: Dispatch an independent reviewer with no implementation ownership**

Require the reviewer to:

```text
1. Regenerate both engines and check for drift.
2. Verify every shared scenario renders equivalent output.
3. Inspect generated ForwardedLinks and FoldedTabs code against the claimed runtime paths.
4. Build throwaway probes for at least one small and one large Attrs bag, a child loop, and an erroring writer.
5. Re-run at least three benchmark groups with independent sample counts.
6. Challenge profile attribution, benchmark artifacts, and every large-change recommendation.
7. Report correctness issues, misleading workloads, unsupported conclusions, and missing cases before style concerns.
```

- [ ] **Step 2: Address factual review findings only**

Reproduce each finding first. If valid, add the smallest missing test/workload or correct the report, regenerate outputs if authored sources changed, and rerun affected benchmarks. Do not start an optimisation in this task.

- [ ] **Step 3: Run the baseline completion gate**

In `gsx-bench`:

```bash
GOCACHE=/tmp/gsx-runtime-audit-cache make generate
git diff --exit-code -- gsxr tw templr
GOCACHE=/tmp/gsx-runtime-audit-cache make test
GOCACHE=/tmp/gsx-runtime-audit-cache go test -count=1 ./...
```

In `gsx`:

```bash
GOCACHE=/tmp/gsx-runtime-audit-cache make check
git diff --check
```

Expected: all commands PASS and regeneration produces no drift.

- [ ] **Step 4: Commit review-driven corrections, if any**

Use one focused commit per repository that changed:

```bash
git commit -m "test(perf): harden runtime benchmark coverage"
git commit -m "docs(perf): correct runtime audit findings"
```

- [ ] **Step 5: Write the measured optimisation implementation plan**

From the ranked candidates, create `docs/superpowers/plans/2026-07-21-runtime-render-optimisations.md`. Name the exact runtime/codegen functions, failing benchmarks or corpus tests, single-variable experiments, keep/reject thresholds, verification commands, and per-slice commits. Do not include candidates the audit rejected.
