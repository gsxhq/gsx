# Folded Element Attribute Materialisation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` to execute this plan task by task,
> and `superpowers:verification-before-completion` before every commit or
> completion claim. Run an independent adversarial review before retaining the
> candidate.

**Goal:** Decide whether lowering folded plain-element attributes into one
source-ordered accumulator materially reduces render allocations, bytes, and
time without changing GSX attribute semantics.

**Architecture:** Replace only the `foldElementSpreads` materialisation path.
Codegen will allocate one accumulator with a compile-time capacity floor, then
append literal entries, spreads, and only the selected conditional branch
directly in source order. It will not build intermediate `ConcatAttrs` results
or call `AttrsCond`; the existing `ClassMerged`, `StyleMerged`, and `Spread`
leaf remains unchanged. Dynamic spread lengths may grow the accumulator; no
expression is moved or evaluated twice merely to predict capacity.

**Tech stack:** Go 1.26.1, GSX codegen and txtar corpus, sibling `gsx-bench`,
the committed `scripts/benchcmp.sh` counterbalanced harness, `gopls`, race and
profile tooling, and
`golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d`.

## Global constraints

- Start from clean committed core and benchmark worktrees. Record both base
  commits under `/tmp/gsx-runtime-folded-materialisation` before editing.
- This is a measured codegen experiment. Do not change the runtime API,
  `Writer.Spread`, `Attrs`, `ConcatAttrs`, `AttrsCond`, authored benchmark
  workload, class/style merger, URL sinks, or parser.
- Preserve exact source evaluation order, once-only evaluation, conditional
  branch laziness, nested `if`/`else if` behavior, tuple-error short circuiting,
  renderer application, duplicate order, scalar last-wins, class/style
  aggregation, `RawURL` provenance, nonce handling, and first writer error.
- Capacity is computed only from syntax-known entry counts. A `SpreadAttr`
  contributes zero to the capacity floor; its expression is evaluated once at
  its authored position and appended once. Never call `len(expr)` ahead of the
  authored position or add a purity heuristic.
- `elementFoldStaticCapacity` counts one for each non-spread entry and uses the
  maximum of mutually exclusive conditional branches. It is a capacity hint,
  not a semantic count. The accumulator may grow.
- Keep `composeBag` and `AttrsCond` for component-prop lowering. Only the
  `bagElementFold` caller moves to the statement accumulator.
- Do not hand-edit generated `.x.go` files or txtar golden sections. Change
  authored source/tests and regenerate.
- Store comparison worktrees, raw benchmark output, profiles, and test binaries
  under `/tmp`, never in either repository.
- Candidate 3 remains deferred. Do not prototype or change the component ABI.

## Files

- Create `internal/codegen/fold_accumulator_emit_test.go`.
- Modify `internal/codegen/emit.go`.
- Modify `internal/codegen/spread_fold_diff_test.go` only to add explicit
  once-only and branch-laziness cases if the new focused test cannot reuse its
  harness without another `packages.Load`.
- Create corpus cases only for semantic surfaces not already pinned by the
  `condmerge`, `multispread`, `pipeerr`, `renderers`, `urlattrs`, `nonce`, and
  spread-fold differential suites.
- Regenerate `internal/corpus/testdata`,
  `examples/tailwind-merge/views/card.x.go`, and sibling `gsxr/*.x.go`/`tw/*.x.go`.
- Modify the runtime audit note and `docs/guide/performance.md` only after the
  keep/restore decision.

---

### Task 1: Pin the candidate base and add red codegen-shape coverage

**Interfaces:**

- Produces `elementFoldStaticCapacity([]ast.Attr) int` and the generated-shape
  contract consumed by Task 2.
- Changes no runtime behavior.

- [ ] **Step 1: Record clean bases and toolchain**

```sh
set -eu
core=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
bench=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
test "$(go env GOVERSION)" = go1.26.1
test "$(templ version)" = v0.3.1020
mkdir -p /tmp/gsx-runtime-folded-materialisation
git -C "$core" rev-parse HEAD > /tmp/gsx-runtime-folded-materialisation/core-base
git -C "$bench" rev-parse HEAD > /tmp/gsx-runtime-folded-materialisation/bench-base
```

- [ ] **Step 2: Add the failing generated-shape test**

Create `internal/codegen/fold_accumulator_emit_test.go`:

```go
package codegen

import (
	"strings"
	"testing"
)

func TestGeneratedElementFoldUsesDirectAccumulator(t *testing.T) {
	tmp := tempModule(t, "example.com/foldaccumulator")
	views := makeSubPkg(t, tmp, "views", `package views

import "github.com/gsxhq/gsx"

component Fold(active bool, attrs gsx.Attrs) {
	<a class="tab" { attrs... } { if active { class="active" } else { class="inactive" } }>x</a>
}
`)
	result, err := GenerateDirs(tmp, []string{views}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dirResult := result[views]
	if hasDiagErrors(dirResult.Diags) {
		t.Fatalf("diagnostics: %+v", dirResult.Diags)
	}
	src := generatedFor(t, dirResult, "views.gsx")
	for _, want := range []string{
		`_gsxv0 := make(_gsxrt.Attrs, 0, 2)`,
		`_gsxv0 = append(_gsxv0, _gsxrt.Attr{Key: "class", Value: _gsxrt.RawURL("tab")})`,
		`_gsxv0 = append(_gsxv0, attrs...)`,
		`if active {`,
		`_gsxv0 = append(_gsxv0, _gsxrt.Attr{Key: "class", Value: _gsxrt.RawURL("active")})`,
		`} else {`,
		`_gsxv0 = append(_gsxv0, _gsxrt.Attr{Key: "class", Value: _gsxrt.RawURL("inactive")})`,
		`_gsxv1 := _gsxv0`,
		`_gsxgw.ClassMerged(_gsxrt.DefaultClassMerge, _gsxv1.Class())`,
		`_gsxgw.StyleMerged("", _gsxv1.Style())`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("missing %q:\n%s", want, src)
		}
	}
	for _, forbidden := range []string{"_gsxrt.ConcatAttrs(", "_gsxrt.AttrsCond("} {
		if strings.Contains(src, forbidden) {
			t.Fatalf("fold still contains %q:\n%s", forbidden, src)
		}
	}
}
```

Run:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
gofmt -w internal/codegen/fold_accumulator_emit_test.go
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/codegen -run '^TestGeneratedElementFoldUsesDirectAccumulator$' -count=1
```

Expected: FAIL because generated output still contains `ConcatAttrs` and
`AttrsCond` and does not contain the accumulator statements.

- [ ] **Step 3: Add capacity-unit tests before implementation**

In the same file, add table tests that parse the source through `GenerateDirs`
and inspect the accumulator capacity for these exact shapes:

```go
func TestGeneratedElementFoldCapacityUsesSyntaxKnownMaximum(t *testing.T) {
	tmp := tempModule(t, "example.com/foldcapacity")
	views := makeSubPkg(t, tmp, "views", `package views

import "github.com/gsxhq/gsx"

component Capacity(a, b gsx.Attrs, first, nested bool) {
	<div id="root" { a... } { /* ignored */ } { if first { data-a="a" data-b="b" } else { data-c="c" { if nested { data-d="d" } } } } { b... }></div>
}
`)
	result, err := GenerateDirs(tmp, []string{views}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dirResult := result[views]
	if hasDiagErrors(dirResult.Diags) {
		t.Fatalf("diagnostics: %+v", dirResult.Diags)
	}
	src := generatedFor(t, dirResult, "views.gsx")
	if !strings.Contains(src, `_gsxv0 := make(_gsxrt.Attrs, 0, 3)`) {
		t.Fatalf("capacity must be root 1 + max(branch 2, branch 1+nested 1):\n%s", src)
	}
	if strings.Contains(src, `Key: ""`) {
		t.Fatalf("source comment became an attribute entry:\n%s", src)
	}
}
```

The expected floor is three. Neither spread contributes to it.

---

### Task 2: Implement one source-ordered folded-element accumulator

**Interfaces:**

- `elementFoldStaticCapacity(attrs []ast.Attr) int` returns the syntax-known
  capacity floor.
- `emitElementFoldAccumulator(...) (bagExpr string, used map[string]string,
  ok bool)` emits statements into the enclosing render body and returns the
  accumulator identifier.
- `emitFoldAttrsInto(...) bool` recursively appends one attribute list to that
  identifier and emits conditional control flow directly.

- [ ] **Step 1: Implement the exact capacity rule**

Add beside `foldElementSpreads` in `internal/codegen/emit.go`:

```go
func elementFoldStaticCapacity(attrs []ast.Attr) int {
	total := 0
	for _, a := range attrs {
		switch t := a.(type) {
		case *ast.CondAttr:
			thenN := elementFoldStaticCapacity(t.Then)
			elseN := elementFoldStaticCapacity(t.Else)
			total += max(thenN, elseN)
		case *ast.StaticAttr, *ast.ExprAttr, *ast.BoolAttr, *ast.ClassAttr, *ast.EmbeddedAttr:
			total++
		case *ast.SpreadAttr, *ast.CommentAttr:
			// No syntax-known emitted entry.
		}
	}
	return total
}
```

Run the capacity test. It remains red on missing statement lowering but its
capacity assertion must turn green once Task 2 is complete.

- [ ] **Step 2: Extract one-entry expression lowering from `composeBag`**

Refactor, without semantic changes, the existing `StaticAttr`, `ExprAttr`,
`BoolAttr`, `ClassAttr`, and `EmbeddedAttr` arms into:

```go
type bagEntryLowering struct {
	key   string
	value string
	used  map[string]string
}

func lowerBagEntry(
	b *bytes.Buffer,
	interpTemp *int,
	wrap func(string) string,
	probeWrap bool,
	a ast.Attr,
	rtPkg, tag, mergeExpr string,
	table funcTables,
	resolved map[ast.Node]types.Type,
	imports map[string]bool,
	rt rtImports,
	bag *diag.Bag,
	errReturn string,
	ctx bagContext,
	beforeStatements func(),
) (bagEntryLowering, error)
```

The helper must return quoted key/value Go expressions and any filter imports;
it emits the same tuple, pipeline, renderer, embedded-hole, JS/CSS URL-sink
diagnostics, and hoists as the current arms. `composeBag` must call this helper
with its existing `materializePrior` closure before any renderer, class/style,
embedded-hole, or other lowering that emits a statement. The accumulator caller
passes a no-op because every prior append is already a statement. This callback
is required even for no-error renderers and embedded assemblers whose statements
do not flow through `wrap`. `composeBag` must keep byte-identical output before
the accumulator caller is switched.
`SpreadAttr` and `CondAttr` are rejected by `lowerBagEntry` because their
control flow belongs to the caller.

Add a generated-byte equality test around the refactor for a component
conditional bag containing a renderer-backed expression, composed class/style,
and a hole-bearing embedded text attribute. Capture the generated source before
the refactor, pin it as the expected string in the test, and prove the helper
refactor does not change it before switching the element-fold caller.

Before switching callers, run:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/codegen -run 'Test(SpreadFold|Renderer|Conditional|Pipe)' -count=1
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/corpus -run TestCorpus -count=1
git diff --check
```

Expected: existing generated goldens remain unchanged at this intermediate
refactor point.

- [ ] **Step 3: Emit direct append statements**

Implement `emitElementFoldAccumulator` and `emitFoldAttrsInto` with these exact
rules:

1. Allocate `make(gsx.Attrs, 0, elementFoldStaticCapacity(attrs))` in a fresh
   `_gsxvN` local.
2. Walk attributes exactly once in source order.
3. For a normal emitting entry, call `lowerBagEntry` with `bagElementFold`, a
   no-op `beforeStatements`, and
   `errReturn="return _gsxerr"`, then emit:
   `_gsxvN = append(_gsxvN, _gsxrt.Attr{Key: KEY, Value: VALUE})`.
4. For a spread, lower its pipeline at that position, apply the existing
   `Attrs` conversion when required by its resolved method set, and emit one
   `_gsxvN = append(_gsxvN, EXPR...)`. Never evaluate `EXPR` in a capacity
   calculation.
5. For a conditional, emit `if COND {`, recursively append `Then`, emit
   `} else {` only when `Else` is non-empty, recursively append `Else`, and
   close the block. Nested conditionals recurse. Branch values and pipelines
   are therefore evaluated only inside the selected branch.
6. Preserve `//line` placement already used for conditional attributes and
   spread/value diagnostics.
7. For `CommentAttr`, emit nothing. For `OrderedAttrsAttr` or any unknown kind,
   preserve the current positioned `unsupported-component-attr` diagnostic.
8. Merge every returned import map into the caller's import set.

Replace `foldElementSpreads`'s `composeBag(..., bagElementFold)` call with the
new accumulator call. Keep its URL-sink literal rejection, synthetic single
spread leaf, nonce behavior, children, and error reporting unchanged. Remove
`bagElementFold` handling from `composeBag` only after `rg` proves the new
accumulator is its sole former caller; retain `bagComponentCond` unchanged.

Run:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
gofmt -w internal/codegen/emit.go internal/codegen/fold_accumulator_emit_test.go
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/codegen -run 'Test(GeneratedElementFold|SpreadFold)' -count=1
gopls check -severity=hint internal/codegen/emit.go internal/codegen/fold_accumulator_emit_test.go
```

- [ ] **Step 4: Prove ordering, laziness, errors, sinks, and nonce behavior**

Extend the existing single-load spread-fold matrix rather than adding one
`packages.Load` per case. Add cases that record side effects into a shared log
for: entry before spread before condition before trailing spread; true/false
and nested branch selection; an untaken nil dereference; a taken `(T, error)`
value; and a later expression that must not run after that error. Assert the
exact log and exact error. Keep the existing byte-equivalence matrix for
duplicates, class/style, `RawURL`, URL sanitization, and conditional branches.

Run the focused semantic set:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/codegen -run 'Test(GeneratedElementFold|SpreadFold)' -count=1
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/corpus -run 'TestCorpus' -count=1
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test -run 'Test(URLSanitize|URLSanitizeImage|SrcsetSanitize|Spread)' -count=1 .
```

---

### Task 3: Regenerate and stage the exact candidate

- [ ] **Step 1: Regenerate core artifacts to a fixed point**

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/corpus -run TestCorpus -update
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/corpus -run TestCorpus -count=1
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go run ./cmd/gsx -C examples/tailwind-merge generate ./views
git add internal/codegen internal/corpus examples/tailwind-merge/views/card.x.go
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test ./internal/corpus -run TestCorpus -update
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go run ./cmd/gsx -C examples/tailwind-merge generate ./views
git diff --exit-code -- internal/corpus examples/tailwind-merge/views/card.x.go
test -z "$(git diff --name-only)"
test -z "$(git ls-files --others --exclude-standard)"
```

- [ ] **Step 2: Regenerate and stage the sibling**

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache make generate
git diff --exit-code -- templr
git add gsxr tw
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache make generate
git diff --exit-code -- gsxr tw templr
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache make test
test -z "$(git diff --name-only)"
test -z "$(git ls-files --others --exclude-standard)"
```

The generated `FoldedTabs` shape must contain the direct accumulator and no
`ConcatAttrs`/`AttrsCond`. Unaffected no-fold generated functions must be
byte-identical to the saved base. Prove that mechanically:

```sh
set -eu
bench_base=$(cat /tmp/gsx-runtime-folded-materialisation/bench-base)
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
git show "${bench_base}:gsxr/attrs.x.go" > /tmp/gsx-runtime-folded-materialisation/attrs.before.x.go
sed -n '/^func ForwardedLink/,/^}/p' /tmp/gsx-runtime-folded-materialisation/attrs.before.x.go \
  > /tmp/gsx-runtime-folded-materialisation/forwarded.before.txt
sed -n '/^func ForwardedLink/,/^}/p' gsxr/attrs.x.go \
  > /tmp/gsx-runtime-folded-materialisation/forwarded.after.txt
cmp /tmp/gsx-runtime-folded-materialisation/forwarded.before.txt \
  /tmp/gsx-runtime-folded-materialisation/forwarded.after.txt
```

---

### Task 4: Measure, review, and retain or restore

- [ ] **Step 1: Create a detached sibling before pair**

```sh
set -eu
core_base=$(cat /tmp/gsx-runtime-folded-materialisation/core-base)
bench_base=$(cat /tmp/gsx-runtime-folded-materialisation/bench-base)
before_root=$(mktemp -d /tmp/gsx-runtime-folded-before.XXXXXX)
rmdir "$before_root"
git -C /Users/jackieli/personal/gsxhq/gsx worktree add --detach "$before_root/gsx" "$core_base"
git -C /Users/jackieli/personal/gsxhq/gsx-bench worktree add --detach "$before_root/gsx-bench" "$bench_base"
before_root=$(CDPATH= cd -- "$before_root" && pwd -P)
printf '%s\n' "$before_root" > /tmp/gsx-runtime-folded-materialisation/before-root
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache make -C "$before_root/gsx-bench" generate
git -C "$before_root/gsx-bench" diff --exit-code -- gsxr tw templr
```

- [ ] **Step 2: Run counterbalanced focused and full screens**

```sh
set -eu
before_root=$(cat /tmp/gsx-runtime-folded-materialisation/before-root)
result_root=$(mktemp -d /tmp/gsx-runtime-folded-results.XXXXXX)
result_root=$(CDPATH= cd -- "$result_root" && pwd -P)
printf '%s\n' "$result_root" > /tmp/gsx-runtime-folded-materialisation/results-root
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
scripts/benchcmp.sh "$before_root/gsx-bench" "$PWD" \
  '^BenchmarkFoldedAttrsGSX(Pooled|Discard)$' "$result_root/external" . \
  github.com/gsxhq/gsx
scripts/benchcmp.sh "$before_root/gsx-bench" "$PWD" \
  '^Benchmark.*GSX(Pooled|Discard|Builder|Parallel)$' "$result_root/full" . \
  github.com/gsxhq/gsx
```

The harness must record ten odd `AB`/even `BA` process pairs, both repositories'
commits/statuses/staged-diff hashes, and the independently resolved core module.

- [ ] **Step 3: Apply the no-waiver gate**

Retain only when every condition holds:

1. FoldedAttrs pooled and discard each improve by at least **7%** in time with
   `p < 0.05`.
2. Both destinations reduce `B/op` by at least **20%** and `allocs/op` by at
   least **12%**, each with `p < 0.05`.
3. No non-parallel full-screen benchmark regresses by 7% or more with
   `p < 0.05`; no parallel benchmark regresses by 12% or more with `p < 0.05`.
4. Generated outputs, source-order/laziness/error tests, corpus render goldens,
   URL/security tests, race test, `make ci`, and `make lint` all pass.

If any condition fails, restore both trees from the saved bases with
this exact block and record the rejection:

```sh
set -eu
core=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
bench=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
core_base=$(cat /tmp/gsx-runtime-folded-materialisation/core-base)
bench_base=$(cat /tmp/gsx-runtime-folded-materialisation/bench-base)
git -C "$core" restore --source "$core_base" --staged --worktree -- .
git -C "$bench" restore --source "$bench_base" --staged --worktree -- .
test -z "$(git -C "$core" ls-files --others --exclude-standard)"
test -z "$(git -C "$bench" ls-files --others --exclude-standard)"
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
```

Do not average, round across a boundary, or waive a failed end-to-end gate
because an explanatory local microbenchmark passes.

- [ ] **Step 4: Review and conditionally commit implementation**

For a passing candidate, run independent specification and code-quality
reviews. The adversary must build throwaway programs for evaluation order,
untaken branches, tuple errors, duplicates, URL sinks, nonce, and a dynamic
spread large enough to grow the accumulator. It must inspect escape/allocation
profiles to distinguish final-bag allocation from class/style work.

Then run:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache go test -race -count=1 .
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache make ci
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache make lint
git diff --check --cached
gopls check -severity=hint internal/codegen/emit.go internal/codegen/fold_accumulator_emit_test.go internal/codegen/spread_fold_diff_test.go
git commit -m 'perf(gen): assemble folded element attrs once'

cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache make generate
git diff --exit-code -- gsxr tw templr
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache go test -race -count=1 ./...
git commit -m 'chore: regenerate folded attribute benchmarks'
```

The commits are conditional. A rejected candidate produces no implementation
or generated-output commit.

---

### Task 5: Record the outcome and reprofile before any next optimisation

- [ ] **Step 1: Update the audit and performance guide from fresh evidence**

Record exact bases/outcome commits, staged-diff hashes, machine, Go version,
`GOMAXPROCS`, schedule, all focused/full medians/deltas/p-values, byte and
allocation deltas, every gate, and raw paths. Produce a fresh ten-sample full
suite after keep/restore and update `docs/guide/performance.md` from it:

```sh
set -eu
result_root=$(cat /tmp/gsx-runtime-folded-materialisation/results-root)
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOMAXPROCS=32 GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache \
  go test -run '^$' -bench . -benchmem -count=10 . \
  > "$result_root/final-full-suite.txt"
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d \
  "$result_root/final-full-suite.txt" > "$result_root/final-full-suite.benchstat.txt"
cat "$result_root/final-full-suite.benchstat.txt"
```

- [ ] **Step 2: Collect separate CPU-only and memory-only FoldedAttrs profiles**

Use `-cpuprofile` and `-memprofile -memprofilerate=1` in separate 5-second runs,
with every artifact and test binary under a fresh `/tmp` directory. Report flat,
non-overlapping allocation frames; do not add cumulative parents to children.

```sh
set -eu
profile_dir=$(mktemp -d /tmp/gsx-runtime-post-folded-profiles.XXXXXX)
profile_dir=$(CDPATH= cd -- "$profile_dir" && pwd -P)
printf '%s\n' "$profile_dir" > /tmp/gsx-runtime-folded-materialisation/profile-dir
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache go test -run '^$' \
  -bench '^BenchmarkFoldedAttrsGSXPooled$' -benchtime=5s \
  -cpuprofile "$profile_dir/FoldedAttrs.cpu" -outputdir "$profile_dir" .
if [ -e gsx-bench.test ]; then mv gsx-bench.test "$profile_dir/FoldedAttrs.cpu.test"; fi
GOCACHE=/tmp/gsx-runtime-folded-materialisation-cache go test -run '^$' \
  -bench '^BenchmarkFoldedAttrsGSXPooled$' -benchtime=5s \
  -memprofile "$profile_dir/FoldedAttrs.mem" -memprofilerate=1 \
  -outputdir "$profile_dir" .
if [ -e gsx-bench.test ]; then mv gsx-bench.test "$profile_dir/FoldedAttrs.mem.test"; fi
go tool pprof -top -nodecount=40 "$profile_dir/FoldedAttrs.cpu" \
  > "$profile_dir/FoldedAttrs.cpu.top"
go tool pprof -top -alloc_objects -nodecount=40 "$profile_dir/FoldedAttrs.mem" \
  > "$profile_dir/FoldedAttrs.objects.top"
go tool pprof -top -alloc_space -nodecount=40 "$profile_dir/FoldedAttrs.mem" \
  > "$profile_dir/FoldedAttrs.space.top"
test ! -e gsx-bench.test
```

- [ ] **Step 3: Keep Candidate 3 deferred and clean comparison worktrees**

Candidate 3 requires a separate measured plan after this candidate's retained
or rejected result. Do not create it in this task. Remove detached comparison
worktrees and commit the outcome documentation:

```sh
set -eu
core=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
bench=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
cd "$core"
git add docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md \
  docs/guide/performance.md
git diff --check --cached
git commit -m 'docs(perf): record folded materialisation decision'
before_root=$(cat /tmp/gsx-runtime-folded-materialisation/before-root)
git -C /Users/jackieli/personal/gsxhq/gsx worktree remove --force "$before_root/gsx"
git -C /Users/jackieli/personal/gsxhq/gsx-bench worktree remove --force "$before_root/gsx-bench"
rmdir "$before_root"
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
```

Do not push, open a pull request, merge, release, or inspect `one-learning`.
