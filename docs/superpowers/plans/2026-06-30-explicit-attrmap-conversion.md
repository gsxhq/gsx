# Explicit `AttrMap` Conversion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `gsx.Attrs` the only template attribute-bag type and require map-backed callers to convert a defined `gsx.AttrMap` explicitly with `ToAttrs()`.

**Architecture:** The runtime owns the sole unordered-to-ordered conversion through `AttrMap.ToAttrs`. Codegen becomes type-agnostic about maps: its synthetic type-checking helpers require `gsx.Attrs`, and emission passes `Attrs` expressions directly without inserting conversions. Corpus fixtures pin both explicit conversion and ordinary Go type errors for unconverted maps.

**Tech Stack:** Go, `go/types`, GSX code generator, txtar corpus tests, Markdown guide.

---

## File map

- `attrs.go`: define `AttrMap` and its sorted `ToAttrs` conversion.
- `attrs_test.go`: runtime conversion contract.
- `internal/codegen/analyze.go`: remove the obsolete map-shape predicate and map-coercion commentary.
- `internal/codegen/module_importer.go`: make the synthetic `_gsxbag` helper accept only `gsx.Attrs`.
- `internal/codegen/emit.go`: remove implicit map wrapping at element spreads and child-component prop bindings.
- `internal/corpus/testdata/cases/attrmap/*.txtar`: replace implicit-conversion fixtures with explicit conversion and rejection coverage.
- `docs/guide/syntax/attributes.md`: document the single template bag type and explicit conversion.
- `docs/guide/syntax/std-functions.md`: document `AttrMap.ToAttrs` and remove `AttrsFromMap`.

### Task 1: Runtime conversion API

**Files:**
- Modify: `attrs_test.go`
- Modify: `attrs.go`

- [ ] **Step 1: Rename the runtime test to require `ToAttrs`**

Replace the current `AsAttrs` test with:

```go
func TestAttrMapToAttrsSorts(t *testing.T) {
	got := AttrMap{"id": "x", "class": "c", "data-z": 1}.ToAttrs()
	want := Attrs{{Key: "class", Value: "c"}, {Key: "data-z", Value: 1}, {Key: "id", Value: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToAttrs = %v, want %v", got, want)
	}
	if (AttrMap)(nil).ToAttrs() != nil {
		t.Fatal("AttrMap(nil).ToAttrs() should be nil")
	}
	if (AttrMap{}).ToAttrs() != nil {
		t.Fatal("empty AttrMap.ToAttrs() should be nil")
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test . -run TestAttrMapToAttrsSorts
```

Expected: compilation fails because `AttrMap` has no `ToAttrs` method.

- [ ] **Step 3: Implement the minimal runtime API**

In `attrs.go`, retain the defined type and rename the method:

```go
// AttrMap is a map-form attribute bag for ergonomic Go literals; convert it to Attrs
// explicitly with ToAttrs before passing or spreading it in templates.
type AttrMap map[string]any

// ToAttrs converts m to an ordered Attrs slice with keys sorted ascending.
func (m AttrMap) ToAttrs() Attrs {
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
```

Ensure there is no `AttrsFromMap` function and no `AsAttrs` method.

- [ ] **Step 4: Verify GREEN**

Run:

```bash
go test . -run TestAttrMapToAttrsSorts
```

Expected: PASS.

- [ ] **Step 5: Commit the runtime API**

```bash
git add attrs.go attrs_test.go
git commit -m "refactor(runtime): make AttrMap conversion explicit"
```

### Task 2: Remove implicit map handling from codegen

**Files:**
- Modify: `internal/corpus/testdata/cases/attrmap/prop_binding.txtar`
- Modify: `internal/corpus/testdata/cases/attrmap/element_spread.txtar`
- Modify: `internal/codegen/analyze.go`
- Modify: `internal/codegen/module_importer.go`
- Modify: `internal/codegen/emit.go`

- [ ] **Step 1: Change positive corpus cases to explicit conversion**

In `prop_binding.txtar`, change the binding to:

```gsx
<Card bag={gsx.AttrMap{"id": "x", "class": "c"}.ToAttrs()}/>
```

In `element_spread.txtar`, make the component accept only `Attrs` and convert at the caller:

```gsx
component Card(attrs gsx.Attrs) {
	<div { attrs... }>x</div>
}

component Page() {
	<Card attrs={gsx.AttrMap{"data-b": "2", "data-a": "1"}.ToAttrs()}/>
}
```

Update each generated golden so the emitted props literal contains the source
`.ToAttrs()` call and each `Spread` receives the `Attrs` parameter directly. No golden
may contain `AttrsFromMap`.

- [ ] **Step 2: Run the positive corpus cases and verify RED**

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/attrmap/(prop_binding|element_spread)'
```

Expected: FAIL with generated/render golden mismatches because the fixtures now require explicit `ToAttrs` source and direct `Attrs` emission.

- [ ] **Step 3: Simplify the synthetic bag constraint**

In `internal/codegen/module_importer.go`, replace the union helper with:

```go
func _gsxbag(v _gsxrt.Attrs) _gsxrt.Attrs { return v }
```

Update the adjacent comment to state that `_gsxbag` preserves `gsx.Attrs` field
checking and admits no map type.

- [ ] **Step 4: Delete map recognition and emission wrappers**

In `internal/codegen/analyze.go`, delete `isStringAnyMap` and comments that describe
implicit map conversion.

In `internal/codegen/emit.go`:

- delete `maybeAttrsFromMap`;
- pass `spreadExpr` directly to both `Spread` emission sites;
- delete the post-tuple loop that wraps `gsx.Attrs` fields with
  `gsx.AttrsFromMap`;
- retain `_gsxbag(fieldVal)` for non-nil values targeting an `Attrs` field, but update
  its comment to say it enforces the sole `Attrs` shape;
- remove obsolete `AttrsFromMap` and map-coercion comments.

The resulting element emission is:

```go
fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s)\n", spreadExpr)
```

The props entry remains:

```go
str = fmt.Sprintf("%s: %s", fn, fieldVal)
```

with `_gsxbag(...)` used only by the type-checking probe.

- [ ] **Step 5: Update positive goldens and verify GREEN**

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/attrmap/(prop_binding|element_spread)' -update
go test ./internal/corpus -run 'TestCorpus/attrmap/(prop_binding|element_spread)'
```

Expected: both cases PASS; generated output contains `.ToAttrs()` and no
`AttrsFromMap`.

- [ ] **Step 6: Run focused codegen tests**

Run:

```bash
go test ./internal/codegen
```

Expected: PASS.

- [ ] **Step 7: Commit codegen simplification**

```bash
git add internal/codegen/analyze.go internal/codegen/module_importer.go internal/codegen/emit.go internal/corpus/testdata/cases/attrmap/prop_binding.txtar internal/corpus/testdata/cases/attrmap/element_spread.txtar
git commit -m "refactor(codegen): accept only Attrs at bag boundaries"
```

### Task 3: Replace implicit-map corpus coverage with rejection coverage

**Files:**
- Modify: `internal/corpus/testdata/cases/attrmap/bare_map_param.txtar`
- Modify: `internal/corpus/testdata/cases/attrmap/named_map_type.txtar`
- Modify: `internal/corpus/testdata/cases/attrmap/tuple_map_compose.txtar`
- Modify: `internal/corpus/testdata/cases/attrmap/map_string_string_no_convert.txtar`
- Modify: `internal/corpus/testdata/cases/attrmap/nil_prop.txtar`
- Modify: `internal/corpus/testdata/cases/attrmap/spread_undefined_error.txtar`

- [ ] **Step 1: Rewrite map fixtures as normal type-error cases**

Keep their existing source expressions unconverted, but change the fixture descriptions
to state that only `gsx.Attrs` is accepted:

```text
# A bare map is not an attribute bag; template spreads require gsx.Attrs.
```

```text
# A named map does not implicitly convert when bound to a gsx.Attrs prop.
```

```text
# A tuple-returned map may unwrap, but it does not implicitly convert to gsx.Attrs.
```

For `map_string_string_no_convert.txtar`, remove references to the old union constraint.
For `nil_prop.txtar`, describe direct nil-to-slice assignment without mentioning
`AttrsFromMap` or `_gsxbag` exemptions. For `spread_undefined_error.txtar`, describe
undefined-variable reporting without map conversion.

- [ ] **Step 2: Run rejection cases and verify RED**

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/attrmap' 
```

Expected: FAIL because diagnostics and generated/render goldens still describe implicit
map conversion.

- [ ] **Step 3: Refresh and inspect corpus goldens**

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/attrmap' -update
```

Inspect every changed diagnostic. Unconverted map cases must report ordinary assignment
or argument incompatibility with `gsx.Attrs`; diagnostics must not contain
`~map[string]any`, `_gsxbag`, or `AttrsFromMap`. Remove stale generated and render
goldens for cases that now fail type checking.

- [ ] **Step 4: Verify all AttrMap corpus cases**

Run:

```bash
go test ./internal/corpus -run 'TestCorpus/attrmap'
rg -n 'AttrsFromMap|~map\\[string\\]any|auto-convert' internal/corpus/testdata/cases/attrmap
```

Expected: corpus tests PASS; `rg` returns no matches.

- [ ] **Step 5: Commit the corpus contract**

```bash
git add internal/corpus/testdata/cases/attrmap
git commit -m "test(codegen): reject implicit map attribute bags"
```

### Task 4: Update public documentation and complete verification

**Files:**
- Modify: `docs/guide/syntax/attributes.md`
- Modify: `docs/guide/syntax/std-functions.md`

- [ ] **Step 1: Update the attributes guide**

Replace implicit conversion guidance with:

````markdown
Templates use `gsx.Attrs` for attribute bags and spreads. `map[string]any` and
`gsx.AttrMap` are not accepted implicitly.

When map-backed construction is convenient, convert explicitly:

```go
attrs := gsx.AttrMap{
    "class": "card",
    "id":    id,
}.ToAttrs()
```

`ToAttrs` sorts keys ascending because maps do not preserve insertion order. Construct
`gsx.Attrs` directly when attribute order is significant.
````

- [ ] **Step 2: Update the standard-functions table**

Document:

```markdown
| `gsx.AttrMap` | type | `type AttrMap map[string]any` | Optional map-backed construction helper. Call `ToAttrs()` explicitly; keys are sorted ascending. |
| `AttrMap.ToAttrs` | method | `func (m AttrMap) ToAttrs() Attrs` | Converts an `AttrMap` to ordered `Attrs`, sorting keys ascending. |
```

Delete the `AttrsFromMap` row and all statements that bare `map[string]any` values are
accepted.

- [ ] **Step 3: Verify stale API references are gone from live code and guides**

Run:

```bash
rg -n 'AttrsFromMap|AsAttrs|~map\\[string\\]any|auto-convert' --glob '*.go' --glob '*.txtar' --glob '*.md' --glob '!docs/superpowers/**' .
```

Expected: no references to removed behavior. Historical design/plan documents under
`docs/superpowers` may retain their original record.

- [ ] **Step 4: Format and run the full test suite**

Run:

```bash
gofmt -w attrs.go attrs_test.go internal/codegen/analyze.go internal/codegen/module_importer.go internal/codegen/emit.go
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Run repository checks**

Run:

```bash
make check
git diff --check
git status --short
```

Expected: checks PASS, no whitespace errors, and status contains only the intended
documentation changes before commit.

- [ ] **Step 6: Commit documentation**

```bash
git add docs/guide/syntax/attributes.md docs/guide/syntax/std-functions.md
git commit -m "docs: require explicit AttrMap conversion"
```
