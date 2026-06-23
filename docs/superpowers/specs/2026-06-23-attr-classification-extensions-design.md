# Attribute-Classification Extensions — Design

**Date:** 2026-06-23
**Status:** Design (approved for plan)
**Related:** `2026-06-19-gsx-pipeline-and-extensions-design.md` (code-level registration philosophy, `gen.Main` options, `gsx info`), `2026-06-23-gsx-js-interpolation-design.md` (the JS/CSS escaping contexts this classifies into), `2026-06-21-codegen-incremental-cache-design.md` (the build cache reused for the manifest).

## 1. Problem

gsx escapes attribute values according to a **security context** derived from the
attribute name: JS (`onclick`, Alpine `@click`, HTMX `hx-on:*`), URL (`href`,
`src`, `hx-get`…), CSS (`style`), or plain. Today those name→context decisions
are **hardcoded**:

- `internal/attrjs.IsJSAttr(name)` — JS-context attrs. Called from **two phases**:
  the parser (`parser/attrs.go:142`, to decide whether a quoted value is split
  into `@{ }` JS holes) and codegen (`internal/codegen/emit.go:992`, in
  `attrContext()` for context-aware escaping).
- `urlAttrs` map (`internal/codegen/emit.go:976`) — URL-context attrs.
- `style` literal (`emit.go`, in `attrContext`) — CSS context.

A user adopting a framework gsx doesn't know about — Vue (`v-on:`, `:class`),
Livewire (`wire:`), Stimulus (`data-action`/`data-controller`), or a bespoke
URL-bearing attribute — has no way to teach the escaper that
`wire:click="@{ expr }"` is a JS context. The value silently falls through to
plain-attr escaping: not a security hole (it is still escaped), but the wrong
context, and `@{ }` holes inside it are not JS-classified.

**Goal:** let users extend the JS / URL / CSS attribute-classification sets,
consistent with gsx's existing **code-level registration** philosophy (no
hand-edited config file), and in a way that the project's external tooling
(LSP, `vet`) can rely on.

## 2. Guiding principle — the last-good build is the ground truth

gsx's existing stance (pipeline-and-extensions spec §C.1) is **code-level
registration, not config files**: `cmd/gsx/main.go` is the single readable,
type-checked, refactor-safe source of truth for what is active. This design keeps
that stance.

The one new pressure is **external tools** — an LSP or `vet` is installed and
invoked independently and cannot reliably read a user's compiled
`cmd/gsx/main.go` (registration args may be variables, computed, or cross-package;
static-reading the source is the "simple heuristic" trap). The resolution is **not**
to read the source, but for tools to consume what the project binary *resolved*
on its last successful build.

> **Principle:** every successful build writes the resolved configuration to a
> durable, machine-readable manifest. Tools ground themselves on that last-good
> manifest. We aim to serve **most** use cases correctly and degrade **visibly**
> on the rest — we do not build elaborate machinery to chase 100% correctness in
> broken-build states.

This makes the design simple: there is exactly one authoritative producer (the
project binary, at build time) and one durable artifact (the manifest) that both
humans (`gsx info`) and tools (`gsx info --json` / the persisted manifest) read.

## 3. The Classifier

Replace the package-global `IsJSAttr` function and `urlAttrs` map with a single
**`internal/attrclass.Classifier`** value: declarative, serializable, additive
over the built-ins.

```go
package attrclass

type Context int
const (
    CtxPlain Context = iota
    CtxJS
    CtxURL
    CtxCSS
)

// Rule matches an attribute name by exact name (case-insensitive) OR by prefix.
// Exactly one of Name/Prefix is set; the other is empty. A Rule with both set
// (or neither) is a registration error reported by gen.Main, not silently
// ignored.
type Rule struct {
    Name   string // exact match, e.g. "x-data"
    Prefix string // prefix match, e.g. "wire:", "v-on:"
}

type Rules struct {
    JS  []Rule // built-in: on*, @*, x-on:, hx-on*, x-data/x-init/…, : (Alpine bind)
    URL []Rule // built-in: href, src, action, formaction, hx-get, xlink:href, …
    CSS []Rule // built-in: style
}

type Classifier struct {
    rules     Rules                              // built-in ∪ user rules (serializable)
    predicate func(name string) (Context, bool)  // escape hatch (NOT serializable)
}

// Context classifies an attribute name. Priority, union semantics:
//   1. built-in rules        (the safety floor — always present)
//   2. user declarative rules
//   3. user predicate        (consulted ONLY for names no rule matched)
// Returns CtxPlain when nothing matches.
func (c *Classifier) Context(name string) Context
```

### Semantics

- **Additive, never replace.** User rules and the predicate *add* classification.
  Built-ins are always present and take priority, so a user can never accidentally
  drop `onclick`/`href` classification or downgrade it to a weaker context. This
  is the safety floor.
- **Built-ins are seed rules.** The current `attrjs`/`urlAttrs`/`style` sets are
  re-expressed as the built-in `Rules`, so there is one matching mechanism, not a
  special case for built-ins vs. user rules.
- **`Context()` subsumes today's `attrContext()`** in codegen and `IsJSAttr()` in
  the parser (the parser consults only the JS facet — `Context(name) == CtxJS`).

## 4. Registration API

Consistent with the existing `gen.WithFilters` / `gen.WithCSSMinifier` /
`gen.WithJSMinifier` options:

```go
gen.Main(
    // declarative — recommended, full-fidelity offline
    gen.WithJSAttrs(attrclass.Rule{Prefix: "wire:"}, attrclass.Rule{Prefix: "v-on:"}),
    gen.WithURLAttrs(attrclass.Rule{Name: "data-href"}),
    gen.WithCSSAttrs(attrclass.Rule{Name: "data-style"}),

    // escape hatch — for matching logic rules can't express
    gen.WithAttrClassifier(func(name string) (attrclass.Context, bool) {
        if strings.HasPrefix(name, "fancy-") && strings.HasSuffix(name, ":on") {
            return attrclass.CtxJS, true
        }
        return attrclass.CtxPlain, false
    }),
)
```

`cmd/gsx/main.go` remains the single source of truth for what is active.

## 5. The predicate escape hatch

Declarative rules are the norm; the predicate is the documented escape hatch for
matching logic rules cannot express. It is safe because:

- **Additive only.** Consulted only for names no rule matched, so it can add
  classification but never downgrade a built-in. Safety floor intact.
- **Codegen is always correct.** Generation always runs in the project binary
  with the real compiled predicate, so emitted escaping is always right. The
  predicate's *only* limitation is that it cannot propagate to external tools
  (closures don't serialize).
- **No silent loss.** The manifest cannot capture predicate logic, but it records
  a marker — `hasPredicate: true` plus an optional author-supplied label. Tools
  reading the manifest without a live binary surface an honest diagnostic
  ("custom predicate classification is active but unavailable offline; rebuild
  for full accuracy") rather than pretending the config is complete.

## 6. Threading

The Classifier must reach **two phases** (this is why the change is more than a
map swap):

- **Parser** (`parser/attrs.go:142`) — consults the **JS** facet only, to decide
  whether a double-quoted value is parsed into `@{ }` JS holes. Threaded into the
  parser entry point (the parser is also used standalone by `gsx fmt` and AST
  tooling, so it must accept a Classifier, defaulting to built-ins-only).
- **Codegen** (`internal/codegen/emit.go:992`, `attrContext`) — consults **all
  three** facets for context-aware escaping.

The resolved `*Classifier` flows from `gen.Main` options → the generate pipeline
(`runGenerate` → `generateCached` → `GeneratePackagesWithFilters` → `generateFile`,
mirroring how `filterPkgs`/`cssMin`/`jsMin` already thread) and → the parser
entry. When no options are given, the Classifier is built-ins-only and behavior is
identical to today.

## 7. Tool delivery — the project binary is the toolserver

`gen.Main` grows `lsp` and `vet` subcommands alongside `generate`/`fmt`/`info`.
Editors launch the **project-local** `cmd/gsx` binary, so customizations are
compiled in and there is nothing to discover at the source level. `gsx info`
already demonstrates this pattern: it reports the resolved filter table by loading
the module, not by parsing source. (Building the LSP/vet servers is **out of scope
here**; this design only reserves the subcommand seam and defines the manifest
they will consume.)

## 8. Degradation tiers

"The project doesn't build" is not one state. What matters is *what* fails:

| Tier | State | Behavior |
|---|---|---|
| 1 | Project compiles | Project binary serves tools: full custom config + live type resolution |
| 2 | Component/template/props code broken, gsx config intact | The long-lived tool process survives (it was started when things built); in-memory Classifier keeps classifying; only type-dependent features (interpolation type-checks) degrade |
| 3 | The gsx binary itself won't build (`cmd/gsx/main.go` or deps) | Tools read the **last-good manifest** from the build cache: full declarative rules + `hasPredicate` warning. No live types |
| 4 | Never built, or cache evicted | Built-in stock classification only (standard `onclick`/`href`…) |

Tier 2 — the common editing case — is handled "for free" because the
classification config lives in `cmd/gsx/main.go` (touched once at setup) and is
independent of whether any component compiles, *provided the tool is a long-lived
process not recompiled per keystroke*.

## 9. The manifest

- **Content:** the resolved configuration — declarative classification rules (JS /
  URL / CSS, built-in ∪ user), the resolved filter table (already computed by
  `gsx info`), and `hasPredicate` (+ optional label). `gsx info` (human-readable)
  and the manifest (machine-readable JSON) are the **same data, two renderings**;
  `gsx info --json` emits the manifest form.
- **Location:** persisted into the **existing build cache** (`~/.cache/gsx`, env
  `GSXCACHE`). Rationale: it is already out-of-tree (no working-tree / git
  pollution), already `CACHEDIR.TAG`-marked, and already honors `GSXCACHE=off`.
- **Key:** a **stable project key** derived from the module root / module path —
  *not* a content hash like the per-directory codegen entries — so an external
  tool can recompute the key and locate the manifest without knowing file hashes.
- **Lifecycle:** written on each successful generate/info (when the resolved
  config is in hand); read as the Tier-3 fallback. Cache eviction or
  `gsx clean --cache` simply drops Tier 3 → Tier 4 (built-in stock), which is
  acceptable because the manifest is explicitly a cache, never a source of truth.

## 10. Out of scope (YAGNI)

- **Custom raw-text tags** beyond `script`/`style` — touches parser tokenization,
  heavier; not requested.
- **Replace/strict mode** for built-ins — additive-only is the safe default;
  add later only if a real need appears.
- **Building the LSP / vet servers** — this design reserves the seam and the
  manifest; the servers are separate work.
- **A hand-edited config file** — rejected; the manifest is a derived cache, not a
  user-authored config. Source of truth stays in `cmd/gsx/main.go`.

## 11. Testing strategy

- `internal/attrclass`: unit tests for `Context()` priority/union (built-in vs
  user rule vs predicate), additive-never-downgrade invariant, exact-vs-prefix
  matching, case-insensitivity. Parity test: built-ins-only Classifier reproduces
  today's `IsJSAttr`/`urlAttrs`/`style` decisions exactly (regression guard).
- Corpus: render goldens proving a user JS-rule (e.g. `wire:click="@{ x }"`) gets
  JS-context escaping and `@{ }` hole splitting; a user URL-rule gets URL
  sanitization; data-origin breakout bytes (`"`/`<`/`>`) are neutralized in each.
- Manifest: round-trip serialize/deserialize; stable-key recompute; `hasPredicate`
  marker present when a predicate is registered; Tier-3 read path produces the
  declarative rules and the predicate warning.
- Threading: a `.gsx` file parsed with a user JS-rule splits holes the parser
  would otherwise treat as plain text.
```
