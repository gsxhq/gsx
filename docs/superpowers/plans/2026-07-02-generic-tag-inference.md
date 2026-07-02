# Generic Tag Inference Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Infer omitted generic component tag type arguments through the Go type checker and emit explicit instantiated Go.

**Architecture:** Add skeleton-only inference helpers for generated-props generic components. Probe omitted generic tag calls through those helpers, harvest the instantiated props type, and reuse the inferred type arguments during final emission. Inference failures surface a gsx diagnostic asking the user to instantiate explicitly.

**Tech Stack:** Go, `go/types.Info.Types`, gsx corpus tests.

---

### Task 1: Corpus Coverage

**Files:**
- Create: `internal/corpus/testdata/cases/components/generic_inferred_tag.txtar`
- Create: `internal/corpus/testdata/cases/components/generic_inference_failed_diag.txtar`

- [ ] **Step 1: Add success corpus case**

Use a generic component call without explicit tag type arguments:

```txtar
-- input.gsx --
package views

component Greeting[V ~string, W any](v V, w W) {
	<p>{ v }</p>
	<p>{ w |> format("%v") }</p>
}

component Page() {
	<Greeting v="hello" w="world" />
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<p>hello</p><p>world</p>
```

- [ ] **Step 2: Add failure corpus case**

Use a generic component whose type parameter has no prop evidence:

```txtar
-- input.gsx --
package views

component Marker[T any]() {
	<p>marker</p>
}

component Page() {
	<Marker />
}
-- invoke --
Page()
-- diagnostics.golden --
8:2: type inference failed for <Marker>; please instantiate with <Marker[type] ...>
```

- [ ] **Step 3: Verify red**

Run: `go test ./internal/corpus -run 'TestCorpus/.*/generic_(inferred_tag|inference_failed_diag)' -count=1`

Expected: `generic_inferred_tag` fails with `cannot use generic type GreetingProps... without instantiation`, and `generic_inference_failed_diag` fails until diagnostics are implemented.

### Task 2: Skeleton Inference

**Files:**
- Modify: `internal/codegen/analyze.go`
- Modify: `internal/codegen/module_importer.go`
- Modify: `internal/codegen/emit.go`

- [ ] **Step 1: Emit skeleton-only helpers**

For each generated-props generic component, emit an exported helper named `GsxInfer<Name>` after the props struct in the skeleton. The helper has one ordinary parameter per component prop and returns the instantiated props type.

- [ ] **Step 2: Probe omitted type arguments through the helper**

When a component tag has no `TypeArgs`, build the props evidence as helper arguments and emit `_ = Component(GsxInferComponent(args...))` in the skeleton. Dotted tags use `pkg.GsxInferComponent`.

- [ ] **Step 3: Harvest inferred props instantiations**

Read `info.Types[helperCall].Type`; when it is a generic named props instantiation, record it on the original `*ast.Element`.

### Task 3: Emit and Diagnostics

**Files:**
- Modify: `internal/codegen/emit.go`

- [ ] **Step 1: Use harvested type args in final emission**

For omitted type args with a harvested props instantiation, emit those type args on both the component call and props literal.

- [ ] **Step 2: Report inference failure**

For omitted type args on a generic component with no harvested instantiation, report:

```text
type inference failed for <Name>; please instantiate with <Name[type, type] ...>
```

- [ ] **Step 3: Verify green**

Run: `go test ./internal/corpus -run 'TestCorpus/.*/generic_(inferred_tag|inference_failed_diag)' -count=1`

Expected: PASS.

### Task 4: Focused Regression

**Files:**
- Existing code only.

- [ ] **Step 1: Run focused suites**

Run: `go test ./internal/codegen ./internal/corpus -count=1`

Expected: PASS.

- [ ] **Step 2: Inspect generated success golden**

Confirm `generic_inferred_tag.txtar` contains an explicit instantiated call like:

```go
Greeting[string, string](GreetingProps[string, string]{V: "hello", W: "world"})
```
