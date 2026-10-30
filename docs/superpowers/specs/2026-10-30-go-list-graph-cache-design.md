# Persistent `go list` graph cache

## Problem

Every `gsx generate` with a usable cache runs
`go list -deps -json -compiled -overlay=… <patterns>`
(`gen/cachekey.go` `loadGraphWithContext`, called from `prepareCache`) to
build the package graph that cache keys are computed from. A warm run in eos
(639 packages, 1,352 in the graph) does nothing else expensive. It costs
about 1.3s idle, and `go list` is roughly 3s of an 8s run under load. Many agent
worktrees run `generate` repeatedly, often with no edit that could change the
graph in between.

`go list` has no cache of its own across invocations
([golang/go#23803](https://golang.org/issue/23803)). gopls has the same
problem and solves it by keeping package metadata and re-running `go list`
only when a file's package clause or imports change, or go.mod changes. This
design applies that approach to the generate cache.

## Goal

Reuse a previously computed graph when every input it depends on is unchanged,
and run `go list` otherwise. The decision must be exact: a reused graph is
identical, in every field gsx reads, to what `go list` would return now. Cache
keys, generated output and the corpus must not change.

Non-goals: caching `packages.Load` on a miss; the `gsx dev`/LSP warm worlds,
which do not go through `prepareCache`; `go env` capture (~0.2s); changing
`GODEBUG` for child `go` processes (see Open questions).

Anti-goal: any validity check that can return a stale graph. A false miss is
acceptable; a false hit is a correctness bug. That includes a hit where a live
`go list` would now *fail*, or return a graph the projection would reject:
today either makes the run bypass the cache, so a hit there would restore
output the live run would not.

## What the graph depends on

The projection reads `ImportPath`, `Dir`, `Imports`, `GoFiles`,
`CompiledGoFiles`, `CgoFiles`, `Standard` and `Module` (`internal/sourceview/cache.go`).
`Deps` is decoded and cloned but never read. A failed `go list` already makes
the run bypass the cache, so a stored graph is always error-free.

Those fields are a function of:

1. **The Go command context**: `CacheFingerprint()` (effective env including
   GOFLAGS, GOOS/GOARCH, tags, launcher and compiler identity). It already
   refuses GOWORK and vendoring, and refuses a GOFLAGS `-modfile`: the
   alternate module file is outside the manifest. The key also folds in the
   gsx binary identity, so a new binary never reuses an older one's entries.
2. **The query**: the pattern list from `graphQueryPatterns` and the overlay
   (each overlay path and its bytes; these are the paired-output and sentinel
   replacements from the manifest).
3. **Main-module go.mod and go.sum bytes.** With 1 and 3 fixed, every
   non-main, non-directory-replaced package (stdlib, module cache) is
   immutable, which is the assumption the existing generate cache already
   makes.
4. **For every main-module directory the graph contains** (testdata included):
   - the names of all its regular files. A directory containing non-Go source
     files that go/build reads (`.c .h .s .S .cc .cpp .cxx .hh .hpp .hxx .m
     .f .F .for .f90 .swig .swigcxx`) is uncacheable, as is any directory with
     a non-test `import "C"`: their headers are not in the manifest and the
     cgo `#include` set is unbounded;
   - each `.go` file's *header*: its bytes from the start through the end of
     its last import declaration (or the package clause when there are no
     imports). That covers build constraints, the package name, imports and
     `import "C"`. `_test.go` names and headers are witnessed too: a
     mismatched package, a broken import or `import "C"` in a test file fails
     `go list -deps`. If the file does not parse in `ImportsOnly` mode, the
     whole file is the header;
   - embeds: the `//go:embed` patterns are resolved the way cmd/go does over
     the walked listings (only matched directories are walked; `.` and `_`
     names are skipped unless the pattern has `all:`), and the matched names
     and kinds are hashed. A pattern that would resolve into an unlisted or
     skipped directory makes the directory uncacheable. `EmbedFiles` entries
     are re-checked with `Stat` on a hit;
   - whether the directory, and each ancestor up to the module root, contains
     a `go.mod`. A new nested module changes ownership.

   Overlaid paths (paired outputs, sentinels) are witnessed by their overlay
   bytes, which are already in the graph key, not by their disk bytes, so a
   regeneration does not cause a miss. The entry is stored only when both the
   witness and the graph key are unchanged across `go list`.
5. **For every graph package whose import path lies under the main module's
   path but resolves to another module**, the absence of the corresponding
   main-module directory, with the same `go.mod` chain. Creating that
   directory makes the import ambiguous and fails a live `go list`.

Bodies below the imports (other than `//go:embed` lines) cannot change any
field gsx reads, nor make `go list` fail.

## Design

**Module-scoped, not path-scoped.** The graph's content is a function of the
module (items 1–5), not of where it is checked out: the same module content at
two paths produces the same graph, differing only in the main-module root
prefix of `Dir`, `GoFiles`, `CompiledGoFiles`, `CgoFiles` and `Module.Dir`/`GoMod`.
Everything else that is absolute (GOROOT, GOMODCACHE paths) is fixed by the
context fingerprint, which already covers the full `go env` except `GOMOD`.
So, like the existing generate cache (`TestCacheSharedAcrossCheckoutPaths`),
the graph cache is shared across checkouts:

- **Every main-module path is stored relative to the module root.** That
  covers the graph fields, the witness, and the overlay paths in the key.
- **On a hit it is rebased onto `filepath.Dir(GOMOD)`** from the run's captured
  Go command context. `go list` spells main-module paths from the root the `go`
  process derives from its working directory: the symlinked spelling when
  `PWD` names it, the resolved one otherwise. `go env GOMOD`, captured in the
  same context, uses the same spelling, so the rebase reproduces what a live
  `go list` would print. The differential test pins this with a symlinked
  checkout.

**Graph key.** A SHA-256 over the context fingerprint, the module path, the
patterns, the overlay (relative paths and bytes), and go.mod/go.sum. Note that
the overlay includes sentinel and paired paths, so this key changes when the
set of GSX packages changes. Checkouts of one module in different states share
a key. The key therefore holds a small fixed number of variants (my call:
4, most recently used), each with its own witness. A lookup tries each
variant's witness.

**Witness digest.** A SHA-256 over items 4 and 5, in sorted order. It is
computed from the manifest: `helperGoFiles` already holds every owned `.go`
file's bytes. A header parse of all 9,965 eos `.go` files takes 27ms. The
manifest walk must additionally record, per walked directory, its full file
name list and its nested-`go.mod` boundary; it keeps only `.go` and `.gsx`
paths today.

A main-module directory in the graph that the walk did not cover (for example a
symlinked directory, which `WalkDir` does not follow) makes the graph
uncacheable for that run.

**Entry.** Stored in the existing cache directory under the graph key (same
atomic temp-file-and-rename store as `storePut`). It contains:
- a format version;
- the witness directory list and the witness digest;
- the graph, reduced to the fields above. Raw eos `go list` output is 15 MB,
  so store only what gsx reads.

**Lookup.** In `prepareCache`, before `loadGraphWithContext`:
1. Compute the graph key; read its variants.
2. For each variant, recompute the witness digest over that variant's own
   witness directory list, from the current manifest. A listed directory that
   no longer exists means that variant does not match.
3. A matching digest is a hit: decode that variant's graph, rebase it, and skip
   `go list`.
4. Anything else is a miss. Run `go list` as today, then store a new entry
   whose witness list is taken from the new graph.

**The stored witness must match what `go list` read.** The manifest is
snapshotted before `go list` runs, and `go list` reads non-overlaid files from
disk later. If a file changes in between, pairing the earlier witness with the
later graph would let a later return to the earlier state hit a graph that does
not describe it. So on a miss, after `go list`, re-read the witnessed files
and store the entry only if the recomputed digest equals the manifest's. A
content re-read misses an edit reverted while `go list` ran, so the store also
requires unchanged stat identity (inode, size, mtime, ctime) of every listed
directory, every listed Go file and `go.mod`/`go.sum`, taken just before and
after `go list`. This is extra work on misses only; hits stay read-free.

Why the stored witness list is sufficient: any change that could add a
main-module package to the graph must add an import to a file that is already
in a witnessed directory, change a witnessed directory's file set, or change
the query (patterns or overlay, part of the graph key). A newly created
directory that nothing imports is not in the graph and cannot affect it.

**Uncacheable cases keep running `go list` every time**: the context
fingerprint errors, as today; or the graph contains any directory-replaced
(mutable) module. Those trees are not in the manifest, so witnessing them would
need extra reads. That is phase 2 if it is ever needed; eos has none.

**Reporting.** `-v` prints `gsx cache: <module root>: graph hit` or
`graph miss (<reason>)`, with `, stored` when the miss stored an entry.
Reasons: no entry, witness changed, decode error, uncacheable (with its
reason). This measures the real hit rate in eos before
any phase 2 work.

## Phase 2 (not now; decide from the phase 1 hit rate)

Scoped re-list, as gopls does: when only some witness directories changed,
run `go list -deps` for just those packages and merge their closure into the
stored graph instead of re-listing everything. It needs a rule for packages
that stop being reachable and for `Imports` of unchanged packages. Witnessing
directory-replaced modules would also go here.

## Verification

- **Differential test (the gate).** For a table of edits on one fixture
  module, a cached lookup must either miss or return a graph equal to a live
  `go list` in every field gsx reads. Edits to cover:
  - an import added, removed or reordered;
  - a build tag changed;
  - a file added, deleted or renamed, including a `_GOOS.go` suffix;
  - the package clause changed;
  - a nested `go.mod` added;
  - go.mod and go.sum edited;
  - a new GSX package (changes the overlay);
  - body-only edits, which must hit;
  - a header that fails to parse;
  - a `.c` or `.swig` file added to a non-cgo package;
  - a `//go:embed` target deleted, and the embed pattern edited;
  - a main-module directory created under the path of a required nested
    module, which makes the import ambiguous;
  - a file edited between the manifest snapshot and `go list`, which must not
    store an entry.

  - the same module at a second checkout path and through a symlinked root:
    must hit and rebase to exactly the live `go list` paths.

  One fixture module shared by the whole table keeps the cost to one
  `packages.Load`-free `go list` per row.
- The corpus and `make ci` pass unchanged.
- eos: `-v` shows `graph hit` on repeated warm runs, and the `.x.go` output is
  byte-identical.
- An independent adversarial review that tries to construct a false hit.

## Open questions

- **`GODEBUG=#goindex=0`.** It made eos `go list` 25–35% faster under load,
  matching the module-index slowdown in
  [golang/go#80273](https://github.com/golang/go/issues/80273). Setting it for
  child `go` processes is a toolchain workaround; decide separately.
- **Entry eviction.** Keys accumulate across Go versions, configs and GSX
  package sets (variants are capped per key). The existing cache has no
  eviction either; follow whatever policy it adopts.

## Left to the builder

The entry encoding, where header extraction lives (`sourceview`, next to
`Inspect`), and the exact shape of the reduced graph type.
