# Performance

gsx streams rendered HTML directly to an `io.Writer`. These figures describe
specific workloads, not a universal renderer ranking.

## Reproduce the snapshot

The current snapshot was recorded on 2026-08-17 from `gsx-bench` commit
`26b68db36773cc0f5f336f51d5f30388220b3f98`, paired with `gsx` commit
`cb5ec7c2b477a0fa6c6690eead3f815dd923ce64`. Both worktrees were clean and the
benchmark module resolved its local `github.com/gsxhq/gsx` dependency to that
exact sibling core checkout.

```sh
git clone https://github.com/gsxhq/gsx
git clone https://github.com/gsxhq/gsx-bench
git -C gsx checkout cb5ec7c2b477a0fa6c6690eead3f815dd923ce64
git -C gsx-bench checkout 26b68db36773cc0f5f336f51d5f30388220b3f98
cd gsx-bench
GOMAXPROCS=32 go test -run '^$' -bench . -benchmem -count=10 . > results.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d results.txt
```

Snapshot environment: Apple M3 Ultra (`darwin/arm64`), Go 1.26.1,
`GOMAXPROCS=32`, and templ v0.3.1020. Values below are ten-sample benchstat
medians. The destination is a warm pooled `bytes.Buffer`, matching a buffered
HTTP handler.

## Small template

`Document` renders a small static/dynamic document.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **275.5 ns ±1%** | **56 B** | **2** |
| [templ](https://templ.guide) | 423.8 ns ±7% | 362 B | 10 |
| `html/template` | 1.457 us ±2% | 643 B | 24 |

## Component composition

`Table` renders 20 rows, each through a `Card` child component. A same-package
child renders straight into the parent's writer, so the loop costs one
allocation in total.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **1.834 us ±1%** | **32 B** | **1** |
| templ | 5.235 us ±1% | 4,816 B | 183 |

## Component-heavy page

`Page` renders a full document with 20 rows of nested components, a dynamic URL
per row, and utility classes on every component root.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **4.368 us ±1%** | **634 B** | **42** |
| templ | 7.098 us ±2% | 4,978 B | 204 |

## Escaping-heavy content

`Comments` renders 20 hostile text strings through the HTML escaper.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **3.788 us ±1%** | **32 B** | **1** |
| templ | 7.237 us ±1% | 9,096 B | 143 |

These figures are machine-, version-, destination-, and workload-specific.
Run the suite on your own templates and deployment hardware before making
performance decisions.
