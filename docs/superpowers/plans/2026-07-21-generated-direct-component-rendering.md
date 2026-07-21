# Generated Direct Component Rendering Implementation Plan

> **Implementer:** Use `superpowers:test-driven-development` for Tasks 1-5,
> `superpowers:subagent-driven-development` for independent task execution and
> review, and `superpowers:verification-before-completion` before each commit and
> the final decision.

**Goal:** Measure and either retain or fully revert direct rendering for proven
same-package plain-function GSX component calls.

**Architecture:** Keep every public component factory returning `gsx.Node`.
For an eligible local call, generate one private body helper, call it with the
current `*gsx.Writer`, and apply its return with the unconditional
`Writer.NodeResult` operation. Imported, method, package-variable, plain-Go,
and dynamic targets continue through `Writer.Node`.

**Repositories:**

- Core: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx`
- Bench: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench`

Do not use `/Users/jackieli/work/one-learning`; it is not a GSX consumer.

## Global Constraints

- Start from a clean core documentation commit containing this spec and plan,
  and clean bench commit `8ca640ab` (or its baseline-fixture descendant created
  by Task 1).
- Pin Go to 1.26.1. Record `go version`, `go env`, CPU, `GOMAXPROCS`, commits,
  staged-diff hashes, and effective dependency directories with every result.
- No heuristic target recognition. Eligibility comes only from exact component
  declaration provenance plus the existing positional call plan.
- Preserve argument evaluation, child-body skipping, raw return values, write
  attempts, escaping, source maps, variants, and generated fixed points.
- Never hand-edit `.x.go` or corpus goldens.
- Do not add another `Node` representation or a compatibility adapter.
- Raw measurements, profiles, and throwaway programs stay under `/tmp`.
- Commit each green task separately. Do not publish, merge, or release in this
  plan.

## Files

Core production files expected to change:

- `writer.go`
- `internal/codegen/component_target_skeleton.go`
- `internal/codegen/component_target_provenance.go`
- `internal/codegen/component_target.go`
- `internal/codegen/component_positional_plan.go`
- `internal/codegen/component_positional_emit.go`
- `internal/codegen/emit.go`
- a focused new allocator/eligibility file under `internal/codegen/`

Core tests and generated truth expected to change:

- `writer_test.go`
- focused new `internal/codegen/direct_component*_test.go` files
- `internal/codegen/declnames_test.go` if shared declaration collection changes
- `internal/corpus/testdata/cases/components/direct_*.txtar`
- `internal/corpus/testdata/coverage.golden`
- generated examples owned by `make examples` and the tailwind example command

Bench baseline/candidate files expected to change:

- `scenarios_test.go`
- `gsxr/fallbacks.gsx` and generated `gsxr/fallbacks.x.go`
- a focused fallback output/generated-shape test
- generated `gsxr/*.x.go` affected by the candidate

## Task 1: Commit stable benchmark surfaces before saving the base

This task is benchmark infrastructure, not part of the candidate. It remains in
both before and after trees and remains committed even if the candidate is
rejected.

### 1.1 Verify the documented starting point

```bash
core=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
bench=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench

test "$(go version | awk '{print $3}')" = go1.26.1
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
git -C "$core" cat-file -e HEAD:docs/superpowers/specs/2026-07-21-generated-direct-component-rendering-design.md
git -C "$core" cat-file -e HEAD:docs/superpowers/plans/2026-07-21-generated-direct-component-rendering.md
git -C "$core" show --stat --oneline HEAD
```

Do not proceed from untracked copies of the design or plan. The documentation
commit is the core pre-candidate base.

### 1.2 Add the missing Table destination and unaffected fallbacks

In `scenarios_test.go`, construct each node outside the benchmark loop, exactly
like the existing pooled Table benchmark, and add:

```go
func BenchmarkTableGSXDiscard(b *testing.B) {
	discard(b, gsxRender(gsxr.Table(rows, nil)))
}

func BenchmarkImportedBoundaryGSXPooled(b *testing.B)  { /* pooled */ }
func BenchmarkImportedBoundaryGSXDiscard(b *testing.B) { /* discard */ }
func BenchmarkMethodBoundaryGSXPooled(b *testing.B)    { /* pooled */ }
func BenchmarkMethodBoundaryGSXDiscard(b *testing.B)   { /* discard */ }
func BenchmarkDynamicBoundaryGSXPooled(b *testing.B)   { /* pooled */ }
func BenchmarkDynamicBoundaryGSXDiscard(b *testing.B)  { /* discard */ }
```

Add `gsxr/fallbacks.gsx` with three stable workloads over the existing twenty
rows:

- `ImportedBoundary` loops over `tw.Button`, an imported generated component;
- `MethodBoundary` loops over a same-package method component on an explicit
  `MethodCards` value parameter;
- `DynamicBoundary` loops over a
  `func(data.Row) gsx.Node` parameter and interpolates its returned node.

Give the method and dynamic leaf bodies the same small deterministic Card
markup. Do not route any of these through a new wrapper that turns the target
back into a statically recognizable plain-function tag.

Add a test which:

1. renders each fallback workload and compares it with a checked-in explicit
   expected string;
2. reads `gsxr/fallbacks.x.go` and requires `Writer.Node` at imported, method,
   and dynamic boundaries;
3. rejects `_gsxrender` or `NodeResult` in that baseline generated file.

### 1.3 Generate, test, and commit the prerequisite

```bash
cd "$bench"
make generate-gsx
make test
go test ./... -count=1
go test -run '^$' \
  -bench '^Benchmark(Table|ImportedBoundary|MethodBoundary|DynamicBoundary)GSX(Pooled|Discard)$' \
  -benchmem -count=3 .
git diff --check
git add scenarios_test.go gsxr/fallbacks.gsx gsxr/fallbacks.x.go '*fallback*test.go'
git commit -m 'test(perf): add direct-rendering regression surfaces'
```

If the test filename differs, stage it by exact path rather than using a broad
`git add`. Verify the committed set with `git show --stat --oneline HEAD`.

Save bases only after this commit:

```bash
state=/tmp/gsx-runtime-direct-component
mkdir -p "$state"
git -C "$core" rev-parse HEAD > "$state/core-base"
git -C "$bench" rev-parse HEAD > "$state/bench-base"
git -C "$core" status --porcelain=v1 > "$state/core-base-status"
git -C "$bench" status --porcelain=v1 > "$state/bench-base-status"
test ! -s "$state/core-base-status"
test ! -s "$state/bench-base-status"
```

## Task 2: Add the exact runtime node-result operation

**Files:** `writer.go`, `writer_test.go`

### 2.1 Write failing tests first

Add `TestWriterNodeResultAssignment` covering this exact table:

| existing `gw.Err()` | supplied result | final `gw.Err()` |
| --- | --- | --- |
| nil | sentinel A | sentinel A |
| sentinel A | sentinel B | sentinel B |
| sentinel A | nil | nil |
| destination write error | sentinel A | sentinel A |
| destination write error | nil | nil |

The last two rows prevent an accidental first-error-only implementation.

```bash
cd "$core"
GOCACHE=/tmp/gsx-runtime-direct-component-cache \
  go test . -run '^TestWriterNodeResultAssignment$' -count=1
```

Expected red state: `(*Writer).NodeResult` is undefined.

### 2.2 Implement only the exact assignment

First update the exported `Writer` and `Err` comments so they remain true. They
must say that ordinary write helpers retain the first write error and no-op
while it is present. `Node` invokes and applies a child result only while the
parent state is clear. A generated direct helper instead uses the shared writer;
after it executes, generated-code `NodeResult` applies the helper return and may
replace or clear state created during that helper. The helper entry guard keeps
state that predated the call unchanged. `Err` reports the current render/write
state; it is not documented as an immutable first error.

Use comments with this contract:

```go
// Writer streams HTML to an underlying io.Writer. Ordinary write helpers
// retain the first error and no-op while it is set. Node applies a child result
// only when the parent state is clear. After a generated direct child uses this
// Writer, generated code uses NodeResult to apply the child's return, which may
// replace or clear state created during that child. Read current state via Err.
type Writer struct { /* existing fields */ }

// Err returns the current render or write error, or nil.
func (gw *Writer) Err() error { return gw.err }
```

Then add beside `Writer.Node`:

```go
// NodeResult records the return from a directly rendered generated child.
// Generated code calls it after the child's helper has used this Writer.
func (gw *Writer) NodeResult(err error) {
	gw.err = err
}
```

Do not guard on `nil` inside `NodeResult` and do not call it `Fail`. The method
must replace or clear state produced during a fresh helper execution. The
generated helper guard returns any error that predated the call unchanged, so
the subsequent assignment restores that same parent error rather than clearing
or replacing it.

### 2.3 Verify and commit

```bash
gofmt -w writer.go writer_test.go
gopls check -severity=hint writer.go writer_test.go
GOCACHE=/tmp/gsx-runtime-direct-component-cache go test . -count=1
git diff --check
git add writer.go writer_test.go
git commit -m 'feat(runtime): apply direct child render results'
```

## Task 3: Prove direct eligibility and allocate names exactly

**Files:** `internal/codegen/component_target_skeleton.go`,
`component_target_provenance.go`, `component_target.go`,
`component_positional_plan.go`, a new focused implementation file, and tests.

### 3.1 Pin the declaration matrix before implementation

Write table-driven tests whose fixtures include:

- local plain-function `Child` called from local `Parent`: eligible;
- generic `Child[T any]`: eligible with the exact type-parameter declaration and
  wrapper-forwarding names;
- grouped `Child[T, U any]`: eligible;
- constraint-only named type parameter: eligible and explicitly forwarded;
- variadic attrs and ordinary variadic parameters: eligible with `...` retained;
- unnamed/blank value parameter: fallback;
- blank type parameter: fallback;
- type parameter named `ctx`: fallback;
- non-colliding reserved-prefix type parameter `_gsxT`: conservative fallback;
- exact required-alias type parameter `_gsxrt` (and `_gsxctx`/`_gsxio`): pin or
  add the existing-style positioned reserved-declaration diagnostic, never a
  successful fallback;
- imported function, method component, bound method, package variable, plain Go
  function returning `gsx.Node`, and dynamic node: fallback.

Assert eligibility on declaration provenance/site plans, not only generated
text. Run the focused tests and record the expected red assertions.

### 3.2 Pin deterministic package-wide naming

Write allocator tests for:

- two GSX files calling one target;
- `_gsxrenderChild` occupied in an authored GSX Go chunk;
- `_gsxrenderChild` occupied in a handwritten `.go` file;
- `_gsxrenderChild` occupied in a handwritten or orphaned `.x.go` file;
- `_gsxrenderChild` occupied in a same-package `_test.go` file;
- the same spelling in external `package p_test`, which must not occupy it;
- the spelling in the exact generated output paired with the active `.gsx`,
  which must be ignored so regeneration does not suffix its own helper;
- mutually exclusive build variants sharing one logical component key;
- repeated generation producing byte-identical helper names.

Do not reuse `packageDeclNames` unchanged: its disk walk deliberately skips all
`_test.go` files. Add a helper-name declaration collector that receives the
exact owned output paths paired with the active GSX inputs and excludes only
those paths. Parse every other sibling `.go` file, including handwritten and
orphaned `.x.go`, retain files whose parsed package clause exactly matches the
generated package (including same-package tests), exclude external test
packages, and scan all build variants. Surface parse errors through the
generator's normal diagnostic/error path; do not silently guess from text or
exclude every `.x.go` by suffix.

The collector must consume the `Module`'s authoritative effective source view,
not perform an independent live-disk read. In normal mode that view is the
saved manifest plus exact Go/GSX overrides and captured present/absent file
states; its disk inventory includes inactive build variants and `_test.go`
siblings. In `SourceOnly` mode the complete source is the bundle/override view,
so helper allocation must not inspect host Go files. Extend the shared
`sourceview.Manifest`/cache projection so every non-owned sibling Go path that
can affect helper naming participates in the persistent key. Preserve exact
paired-output exclusion in both generation and cache identity.

Back this with a generated temp-package test: place a non-owned
`orphan.x.go` declaring `_gsxrenderChild`, generate the package, require the
deterministic suffixed helper, and run `go test`. Separately regenerate a package
whose exact paired output already contains the helper and require byte-identical
output rather than a self-induced suffix.

Also pin that host-only declarations cannot affect `SourceOnly`, that Go
present/absent overrides and frozen saved snapshots control helper allocation,
and that edits to inactive variants and same-package `_test.go` files change the
persistent cache key before a stale generated helper can be restored.

### 3.3 Implement declaration-owned metadata

Extend the package plan/emission and
`componentTargetDeclarationProvenance` with explicit fields equivalent to:

```go
type directComponentFamily struct {
	logicalKey string
	helperName string
}

type directComponentDeclaration struct {
	family         directComponentFamily
	typeParamNames []string
	paramNames     []string
	variadic       bool
}
```

The concrete representation may differ, but these facts must be populated only
while walking authoritative `*ast.Component` declarations. Parse grouped type
parameter names through Go AST fields; do not split source strings. Determine
semantic eligibility once per declaration family. A valid family receives one
helper name, but forwarding spellings remain attached to each declaration.
Every member emits the family name in its own generated variant file using its
own authored type/value parameter names and variadic fact.

Add a compile-backed alpha-renaming fixture: two mutually exclusive build-tag
variants declare equivalent `Child[T any]` and `Child[U any]`, while one common
untagged parent calls `Child[string]`. Generate once, then run `go test` both
without tags and with the alternate tag. Both selections must compile and the
generated wrappers/helpers must use their local `T` or `U`, never a name copied
from the other variant.

Allocate names by sorted logical key from the union of:

- GSX package declarations;
- every non-owned same-package Go declaration, including handwritten/orphaned
  `.x.go` and `_test.go` files;
- names already allocated in this run.

Use `_gsxrender<Name>`, then the first free deterministic numeric suffix.

Propagate the exact metadata from target fact to positional site plan. The
emitter must not reconstruct it from `Element.Tag` or printed selector text.

### 3.4 Verify and commit

```bash
cd "$core"
gofmt -w internal/codegen/*.go
gopls check -severity=hint \
  internal/codegen/component_target_skeleton.go \
  internal/codegen/component_target_provenance.go \
  internal/codegen/component_target.go \
  internal/codegen/component_positional_plan.go \
  internal/codegen/direct_component*.go
GOCACHE=/tmp/gsx-runtime-direct-component-cache \
  go test ./internal/codegen \
  -run 'DirectComponent|DirectHelper|HelperName|ComponentTarget' -count=1
GOCACHE=/tmp/gsx-runtime-direct-component-cache go test ./internal/codegen -count=1
git diff --check
git add internal/codegen
git commit -m 'feat(codegen): prove direct local component targets'
```

## Task 4: Emit one shared body and direct calls

**Files:** `internal/codegen/emit.go`,
`internal/codegen/component_positional_emit.go`, focused generated-shape tests.

### 4.1 Write failing generated-shape tests

For an eligible local pair, require one public factory, one helper, and this
call shape:

```go
_gsxgw.NodeResult(_gsxrenderChild(ctx, _gsxgw, args...))
```

Require the public factory to keep the real API and delegate its body once:

```go
func Child(args...) _gsxrt.Node {
	return _gsxrt.Func(func(ctx _gsxctx.Context, _gsxw _gsxio.Writer) error {
		_gsxgw := _gsxrt.W(_gsxw)
		return _gsxrenderChild(ctx, _gsxgw, args...)
	})
}
```

Require the helper to begin with:

```go
if _gsxerr := _gsxgw.Err(); _gsxerr != nil {
	return _gsxerr
}
```

and to end with `return _gsxgw.Err()` on ordinary fallthrough. Pin generic
explicit forwarding, grouped type parameters, constraint-only type parameters,
ordinary variadics, attrs-only variadics, children, tuples, adapters, and
source-order temporaries. Include the alpha-renamed build-variant/common-caller
fixture from Task 3 and compile both tag selections.

In the same test suite, require `_gsxgw.Node(ctx, ...)` for imported, method,
package-variable, plain-Go, dynamic, blank-param, blank-type-param,
`ctx`-type-param, and non-colliding `_gsxT` targets. Require a positioned
diagnostic for an exact alias collision such as `_gsxrt`; such a declaration
cannot compile through the ordinary public wrapper because it shadows a
generated runtime alias. Add the same-package `_test.go` collision fixture and
run `go test` on its generated temp package so duplicate declarations cannot
hide behind a string assertion.

### 4.2 Share the body emitter

Refactor component emission so the existing markup/node lowering remains the
single body implementation. It must support two scaffolds:

1. unsplit public factory: current closure body;
2. split public factory plus top-level helper: wrapper binds `W(_gsxw)`, helper
   performs the prior-error guard and emits the shared body statements.

Do not duplicate `genNode`, escaping, numeric scratch, attrs, or source-line
logic. A component gets a helper only if its declaration family has at least
one proven direct call.

Use declaration parameter names to forward the wrapper. Preserve explicit type
arguments and `...` exactly. If the signature is not forwardable, the metadata
must already mark it fallback; do not synthesize placeholder names here.

### 4.3 Change only the proven call branch

In `component_positional_emit.go`, leave positional argument assembly unchanged.
After it has produced the final arguments and type arguments:

```go
if plan.directTarget != nil {
	fmt.Fprintf(b, "_gsxgw.NodeResult(%s%s(ctx, _gsxgw, %s))\n", ...)
	return
}
fmt.Fprintf(b, "_gsxgw.Node(ctx, %s%s(%s))\n", ...)
```

Handle the zero-argument form without a dangling comma. The direct branch must
consume the allocated helper name from metadata; no tag-name transformation is
allowed.

### 4.4 Verify and commit

```bash
cd "$core"
gofmt -w internal/codegen/*.go
gopls check -severity=hint \
  internal/codegen/emit.go internal/codegen/component_positional_emit.go
GOCACHE=/tmp/gsx-runtime-direct-component-cache \
  go test ./internal/codegen -run 'DirectComponent|DirectHelper' -count=1
GOCACHE=/tmp/gsx-runtime-direct-component-cache go test ./internal/codegen -count=1
git diff --check
git add internal/codegen
git commit -m 'feat(codegen): render proven local components directly'
```

## Task 5: Pin semantics, regenerate, and run the numeric gate

### 5.1 Add semantic corpus and failing-writer probes

Add corpus cases under `internal/corpus/testdata/cases/components/` for:

- basic and cross-file local direct calls;
- generic/grouped/constraint-only/variadic forwarding;
- scalar children, child nodes, attrs bags, and attrs-only variadics;
- source-order side effects and `(T, error)` short-circuiting;
- HTML, URL, srcset, JavaScript, CSS, and attribute escaping in a direct child;
- imported, method, package-variable, plain-Go, and dynamic fallbacks;
- helper-name collisions and build variants;
- source anchors.

Add a generated temp-module test under `internal/codegen` which compares a
generated direct parent with a hand-written current-`Node` reference using a
destination that fails at each write attempt. Run separate children which raw
return a sentinel and raw return `nil` after their writes. For every failure
index assert exact output bytes, write-attempt count, and error identity. Also
assert argument expressions run after an earlier parent error while the child
body does not. This is the shipping guard against the rejected `Fail` design.

### 5.2 Regenerate core truth to a fixed point

```bash
cd "$core"
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
make examples
go run ./cmd/gsx -C examples/tailwind-merge generate ./views

core_paths=(
  internal/corpus/testdata/cases
  internal/corpus/testdata/coverage.golden
  docs/examples.json
  playground/server/examples.json
  docs/guide/syntax/_generated
  examples/tailwind-merge/views/card.x.go
)
hash_tree_paths() {
	for path in "$@"; do
		if [ -d "$path" ]; then
			find "$path" -type f -print
		elif [ -f "$path" ]; then
			printf '%s\n' "$path"
		fi
	done | LC_ALL=C sort | while IFS= read -r file; do
		sha=$(shasum -a 256 "$file" | awk '{print $1}')
		printf '%s  %s\n' "$sha" "$file"
	done | shasum -a 256 | awk '{print $1}'
}
core_hash_1=$(hash_tree_paths "${core_paths[@]}")

go test ./internal/corpus -run TestCorpus -update
make examples
go run ./cmd/gsx -C examples/tailwind-merge generate ./views
core_hash_2=$(hash_tree_paths "${core_paths[@]}")
test "$core_hash_1" = "$core_hash_2"
```

There is no `make generate` target in core. The commands above are the complete
owned generation surfaces for this change. `hash_tree_paths` hashes path names
and file bytes directly, so newly created untracked corpus cases participate in
the fixed-point check before they are staged.

### 5.3 Regenerate bench truth to a fixed point

```bash
cd "$bench"
make generate-gsx
bench_hash_1=$(git diff --binary -- gsxr tw | shasum -a 256 | awk '{print $1}')
make generate-gsx
bench_hash_2=$(git diff --binary -- gsxr tw | shasum -a 256 | awk '{print $1}')
test "$bench_hash_1" = "$bench_hash_2"
make test
go test ./... -count=1
```

Inspect generated source directly:

```bash
rg -n 'NodeResult|_gsxrender|\.Node\(' gsxr/*.x.go
```

Table/Card must use the direct helper. ImportedBoundary, MethodBoundary, and
DynamicBoundary must retain `Node` at their measured boundary.

Commit generated/corpus tests separately:

```bash
cd "$core"
git diff --check
git add internal/corpus internal/codegen docs/examples.json \
  playground/server/examples.json docs/guide/syntax/_generated \
  examples/tailwind-merge/views/card.x.go
git commit -m 'test(codegen): pin direct component semantics'

cd "$bench"
git diff --check
git add gsxr tw scenarios_test.go '*fallback*test.go'
git commit -m 'bench: regenerate direct component calls'
```

Stage only exact existing test paths if shell globs do not match.

### 5.4 Create a detached paired baseline

```bash
state=/tmp/gsx-runtime-direct-component
core_base=$(cat "$state/core-base")
bench_base=$(cat "$state/bench-base")
pair=$(mktemp -d /tmp/gsx-runtime-direct-pair.XXXXXX)

git -C "$core" worktree add --detach "$pair/gsx" "$core_base"
git -C "$bench" worktree add --detach "$pair/gsx-bench" "$bench_base"
test "$(cd "$pair/gsx-bench" && go list -m -f '{{.Dir}}' github.com/gsxhq/gsx)" = "$pair/gsx"
test "$(cd "$bench" && go list -m -f '{{.Dir}}' github.com/gsxhq/gsx)" = "$core"
```

The sibling names are required by bench's `replace ../gsx`.

### 5.5 Run the hard Table gate first

```bash
result=/tmp/gsx-runtime-direct-table-$(date +%s)
test ! -e "$result"
cd "$bench"
scripts/benchcmp.sh \
  "$pair/gsx-bench" "$bench" \
  '^BenchmarkTableGSX(Pooled|Discard)$' \
  "$result" . github.com/gsxhq/gsx
cat "$result/benchstat.txt"
```

Retain eligibility requires, independently for pooled and discard:

- time improvement at least 10%, `p < 0.05`;
- bytes improvement at least 90%, `p < 0.05`;
- allocations improvement at least 90%, `p < 0.05`.

If any Table gate fails, skip the full benchmark and execute the rejection path
in Task 6.4. There is no near-miss waiver.

### 5.6 Run the full regression screen

```bash
full=/tmp/gsx-runtime-direct-full-$(date +%s)
test ! -e "$full"
scripts/benchcmp.sh \
  "$pair/gsx-bench" "$bench" \
  '^Benchmark.*GSX(Pooled|Discard|Builder|Parallel)$' \
  "$full" . github.com/gsxhq/gsx
cat "$full/benchstat.txt"
```

Reject significant regressions at or above 7% for all non-Table serial paths,
including ImportedBoundary, MethodBoundary, DynamicBoundary, and every
available Builder benchmark. Page parallel uses a 12% threshold. Record all
medians, sample counts, and p-values, including non-significant movements.

## Task 6: Independent adversarial verification and keep/revert decision

### 6.1 Run authoritative repository checks

```bash
cd "$core"
GOCACHE=/tmp/gsx-runtime-direct-component-cache make ci
GOCACHE=/tmp/gsx-runtime-direct-component-cache make lint
GOCACHE=/tmp/gsx-runtime-direct-component-cache go test -race ./... -count=1
git diff --check
test -z "$(git status --porcelain=v1)"

cd "$bench"
make generate-gsx
make test
go test -race ./... -count=1
git diff --exit-code
test -z "$(git status --porcelain=v1)"
```

If docs/examples or generated bench output drifts here, fix the source and
repeat the fixed-point and benchmark runs; do not hand-edit generated files.

### 6.2 Profile the measured Table path

```bash
profiles=/tmp/gsx-runtime-direct-profiles-$(date +%s)
mkdir -p "$profiles"
cd "$bench"
GOCACHE=/tmp/gsx-runtime-direct-component-cache \
  go test -c -o "$profiles/bench.test" .
"$profiles/bench.test" -test.run '^$' \
  -test.bench '^BenchmarkTableGSXPooled$' -test.benchtime=5s \
  -test.memprofile "$profiles/Table.mem" -test.memprofilerate=1
go tool pprof -top -alloc_objects "$profiles/bench.test" "$profiles/Table.mem" \
  > "$profiles/Table.alloc_objects.txt"
go tool pprof -top -alloc_space "$profiles/bench.test" "$profiles/Table.mem" \
  > "$profiles/Table.alloc_space.txt"
go test -run '^$' \
  -gcflags='github.com/gsxhq/gsx-bench/gsxr=-m=2' . \
  > "$profiles/escape.txt" 2>&1
```

The Table profile must no longer attribute one closure/object per Card call.
The public Card factory may still escape when used as the dynamic/value API;
that is expected and must not be confused with the measured direct path.

### 6.3 Commission an independent adversarial review

Use `superpowers:requesting-code-review` with a reviewer who did not implement
the subsystem. Give it the saved bases, candidate tips, spec, plan, raw
benchstat roots, and profiles. Require the reviewer to build throwaway programs
under `/tmp`, not merely read the diff. It must probe:

1. raw sentinel and raw nil returns after every failing child write boundary;
2. prior-error argument evaluation and skipped child body;
3. generic/grouped/constraint-only and variadic calls;
4. same-package `_test.go`, external-test, non-owned `.x.go`, multi-file, and
   build-variant name collisions by actually running `go test`, including
   alpha-renamed generic variants through a common caller;
5. imported, method, plain-Go, package-variable, and dynamic generated fallbacks;
6. concurrent rendering with `-race`;
7. exact output and escaping contexts;
8. fixed-point regeneration commands from Tasks 5.2 and 5.3.

Resolve every P0/P1 finding and repeat measurements affected by the fix. Do not
retain with an open correctness or measurement blocker.

### 6.4 Keep or revert exactly

Capture candidate tips before any decision:

```bash
core_tip=$(git -C "$core" rev-parse HEAD)
bench_tip=$(git -C "$bench" rev-parse HEAD)
core_base=$(cat "$state/core-base")
bench_base=$(cat "$state/bench-base")
```

If every correctness and numeric gate passes, keep the candidate commits.

If any gate fails, revert every post-base candidate commit in newest-first order
without resetting history:

```bash
git -C "$core" rev-list "$core_base..$core_tip" | while read -r commit; do
  git -C "$core" revert --no-edit "$commit"
done
git -C "$bench" rev-list "$bench_base..$bench_tip" | while read -r commit; do
  git -C "$bench" revert --no-edit "$commit"
done

git -C "$core" diff --exit-code "$core_base"
git -C "$bench" diff --exit-code "$bench_base"
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
```

This preserves the Task 1 benchmark-fixture commit because it is the saved bench
base. It also preserves the documentation base; only candidate production,
generated, and candidate-test commits are reverted. Whole-tree comparison plus
clean status covers runtime tests, codegen tests, generated outputs, benchmark
fallback tests, path-list omissions, and untracked leftovers; a selected
path list is not an acceptable restoration proof.

### 6.5 Record evidence and clean detached worktrees

Update `docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md`
with:

- base and candidate SHAs for both repositories;
- Go/CPU/GOMAXPROCS and dependency fingerprints;
- probe/result/profile paths and SHA-256 values;
- Table and full-suite medians, p-values, and gate decisions;
- fixed-point, CI, lint, race, gopls, and adversarial results;
- exact retained commits or exact revert commits.

Then:

```bash
git -C "$core" worktree remove "$pair/gsx"
git -C "$bench" worktree remove "$pair/gsx-bench"
cd "$core"
git add docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md
git commit -m 'docs(perf): record direct component decision'
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
```

The final handoff must state the decision first, then the exact measured
improvements/regressions, verification commands, commits, and raw evidence
paths. Do not claim the optimisation from the throwaway probe alone; only the
generated candidate comparison authorizes retention.
