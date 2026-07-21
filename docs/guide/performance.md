# Performance

gsx streams rendered HTML directly to an `io.Writer`. These figures describe
specific workloads, not a universal renderer ranking.

## Reproduce the snapshot

The current snapshot was recorded on 2026-07-21 from `gsx-bench` commit
`8ca640ab42038917ae389177a239e467fa22816c`, paired with `gsx` commit
`5c62bf7760c7a8aae369197e3de2160354968c25`. Both worktrees were clean and the
benchmark module resolved its local `github.com/gsxhq/gsx` dependency to that
exact sibling core checkout.

```sh
git clone https://github.com/gsxhq/gsx
git clone https://github.com/gsxhq/gsx-bench
git -C gsx checkout 5c62bf7760c7a8aae369197e3de2160354968c25
git -C gsx-bench checkout 8ca640ab42038917ae389177a239e467fa22816c
cd gsx-bench
GOMAXPROCS=32 go test -run '^$' -bench . -benchmem -count=10 . > results.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d results.txt
```

Snapshot environment: Apple M3 Ultra (`darwin/arm64`), Go 1.26.1,
`GOMAXPROCS=32`, and templ v0.3.1020. Values below are ten-sample benchstat
medians. The destination is a warm pooled `bytes.Buffer`, matching a buffered
HTTP handler. The exact raw output is
`/private/tmp/gsx-runtime-spread-results.wn175d/final-full-suite.txt` (SHA-256
`9f8f5ffa14bc54ba1075e075745c95e13d95c0a221f5750691c5871d86da0d88`).

## Small template

`Document` renders a small static/dynamic document.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **280.2 ns ±1%** | **56 B** | **2** |
| [templ](https://templ.guide) | 414.9 ns ±0% | 362 B | 10 |
| `html/template` | 1.514 us ±9% | 642 B | 24 |

## Component-heavy page

`Page` renders 20 rows with nested components and utility classes.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **4.862 us ±2%** | **2,563 B** | **62** |
| templ | 7.093 us ±3% | 4,976 B | 204 |

## Escaping-heavy content

`Comments` renders 20 hostile text strings through the HTML escaper.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **3.761 us ±1%** | **32 B** | **1** |
| templ | 7.206 us ±2% | 9,094 B | 143 |

## Forwarded and folded attributes

These GSX-only acceptance surfaces render 20 links with a six-entry attribute
bag. `FoldedAttrs` also combines a static class and one selected conditional
class per link.

| workload | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| ForwardedAttrs | 12.69 us ±0% | 2,916 B | 81 |
| FoldedAttrs | 18.01 us ±7% | 11,811 B | 161 |

The folded workload's allocation profile is the basis for a separate measured
codegen experiment; it is not evidence that an unmeasured rewrite is faster.

These figures are machine-, version-, destination-, and workload-specific.
Run the suite on your own templates and deployment hardware before making
performance decisions.
