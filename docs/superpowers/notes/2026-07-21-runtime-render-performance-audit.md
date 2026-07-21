# Runtime Render Performance Audit

This note records the measurement-only baseline for the runtime/codegen audit.
No runtime, code generator, generated benchmark output, or benchmark input was
changed while collecting it. Raw benchmark output and profiles live under
`/tmp/gsx-runtime-audit` and are intentionally not committed.

## Environment

The commands required by the audit plan produced this exact output before the
note commit:

```text
go version go1.26.1 darwin/arm64
arm64
Apple M3 Ultra
9aff57262d6a00d11c92f2e4655e8fae16c23813
20a26daec13f3dd7bf03003dde440e2862bbb696
```

The final two lines are, respectively, the audited core GSX revision and the
paired `gsx-bench` revision. Benchmark names report `-32`; `GOMAXPROCS` was not
overridden. All commands used `GOCACHE=/tmp/gsx-runtime-audit-cache` where the
plan prescribed it.

## Benchmark Repositories and Commands

The paired repositories were clean before measurement:

- core: `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx`
- external suite:
  `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench`

The external baseline is one ten-sample collection. Its reported values are the
median and spread from the exact pinned benchstat version. An independent
review confirmed that Go emits `-count=10` samples grouped by benchmark rather
than alternating benchmark names, so this baseline did not satisfy the
design's interleaving requirement. It remains valid absolute baseline and
allocation-ownership evidence, but it is not a before/after optimisation
comparison; follow-up experiments must alternate separate before/after
worktrees one sample at a time before running benchstat:

```sh
mkdir -p /tmp/gsx-runtime-audit
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench . -benchmem -count=10 . | tee /tmp/gsx-runtime-audit/baseline.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d /tmp/gsx-runtime-audit/baseline.txt
```

The focused core command also completed with ten samples. A second independent
ten-sample run of the same command was captured in
`/tmp/gsx-runtime-audit/core.txt`; the table below reports that captured run,
not a combination of the two runs.

```sh
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench 'Benchmark(Class|Style|Root|Forwarding|Cond|Without)' -benchmem -count=10 .
```

The five prescribed 5-second profiles used `-cpuprofile`, `-memprofile`, and
`-memprofilerate=1` together for Page, Table, ForwardedAttrs, FoldedAttrs, and
Comments. Their rate-1 allocation instrumentation made the CPU side of the four
allocation-heavy profiles spend most samples in allocation stack collection;
for example, `runtime.mallocgc` accumulated 69% to 80% of those combined CPU
profiles, with allocation traceback collection itself at roughly 52% to 58%.
The combined artifacts remain at the prescribed `Page.cpu`,
`Table.cpu`, and corresponding paths. They are used for allocation call-tree
ownership, not normal CPU-share claims.

The exact prescribed combined-profile loop was:

```sh
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
for name in Page Table ForwardedAttrs FoldedAttrs Comments; do
  GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench "^Benchmark${name}GSXPooled$" -benchtime=5s -cpuprofile "/tmp/gsx-runtime-audit/${name}.cpu" -memprofile "/tmp/gsx-runtime-audit/${name}.mem" -memprofilerate=1 .
  go tool pprof -top -nodecount=30 "/tmp/gsx-runtime-audit/${name}.cpu" > "/tmp/gsx-runtime-audit/${name}.cpu.top"
  go tool pprof -top -alloc_space -nodecount=30 "/tmp/gsx-runtime-audit/${name}.mem" > "/tmp/gsx-runtime-audit/${name}.mem.top"
done
```

With explicit approval, a separate, non-replacing CPU-only set was collected
with the same benchmark expressions and 5-second duration at
`*CPUOnly.cpu`/`*CPUOnly.cpu.top`. Normal CPU attribution below comes from that
set. Its distinct-filename loop was:

```sh
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
for name in Page Table ForwardedAttrs FoldedAttrs Comments; do
  GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench "^Benchmark${name}GSXPooled$" -benchtime=5s -cpuprofile "/tmp/gsx-runtime-audit/${name}CPUOnly.cpu" .
  go tool pprof -top -nodecount=30 "/tmp/gsx-runtime-audit/${name}CPUOnly.cpu" > "/tmp/gsx-runtime-audit/${name}CPUOnly.cpu.top"
done
```

Memory-only Stats and Piped profiles were also added to separate numeric
scratch allocation from pipeline user work. They used rate-1 sampling for 5
seconds and retained both allocation-space and allocation-object reports:

```sh
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
for name in Stats Piped; do
  GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -bench "^Benchmark${name}GSXPooled$" -benchtime=5s -memprofile "/tmp/gsx-runtime-audit/${name}.mem" -memprofilerate=1 .
  go tool pprof -top -alloc_space -nodecount=30 "/tmp/gsx-runtime-audit/${name}.mem" > "/tmp/gsx-runtime-audit/${name}.mem.top"
  go tool pprof -top -alloc_objects -nodecount=30 "/tmp/gsx-runtime-audit/${name}.mem" > "/tmp/gsx-runtime-audit/${name}.objects.top"
done
```

The compiler and symbol traces were collected exactly as planned:

```sh
GOCACHE=/tmp/gsx-runtime-audit-cache go test -run '^$' -gcflags='all=-m=2' . 2> /tmp/gsx-runtime-audit/escape.txt
gopls references -d attrs.go:326:20
gopls references -d writer.go:187:20
```

The harness constructs each render node outside its benchmark loop. `Pooled`
uses a warm `*bytes.Buffer` from `sync.Pool`; `Discard` removes buffer writes;
`Builder` resets a cold `strings.Builder` to nil each iteration. Inputs are 20
rows or 20 comments. Templ and `html/template` numbers are context only, not an
acceptance criterion for a GSX change.

## External Baseline

All time columns are the ten-sample benchstat medians; `±` is benchstat's
reported spread. Allocation counts were invariant. A few concurrent or pooled
byte counters varied by single-byte rounding across samples; the table reports
their median rounded to the nearest byte.

| Benchmark | time/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| ForwardedAttrs GSX pooled | 14.99 us ±1% | 2,916 | 81 |
| ForwardedAttrs templ pooled | 8.902 us ±4% | 4,324 | 123 |
| ForwardedAttrs GSX discard | 13.38 us ±2% | 2,912 | 81 |
| FoldedAttrs GSX pooled | 20.61 us ±3% | 11,810 | 161 |
| FoldedAttrs GSX discard | 19.62 us ±5% | 11,792 | 161 |
| Page GSX parallel | 1.884 us ±7% | 2,567 | 62 |
| Page templ parallel | 2.939 us ±9% | 4,976 | 204 |
| Document GSX pooled | 332.9 ns ±4% | 56 | 2 |
| Document templ pooled | 505.9 ns ±1% | 362 | 10 |
| Document html/template pooled | 1.735 us ±4% | 642 | 24 |
| Document GSX discard | 221.2 ns ±4% | 56 | 2 |
| Document templ discard | 489.6 ns ±1% | 361 | 10 |
| Document GSX builder | 608.7 ns ±1% | 784 | 8 |
| Document templ builder | 579.6 ns ±10% | 650 | 11 |
| Document html/template builder | 1.989 us ±1% | 1,266 | 28 |
| Document raw builder floor | 89.75 ns ±1% | 320 | 1 |
| Stats GSX pooled | 1.370 us ±4% | 64 | 2 |
| Stats templ pooled | 4.391 us ±2% | 1,392 | 134 |
| List GSX pooled | 1.708 us ±2% | 32 | 1 |
| List templ pooled | 4.389 us ±3% | 1,915 | 123 |
| Table GSX pooled | 2.821 us ±1% | 1,955 | 21 |
| Table templ pooled | 6.361 us ±1% | 4,814 | 183 |
| Piped GSX pooled | 2.089 us ±2% | 352 | 41 |
| Page GSX pooled | 5.533 us ±8% | 2,562 | 62 |
| Page templ pooled | 8.530 us ±1% | 4,975 | 204 |
| Comments GSX pooled | 4.297 us ±3% | 32 | 1 |
| Comments templ pooled | 8.742 us ±0% | 9,090 | 143 |
| Buttons GSX pooled | 7.485 us ±1% | 5,803 | 161 |
| Buttons templ pooled | 10.42 us ±1% | 10,225 | 203 |

Destination cost is visible but is not the non-empty attribute result. The
ForwardedAttrs pooled destination adds about 1.61 us over discard while keeping
81 allocations; FoldedAttrs adds about 0.99 us while keeping 161 allocations.
The internal path therefore dominates both attribute workloads. Conversely,
the cold Document builder grows its destination and raises GSX from 56 B/2
allocations on pooled/discard to 784 B/8 allocations. That builder growth is
destination work and is not ranked as runtime overhead.

The parallel Page result is throughput context on 32 logical CPUs, not a
single-request latency result. Its equal 62 allocations prove only that the
measured per-operation allocation count is unchanged under the benchmark's
parallel scheduling. Allocation equality does not prove an absence of shared
state; concurrency safety must come from race tests and adversarial concurrent
probes. The parallel time is not compared to the serial value as an
optimisation claim.

## Core Microbenchmarks

These are the medians from the captured ten-sample core run, summarized with
the same pinned benchstat revision.

| Benchmark | time/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| ClassLoneToken | 12.29 ns ±2% | 0 | 0 |
| ClassSingleMultiToken | 36.43 ns ±2% | 16 | 1 |
| ClassMergeFallthrough | 160.4 ns ±1% | 208 | 6 |
| ClassMergedRoot | 41.09 ns ±1% | 16 | 1 |
| CondMergeFolded | 359.5 ns ±2% | 256 | 4 |
| CondMergeComposable | 320.7 ns ±1% | 128 | 6 |
| StyleMergedEmpty | 1.966 ns ±1% | 0 | 0 |
| WithoutEmpty | 1.762 ns ±1% | 0 | 0 |
| RootAttrMachineryEmpty | 7.419 ns ±2% | 0 | 0 |
| ForwardingLeafNoURL (pre-review 11-name metadata) | 310.7 ns ±1% | 16 | 1 |
| StyleMergedDedup | 350.9 ns ±1% | 136 | 4 |
| WithoutAttrs | 10.54 ns ±1% | 0 | 0 |

The empty root machinery and empty bag helpers are already allocation-free.
The one-element folded shape is 12.1% slower than its composable counterpart
and uses twice the bytes, although it uses two fewer allocations; that mixed
result is why the external folded profile, rather than this microbenchmark
alone, decides whether folded materialisation remains a candidate. Non-empty
class/style work is real: a multi-token sole class allocates 16 B, an actual
cross-source class merge allocates 208 B/6 objects, and style deduplication uses
136 B/4 objects.

An independent adversarial review found that the original
`BenchmarkForwardingLeafNoURL` passed the generated navigation and image name
sets but omitted the generated `[]string{"imagesrcset", "srcset"}` set. The
review corrected the benchmark to the exact 13-name generated metadata shape.
Its fresh ten-sample median is 378.7 ns ±5%, 16 B, and one allocation. This is a
workload correction, not an optimisation comparison, so the old and corrected
times are not treated as before/after evidence. The same review's direct
`Spread` boundary probes measured zero allocations for eight plain attributes,
456 B in three `lastValidAttrIndexes` allocations for nine, and 3,496 B in
three allocations for 70. Those larger-bag results scope, but do not
contradict, the allocation ownership of the audited six/eight-entry external
workloads.

## CPU Profile Attribution

Percentages here are cumulative shares of total samples in the CPU-only
profiles and are not additive. Destination writes, GC, and scheduler work remain
in the denominator. The 5-second profile benchmark times are not substituted
for the ten-sample baseline medians.

| Profile | Relevant normal-CPU attribution |
| --- | --- |
| ForwardedAttrs | `(*Writer).Spread` 30.56% broad cumulative; `attrNameExcluded` 9.64% cumulative, including `strings.EqualFold` 7.35% flat; duplicate-resolution `lastValidAttrIndexes` 4.90%; `StyleMerged` 6.21%; `ClassMerged` 1.47% |
| FoldedAttrs | `(*Writer).Spread` 26.88% broad cumulative; `attrNameExcluded` 8.19% cumulative, including `strings.EqualFold` 6.55% flat; duplicate-resolution `lastValidAttrIndexes` 5.59%; `StyleMerged` 6.28%; `Attrs.Class` 4.37%; `ConcatAttrs` 2.73%; `ClassMerged` 2.32% |
| Table | child `Card.func1` 34.25%; `(*Writer).Node` 34.66% including that child body; `Card` constructor 7.98%; `runtime.mallocgc` 7.36%; `Writer.Text` 12.99% |
| Page | child `UserCard.func1` 44.27%; `Writer.Node` 44.94% including the child; user `Row.Href`/`fmt.Sprintf` 9.94%; `Writer.Class` 7.73%; `Writer.Text` 7.39%; `runtime.mallocgc` 8.41% |
| Comments | `Writer.Text` 71.83%; `writeHTML` 70.63%; `strings.(*byteStringReplacer).WriteString` 69.87%; buffer `WriteString` 25.44% |

`Writer.Node` includes the complete child render and therefore is not itself a
34% to 45% removable dispatch cost. The constructor and allocation samples,
plus the allocation profile, isolate the removable composition part. Likewise,
`Spread` includes writes, value sinks, classification, and duplicate
resolution. Its cumulative share is broader than the static URL/name
classifier. The `attrNameExcluded` cumulative share already contains the flat
`strings.EqualFold` share, so those percentages are not additive.
`lastValidAttrIndexes` and its map operations implement separate duplicate
resolution and would remain unchanged by a static URL/name-classifier
experiment.

Comments is an intentionally escape-heavy user workload. It confirms that the
faithful HTML escaper and destination copying dominate that scenario; it does
not identify a general allocation problem.

## Allocation and Escape Attribution

Allocation-profile object and byte percentages are from the prescribed
rate-1 memory profiles. They agree with the exact `allocs/op` counters and the
compiler trace:

- **ForwardedAttrs:** 81 allocations are one top-level `W` plus four repeated
  allocations per each of 20 links. `ForwardedLink` closure construction owns
  24.67% of objects and 64.46% of bytes; `ClassMerged` owns 24.67% of objects;
  `StyleMerged` plus `splitDecls` owns 49.34% of objects. `Spread` owns no heap
  allocation.
- **FoldedAttrs:** `ConcatAttrs` owns 26.47% of objects and 80.58% of allocated
  bytes. `joinAttrStrings` owns 19.85% of objects, `ClassMerged` 13.24%,
  `StyleMerged` plus `splitDecls` 26.47%, and the selected conditional branch
  literal 13.24% across its two source-labelled arms. This is materialisation,
  string aggregation, and style parsing rather than destination growth.
- **Table:** constructing 20 `Card(r, nil)` child nodes owns 95.18% of objects
  and 97.59% of bytes. `W` owns the remaining one-per-render allocation. The
  exact 21 allocations are therefore 20 child closures plus one writer.
- **Page:** `UserCard` constructor closures own 34.75% of objects and 73.66% of
  bytes; `Writer.Class` owns another 34.75% of objects/12.28% of bytes; and
  user `fmt.Sprintf` in `Row.Href` owns 26.93% of objects/9.51% of bytes. The
  generated numeric scratch and `W` are one-per-render rather than per row.
- **Comments:** the benchmark has 32 B and one allocation total. The object
  profile attributes that allocation to `W`; repeated escaping allocates
  nothing.

The compiler trace explains the generated shapes:

- At `Writer.Spread`, the inlined
  `make(map[string]int, len(attrs))` for `lastValidAttrIndexes` does not escape.
  Generated nav/image/srcset/excluded slice literals also do not escape. The
  standalone helper's returned map would escape without inlining, so the
  call-site diagnostic and profile, not the standalone diagnostic, govern this
  generated path.
- `ConcatAttrs`' result backing slice escapes, as do the selected conditional
  branch's returned `Attrs` literal values. The branch function literals
  themselves do not escape.
- `Card(r, nil)`, `UserCard(r, nil)`, and `ForwardedLink(row, attrs)` return a
  closure-backed `Func`; converting that value to `Node` for `Writer.Node`
  makes the closure storage escape.
- The generated `[32]byte` numeric scratch moves to the heap because its slice
  crosses the `io.Writer.Write` interface. It is allocated once and reused for
  every number in the render.

The prescribed `gopls references` queries found 22 core references for
`Writer.Spread` (definition plus runtime, fuzz, differential, unit, and
microbenchmark coverage) and six for `Writer.Node` (definition and five core
tests). The external generated references were inspected separately at
`gsxr/attrs.x.go`, `gsxr/page.x.go`, and `gsxr/scenarios.x.go` because the
sibling repository is outside the core gopls workspace.

Two supplemental memory profiles close possible attribution mistakes:

- Stats is 64 B/2 allocations. Allocation objects split exactly 50/50 between
  `W` and the generated render function holding `_gsxnum`; 60 `IntInto` calls
  across 20 rows add no per-number allocation.
- Piped is 352 B/41 allocations. Forty allocations (95.23% of objects) are
  `strings.ToUpper` through `gsx/std.Upper`; `W` is the remaining allocation.
  Those transforms are user-requested pipeline work, not pipeline dispatch or
  writer overhead.

## Ranked Candidate 1

### Keep investigating: exact non-empty spread classification

The exact hot call shape is the generated
`_gsxgw.Spread(ctx, attrs, navNames, imageNames, srcsetNames, prefixes,
excluded)` in `ForwardedLink` and the corresponding call over `_gsxv2` in
`FoldedTabs`. `Writer.Spread` is 30.56% of ForwardedAttrs and 26.88% of
FoldedAttrs CPU-only samples, but that is the broad cumulative function share,
not removable classifier cost. Static URL/name membership appears in the
`attrNameExcluded` cumulative subtree (9.64% and 8.19%), which already includes
the flat `strings.EqualFold` samples (7.35% and 6.55%). In contrast,
`lastValidAttrIndexes` and its map operations implement duplicate resolution;
they are not changed by the proposed classifier representation. None of these
shares may be summed. The path allocates nothing at these call sites, so this is
a CPU candidate only. The 13.38 us ForwardedAttrs discard result proves the
time is not mainly the pooled destination.

The single-variable experiment is to change only the representation and lookup
of codegen-known URL/name policy: replace the repeated linear scans of static
name slices with one immutable, collision-safe classifier shared by generated
calls. Its common ASCII path must not allocate, while any non-ASCII key or
comparison name must fall back to `strings.EqualFold` so live Unicode
simple-fold behavior remains byte-for-byte exact (for example, `ſrc` currently
matches `src`). Attribute order, last-valid-index handling, validity checks,
overlap precedence (image before srcset before navigation), prefixes, value
sinks, and writes stay unchanged. This must be an exact two-path classifier,
not a shortcut based on common names.

The deciding benchmarks are `BenchmarkForwardedAttrsGSXPooled`, its discard
variant, `BenchmarkFoldedAttrsGSXPooled`, and
`BenchmarkForwardingLeafNoURL`, using interleaved ten-sample before/after files
and the pinned benchstat. Correctness gates are all spread security corpus
cases, case-variant URL names, prefix rules, duplicate/ordering tests,
`spread_fold_diff_test.go`, root package fuzz/tests, `make ci`, and `make lint`.
Keep only a statistically significant end-to-end time reduction with zero
allocation regression.

## Ranked Candidate 2

### Keep investigating: folded attribute bag materialisation

The exact generated shape in `FoldedTabs` is two calls to `ConcatAttrs` around
an `AttrsCond` result, followed by `v2.Class()`, `v2.Style()`, and `Spread`.
The external workload measures 20.61 us, 11,810 B, and 161 allocations for 20
rows; pooled and discard allocation counts are identical. `ConcatAttrs` alone
owns 80.58% of allocation space. The independent core shape measures 359.5 ns,
256 B, and four allocations versus 320.7 ns, 128 B, and six allocations for the
composable form. The external allocation profile resolves that microbenchmark's
mixed result in favor of investigating bytes and materialisation.

The single-variable experiment is a proper codegen lowering that assembles the
folded leaf bag once into one pre-sized final `Attrs` backing store, rather than
materialising two concatenation results and a returned branch bag. It must
preserve source evaluation order, selected-branch laziness, tuple-error
propagation, duplicate ordering, and class/style aggregation. `Spread`,
class/style semantics, and the authored benchmark source stay unchanged.

The deciding benchmarks are both FoldedAttrs destination variants and
`BenchmarkCondMergeFolded`, again as interleaved ten-sample before/after sets.
The experiment must materially reduce 11,810 B and 161 allocations as well as
improve time beyond noise. Correctness gates are `TestFoldedAttrsOutput`, the
conditional-merge corpus (including all forms and style), side-effect/evaluation
order and error cases, the spread differential test, regenerated semantic
goldens, `make ci`, and `make lint`.

## Ranked Candidate 3

### Evidence for a separate follow-up plan: generated child boundary

Decision: this measurement task authorises no generated/runtime ABI experiment.
Preserve the evidence for a separate follow-up optimisation plan. After
Candidates 1 and 2, that plan may reprofile composition and explicitly decide
whether to select a large direct-render experiment under the large-change gate.
Until such a plan selects it, implementation is deferred.

The exact generated call is `_gsxgw.Node(ctx, Card(r, nil))`, with the same
shape for `UserCard` and `ForwardedLink`. Table measures 2.821 us, 1,955 B, and
21 allocations; 20 child constructors account for 95.18% of allocation objects
and 97.59% of bytes. Page repeats 20 `UserCard` constructors within its 62
allocations, and ForwardedAttrs repeats 20 `ForwardedLink` constructors within
its 81. Escape analysis proves the closure-backed `Func` escapes at the `Node`
interface boundary.

The evidence rules out assuming that a different `Node` representation alone
would remove the escapes. If a separate approved plan selects component
composition for experimentation, it must define a genuinely direct ABI and its
large-change acceptance and correctness gates. This note neither prescribes nor
authorises that code change. The relevant evidence surfaces for such a future
plan are Table, Page, and ForwardedAttrs; component call and tuple-error corpus
cases; nested/imported/method components; render-error probes; output
equivalence; race tests; full corpus regeneration; `make ci`; and `make lint`.

## Paths Already Fast or Inherent

- Empty `StyleMerged`, `Without`, and complete empty root attribute machinery
  are 1.8 to 7.4 ns with zero allocations. Do not add nil-bag ABI complexity.
- The generated static name slices and Spread's inlined last-index map are
  stack-only for the audited six/eight-entry ForwardedAttrs/FoldedAttrs bags.
  A 70-entry adversarial bag allocates map buckets, so Candidate 1 targets
  repeated exact lookup time—not heap removal—on the measured small-bag path;
  fresh profiles must keep allocation ownership scoped to their bag size.
- A lone static class token is 12.29 ns/0 allocations. Static markup and the
  inline List loop are already allocation-flat apart from the one writer per
  render.
- Comments' 71.83% `Writer.Text` share is required HTML escaping and buffer
  copying. It is allocation-free inside the loop and must remain a faithful
  `html/template`-equivalent escaper.
- Numeric interpolation performs 60 writes with one 32-byte scratch allocation
  plus the writer. It is already amortized per render, not per number.
- Piped's 40 repeated allocations are `strings.ToUpper` results requested by
  the authored pipeline. Generated lowering is a direct function call and does
  not own them.
- Page's `fmt.Sprintf` share belongs to the benchmark model's `Row.Href` helper.
  It is user computation, not GSX URL escaping or component dispatch.
- Warm buffer and discard comparisons separate destination work. Cold builder
  growth and the raw builder floor describe a different destination and are not
  runtime optimisation targets.

## Rejected Designs

- **Reject the old struct-backed Node experiment.** It retained per-child
  allocations at `Writer.Node(Node)` because the interface boundary, not merely
  the closure representation, caused the escape. Any future approved plan must
  not reintroduce that struct design; this note does not authorise an
  alternative.
- **Reject empty-bag and empty-style specialisation work.** The measured path is
  already zero-allocation and single-digit nanoseconds.
- **Reject weakening HTML or URL handling to chase profile shares.** Escaping,
  case-insensitive URL classification, precedence, and duplicate/order semantics
  are correctness and security contracts. Candidate 1 may replace their exact
  classifier implementation only with a differential proof.
- **Reject numeric string conversion and pipeline lowering as current targets.**
  The allocation profiles assign their costs to the amortized scratch buffer
  and user `strings.ToUpper`, respectively.
- **Reject tuning for the cold Builder ranking.** Its extra six GSX allocations
  and 728 bytes relative to pooled/discard are destination capacity growth; the
  production-representative warm buffer is the deciding surface.

## Recommended Optimisation Slices

1. Add an exact URL/name-classifier differential benchmark and run the
   single-variable immutable spread-policy experiment from Candidate 1. Keep or
   remove it solely on repeated external results and full security equivalence.
2. Add an allocation-regression benchmark for the exact external folded shape,
   then implement the one-final-bag lowering from Candidate 2 without changing
   leaf semantics.
3. Reprofile non-empty attributes after slices 1 and 2. If the 40 per-render
   `StyleMerged`/`splitDecls` allocations in ForwardedAttrs remain material, run
   a separate exact quote/parenthesis-aware single-contributor style-parser
   experiment; do not use a delimiter shortcut.
4. After those smaller slices and fresh profiles, carry Candidate 3's evidence
   into a separate follow-up optimisation plan. That plan, not this baseline
   task, must decide whether to authorise a large direct-render ABI experiment
   and define its acceptance gate. Without that explicit selection, make no
   component ABI change.

Every slice starts from a focused correctness case and benchmark, changes one
performance variable, collects interleaved ten-sample before/after data with
the pinned benchstat tool, and records both retained and rejected results in
this note.
