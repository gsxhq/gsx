# Ordered `Attrs` by default — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the ordered `[]Attr` slice the one attribute-bag type `gsx.Attrs` (default everywhere, source-order rendering), demote the old map to `gsx.AttrMap = map[string]any` with an implicit `AttrsFromMap` coercion at bag boundaries, and delete the `OrderedAttrs`/`SpreadOrdered` two-type dispatch.

**Architecture:** Runtime first (defines the final `Attrs` slice API + `AttrMap` alias + `AttrsFromMap`), then codegen flips its emitted literals from map form to slice form and collapses the two-type spread dispatch to one path, then the new `AttrMap→Attrs` auto-conversion is added at the prop-binding and element-spread boundaries, then dead plumbing is removed, then docs + the one-learning migration.

**Tech Stack:** Go 1.26.1 (pin to `GO_VERSION` in `.github/workflows/ci.yml`). Runtime (root `gsx` package) is **standard-library only**. Codegen lives in `internal/codegen` and may use `golang.org/x/tools`. Tests: root-package unit tests + the txtar corpus (`internal/corpus/testdata/cases/**/*.txtar`).

**Reference spec:** `docs/superpowers/specs/2026-06-30-attrs-default-ordered-design.md`.

## Global Constraints

- Runtime (root package) stays **stdlib-only** — no new dependencies in `attrs.go`.
- Go pinned to **1.26.1** (gofmt drift on a different minor). Inner loop: `make check`; authoritative pre-merge: `make ci`.
- **Don't hand-edit `.x.go` or `.txtar` goldens** — change the `.gsx`/source and regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify WITHOUT `-update`. `git add` the rewritten `coverage.golden`.
- Every syntax/codegen change ships corpus coverage (per-context) + unit coverage. This is permanent grammar work — apply highest review rigor (see `gsx-syntax-work-highest-rigor`).
- Conversion fires ONLY for resolved type exactly `map[string]any` (key kind string, elem the empty interface). A `map[string]string` is NOT a bag and stays a type error.
- Method duplicate-key rules are the contract: `Class()`/`Style()` **aggregate**, `Get`/`Has` **first-wins**, `Without` removes **all**, `Merge` overwrites in place + concats class/style in place.

---

### Task 1: Runtime — `Attrs` slice type, methods, `AttrMap`, `AttrsFromMap`

**Files:**
- Modify (rewrite): `attrs.go`
- Delete: `orderedattrs.go` (its `Attr` + slice fold into `attrs.go`)
- Modify (rewrite): `attrs_test.go`
- Delete: `orderedattrs_test.go` (coverage folds into `attrs_test.go`)

**Interfaces:**
- Produces: `type Attr struct{ Key string; Value any }`; `type Attrs []Attr`; `type AttrMap = map[string]any`; `func AttrsFromMap(m map[string]any) Attrs`; methods `(Attrs) Class() string`, `Style() string`, `Get(string)(any,bool)`, `Has(string) bool`, `Without(...string) Attrs`, `Take(string)(any,Attrs)`, `Merge(Attrs) Attrs`; `func AttrsCond(cond bool, then, els func() Attrs) Attrs`; `func (gw *Writer) Spread(ctx context.Context, a Attrs)`. Helpers `validAttrName`, `toStr`, `joinAttrStrings` unchanged.
- Note: after this task `go build ./...` FAILS (codegen still emits `gsx.OrderedAttrs`/`SpreadOrdered`/map-literal `gsx.Attrs{…}`). That is expected and resolved by Task 2. **Verify this task against the root package only.**

- [ ] **Step 1: Write the failing tests** — rewrite `attrs_test.go` to:

```go
package gsx

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func TestAttrsFromMapSorts(t *testing.T) {
	got := AttrsFromMap(map[string]any{"id": "x", "class": "c", "data-z": 1})
	want := Attrs{{Key: "class", Value: "c"}, {Key: "data-z", Value: 1}, {Key: "id", Value: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttrsFromMap = %v, want %v", got, want)
	}
	if AttrsFromMap(nil) != nil {
		t.Fatal("AttrsFromMap(nil) should be nil")
	}
}

func TestAttrsClassStyleAggregate(t *testing.T) {
	a := Attrs{{Key: "class", Value: "a"}, {Key: "x", Value: "1"}, {Key: "class", Value: "b"}}
	if got := a.Class(); got != "a b" {
		t.Fatalf("Class aggregate = %q, want %q", got, "a b")
	}
	s := Attrs{{Key: "style", Value: "color:red"}, {Key: "style", Value: "margin:0"}}
	if got := s.Style(); got != "color:red; margin:0" {
		t.Fatalf("Style aggregate = %q, want %q", got, "color:red; margin:0")
	}
}

func TestAttrsGetFirstWins(t *testing.T) {
	a := Attrs{{Key: "k", Value: "first"}, {Key: "k", Value: "second"}}
	v, ok := a.Get("k")
	if !ok || v != "first" {
		t.Fatalf("Get first-wins = %v,%v want first,true", v, ok)
	}
	if !a.Has("k") || a.Has("nope") {
		t.Fatal("Has wrong")
	}
}

func TestAttrsWithoutRemovesAll(t *testing.T) {
	a := Attrs{{Key: "class", Value: "a"}, {Key: "x", Value: "1"}, {Key: "class", Value: "b"}}
	got := a.Without("class")
	want := Attrs{{Key: "x", Value: "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Without = %v, want %v", got, want)
	}
	if Attrs(nil).Without("x") != nil {
		t.Fatal("Without on nil should be nil")
	}
}

func TestAttrsMergeOverwriteInPlace(t *testing.T) {
	a := Attrs{{Key: "id", Value: "old"}, {Key: "class", Value: "base"}}
	got := a.Merge(Attrs{{Key: "id", Value: "new"}, {Key: "class", Value: "extra"}, {Key: "data-x", Value: "1"}})
	want := Attrs{{Key: "id", Value: "new"}, {Key: "class", Value: "base extra"}, {Key: "data-x", Value: "1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %v, want %v", got, want)
	}
}

func TestAttrsCondThunks(t *testing.T) {
	if got := AttrsCond(true, func() Attrs { return Attrs{{Key: "a", Value: "1"}} }, nil); !reflect.DeepEqual(got, Attrs{{Key: "a", Value: "1"}}) {
		t.Fatalf("AttrsCond true = %v", got)
	}
	if got := AttrsCond(false, func() Attrs { return Attrs{{Key: "a", Value: "1"}} }, nil); got != nil {
		t.Fatalf("AttrsCond false no-else = %v, want nil", got)
	}
}

func TestSpreadOrderAndDrop(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.Spread(context.Background(), Attrs{
		{Key: "data-b", Value: "2"},
		{Key: "data-a", Value: "1"},
		{Key: "checked", Value: true},
		{Key: "skip me", Value: "x"}, // invalid name → dropped
		{Key: "off", Value: false},   // false bool → omitted
	})
	if got := buf.String(); got != ` data-b="2" data-a="1" checked` {
		t.Fatalf("Spread = %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ -run 'TestAttrs|TestSpread' -v`
Expected: FAIL to compile (`AttrsFromMap` undefined / `Attrs` is still a map).

- [ ] **Step 3: Rewrite `attrs.go`** to the full new runtime (and delete `orderedattrs.go` + `orderedattrs_test.go`):

```go
package gsx

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Attr is one ordered attribute pair. Value is rendered like any attribute value:
// a bool toggles a bare boolean attribute; anything else is stringified (toStr) and
// attribute-escaped.
type Attr struct {
	Key   string
	Value any
}

// Attrs is gsx's attribute bag: an insertion-ordered, duplicate-tolerant slice of
// pairs. It is the type of the implicit fallthrough bag, every declared bag prop, the
// {{ "k": v }} literal, and conditional-attr bags. Spread renders it in SLICE ORDER
// (no sort) so callers control attribute order (e.g. Datastar data-* directives).
//
// Security contract: keys are HTML attribute NAMES emitted (after a validity check,
// see Spread) without entity-encoding — they must come from generated code or trusted
// developer input, never from untrusted strings. Values are HTML-attribute-escaped but
// NOT URL-sanitized: a URL-typed attribute (href, src, action, formaction, …) carrying
// an untrusted value must be written with gw.URL, not passed through Spread.
type Attrs []Attr

// AttrMap is the map form of an attribute bag. It is an ALIAS for map[string]any, so a
// bare map[string]any, a gsx.AttrMap{…} literal, and a map returned from user code are
// the same type — and gsx auto-converts any of them to Attrs (via AttrsFromMap) at a
// component bag-prop binding or an element spread. A map has no order, so the
// conversion SORTS keys, keeping map-sourced attributes deterministic.
type AttrMap = map[string]any

// AttrsFromMap converts a map bag to an ordered bag with keys sorted ascending. This is
// the implicit AttrMap→Attrs coercion gsx inserts at bag boundaries; it is exported for
// explicit use too. An empty/nil map yields a nil bag.
func AttrsFromMap(m map[string]any) Attrs {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(Attrs, 0, len(keys))
	for _, k := range keys {
		out = append(out, Attr{Key: k, Value: m[k]})
	}
	return out
}

// Class returns the bag's class string. DUPLICATE-KEY RULE: it AGGREGATES — the values
// of ALL "class" pairs are joined (space-separated, each trimmed), so no class is
// silently dropped. It does NOT merge/dedupe tokens; the single outer codegen-emitted
// class site applies the configured merger exactly once over this plus the root's parts.
func (a Attrs) Class() string {
	var out string
	for _, kv := range a {
		if kv.Key == "class" {
			out = joinAttrStrings("class", out, strings.TrimSpace(toStr(kv.Value)))
		}
	}
	return out
}

// Style returns the bag's style declaration. DUPLICATE-KEY RULE: AGGREGATES — the
// values of ALL "style" pairs are joined ("; "-separated).
func (a Attrs) Style() string {
	var out string
	for _, kv := range a {
		if kv.Key == "style" {
			out = joinAttrStrings("style", out, toStr(kv.Value))
		}
	}
	return out
}

// Get returns the value for key and whether it was present. DUPLICATE-KEY RULE: FIRST
// occurrence wins (matches browser "first attribute wins").
func (a Attrs) Get(key string) (any, bool) {
	for _, kv := range a {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return nil, false
}

// Has reports whether key is present (first-occurrence scan).
func (a Attrs) Has(key string) bool {
	_, ok := a.Get(key)
	return ok
}

// Without returns a copy of a without ANY pair whose key is in keys (a is not mutated);
// the order of the rest is preserved. An empty result (or empty input) yields nil.
func (a Attrs) Without(keys ...string) Attrs {
	if len(a) == 0 {
		return nil
	}
	out := make(Attrs, 0, len(a))
	for _, kv := range a {
		if !slices.Contains(keys, kv.Key) {
			out = append(out, kv)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Take returns Get(key)'s first value and a copy of a without ALL occurrences of key.
func (a Attrs) Take(key string) (any, Attrs) {
	v, _ := a.Get(key)
	return v, a.Without(key)
}

// Merge returns a new bag combining a and other, preserving order. For each pair in
// other: a "class"/"style" value is CONCATENATED onto the first such pair already in
// the result (or appended if none) — so a directly-spread merged bag never carries a
// duplicate class/style. Any other key OVERWRITES the first existing occurrence in
// place (other wins, position preserved), or is appended if absent.
func (a Attrs) Merge(other Attrs) Attrs {
	out := make(Attrs, len(a))
	copy(out, a)
	for _, kv := range other {
		idx := -1
		for i := range out {
			if out[i].Key == kv.Key {
				idx = i
				break
			}
		}
		switch {
		case idx < 0:
			out = append(out, kv)
		case kv.Key == "class" || kv.Key == "style":
			out[idx].Value = joinAttrStrings(kv.Key, toStr(out[idx].Value), toStr(kv.Value))
		default:
			out[idx].Value = kv.Value
		}
	}
	return out
}

// AttrsCond selects one of two attribute-bag thunks for a conditional component
// attribute: it calls and returns then() when cond is true, otherwise els(). The
// branches are THUNKS so the untaken branch is never evaluated — mirroring a real Go
// if/else, where the untaken block's expressions (e.g. u.Name when u == nil) never run.
// els may be nil (no else branch); a false cond then yields a nil bag (a nil Attrs
// merges as empty). Generated code chains this into a bag-building .Merge(...).
func AttrsCond(cond bool, then, els func() Attrs) Attrs {
	if cond {
		if then != nil {
			return then()
		}
	} else if els != nil {
		return els()
	}
	return nil
}

// Spread renders the bag in SLICE ORDER (no sort). A bool value uses boolean-attribute
// semantics (true → bare attribute, false → omitted); everything else is written as
// key="value" with attribute escaping. A key that is not a structurally valid HTML
// attribute name (see validAttrName) is SKIPPED rather than emitted. Values are
// attribute-escaped but NOT URL-sanitized (see Attrs). ctx is reserved for
// forward-compatibility.
func (gw *Writer) Spread(ctx context.Context, a Attrs) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	for _, kv := range a {
		if !validAttrName(kv.Key) {
			continue // unsafe/invalid attribute name — drop it
		}
		if b, ok := kv.Value.(bool); ok {
			gw.BoolAttr(kv.Key, b)
			continue
		}
		gw.writeStr(" ")
		gw.writeStr(kv.Key)
		gw.writeStr(`="`)
		gw.AttrValue(toStr(kv.Value))
		gw.writeStr(`"`)
	}
}

// validAttrName reports whether k is a structurally safe HTML attribute name: non-empty
// and free of whitespace, control bytes, and the characters that could break out of the
// tag or the name (" ' < > = / &). Names like "hx-on::click", ":class", "@click.away",
// "data-x", and "_" pass.
func validAttrName(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c <= ' ' || c == 0x7f {
			return false
		}
		switch c {
		case '"', '\'', '<', '>', '=', '/', '&':
			return false
		}
	}
	return true
}

// toStr renders an attribute/class value to a string.
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		return strings.Join(t, " ")
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// joinAttrStrings concatenates two non-empty class/style values with the right
// separator (space for class, "; " for style).
func joinAttrStrings(key, a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	if key == "style" {
		return a + "; " + b
	}
	return a + " " + b
}
```

Then: `rm -f orderedattrs.go orderedattrs_test.go`.

- [ ] **Step 4: Run tests to verify they pass (root package only)**

Run: `go test ./ -run 'TestAttrs|TestSpread'` then `go vet ./` and `gofmt -l attrs.go attrs_test.go`
Expected: PASS; `gofmt -l` prints nothing. (`go build ./...` still fails — expected, Task 2 fixes codegen.)

- [ ] **Step 5: Commit**

```bash
git add attrs.go attrs_test.go && git rm orderedattrs.go orderedattrs_test.go
git commit -m "feat(runtime): Attrs is the ordered bag; AttrMap alias + AttrsFromMap

Promote the []Attr slice (was OrderedAttrs) to be the one Attrs type, rendered in
slice order. Add AttrMap = map[string]any alias and AttrsFromMap (sorts keys).
Class/Style aggregate duplicate keys; Get/Has first-wins; Without removes all;
Merge overwrites in place. Delete OrderedAttrs/SpreadOrdered. (Codegen catches up
in the next commit; ./... does not build until then.)"
```

---

### Task 2: Codegen — emit slice literals, collapse spread dispatch, regenerate corpus

**Files:**
- Modify: `internal/codegen/emit.go` (bag entries `:2383,:2441,:2454,:2484`; `condBranchAttrs` `:2652,:2658,:2660,:2671`; `{{ }}` lowering `:2127,:2519`; spread dispatch `:612-616` and `:1328-1333`; error messages `:1357-1361,:2504-2507`)
- Regenerate: `internal/corpus/testdata/cases/**/*.txtar` + `coverage.golden`

**Interfaces:**
- Consumes: the runtime API from Task 1 (`gsx.Attrs` is `[]Attr`; `Spread` ordered; methods present).
- Produces: generated code that references only `gsx.Attrs` (no `OrderedAttrs`/`SpreadOrdered`); the implicit bag, `{{ }}` literal, and conditional-attr bags all emit `gsx.Attrs{{Key: …, Value: …}, …}` slice literals.

- [ ] **Step 1: Flip the implicit-bag entry format.** In `emit.go`, change the four `bag = append(...)` sites from map-entry to slice-element form (the surrounding `gsx.Attrs{…}` wrapper at `:2568` is unchanged — it now wraps slice elements):

```go
// :2383 (StaticAttr)
bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(t.Value)))
// :2441 (ExprAttr)
bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), val))
// :2454 (BoolAttr)
bag = append(bag, fmt.Sprintf("{Key: %s, Value: true}", strconv.Quote(t.Name)))
// :2484 (ClassAttr → ClassJoin entry)
bag = append(bag, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), entry))
```

- [ ] **Step 2: Flip the conditional-attr branch entries.** In `condBranchAttrs` change the four entry formats the same way (the `gsx.Attrs{%s}` wrapper at `:2677` is unchanged):

```go
// StaticAttr
entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strconv.Quote(t.Value)))
// ExprAttr
entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), strings.TrimSpace(t.Expr)))
// BoolAttr
entries = append(entries, fmt.Sprintf("{Key: %s, Value: true}", strconv.Quote(t.Name)))
// ClassAttr
entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", strconv.Quote(t.Name), entry))
```

- [ ] **Step 3: Retarget the `{{ }}` lowering to `gsx.Attrs`.** At `emit.go:2127` and `:2519` change the literal type name (the `{Key:…, Value:…}` element bodies are already slice-shaped):

```go
// :2127
fmt.Fprintf(&sb, "%s: gsx.Attrs{", fe.fieldName)
// :2519
fmt.Fprintf(&sb, "%s: %s.Attrs{", fn, rtPkg)
```

- [ ] **Step 4: Collapse the spread dispatch to one path.** At `emit.go:612-616` replace the ordered/plain branch with a single `Spread`, and remove the now-unused `orderedFields` local (its only reads are here and at `:1328`):

```go
// :612-616 (emitFallthroughAttrs SpreadAttr case)
fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", spreadExpr)
```

Do the same at `emit.go:1328-1333` (the `emitAttr` `*ast.SpreadAttr` case): emit only `_gsxgw.Spread(ctx, %s)`. Then delete the `orderedFields := orderedProps[propsName]` local (`~:246`) and remove `orderedFields` reads. Leave the `orderedProps` parameters/returns threaded for now (unused params/returns do not break the build); they are deleted in Task 4. If any local or import becomes unused and fails the build, remove that local/import in this task.

- [ ] **Step 5: Update the two error messages that name the old type.** At `emit.go:1357-1361` (`{{ }}` on a plain element) and `:2504-2507` (no field for an ordered literal), change `gsx.OrderedAttrs` → `gsx.Attrs` in the message text.

- [ ] **Step 6: Build, regenerate goldens, review the diff**

Run: `go build ./... && go test ./internal/corpus -run TestCorpus -update`
Then **review the regenerated diff by hand** — confirm: (a) no `gsx.OrderedAttrs` / `SpreadOrdered` remain (`grep -rn 'OrderedAttrs\|SpreadOrdered' internal/corpus/testdata` → empty); (b) fallthrough `render.golden`s changed from alphabetical to **source order** (e.g. `internal/corpus/testdata/cases/fallthrough/*`); (c) bag literals are now `gsx.Attrs{{Key: …}}`.

- [ ] **Step 7: Verify without `-update`**

Run: `go test ./internal/corpus -run TestCorpus` then `make check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(codegen): emit ordered gsx.Attrs slice literals; collapse spread dispatch

Implicit bag, {{ }} literal, and conditional-attr bags emit gsx.Attrs{{Key,Value}}
slice literals in source order. Spread dispatch collapses to one ordered Spread.
Error messages reference gsx.Attrs. Regenerate corpus: fallthrough bags now render
in source order."
```

---

### Task 3: `AttrMap → Attrs` auto-conversion at bag boundaries

**Files:**
- Modify: `internal/codegen/emit.go` (element-spread emit site `:612` region; child-prop value emit in `childPropsLiteral`) and/or `internal/codegen/analyze.go` (where prop-value / spread types are resolved — the same `resolved map[ast.Node]types.Type` the `(T,error)` unwrap consults)
- Create corpus: `internal/corpus/testdata/cases/attrmap/prop_binding.txtar`, `attrmap/element_spread.txtar`, `attrmap/bare_map_param.txtar`, `attrmap/tuple_map_compose.txtar`

**Interfaces:**
- Consumes: resolved expression types (already available where the `(T,error)` unwrap runs); `gsx.AttrsFromMap` from Task 1.
- Produces: a helper `func isStringAnyMap(t types.Type) bool` and conversion-wrapping at the two boundaries.

- [ ] **Step 1: Write the failing corpus case — prop binding.** Create `internal/corpus/testdata/cases/attrmap/prop_binding.txtar`:

```
# A map[string]any value bound to an Attrs bag prop auto-converts (sorted).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Card(bag gsx.Attrs) {
	<div { bag... }>x</div>
}

component Page() {
	<Card bag={gsx.AttrMap{"id": "x", "class": "c"}}/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div class="c" id="x">x</div>
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/corpus -run 'TestCorpus/attrmap/prop_binding'`
Expected: FAIL — generated code binds the raw map to an `Attrs` (slice) field → `cannot use … map … as gsx.Attrs`.

- [ ] **Step 3: Add the map-type predicate.** In `internal/codegen/analyze.go` (near the other type predicates) add:

```go
// isStringAnyMap reports whether t is exactly map[string]any (gsx.AttrMap). Only this
// map shape auto-converts to Attrs at a bag boundary; any other map stays a type error.
func isStringAnyMap(t types.Type) bool {
	m, ok := t.Underlying().(*types.Map)
	if !ok {
		return false
	}
	if b, ok := m.Key().Underlying().(*types.Basic); !ok || b.Kind() != types.String {
		return false
	}
	iface, ok := m.Elem().Underlying().(*types.Interface)
	return ok && iface.NumMethods() == 0
}
```

- [ ] **Step 4: Wrap at the two boundaries.** Where a child-prop value expression is emitted into the props literal for a field whose resolved type is `gsx.Attrs`, and where an element-spread expression is emitted (`emit.go:612` region), check the value's resolved type with `isStringAnyMap`; if true, wrap the final expression (the unwrap temp if the value was a `(T,error)` call already hoisted, else the raw expr) as `gsx.AttrsFromMap(<expr>)`. Apply the wrap AFTER the `(T,error)` unwrap so a `(map[string]any, error)` call unwraps to a temp first, then converts. Pseudostructure at each site:

```go
expr := <final value expression, post-unwrap>
if isStringAnyMap(resolvedTypeOf(valueNode)) {
	expr = fmt.Sprintf("%s.AttrsFromMap(%s)", rtPkg, expr)
}
// emit expr into the props literal field / the Spread(ctx, expr) call
```

- [ ] **Step 5: Add the remaining cases.** Create:

`attrmap/element_spread.txtar` (map param spread on an element):
```
# A map[string]any param spread on an element auto-converts (sorted).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Card(m gsx.AttrMap) {
	<div { m... }>x</div>
}

component Page() {
	<Card m={gsx.AttrMap{"data-b": "2", "data-a": "1"}}/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div data-a="1" data-b="2">x</div>
```

`attrmap/bare_map_param.txtar` (bare `map[string]any`, not the alias):
```
# A bare map[string]any (not the gsx.AttrMap alias) also auto-converts.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Card(m map[string]any) {
	<div { m... }>x</div>
}

component Page() {
	<Card m={map[string]any{"z": "1", "a": "2"}}/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div a="2" z="1">x</div>
```

`attrmap/tuple_map_compose.txtar` (`(map[string]any, error)` → unwrap then convert):
```
# A (map[string]any, error) call in a bag prop unwraps, then auto-converts.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

func attrsOf(id string) (map[string]any, error) {
	return map[string]any{"id": id, "class": "c"}, nil
}

component Card(bag gsx.Attrs) {
	<div { bag... }>x</div>
}

component Page() {
	<Card bag={attrsOf("e1")}/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div class="c" id="e1">x</div>
```

- [ ] **Step 6: Regenerate + verify**

Run: `go test ./internal/corpus -run 'TestCorpus/attrmap' -update` then review each `generated.x.go.golden` (confirm `AttrsFromMap(` wraps appear, and the tuple case shows unwrap-to-temp THEN `AttrsFromMap(<temp>)`). Then `go test ./internal/corpus -run 'TestCorpus/attrmap'` (no `-update`) and `make check`.
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(codegen): implicit AttrMap->Attrs conversion at bag boundaries

A map[string]any value (gsx.AttrMap or bare) bound to an Attrs prop or spread on an
element auto-wraps with gsx.AttrsFromMap (sorted). Composes with (T,error) unwrap."
```

---

### Task 4: Delete the dead two-type plumbing

**Files:**
- Modify: `internal/codegen/analyze.go` (`isOrderedAttrsType` `:259-261` and its callers `:90,:65,:757`), `internal/codegen/module_importer.go` (`orderedProps` field/threading `:340,:395,:658`), `internal/codegen/module.go` (`:401,:449`), `internal/codegen/emit.go` (`orderedProps`/`orderedFields` params on `generateFile`/`genComponent`/`emitRootElement`/`emitFallthroughAttrs`/`emitManualSpreadElement`)

**Interfaces:**
- Consumes: nothing new.
- Produces: the codegen API with the `orderedProps`/`orderedFields` parameters and `isOrderedAttrsType` removed. Pure dead-code deletion; generated output is byte-identical to Task 3.

- [ ] **Step 1: Remove `isOrderedAttrsType` and its callers.** Delete the function (`analyze.go:259-261`) and the `orderedProps` map it populated in `genProps` (`:90`); stop returning `orderedProps` from `componentPropFieldsFor` (`:65`) and from `childPropsLiteral` plumbing (`:757`).

- [ ] **Step 2: Unthread `orderedProps`/`orderedFields`.** Remove the parameter from `generateFile`, `genComponent`, `emitRootElement`, `emitFallthroughAttrs`, `emitManualSpreadElement` (the `emit.go` sites the touchpoint map lists: `:29,:107,:210,:273,:383,:388,:419,:466,:503-545,:651`) and the `orderedProps` field + threading in `module_importer.go` (`:340,:395,:658`) and `module.go` (`:401,:449`).

- [ ] **Step 3: Build + full corpus (no output change expected)**

Run: `go build ./... && go test ./internal/corpus -run TestCorpus`
Expected: PASS with **zero golden changes** (`git status` shows only `.go` edits). If any golden changed, a deletion altered behavior — revert and investigate.

- [ ] **Step 4: Confirm no stragglers**

Run: `grep -rn 'OrderedAttrs\|SpreadOrdered\|orderedProps\|orderedFields\|isOrderedAttrsType' internal/codegen` then `gopls check -severity=hint internal/codegen/emit.go internal/codegen/analyze.go`
Expected: only AST-node references remain (`OrderedAttrsAttr`/`OrderedPair`, intentionally kept); no `orderedProps`/`isOrderedAttrsType`; no new unused-symbol hints.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(codegen): delete dead OrderedAttrs two-type dispatch plumbing"
```

---

### Task 5: Headline corpus coverage — source order, `{{ }}`→bag prop

**Files:**
- Create corpus: `internal/corpus/testdata/cases/fallthrough/source_order.txtar`, `internal/corpus/testdata/cases/orderedattrs/literal_to_attrs_prop.txtar`

**Interfaces:**
- Consumes: behavior from Tasks 1–3.
- Produces: pinned cases proving the two headline behavior changes.

- [ ] **Step 1: Write the source-order fallthrough case.** Create `internal/corpus/testdata/cases/fallthrough/source_order.txtar`:

```
# Fallthrough bag renders in SOURCE order (not alphabetical).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Field(label string) {
	<input { attrs... }/>
}

component Page() {
	<Field label="Email" type="email" placeholder="you@co" name="email"/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<input type="email" placeholder="you@co" name="email"/>
```

- [ ] **Step 2: Write the `{{ }}`→plain-`Attrs`-prop case.** Create `internal/corpus/testdata/cases/orderedattrs/literal_to_attrs_prop.txtar`:

```
# A {{ }} literal binds to a plain gsx.Attrs prop and renders in source order.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component Card(bag gsx.Attrs) {
	<div { bag... }>x</div>
}

component Page() {
	<Card bag={{ "data-b": "2", "data-a": "1" }}/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div data-b="2" data-a="1">x</div>
```

- [ ] **Step 3: Regenerate + verify**

Run: `go test ./internal/corpus -run 'TestCorpus/fallthrough/source_order|TestCorpus/orderedattrs/literal_to_attrs_prop' -update`
Then review the goldens (source order preserved, no sort), then run the same without `-update`, then `make check`.
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "test(corpus): pin source-order fallthrough + {{ }}->Attrs-prop binding"
```

---

### Task 6: Docs — rewrite the ordered-attributes section

**Files:**
- Modify: `docs/guide/syntax.md` (`:128-241` ordered-attributes section + `:37,:56,:64,:66-67,:104,:105` bag-prop/spread mentions)

**Interfaces:** none (prose).

- [ ] **Step 1: Rewrite the "Ordered attributes" section.** Invert the framing:
  - `gsx.Attrs` is the **default ordered bag** (`[]gsx.Attr`), rendered in source order; it is the implicit fallthrough bag and every declared bag prop, and the `{{ "k": v }}` literal lowers to it.
  - `gsx.AttrMap = map[string]any` is the **map convenience**; gsx auto-converts it (via `gsx.AttrsFromMap`, sorting keys) at a component bag-prop binding and an element spread; a bare `map[string]any` works too.
  - Document the **duplicate-key rules** prominently — especially that `Class()`/`Style()` **aggregate** (join all matching pairs; nothing dropped), `Get`/`Has` are first-wins, `Merge` overwrites in place.
  - Flip the comparison table (`:225-241`): the slice is the default `gsx.Attrs`; the map is `gsx.AttrMap` (auto-sorted).
  - Update bag-prop/spread mentions (`:37,:56,:64,:66-67,:104,:105`) so `Attrs gsx.Attrs` reads as "ordered bag."

- [ ] **Step 2: Verify doc references compile against the corpus.** If `syntax.md` references corpus case paths (e.g. `:343`), update them to the new/renamed cases (`attrmap/*`, `fallthrough/source_order`, `orderedattrs/literal_to_attrs_prop`).

- [ ] **Step 3: Commit**

```bash
git add docs/guide/syntax.md
git commit -m "docs(guide): Attrs is the ordered default; AttrMap is the auto-sorted map"
```

---

### Task 7: Migrate `one-learning` to the new semantics

**Files:**
- The `one-learning` repo (migration worktree `one-learning-gsx`, branch `gsx-migration`) — audit + fix only; no gsx-repo changes.

**Interfaces:** none in this repo.

- [ ] **Step 1: Audit map-style `gsx.Attrs` usage.** In the one-learning UI package, grep for code that treats `gsx.Attrs` as a map: `gsx.Attrs{` map literals (`"k": v` form), `attrs[` index reads/writes, `range` over an `Attrs`, `len(` / `delete(` on an `Attrs`. List each site.

- [ ] **Step 2: Convert each site.** For genuine map needs, declare `gsx.AttrMap` (or a plain `map[string]any`) and let the boundary auto-convert. For bag construction in `.gsx`, use `{{ }}` (ordered) or a map literal (auto-sorted). Replace `gsx.Attrs{"k": v}` map literals with either `gsx.AttrMap{"k": v}` (if sort is fine) or `gsx.Attrs{{Key:"k", Value:v}}` (if order matters).

- [ ] **Step 3: Build + the DOM-equivalence harness**

Run (in the one-learning worktree): the package build + the existing templ↔gsx DOM-equivalence test harness.
Expected: PASS — rendered DOM still equivalent (attribute *order* may change vs. the old alphabetical bag; the DOM-equivalence harness compares attribute sets, not order, so equivalence holds; spot-check any order-sensitive markup).

- [ ] **Step 4: Commit** (in the one-learning repo, per its conventions).

---

## Self-Review

**Spec coverage:**
- §3 type model → Task 1. §4 method/render semantics → Task 1 (+ unit tests). §5 auto-conversion → Task 3. §6 codegen (slice literals, dispatch collapse, lowering, messages, field unchanged) → Task 2; dead-plumbing removal → Task 4. §7 parser/AST (node names kept, lowering retargeted) → Task 2 (lowering) — AST node names unchanged, no task needed. §8 `{{ }}` role → Task 5 + Task 6. §9 migration (runtime, goldens, one-learning) → Tasks 1/2/7. §10 testing → Tasks 1/3/5 + adversarial probe at execution. §11 docs → Task 6; sibling-repo grammar is unchanged by this change (follow-up #2 tracked separately) — no task. §13 risks (source-order fidelity, golden review, over-firing) → Task 2 Step 6 review, Task 3 Step 3 predicate. All covered.

**Placeholder scan:** Runtime + test code is complete (Task 1). Codegen edits give exact sites + target patterns; Task 3 Step 4 is the one structural (non-line-exact) edit — it specifies the predicate, the wrap expression, the two boundaries, and the compose-after-unwrap ordering, with corpus cases as the executable contract. No "TBD"/"handle edge cases"/"similar to" placeholders.

**Type consistency:** `Attrs`, `Attr{Key,Value}`, `AttrMap`, `AttrsFromMap`, `isStringAnyMap`, `AttrsCond(cond, then, els func() Attrs)` are spelled identically across tasks. Emitted literal shape `gsx.Attrs{{Key: …, Value: …}}` is consistent in Tasks 2/3/5. Method names (`Class/Style/Get/Has/Without/Take/Merge`) match the runtime and the codegen call sites (`emit.go:523-564`).
