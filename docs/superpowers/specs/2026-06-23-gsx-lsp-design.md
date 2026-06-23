# gsx LSP — Design

**Status:** approved design, slice 1 ready to plan
**Date:** 2026-06-23
**Topic:** A full Language Server for `.gsx`, in-process on `go/types` (not a gopls proxy)

## 1. Goal & non-goals

Build a real Language Server for `.gsx` files, shipped as a long-lived `gsx lsp`
subcommand. It must give editors Go-aware intelligence over the Go embedded in
templates (component params, `{expr}` interpolations, `GoChunk`s) without
proxying gopls.

**Non-goals:** this is not a gopls proxy and not a gopls plugin. We do not shell
out to gopls, do not depend on a gopls being installed, and do not couple to a
gopls version. We also do not write `.x.go` to disk from the LSP — code
generation stays the job of `gsx generate`; the LSP is read-only analysis.

## 2. Why not the proxy approach (research summary)

Every comparable system — templ, Volar (Vue/Astro/Svelte/MDX), Razor — rests on
the same primitive: generate a host-language artifact from the DSL, emit the
user's embedded expressions as real host-language text at stable mapped offsets,
and keep a bidirectional position map. They differ only in **where the host
type-checker lives**:

- **(a) Proxy** to an external host server (templ → gopls; Razor's middle era).
- **(b) In-process** — embed the host type-checker as a library (Volar runs the
  TypeScript service in-process; Razor's current "cohosting" runs Roslyn
  in-process).
- **(c) Native** — host compiler parses the DSL directly (TypeScript + JSX). Not
  available to us.

The industry is migrating (a) → (b): Vetur → Volar, Razor projection → cohosting,
for cross-file correctness, real diagnostics, and to avoid running N heavyweight
type-checkers.

templ's documented pain is **structural**, not incidental:

- gopls version coupling breaks the editor experience on either-side upgrades
  (a-h/templ #830, #1371).
- "no mapping → drop request / drop diagnostic" makes hover/definition silently
  dead in HTML regions and hides real generated-code errors (#85, #1201).
- completion only works when the parser emits a usable partial AST (#1086,
  #1102).
- import edits land in generated coordinates and get rebuilt with heuristics
  that corrupt the source (#1291).
- Windows `file://` URI escaping (#900, #1121).

### 2.1 The reframing: gsx is not in templ's position

templ proxies gopls because templ-the-generator cannot host Go intelligence.
**gsx-the-compiler already hosts it.** gsx already type-checks each package with
`go/packages` over skeleton overlays and harvests `types.Info`
(`internal/codegen/analyze.go`). For the public Go libraries, the relevant
`types.Info` maps are a near-direct fit for LSP features:

- `Types` (`map[ast.Expr]TypeAndValue`) → hover / type-of-expression.
- `Uses` / `Defs` (`map[*ast.Ident]types.Object`) → go-to-definition
  (`info.Uses[id].Pos()`) and find-references (object-identity scan).
- `Selections` → selector/member resolution.

So gsx belongs at the **in-process / cohosting end (pattern b)**, using
`go/types`-as-library instead of gopls-as-library. This matches the project's
stated philosophy ("no symbol resolver, lean on the Go compiler"). The mental
model maps cleanly onto the prior art:

| Prior art | gsx equivalent |
|---|---|
| Volar "virtual code" / Razor "design-time C#" | the **skeleton overlay** gsx already builds for codegen |
| Host type-checker (TS service / Roslyn) in-process | `go/types` + `go/packages`, which gsx already runs |
| `//line` directives (forward map, for diagnostics) | already emitted by codegen |
| Volar `CodeMapping` bidirectional map (hover/def) | reverse map — the one new piece, needed only when read features land (slice 2) |

### 2.2 The one genuine exception: completion

`go/types` only type-checks **complete, valid** code; at a cursor the source is
broken (`user.`). gopls's completion engine = AST-repair (the dangling-selector
`_`-insertion trick in `gopls/internal/cache/parsego/parse.go`, a 10-iteration
repair loop) + ranking heuristics — and **none of it is importable** (all under
`internal/`). go/types alone cannot do completion of partial expressions, and
there is no drop-in library for it.

**Decision:** completion is deferred to a later slice (see §6). Every other read
feature is a free `types.Info` query and ships first.

## 3. Architecture

`gsx lsp` is a long-lived process speaking LSP/JSON-RPC over stdio. It holds
parsed and type-checked state in memory and answers requests in-process. Five
components, each independently testable:

- **transport** — stdio JSON-RPC 2.0 framing (`Content-Length`) + request/notification dispatch.
- **server/session** — lifecycle (`initialize` / `initialized` / `shutdown` / `exit`), advertised capabilities, position-encoding negotiation.
- **workspace/documents** — in-memory `.gsx` buffers keyed by URI, version tracking, and dirty-file → package (directory) resolution.
- **analysis** — wraps the **existing** codegen pipeline (parse → `wsnorm` → `jsx.ResolveScripts` → skeleton build → `packages.Load` with overlay) but emits **no `.x.go` to disk**; it produces a `diag.Bag` (and, in later slices, the harvested type info + position map).
- **mapping** (slice 2+) — `.gsx` ↔ skeleton offset map plus LSP position encoding (0-based, UTF-16/UTF-8).

**Key reuse:** the edited, unsaved `.gsx` buffer feeds skeleton generation, and
the skeleton is injected through the `go/packages` **Overlay** gsx already uses
in `analyze.go` / `batch.go`. Open buffers shadow on-disk content; on-disk
`.x.go` files for other files in the package are shadowed by their skeletons, as
they already are during `gsx generate`.

## 4. Slice 1 — walking skeleton (server + diagnostics)

The smallest real increment: stand up the server loop and document lifecycle,
publish diagnostics from the existing `diag.Bag`. No `go/types` feature handler
and no reverse position map yet — diagnostics are already `.gsx`-positioned, so
they need only encoding conversion, not a reverse map.

### 4.1 Transport & protocol types

- Hand-rolled `Content-Length` framing over stdin/stdout and a small dispatcher
  keyed by JSON-RPC `method`.
- **Own LSP message structs, defined only for what we implement, growing per
  slice.** Slice 1 needs ~8: `initialize`, `initialized`, `shutdown`, `exit`,
  `textDocument/didOpen`, `textDocument/didChange`, `textDocument/didClose`,
  `textDocument/publishDiagnostics`.
- Rationale: matches gsx's minimal-dependency ethos and avoids pulling in a full
  protocol package (`go.lsp.dev/protocol`). The tool already depends on
  `golang.org/x/tools/go/packages`; we do not add more.
- Alternative considered and rejected for now: depend on `go.lsp.dev/protocol` +
  `go.lsp.dev/jsonrpc2`. Reconsider if hand-maintained types become a drag once
  many features land.

### 4.2 Lifecycle & capabilities

- `initialize`: negotiate **position encoding** — advertise `utf-8` and `utf-16`
  in `general.positionEncodings`; prefer `utf-8` if the client offers it, else
  fall back to `utf-16`. Store the chosen encoding on the session.
- Advertise slice-1 capabilities: `textDocumentSync` = **full** (open/change/close),
  diagnostics via push (`publishDiagnostics`). Nothing else yet.
- `shutdown` / `exit`: clean teardown.

### 4.3 Document lifecycle & incremental re-check

- `didOpen` / `didChange` (full-document sync) update the in-memory buffer and
  bump its version. `didClose` drops the buffer (reverting to on-disk).
- On change, resolve the file's package (its directory), then run the analysis
  component over that package using open-buffer overlays for any open `.gsx`
  files in it. Collect the `diag.Bag`.
- Debounce re-checks so a burst of keystrokes coalesces. (`go/packages.Load`
  with full type info is in the hundreds-of-ms range for a moderate package;
  debouncing keeps the editor responsive. Cross-package caching beyond
  debouncing is out of scope for slice 1 and tracked for a later perf slice,
  reusing the existing Tier 0/2 cache work.)

### 4.4 Publishing diagnostics

- Convert each `diag.Diagnostic` (1-based line/col, resolved `token.Position`) to
  an LSP `Diagnostic`:
  - line: 1-based → 0-based.
  - column: byte column → negotiated encoding offset, computed against the
    buffer's line text (UTF-16 code-unit count for `utf-16`; byte offset for
    `utf-8`). This single conversion layer is where templ's worst bugs lived;
    we centralize and test it.
  - `Severity` enum → LSP `DiagnosticSeverity`; `Code` → `code`; `Source` →
    `source`; `Message` (+ `Help`) → `message`.
- `publishDiagnostics` per file URI in the analyzed package; **clear** (publish
  empty) for files that previously had diagnostics and are now clean, so stale
  squiggles never linger.

### 4.5 Subcommand wiring

`gen.Main` grows an `lsp` subcommand alongside `generate` / `fmt` / `info`.
`gsx lsp` starts the server on stdio. Editors launch the project-local
`cmd/gsx` binary, so any project customizations are compiled in.

### 4.6 Testing

- Drive the JSON-RPC loop with scripted client messages and assert the resulting
  `publishDiagnostics` payloads — txtar-style fixtures, matching gsx's corpus
  testing culture. TDD from the first commit: a failing test that opens a `.gsx`
  with a known error and expects a specific diagnostic, before the handler
  exists.
- Unit-test the position-encoding conversion against multi-byte / non-ASCII
  lines for both `utf-8` and `utf-16`.

## 5. Slice 2 — read intelligence (designed, not yet built)

Build the reverse `.gsx` → skeleton position map (Volar-style: parallel
source/generated offset segments, with a per-segment capability flag so
generated glue is marked non-navigable). Then add, each as a `types.Info` query
mapped through it:

- **hover** — expression/identifier at cursor → type (and object docs).
- **go-to-definition** — `info.Uses[id].Pos()` → `fset.Position` → `Location`
  (see §5.1 for the cross-file strategy).
- **find-references** — object-identity scan over `types.Info` within loaded
  packages. (Cross-boundary references are partial; see §5.1.)
- **document symbols** — from the AST (components, etc.).
- **formatting** — wire `textDocument/formatting` to `internal/printer`.

The reverse map is the only substantial new machinery; everything else is a thin
handler over data gsx already computes.

### 5.1 Cross-file navigation (`.gsx` ↔ `.go`)

Navigation across the template/Go boundary is **asymmetric**, because the editor
routes `.gsx` files to `gsx lsp` and `.go` files to gopls — only one side is
ours. The bridge is the `//line` directive (the same mechanism codegen already
uses to map `go/types` errors back to `.gsx`).

**`.gsx` → definition (owned by `gsx lsp`, in-process).**
`info.Uses[id]` → `types.Object` → `fset.Position(obj.Pos())`. Because the
`FileSet` honors `//line`, this resolves correctly to either side with no
special-casing:

- a hand-written Go type/func → its real `.go` file (pass the `Location` through
  unchanged);
- another gsx component (`<Card/>` → `component Card` in `card.gsx`) → resolves
  to `.gsx`, because the skeleton / `.x.go` carries `//line card.gsx`;
- a same-file component param (`{user.Name}` → the `user` param) → `.gsx`, via
  our own skeleton→source reverse map.

We own both sides of the package analysis, so this path is complete.

**`.go` → a gsx component (works for free, served by gopls, not us).**
When editing a plain `.go` file and jumping to `Card(...)`, gopls resolves it.
`Card` is declared in the generated `card.x.go`, preceded by a `//line card.gsx:7`
directive — so gopls reports the location *as* `card.gsx` and the editor opens
the template. This is free, **provided**:

- `gsx generate` has produced `card.x.go` on disk (the LSP deliberately does not
  write generated code, so this relies on it being generated / committed); and
- the `//line` is **column-accurate** so the jump lands on the component name,
  not just the line start — exactly the open codegen TODO (commit `9a1b601`).
  Line-accuracy already lands on the right line.

**The boundary is deliberate.** `gsx lsp` does not handle `.go` files — that is
the gopls-takeover / proxy complexity we are avoiding. gsx owns `.gsx`, gopls
owns `.go`, and `//line` is the one-way bridge from the Go side. One honest
limitation: cross-boundary **find-references** is partial — each server sees only
its own file type. Go-to-definition via `//line` is the clean, complete path.

**Dependency this creates:** the `.go` → `.gsx` path makes column-accurate
`//line` directives a prerequisite, promoting the existing codegen `//line`
column-accuracy TODO from a nicety to a tracked dependency of the read-intelligence
slice.

## 6. Slice 3 — completion (decided later)

Deferred. When taken up, decide between:

- **In-process engine on `go/types` primitives** — locate the expression at the
  cursor, apply the documented dangling-selector repair (insert `_` after `.`),
  re-type-check the skeleton, enumerate members via `types.NewMethodSet` + struct
  fields plus in-scope identifiers. Owns the experience fully, no gopls, but is
  the single hardest piece and our ranking will not match gopls.
- **Narrow gopls subprocess for completion only** — serve everything else
  in-process; spawn gopls solely for `textDocument/completion` against the
  generated Go via the source map. Real gopls quality for one path, at the cost
  of reintroducing gopls-installed + version coupling for that path only.

Decide with real usage in hand.

## 7. Risks & mitigations

- **Position encoding / URIs** (templ's #900/#1121/#1291 class). Mitigation:
  negotiate encoding explicitly, centralize the one byte→encoding conversion,
  unit-test it on non-ASCII, and use `net/url` / proper URI handling rather than
  string surgery.
- **Re-check latency.** `go/packages.Load` is hundreds of ms. Mitigation:
  debounce in slice 1; a later perf slice reuses the existing incremental cache.
- **Unsaved-buffer correctness.** The package must type-check the in-memory
  buffer, not disk. Mitigation: generate skeletons from open buffers and inject
  via the existing `go/packages` Overlay path.
- **Stale diagnostics.** Mitigation: explicitly clear diagnostics for files that
  became clean.

## 8. What ships in slice 1

A `gsx lsp` subcommand that, in a real editor, opens a `.gsx` file, and as you
type, shows gsx's existing rich diagnostics inline — server lifecycle, document
sync, debounced per-package re-check, correct LSP positions, no gopls. That
proves the loop end-to-end before any `go/types` feature is built on top.
