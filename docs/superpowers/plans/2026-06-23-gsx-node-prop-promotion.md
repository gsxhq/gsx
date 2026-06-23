# `gsx.Node` prop promotion (`gsx.Val`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A `gsx.Node`-typed component prop accepts any renderable value from a `.gsx` caller — codegen wraps a non-node value in `gsx.Val(expr)` (a static string in `gsx.Text(lit)`).

**Architecture:** A new runtime `gsx.Val(v any) Node` boxes any value as a Node and renders it by a type switch; `gsx.Text(string) Node` is the escaped-text fast-path. Codegen, when a non-node value binds to a `gsx.Node` prop (a new AST-derived `nodeProps` signal), emits `gsx.Val(expr)` / `gsx.Text(lit)` — **identically in the emit and the type-check probe**, so there is no `classify` and no emit/probe asymmetry.

**Tech Stack:** Go; runtime `gsx` (stdlib-only) + codegen `internal/codegen`.

## Global Constraints

- **Go stays strict:** promotion only at the `.gsx`→Go boundary; a hand-written `CardProps{Title: "x"}` still won't compile (use `gsx.Val("x")`).
- **emit ≡ probe:** `gsx.Val(expr)` / `gsx.Text(lit)` are emitted IDENTICALLY by `emitProbes` (skeleton) and emit (`genChildComponent`) — no resolved type, no callback.
- **Escaping parity:** `gsx.Val`'s string/Stringer path uses the same `gw.Text` escaper as `{ x }`.
- **Float/number formatting is the pipeline's job** (`{ f | money("$") }`), not `Val` — `Val` renders the plain Go default.
- Out of scope: per-type boxes (`gsx.Int`/`gsx.Float`); a build-time guard for unrenderable values (v1 accepts a render-time error from `Val`'s `default`).
- After each task: `go build ./...` + `go test ./...` pass. Bump `internal/codegen/version.go` when emitted code changes.

---

### Task 1: Derive + thread the `nodeProps` signal

**Files:** Modify `internal/codegen/analyze.go` (the `propFields` derivation ~146-165; the functions threading `propFields`); thread the new map to the `childPropsLiteral` call sites in `emit.go`.

**Interfaces — Produces:** `nodeProps map[string]map[string]bool` (propsType → fieldName → isGsxNode), built in the same loop as `propFields`; `func isGsxNodeType(typ string) bool { return strings.TrimSpace(typ) == "gsx.Node" }`.

- [ ] **Step 1:** In `analyze.go`'s prop-field loop (~146), build a parallel `nodeFields`:
```go
fields := map[string]bool{}
nodeFields := map[string]bool{}
for _, p := range params {
	fields[fieldName(p.name)] = true
	if isGsxNodeType(p.typ) {
		nodeFields[fieldName(p.name)] = true
	}
}
```
and store it (e.g. a second return map `nodeOut[propsName] = nodeFields`). Add `isGsxNodeType` near `fieldName`. (`p.typ` is the param's declared-type source string from `parseParams`.)
- [ ] **Step 2:** Thread `nodeProps map[string]map[string]bool` parallel to `propFields` from where it's built to `childPropsLiteral`'s call sites — add it beside each `propFields map[string]map[string]bool` parameter through `genComponent`/`genChildComponent`/`emitProbes`/`buildSkeleton` (search those signatures). Construct it where `propFields` is constructed.
- [ ] **Step 3: Unit test** (mirror however `propFields` is tested; else add a small one): for `component Card(title gsx.Node, n int)`, `nodeProps["CardProps"]` has `Title:true`, not `N`; plus an `isGsxNodeType` table (`"gsx.Node"`→true, `" gsx.Node "`→true, `"string"`/`"int"`/`"[]gsx.Node"`→false).
- [ ] **Step 4: Run** `go build ./...` (threading compiles) + `go test ./...` (no behavior change — `nodeProps` derived but unused). Green.
- [ ] **Step 5: Commit** `codegen: derive nodeProps (which declared props are gsx.Node), threaded alongside propFields`.

---

### Task 2: Runtime `gsx.Val(any)` + `gsx.Text(string)` + `gsx.Fragment(...Node)`

**Files:** Create `val.go` (root `gsx` package: `Val`/`valNode`/`Text`/`textNode`/`Fragment`/`fragmentNode`); Test `val_test.go`.

**Interfaces — Produces:** `func Val(v any) Node`; `func Text(s string) Node`; `func Fragment(nodes ...Node) Node` (a Node that renders each child in order, no wrapper element — the type-safe, variadic way to build one multi-node `gsx.Node` value; same render semantics as `Val`'s `[]Node` case: nil children skipped). `Fragment` and `Val`'s `[]Node` case SHOULD share one render helper to avoid duplicate slice-render logic.

- [ ] **Step 1: Failing tests** `val_test.go`:
```go
package gsx

import ("fmt"; "strings"; "testing")

func renderNode(n Node) string { var b strings.Builder; _ = n.Render(nil, &b); return b.String() }
type stringerT struct{}
func (stringerT) String() string { return "S<x>" }

func TestVal(t *testing.T) {
	for _, tt := range []struct{ in any; want string }{
		{"a", "a"}, {"<b>", "&lt;b&gt;"}, {5, "5"}, {int64(-3), "-3"}, {uint(7), "7"},
		{3.5, "3.5"}, {true, "true"}, {[]byte("<x>"), "&lt;x&gt;"},
		{stringerT{}, "S&lt;x&gt;"}, {nil, ""}, {Raw("<i>"), "<i>"},
		{[]Node{Text("a"), nil, Text("b")}, "ab"}, // catNodeSlice parity; nil skipped
	} {
		if got := renderNode(Val(tt.in)); got != tt.want {
			t.Errorf("Val(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
func TestText(t *testing.T) {
	if got := renderNode(Text("<b>")); got != "&lt;b&gt;" {
		t.Errorf("Text = %q, want escaped", got)
	}
}
func TestFragment(t *testing.T) {
	// renders each child in order, no wrapper element; nil children skipped; empty → "".
	if got := renderNode(Fragment(Text("a"), nil, Text("<b>"))); got != "a&lt;b&gt;" {
		t.Errorf("Fragment = %q, want %q", got, "a&lt;b&gt;")
	}
	if got := renderNode(Fragment()); got != "" {
		t.Errorf("Fragment() = %q, want empty", got)
	}
	// Fragment is usable as a promoted value too: Val(Fragment(...)) == Fragment(...).
	if got := renderNode(Val(Fragment(Text("x"), Text("y")))); got != "xy" {
		t.Errorf("Val(Fragment) = %q, want %q", got, "xy")
	}
}
```
(`Render(nil, …)` — a nil ctx is fine; the helpers don't read ctx. Confirm `gw.Text`/`gw.S` write the expected escaping; adjust `want` to the actual `gw.Text` entity output if it differs, keeping the escaping principle.)
- [ ] **Step 2: Run** `go test . -run 'TestVal|TestText'` — FAIL (undefined).
- [ ] **Step 3: Implement** `val.go`:
```go
package gsx

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

// Val wraps any renderable value as a Node (so a value can fill a gsx.Node prop).
// A Node renders itself; string/[]byte/fmt.Stringer render as escaped text; the
// numeric and bool kinds render their plain Go form (use the |> pipeline for
// formatted numbers, e.g. { f | money("$") }); nil renders nothing.
func Val(v any) Node { return valNode{v} }

type valNode struct{ v any }

func (n valNode) Render(ctx context.Context, w io.Writer) error {
	if n.v == nil {
		return nil
	}
	gw := W(w)
	switch t := n.v.(type) {
	case Node:
		if t == nil {
			return nil
		}
		return t.Render(ctx, w)
	case []Node:
		// Parity with emitRender's catNodeSlice: a []gsx.Node renders each
		// element in order (so a value that renders inline as { rows } also
		// renders when promoted into a gsx.Node prop). nil elements skipped.
		return renderNodes(ctx, w, t)
	case string:
		gw.Text(t)
	case []byte:
		gw.Text(string(t))
	case fmt.Stringer:
		gw.Text(t.String())
	case bool:
		gw.S(strconv.FormatBool(t))
	case int:
		gw.S(strconv.FormatInt(int64(t), 10))
	case int8:
		gw.S(strconv.FormatInt(int64(t), 10))
	case int16:
		gw.S(strconv.FormatInt(int64(t), 10))
	case int32:
		gw.S(strconv.FormatInt(int64(t), 10))
	case int64:
		gw.S(strconv.FormatInt(t, 10))
	case uint:
		gw.S(strconv.FormatUint(uint64(t), 10))
	case uint8:
		gw.S(strconv.FormatUint(uint64(t), 10))
	case uint16:
		gw.S(strconv.FormatUint(uint64(t), 10))
	case uint32:
		gw.S(strconv.FormatUint(uint64(t), 10))
	case uint64:
		gw.S(strconv.FormatUint(t, 10))
	case float32:
		gw.S(strconv.FormatFloat(float64(t), 'g', -1, 32))
	case float64:
		gw.S(strconv.FormatFloat(t, 'g', -1, 64))
	default:
		return fmt.Errorf("gsx.Val: value of type %T is not renderable in a gsx.Node prop", n.v)
	}
	return gw.Err()
}

// Text is the escaped-text Node — codegen's static-string fast-path and a Go-side
// text constructor (one alloc, no any-box).
func Text(s string) Node { return textNode(s) }

type textNode string

func (t textNode) Render(_ context.Context, w io.Writer) error {
	gw := W(w)
	gw.Text(string(t))
	return gw.Err()
}

// Fragment groups children into one Node with no wrapper element — the
// type-safe, variadic way to fill a single gsx.Node prop with multiple nodes
// (and the lowering target for a future <>…</> syntax). Renders each child in
// order; nil children are skipped; Fragment() renders nothing.
func Fragment(nodes ...Node) Node { return fragmentNode(nodes) }

type fragmentNode []Node

func (f fragmentNode) Render(ctx context.Context, w io.Writer) error {
	return renderNodes(ctx, w, f)
}

// renderNodes renders each node in order, skipping nils — the shared body of
// Val's []Node case and fragmentNode (one place for the slice-render rule).
func renderNodes(ctx context.Context, w io.Writer, nodes []Node) error {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if err := n.Render(ctx, w); err != nil {
			return err
		}
	}
	return nil
}
```
(Confirm `W`/`gw.Text`/`gw.S`/`gw.Err` signatures in `writer.go`. The numeric formatting MIRRORS `emitRender` (`emit.go:823`) — read it and match `FormatFloat`'s verb/precision so `Val(f)` == inline `{ f }`.)
- [ ] **Step 4: Run** `go test .` green. Confirm `go list -deps github.com/gsxhq/gsx | grep -c tdewolff` is 0 (val.go is stdlib-only).
- [ ] **Step 5: Commit** `runtime: gsx.Val(any) value-Node box + gsx.Text escaped-text node + gsx.Fragment multi-node group`.

---

### Task 3: Codegen promotion in `childPropsLiteral` + corpus

**Files:** Modify `internal/codegen/emit.go` (`childPropsLiteral` ~1647, the `StaticAttr`/`ExprAttr` cases); add corpus under `internal/corpus/testdata/cases/slots/`; bump `version.go`.

**Interfaces — Consumes:** `nodeProps` (Task 1), `gsx.Val`/`gsx.Text` (Task 2). **Produces:** a `gsx.Node` prop receiving a non-node value emits `gsx.Val`/`gsx.Text`.

**Design.** `childPropsLiteral` receives the child's node-field set `nodeFields := nodeProps[propsType]`. In the `*ast.StaticAttr` and `*ast.ExprAttr` cases, when `isPropField(declared, name)` AND the field is a node (`nodeFields[fieldName(name)]`):
- `StaticAttr` → `fields = append(fields, fmt.Sprintf("%s: %s.Text(%s)", fieldName(t.Name), rtPkg, strconv.Quote(t.Value)))`.
- `ExprAttr` (reject Try/Stages as today) → `fields = append(fields, fmt.Sprintf("%s: %s.Val(%s)", fieldName(t.Name), rtPkg, strings.TrimSpace(t.Expr)))`.
- a `BoolAttr` bound to a node field → `%s.Val(true)` (edge case, consistent).
- Non-node fields → exactly today's behavior (quoted / expr / true). `MarkupAttr` → slot (unchanged). `rtPkg` is the runtime alias (`gsx` on emit, `_gsxrt` on probe) — already a param, so emit and probe produce the SAME call under their alias.

This is emitted identically by emit and probe (no `resolved`, no `classify`) — `childPropsLiteral`'s signature gains only `nodeFields map[string]bool`.

- [ ] **Step 1: Failing corpus** `internal/corpus/testdata/cases/slots/node_prop_promotion.txtar`: `component Card(title gsx.Node, content gsx.Node)` with a `Page` that invokes `<Card title="Card Title" content={ n }>…</Card>` (n int via a Page prop) and `<Card title={ <span>x</span> } content={ someNode }/>`. Pin `generated.x.go.golden` (`Title: gsx.Text("Card Title")`, `Content: gsx.Val(n)`, markup slot unchanged) + `render.golden`. (Model on an existing `slots`/named-slot case; check how to supply `n int` + a `gsx.Node` value — e.g. props on `Page`.)
- [ ] **Step 2: Run** `go test ./internal/corpus/ -run 'TestCorpus/slots'` — FAIL (today: `cannot use "Card Title" (string) as gsx.Node` resolution diagnostic).
- [ ] **Step 3: Implement** the `childPropsLiteral` change + thread `nodeFields` into it at both call sites (emit + probe). Bump `version.go`.
- [ ] **Step 4:** Add `slots/node_prop_escaping.txtar` (`<Card title={ userStr }>`, `userStr="<script>"` → render `&lt;script&gt;`) and `slots/node_prop_realworld.txtar` (the user's `Layout`+`Index`+`Card` file → renders correctly). Regenerate goldens with the corpus `-update` flag; **also `-update` the full `TestCorpus`** so `coverage.golden` reflects the new cases (avoid the count mismatch). EYEBALL the escaping golden.
- [ ] **Step 5: Run** full `go test ./...` green; existing named-slot/`{children}`/child-props cases unchanged (markup/node map as before). `go vet ./...` clean.
- [ ] **Step 6: Commit** `codegen: promote renderable values to gsx.Node props via gsx.Val/gsx.Text; corpus`.

---

## Self-Review

**Spec coverage:** §2 runtime → T2 (`Val`/`Text` + parity/escaping tests); §3 codegen rule → T3 (`Static→Text`, `Expr→Val`, identical emit/probe); §4 `nodeProps` → T1; §6 tests → T2 (unit) + T3 (corpus: string/int/markup/node + escaping + real-world). Build-time looseness (§4) is accepted — no unrenderable-build-error task; `Val`'s `default` is the render-time backstop, exercised by the unit test's absence of a struct case (add one if a render-time error case is wanted).

**Placeholder scan:** all code is concrete (`Val`/`Text` full body, the `childPropsLiteral` `fmt.Sprintf` forms, the `nodeProps` loop). The float-format verb is specified ('g', -1) with "match emitRender" as the cross-check.

**Type consistency:** `Val`/`Text`/`valNode`/`textNode`/`nodeProps`/`isGsxNodeType`/`rtPkg` consistent across tasks; `childPropsLiteral` gains `nodeFields map[string]bool`; `rtPkg` reused as today.

## Risks
- **`nodeProps` threading** (T1) — wide but mechanical; mirror `propFields` so emit ≡ probe.
- **`Val` float/escaping parity with `emitRender`** (T2) — the parity unit test pins it; read `emit.go:823` to match the float verb.
- **`rtPkg.Val`/`rtPkg.Text` under the probe alias** (`_gsxrt`) — confirm the skeleton imports the runtime as `_gsxrt` and that `_gsxrt.Val`/`_gsxrt.Text` resolve (they're new exported funcs — the skeleton's runtime import already covers `_gsxrt.Node`, so it covers these).
- **`coverage.golden`** must be `-update`d for the new cases (T3 Step 4) — a forgotten manifest bump fails the suite.
