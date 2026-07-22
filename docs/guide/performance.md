# Performance

gsx streams rendered HTML directly to an `io.Writer`. These figures describe
specific workloads, not a universal renderer ranking.

## Reproduce the snapshot

The current snapshot was recorded on 2026-07-21 from `gsx-bench` commit
`8ca640ab42038917ae389177a239e467fa22816c`, paired with `gsx` commit
`521d0a9f33d6959170618c057b16562ff195713a`. Both worktrees were clean and the
benchmark module resolved its local `github.com/gsxhq/gsx` dependency to that
exact sibling core checkout.

```sh
git clone https://github.com/gsxhq/gsx
git clone https://github.com/gsxhq/gsx-bench
git -C gsx checkout 521d0a9f33d6959170618c057b16562ff195713a
git -C gsx-bench checkout 8ca640ab42038917ae389177a239e467fa22816c
cd gsx-bench
GOMAXPROCS=32 go test -run '^$' -bench . -benchmem -count=10 . > results.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d results.txt
```

Snapshot environment: Apple M3 Ultra (`darwin/arm64`), Go 1.26.1,
`GOMAXPROCS=32`, and templ v0.3.1020. Values below are ten-sample benchstat
medians. The destination is a warm pooled `bytes.Buffer`, matching a buffered
HTTP handler. The exact raw output is
`/private/tmp/gsx-runtime-folded-results.VSlXYp/final-full-suite.txt` (SHA-256
`71d66c11f805d6fa7990eb4bc5017c143e581f6ab77b6884ff069930930ea431`),
with its pinned benchstat summary at
`/private/tmp/gsx-runtime-folded-results.VSlXYp/final-full-suite.benchstat.txt`.

## Small template

`Document` renders a small static/dynamic document.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **330.2 ns ±3%** | **56 B** | **2** |
| [templ](https://templ.guide) | 561.4 ns ±12% | 362 B | 10 |
| `html/template` | 1.817 us ±12% | 642 B | 24 |

## Component-heavy page

`Page` renders 20 rows with nested components and utility classes.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **5.574 us ±1%** | **2,563 B** | **62** |
| templ | 8.119 us ±13% | 4,976 B | 204 |

## Post-snapshot GSX-only direct-component update

The all-engine snapshot above remains the 2026-07-21 comparison at its pinned
commits; it has not been silently refreshed. A later GSX-only before/after run
measured one code-generation change: a proven same-package plain GSX child now
renders through a private helper into the parent writer. Public component
factories still return `gsx.Node`, and imported, method, plain-Go,
package-variable, and dynamic calls keep the normal `Writer.Node` path.

For example, the authored component call is unchanged. The generated call for
an eligible child is now equivalent to:

```go
_gsxgw.NodeResult(_gsxrenderCard(ctx, _gsxgw, row, nil))
```

`Table` renders 20 such `Card` children. Ten counterbalanced process pairs on
the same machine and Go version measured:

| destination | before | direct | time | bytes | allocations |
| --- | ---: | ---: | ---: | ---: | ---: |
| pooled buffer | 2.212 us | 1.895 us | **-14.33%** | 1,955 to 32 (**-98.36%**) | 21 to 1 (**-95.24%**) |
| discard | 1.877 us | 1.543 us | **-17.80%** | 1,952 to 32 (**-98.36%**) | 21 to 1 (**-95.24%**) |

All six improvements had `p < 0.001`. The wider GSX-only screen also measured
Page pooled at -5.89%, Page parallel at -53.86%, ForwardedAttrs pooled at
-2.36%, and Buttons pooled at -9.16%. Unaffected fallback allocation counts
were unchanged. These numbers compare GSX before and after the direct-component
change; they are not a new GSX-versus-templ snapshot.

## Escaping-heavy content

`Comments` renders 20 hostile text strings through the HTML escaper.

| engine | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| **gsx** | **4.152 us ±0%** | **32 B** | **1** |
| templ | 10.47 us ±6% | 9,094 B | 143 |

## Forwarded and folded attributes

These GSX-only acceptance surfaces render 20 links with a six-entry attribute
bag. `FoldedAttrs` also combines a static class and one selected conditional
class per link.

| workload | time | bytes | allocations |
| --- | ---: | ---: | ---: |
| ForwardedAttrs | 14.28 us ±1% | 2,916 B | 81 |
| FoldedAttrs | 19.54 us ±8% | 11,810 B | 161 |

The folded workload's allocation profile motivated a direct-accumulator
codegen experiment. That experiment was rejected: it cut allocations by 12.42%
but increased bytes by about 27% and did not improve end-to-end render time.

These figures are machine-, version-, destination-, and workload-specific.
Run the suite on your own templates and deployment hardware before making
performance decisions.
