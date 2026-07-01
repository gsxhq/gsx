# Performance

gsx renders by streaming HTML straight to your `io.Writer` — no intermediate
document, no per-component buffer pool. Generated code is direct write calls, and
the escaper writes safe runs in place, so rendering allocates very little.

## Reproduce

The benchmark source lives at [github.com/gsxhq/gsx-bench](https://github.com/gsxhq/gsx-bench).

```sh
git clone https://github.com/gsxhq/gsx-bench
cd gsx-bench
go test -bench . -benchmem
```

The numbers below are a snapshot from Apple M3 Ultra with Go 1.26.1. Treat them as directional; use the command above on your hardware for local decisions.

## Numbers

Apple M3 Ultra, Go 1.26.1, rendering into a pooled `bytes.Buffer` (as an HTTP
handler would). Lower is better.

The same small template through all three engines:

| engine | time | allocs |
| --- | --- | --- |
| **gsx** | **266 ns** | **2** |
| [templ](https://templ.guide) | 390 ns | 10 |
| `html/template` | 1412 ns | 24 |

A realistic, component- and class-heavy page (20 rows, nested components):

| engine | time | allocs |
| --- | --- | --- |
| **gsx** | **4.7 µs** | **61** |
| templ | 6.7 µs | 204 |

Escaping-heavy content (bodies full of `< > & " '`) — gsx's `html/template`-derived
escaper never allocates:

| engine | time | allocs |
| --- | --- | --- |
| **gsx** | **3.7 µs** | **1** |
| templ | 6.5 µs | 143 |

## Notes

- In this benchmark snapshot, gsx is faster than `html/template` and templ with
  fewer allocations.
- Numbers are machine- and version-specific.
