# Test suite performance — measurement and design

**Status:** Phase 1 shipped. Phase 2 (this document) awaiting a decision on Lever B's
security semantics.

**Date:** 2026-07-30

## Why now

`go test ./... -count=1` had grown to **292s**. It had been optimised twice before
(PRs #56/#60, 119s → 61s on 2026-07-09), and both prior wins came from removing
*production* codegen redundancies, not from touching test code. The same is true
again.

Everything below is measured on this machine (32 cores, Go 1.26.1, warm build
cache). No estimates.

## Where the time goes

`gen` and `internal/codegen` **are** the suite. They run concurrently, so wall ≈
max of the two; all ~40 other packages finish in under a second each.

| package | wall (p=4) | tests | serial sum | parallel sum | `packages.Load` | load time |
| --- | --- | --- | --- | --- | --- | --- |
| `gen` | 264s | 552 | 208s | 203s | 337 | 104s |
| `internal/codegen` | 260s | 616 | 240s | 78s | 466 | 115s |

CPU profiles of both packages agree closely on the four cost centres:

| cost centre | `gen` (of 394s samples) | `codegen` (of 458s samples) |
| --- | --- | --- |
| `packages.(*loader).parseFiles` | 86.5s (22.0%) | 95.8s (20.9%) |
| `go/types.(*Checker).checkFiles` | 57.4s (14.6%) | 60.9s (13.3%) |
| `golauncher.inspect` (SHA-256) | 54.3s (13.8%) | 53.9s (11.8%) |
| `freezeGoCommandEnvironment` (`go env`) | 23.0s (5.9%) | 25.0s (5.5%) |

Plus 106s of GC in `codegen` alone, which is garbage produced by the parsing above.

### The unit of cost is a Module open, not a test function

Every one of the 803 `packages.Load` calls includes `github.com/gsxhq/gsx` +
`github.com/gsxhq/gsx/std` — an **85-package, 667-file, 205,468-line** closure,
re-parsed and re-type-checked from scratch each time (~165M lines of parsing per
run) at 0.25–0.31s a call. The per-test module riding along (`testmod`,
`example.com/x/...`) is one to three tiny files.

In `codegen` the 466 loads collapse to only **146 distinct signatures**; the top 12
signatures alone account for 270 of them:

```
53 x  github.com/gsxhq/gsx github.com/gsxhq/gsx/std testmod ./...
51 x  github.com/gsxhq/gsx github.com/gsxhq/gsx/std example.com/x/components example.com/x/pages
36 x  github.com/gsxhq/gsx github.com/gsxhq/gsx/std example.com/app/ui ./...
34 x  github.com/gsxhq/gsx github.com/gsxhq/gsx/std example.com/app/page ./...
29 x  github.com/gsxhq/gsx github.com/gsxhq/gsx/std example.com/u ./...
...
```

**Consequence for how we write tests:** adding table rows to an existing
Module-backed test is ~free; adding one more test that opens its own Module costs
~0.3s forever. This is now recorded in CLAUDE.md.

**Consequence for "merge similar tests":** merging only pays when it removes a
`packages.Load`. In `gen`, 344 of 552 tests take <1s and total 46s *combined* —
merging those is churn for no measurable gain.

## Phase 1 — shipped

### Parallelism cap (`PARALLEL ?= -parallel 4` in the Makefile)

The Go default is `GOMAXPROCS` (32 here), which is actively counterproductive:
each `go list` internally saturates every core, so 32 concurrent tests thrash.

| | wall | sys |
| --- | --- | --- |
| `-parallel 32` (default) | 292s | 1443s |
| `-parallel 8` | 259s | 808s |
| `-parallel 4` | **232s** | 636s |

A test that takes 1.46s in isolation takes 52.4s under the default fan-out — **36×**.
`sys` time exceeding `user` time was the tell.

Do not raise this without re-measuring.

### CLAUDE.md "Test performance" section

Records the inner-loop rule (run the single affected test, ~1.5s; run `make ci`
once at final review), the Module-open cost model, and the parallelism cap
rationale.

## Phase 2 — Lever B: stop re-hashing the toolchain (recommended first)

### The finding

`golauncher.inspect` SHA-256s an entire binary on every call. `Launcher.Validate`
inspects **both** the `go` command (14.5MB) and the `compile` binary (24.6MB), and
`Module.validateGoCommandContext()` is called **twice** around every
`packages.Load` (once before, once after).

**A single `gsx generate` on a one-component module performs 18 full hashes** —
roughly 352MB of SHA-256 for one generate. Across the test suite it is on the order
of 90GB.

This is not a test problem. It is production latency in `gsx generate`, and
therefore in every `gsx dev` rebuild and every LSP analysis.

### Probe result

A prototype memoising `inspect` on stat identity (`os.SameFile` + size + mtime),
with everything else unchanged:

| | before | after |
| --- | --- | --- |
| single `gsx generate` (×10) | 3.36s | **2.53s** (−25%) |
| `internal/codegen` tests | 260s | **118.9s** (−54%) |
| `gen` tests | 264s | **174.3s** (−34%) |
| full suite (with `-parallel 4`) | 217s | **176s** |

The full suite passed, **including `internal/golauncher`'s own change-detection
tests** — the scenarios they exercise (rewriting the `go` script) change size and
mtime, so the memo correctly misses.

### The decision to make

Re-hashing detects one thing that a stat check does not: an **in-place content
rewrite that preserves the inode**. The existing code already checks `os.SameFile`
(dev+inode) separately, so the digest comparison is *only* buying that case.

Adding `ctime` to the memo key closes it in practice: any write updates ctime, and
ctime cannot be back-dated without root — and a root-level attacker can replace the
toolchain wholesale anyway. But this is a real, if narrow, weakening of a
deliberate integrity guarantee, so it is a decision to take explicitly rather than
a change to slip in for speed.

Options, in preference order:

1. **Memoise on stat identity (path, dev, inode, size, mtime, ctime).** Smallest
   change, delivers the numbers above, keeps the digest as the seal-time capture.
2. **Hash once at seal, validate by stat thereafter.** Arguably what the design
   always meant: `validateLive`'s job is "has it changed *since capture*", and stat
   identity answers that. Removes the re-hash entirely rather than caching it.
3. **Reduce the call count.** 18 hashes for one generate is high independent of
   per-call cost — `Capture` + `Seal` + 2 validations per load × 2 binaries. Worth
   auditing even if 1 or 2 lands.

Options 1 and 3 compose.

## Phase 2 — Lever A: amortise the runtime closure

The larger and harder lever: `parseFiles` + `checkFiles` is ~36% of samples in both
packages (~144s in `gen`, ~157s in `codegen`), all of it re-deriving the same
205k-line gsx runtime closure 803 times.

The seam already exists. `Options.Bundle` + `internal/typebundle` run codegen with
**no `packages.Load` and no subprocess** — built for the WASM playground. One
bundle built per test binary, with test Modules loading only their own tiny
package, would remove most of it.

**The cost:** bundle mode is a *different code path* from production. Moving
hundreds of tests onto it means they stop exercising the real loader — precisely
the integration surface `gen`'s e2e tests exist to cover. A partial move (bundle
for tests that only need types to resolve; real loads retained for tests about
loading) is likely the right shape, but it needs its own design pass.

Note that the previous rejection of fixture-sharing (2026-07-09: "poor payoff, 64
files of churn") was decided when `make ci` was 61s. The payoff side has since
changed by ~5×. The isolation objection stands and still needs answering.

## Not a lever: the sleeps

Investigated per request. `gen`'s 208s serial floor is **not** sleep-bound:

- 44 `time.Sleep` call sites totalling **21.3s** of fixed sleeps — ~10% of the floor.
- The largest fixed sleeps (4s, 3s, 2×2.5s, 2s) are *quiesce* waits that assert an
  **absence** (e.g. "the front door gave up and posts nothing further"). Shortening
  them weakens the assertion; they cannot be made event-driven, because the event
  being waited for is that nothing happens.
- The polling loops already exit early on their condition.
- The single largest serial test, `TestRealisticCacheInvalidation` (35.1s, 14
  cases, **zero sleeps**), profiles as 30% `golauncher.inspect` and 16%
  `packages.Load` — i.e. it is Lever A + Lever B, not timers.

Conclusion: fix Levers B and A; leave the dev-loop timing tests alone. Their
serial-ness is also load-bearing (real-timer deadlines flake under parallel load —
see the 2026-06-26 parallelism work).

## Method note

The instrumentation that found all of this, reusable next time:

- Per-package wall: `go test ./... -count=1` and read the `ok pkg Ns` column.
- Per-test: `-json`, aggregate `Elapsed` on `pass` actions for names without `/`.
- **Contention check:** run one heavy test alone and compare. 1.46s vs 52.4s is the
  whole oversubscription story in one number.
- **Serial floor:** classify tests by whether their body opens with `t.Parallel()`,
  then sum each group. Parallelism can never touch the serial sum.
- Load count/signatures: a temporary atomic counter + map at the `packages.Load`
  site in `module.go`, dumped from `TestMain`.
- Cost centres: `-cpuprofile`, then `pprof -top -cum`.
- `sys` > `user` in `/usr/bin/time -p` means oversubscription, not compute.

A PATH shim that logs and re-execs `go` **breaks the build cache key** and fails
tests — use it for counting subprocesses only, never for timing.
