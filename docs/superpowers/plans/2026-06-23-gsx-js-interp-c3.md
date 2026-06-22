# gsx Slice C3 — data-island sugar (`<script type="application/json">@{ data }</script>`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** The `templ.JSONScript` replacement: a non-executable `<script>` (e.g. `type="application/json"`) whose body is a single `@{ data }` hole emits the Go value as JSON, safe in the script raw-text context. `<script type="application/json" id="cfg">@{ cfg }</script>` → `<script type="application/json" id="cfg">{"tab":"home"}</script>`.

**Architecture:** A `<script>` with a non-executable `type` is a "data block" (HTML spec), not JavaScript — so it must NOT go through the C1 JS-context classifier (which fail-closes a bare `@{ data }` as start-of-input). Instead, `jsx.ResolveScripts` detects a data island and assigns its single hole `JSCtxValue` directly (JSON-encode via `gw.JSVal`, which already carries the `</script>`/U+2028-29 defenses). The body must be exactly one `@{ }` hole modulo whitespace ("whole body = one JSON value" — per the spec); anything else fails closed. Codegen already emits `JSCtxValue` → `JSVal` (C1), so no emit change. `jsmin` must skip data-island scripts (don't minify a JSON/data block as JavaScript).

**Tech Stack:** Go; reuses C1 `internal/jsx` + `gw.JSVal`. No new runtime API.

## Global Constraints

- **A `<script>` is executable JS** (and keeps C1 behavior) iff its `type` attribute is absent/empty or one of the JS MIME types: `text/javascript`, `module`, `application/javascript`, `text/ecmascript`, `application/ecmascript` (case-insensitive, trimmed). ANY other `type` value → data island.
- **A data-island body must be exactly one `@{ }` `Interp`** plus optional whitespace-only `Text` (per the spec "whole body = one JSON value"). Multiple holes, or a hole alongside non-whitespace literal text, → fail-closed compile error. (Structured-JSON-with-holes is a future enhancement; a struct value JSON-encodes the whole shape, which is the idiomatic JSONScript pattern.)
- **The single hole is `JSCtxValue`** → `gw.JSVal` (JSON + `</script>`/`*/`/`<!--`/U+2028-29 neutralization from the C1 port). `gsx.RawJS` passthrough applies (an author can vouch raw content).
- Reuses the existing pipeline wiring (`ResolveScripts` already runs before type resolution); no new insertion point. Codegen `genScriptChild` already emits `JSCtxValue`.
- After each task: `go build ./...` and `go test ./...` pass before committing.

---

### Task 1: Data-island detection + classification + jsmin skip + corpus

**Files:** Modify `internal/jsx/jsx.go` (data-island detection in `resolveScript`); `internal/jsmin/file.go` (skip data-island `<script>`); add corpus cases. Tests in `internal/jsx/jsx_test.go`.

**Interfaces — Produces:** an unexported `isDataIslandScript(el *ast.Element) bool` reusable by jsx; jsmin gets its own local copy or a shared predicate (see Step 2).

- [ ] **Step 1 — jsx detection + classification.** In `internal/jsx/jsx.go`, add:
```go
// jsExecutableTypes are the <script type> values that run as JavaScript. Any
// other (non-empty) type marks a data block (e.g. application/json) — not JS.
var jsExecutableTypes = map[string]bool{
	"text/javascript": true, "module": true, "application/javascript": true,
	"text/ecmascript": true, "application/ecmascript": true,
}

// isDataIslandScript reports whether el is a <script> whose type marks it a data
// block (not executable JS), e.g. <script type="application/json">.
func isDataIslandScript(el *ast.Element) bool {
	for _, a := range el.Attrs {
		if sa, ok := a.(*ast.StaticAttr); ok && strings.EqualFold(sa.Name, "type") {
			t := strings.ToLower(strings.TrimSpace(sa.Value))
			return t != "" && !jsExecutableTypes[t]
		}
	}
	return false
}
```
In `resolveScript`, at the very top (after the early `hasInterp` bail or before building the JS skeleton), branch:
```go
	if isDataIslandScript(el) {
		return resolveDataIsland(el)
	}
```
and add `resolveDataIsland`:
```go
// resolveDataIsland classifies a data-block <script> (e.g. application/json):
// the whole body must be exactly one @{ } hole (modulo whitespace), emitted as a
// JSON value. Anything else fails closed.
func resolveDataIsland(el *ast.Element) error {
	var theInterp *ast.Interp
	for _, c := range el.Children {
		switch v := c.(type) {
		case *ast.Text:
			if strings.TrimSpace(v.Value) != "" {
				return fmt.Errorf("jsx: a data <script> (type=%q) must contain exactly one @{ } value; found literal text %q",
					scriptType(el), strings.TrimSpace(v.Value))
			}
		case *ast.Interp:
			if theInterp != nil {
				return fmt.Errorf("jsx: a data <script> must contain exactly one @{ } value; found more than one")
			}
			theInterp = v
		default:
			return fmt.Errorf("jsx: unexpected %T in data <script> body", c)
		}
	}
	if theInterp == nil {
		return nil // holeless data block (static JSON) — nothing to interpolate.
	}
	theInterp.JSCtx = ast.JSCtxValue
	return nil
}
```
(Add a tiny `scriptType(el)` helper returning the `type` value for the message, or inline it. `hasInterp` early-return in `resolveScript` must NOT pre-empt the data-island branch for a holeless data block — order the data-island check to run regardless; a holeless data island simply returns nil, same as a holeless JS script.)

- [ ] **Step 2 — jsmin skip.** A data block is not JavaScript; minifying it with the JS minifier is wrong. In `internal/jsmin/file.go` `minifyMarkup`'s `<script>` case (where it currently calls `minifyScriptChildren`), skip when the script is a data island. Add a local `isDataIslandScript` predicate in jsmin (mirror the jsx one — a ~6-line pure function; note the duplication for review, OR factor a shared `internal/attrjs`-style predicate if clean; copying is acceptable). When `isDataIslandScript(v)` is true, leave `v.Children` unchanged (skip minification) and `continue`. (The holey-skip from C1 already covers data islands WITH a hole; this Step additionally covers a HOLELESS static JSON block.)

- [ ] **Step 3 — jsx tests** (`internal/jsx/jsx_test.go`): build a `<script type="application/json">` element (StaticAttr `type`) and assert:
  - body `@{ data }` (bare) → no error, the Interp's `JSCtx == ast.JSCtxValue` (this is the case that fail-closed before C3).
  - body `  @{ data }  ` (whitespace around) → JSCtxValue, no error.
  - body `@{ a } @{ b }` → error (more than one value).
  - body `[@{ a }]` (literal text `[` `]` + hole) → error (literal text in a data block).
  - a `<script>` with NO type (or `type="module"`) keeps the JS-classifier path: body `@{ data }` → fail-closed (unchanged C1 behavior) — assert it still errors. (Reuse the existing `parseScript` helper + append a `type` StaticAttr; see how `internal/jsx/jsx_test.go` builds elements.)

- [ ] **Step 4 — corpus** (`internal/corpus/testdata/cases/datajson/`): model on `script/interp_value.txtar`.
  - `island_value.txtar`: `<script type="application/json" id="cfg">@{ cfg }</script>` with a small `Cfg` struct (Go chunk) → `generated.x.go.golden` shows `_gsxgw.JSVal(cfg)` inside the `<script type="application/json"...>` tags; `render.golden` shows the JSON body.
  - `island_breakout.txtar` (SECURITY): `cfg` value (e.g. a `map[string]string` or struct field) containing `</script><script>alert(1)</script>` → `render.golden` shows it neutralized (`</script>` or `</script`), NOT a literal `</script>` from data. EYEBALL this golden.
  - `island_multi_hole_rejected.txtar`: `<script type="application/json">@{ a } @{ b }</script>` → `diagnostics.golden` with the fail-closed error (model on `script/interp_identifier_rejected.txtar`).
  Bump `internal/codegen/version.go` `"5"`→`"6"`.

- [ ] **Step 5:** `go build ./...`; `go vet ./internal/jsx/ ./internal/jsmin/`; full `go test ./...` green; `go list -deps github.com/gsxhq/gsx | grep -c tdewolff` → 0. Commit: `jsx+jsmin: data-island <script> sugar (type=application/json @{ data } → JSON value); skip data blocks in JS minify; corpus; bump version`.

---

## Self-Review

**Spec coverage (Component 3 part 3 — data island):** detection + single-hole JSON classification (Step 1) ✓; jsmin no longer treats data blocks as JS (Step 2) ✓; fail-closed for multi-hole/text (Step 1/3) ✓; security corpus (Step 4) ✓. Emit reuses C1's `JSCtxValue`→`JSVal` (no change). This completes the feature (C1 `<script>`, C2 attributes, C3 data island).

**Placeholder scan:** corpus goldens + the `isDataIslandScript` duplication (jsx/jsmin) are the only notable items, both by design (duplication noted for review).

**Type/name consistency:** `isDataIslandScript`, `resolveDataIsland`, `jsExecutableTypes`, `ast.JSCtxValue`, `gw.JSVal` — consistent; reuses existing names.

## Risks
- **jsmin/jsx predicate duplication** — acceptable (~6 lines, pure); a shared predicate could unify later. Noted for review.
- **Holeless data block** must NOT error and must NOT be JS-minified (Step 1 returns nil; Step 2 skips). Covered by the holeless path returning nil + the jsmin type-skip.
- **The `type` attr is a `StaticAttr`** (plain `type="application/json"`). If an author writes `type={ expr }` (an ExprAttr) the detection can't see the value statically → it stays the JS path (fail-closed for a bare hole). Acceptable: a dynamic script type is pathological; document that `type` must be static for data-island treatment.
