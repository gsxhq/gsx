# Performance

gsx streams rendered HTML directly to an `io.Writer`.

## Reproduce the snapshot

These results come from
[`gsx-bench` commit 3b701ca8](https://github.com/gsxhq/gsx-bench/commit/3b701ca8dbfe163502da8eead3ff36c3661c6a11),
paired with
[`gsx` commit c66db91b](https://github.com/gsxhq/gsx/commit/c66db91bf5c8e686973375e99d8aec6838da4bfb)
and recorded on 2026-07-01. Keep the checkouts side by side:

```sh
git clone https://github.com/gsxhq/gsx
git clone https://github.com/gsxhq/gsx-bench
git -C gsx checkout c66db91b
git -C gsx-bench checkout 3b701ca8
cd gsx-bench
go test -bench Pooled -benchmem -run '^$' .
```

Snapshot environment: Apple M3 Ultra, Go 1.26.1, and templ v0.3.1020. The
destination is a warm pooled `bytes.Buffer`, matching a buffered HTTP handler.

## Small template

| engine | time | allocations |
| --- | --- | --- |
| **gsx** | **270 ns** | **2** |
| [templ](https://templ.guide) | 394 ns | 10 |
| `html/template` | 1428 ns | 24 |

## Component-heavy page

This case renders 20 rows with nested components and utility classes.

| engine | time | allocations |
| --- | --- | --- |
| **gsx** | **4.7 µs** | **62** |
| templ | 6.8 µs | 204 |

## Escaping-heavy content

| engine | time | allocations |
| --- | --- | --- |
| **gsx** | **3.6 µs** | **1** |
| templ | 6.6 µs | 143 |

These figures are machine-, version-, and workload-specific. Run the suite on
your own templates and deployment hardware before making performance decisions.
