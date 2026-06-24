# gsx docs playground — render API + Cloud Run deploy (design)

**Status:** design · **Date:** 2026-06-24

## Goal

An interactive playground on the docs site (<https://gsxhq.github.io/>) where a
visitor edits a `.gsx` component and sees it **compile and render live**, through
the *real* gsx pipeline. It doubles as the home for the language's worked
examples: the playground's presets are the canonical examples, and they are
verified in CI so the docs cannot drift from the implementation.

This supersedes the removed `examples/` folder: instead of static `.gsx` files
that go stale, examples live as playground presets backed by an authentic,
CI-checked render.

## Context / what already exists (the prototype)

A working local prototype is committed:

- **Backend** — `playground/server` (gsx repo): a `POST /render` HTTP API that
  runs the real pipeline (`gsx generate --json` → `go build`/run) over a prepared
  fixed module and returns `{ html, generatedGo, diagnostics }`.
- **Frontend** — `gsxhq.github.io` `.vitepress/theme/GsxPlayground.vue`: a
  full-bleed VitePress workspace (CodeMirror editor, tabbed output, presets,
  light/dark, shareable `#try=` URLs) that calls the API
  (`VITE_GSX_PLAYGROUND_API`, default `http://localhost:8088`).

This slice productionizes that prototype and deploys the backend to Cloud Run.

## Decisions (with rationale)

1. **Authentic server-side render — not an interpreter, not static output.**
   Spiked both. A yaegi-in-WASM client render reproduced only **39/45** corpus
   cases and, critically, *diverged on security-sensitive escaping* (JS/CSS/JSON
   contexts) — unacceptable for a tool that teaches the language. The authentic
   pipeline reproduced **148/150** (the 2 are multi-package harness gaps, not
   pipeline failures). Fidelity wins.

2. **Cloud Run, free tier.** Scale-to-zero (≈$0 idle, fits docs traffic) and a
   **gVisor sandbox by default** — the main reason running visitor-supplied code
   is acceptable. Free tier forbids a pinned warm instance, so **cold starts are
   the one rough edge** and the design must absorb them (baked `GOCACHE`).

3. **The "no shell-out in codegen/runtime" principle does not bind the docs
   server.** That principle governs the *shipped gsx CLI/library*. The docs
   server is separate infrastructure and may use the Go toolchain (`go list`,
   `go build`) freely — like tests. Toolchain-free codegen was investigated
   (vendoring `go list` internals) and rejected as out of scope here: huge,
   high-maintenance, and it wouldn't help the browser anyway. It remains a future
   option for a fully-offline WASM playground; the yaegi spike is the client-side
   render fallback if we ever want one.

## Architecture

```
Browser (static VitePress, GitHub Pages)
  GsxPlayground.vue ──POST /render {gsx, invoke}──▶ Cloud Run service (Go)
                    ◀── {html, generatedGo, diagnostics, ms} ──   gVisor sandbox
                                                                  real gsx + go toolchain
```

- **Frontend** stays static. Only *editing* calls out; reading the docs needs no
  server. Built with `VITE_GSX_PLAYGROUND_API` set to the deployed URL.
- **Backend** is a single stateless Cloud Run service holding a *prepared fixed
  module*; each request renders one component.

### API contract

`POST /render`

```jsonc
// request
{ "gsx": "package views\n\ncomponent ...", "invoke": "Greeting(GreetingProps{...})" }
// response
{
  "html": "<p>…</p>",                 // rendered output ("" on error)
  "generatedGo": "// Code generated…", // the .x.go ("" on error)
  "diagnostics": [                     // structured, from `gsx generate --json`
    { "severity": "error", "message": "code: msg", "line": 4, "column": 13 }
  ],
  "error": "",                         // operational error (non-diagnostic)
  "ms": 652                            // server-side render time
}
```

### Safety model

The thing that makes running visitor code acceptable:

- **Fixed module.** The visitor supplies *only* a component body + an invoke
  expression. `go.mod` is fixed (gsx + stdlib), written by the server — there is
  no untrusted `go.mod`, no arbitrary dependency, no module fetch.
- **gVisor** (Cloud Run default) — syscall isolation per request.
- **No network** in the render step; **request timeout** (~10s); **CPU/memory
  caps**; **input size cap** (64 KB); **client debounce** + **server rate limit**.
- The package clause is normalized to `package views`; output is captured from a
  fixed runner `main` that renders the invoke expression.

### Free-tier fit & performance

Measured on the prototype (local, warm instance):

| Scenario | Latency |
|---|---|
| cache-warm re-render | ~250 ms |
| realistic edit (new content, GOCACHE warm) | ~600–700 ms (dominated by `go list`) |
| cold start (first edit after scale-to-zero) | a few seconds |

CPU per render (~0.5–1 vCPU-s warm) sits comfortably inside the free-tier
allowance for docs-level traffic. The binding constraint is *cold-start latency*,
mitigated by **baking a warm `GOCACHE` + pre-resolved module into the image** so
even a cold instance only compiles the visitor's one changed file.

## Productionization gaps (prototype → production)

1. **Per-request isolation.** Prototype serializes on one shared work dir behind
   a mutex. Production: a small pool of pre-warmed module dirs (or a temp copy per
   request) so concurrent requests don't clobber, while keeping `GOCACHE` shared.
2. **Container image.** Dockerfile: Go toolchain + a built `gsx` + the prepared
   module + **pre-populated `GOCACHE`** (build the fixed module at image-build time).
3. **Request limits.** Enforce timeout, body-size, max concurrency per instance,
   and basic rate limiting / abuse guards at the edge.
4. **Observability.** Structured logs + request timing; surface compile vs run vs
   timeout outcomes.
5. **Frontend wiring.** Site CI build sets `VITE_GSX_PLAYGROUND_API` to the
   deployed URL; local dev keeps `localhost:8088`.
6. **CI example check.** The playground presets are mirrored as corpus cases (or a
   dedicated test) so a preset that stops compiling/rendering fails CI.

## Out of scope (future)

- Toolchain-free / offline WASM playground (yaegi client render).
- tree-sitter-gsx syntax highlighting in the editor (currently JSX approximation).
- Multi-file / multi-component editing; package imports beyond stdlib + gsx.
- Short-link sharing (current `#try=` base64 is fine for now).

## Open questions

- **Hosting region / project** for Cloud Run (free tier is per-region quota).
- **Concurrency model**: pre-warmed pool size vs temp-dir-per-request — decide
  with a load measurement during implementation.
- **Where the backend lives long-term**: stays in the gsx repo (`playground/`) vs
  its own repo. Default: keep in gsx repo for now.

## Testing strategy

- Backend unit/integration: table of `{gsx, invoke}` → expected `{html|diags}`,
  reusing corpus fixtures; assert the safety caps (oversize input, timeout).
- Preset validity: every frontend preset renders clean in CI.
- Manual: the existing browser verification (render, diagnostics, escaping,
  light/dark, share round-trip) as a smoke checklist.
