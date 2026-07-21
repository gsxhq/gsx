# Generated Direct Component Rendering

**Date:** 2026-07-21
**Status:** Ready for measured implementation
**Scope:** Proven same-package calls to plain-function GSX components

## Goal

Remove the per-call `gsx.Func` closure allocation when generated GSX calls a
plain-function GSX component in the same package. Keep the public `gsx.Node`
factory for Go callers and every target that cannot be proved eligible from the
compiler's existing semantic facts.

This is a measured experiment. Retain it only if generated `gsx-bench` Table
improves by at least 10% on both pooled and discard destinations, removes at
least 90% of its bytes and allocations, preserves exact rendering/error
semantics, and passes the unaffected-path regression gates below.

## Evidence

The restored baseline renders twenty `Card` children as:

```go
_gsxgw.Node(ctx, Card(r, nil))
```

Table measures 21 allocations. A rate-1 allocation profile attributes twenty
of them, 95.18% of objects and 97.59% of bytes, to the child component
factories. A prior struct-backed `Node` experiment retained those allocations
at the `Writer.Node(Node)` interface boundary and is rejected.

A throwaway probe at `/tmp/gsx-runtime-audit/candidate3-probe` copied the
generated Table/Card bodies and replaced only the proven child factory boundary
with a direct body call. It checks byte output, every failing destination-write
boundary, source-order argument evaluation after an earlier error, and raw
`return sentinel` and raw `return nil` after each child write boundary.

The final probe models the current `Writer.Node` assignment exactly: a direct
helper shares the writer, then an unconditional node-result operation applies
the helper's return value. This distinction matters. A first-error-only latch
would change current semantics when a raw component return follows a failed
write, and is not acceptable.

Ten process pairs, counterbalanced odd/current-direct and
even/direct-current, on Go 1.26.1, Apple M3 Ultra, `GOMAXPROCS=32`, produced:

| Table destination | Time | Bytes | Allocations |
| --- | ---: | ---: | ---: |
| discard | -19.16%, `p < 0.001` | 1,952 to 32 (-98.36%) | 21 to 1 (-95.24%) |
| pooled | -13.70%, `p < 0.001` | 1,954 to 32 (-98.36%) | 21 to 1 (-95.24%) |

Raw data is in
`/tmp/gsx-runtime-candidate3-resultsetter-results.dr93VE`. The probe source
SHA-256 is
`3468c7b4d38b4ca2989c9b91e8c52ff46ae07570035686a746e2ac1a842963b9`;
the before, after, and benchstat SHA-256 values are respectively
`fe4af59b7d3fdbc7ed371c56c5b7fdfae580cf2b53c12ca5b799fb5b1de14927`,
`eb6f527b9378168c57d0054aec11a09ed33a05cc7b0a9ff4afcd73f29eab5603`,
and `041c2b85c1301219bd29a4002051faa8140d50262b3ebccfd9d173417194578f`.

Only generated-code measurements decide retention. The independent time and
allocation gates prevent either a noisy time-only result or an allocation-only
micro-optimisation from changing the runtime API.

## Considered Designs

1. **Struct-backed `Node`: rejected.** It keeps the interface conversion and
   did not remove the child allocations.
2. **Callback-based runtime API: rejected.** It recreates the closure boundary
   being removed.
3. **Shared writer plus first-error `Fail`: rejected.** It changes raw-return
   behavior after a child write failure.
4. **Private generated body helper plus exact node-result assignment:
   selected.** The generated direct call shares the current writer and then
   applies the helper result exactly as `Writer.Node` applies `Node.Render`'s
   result.

## Runtime Contract

Add one narrow operation to `Writer`:

```go
// NodeResult records the return from a directly rendered generated child.
// Generated code calls it after the child's helper has used this Writer.
func (gw *Writer) NodeResult(err error) {
	gw.err = err
}
```

`NodeResult` intentionally assigns unconditionally, including `nil`. It is not
a general first-error latch. The generated helper entry guard returns an
already-present parent error unchanged, so calls after an earlier error still
preserve that error. `Writer.Node` itself invokes a child and assigns its result
only when the parent state was clear. For such a fresh call, unconditional
post-helper assignment reproduces the observable external boundary of:

```go
if gw.err == nil && n != nil {
	gw.err = n.Render(ctx, gw.w)
}
```

The generated helper differs internally because it writes through the parent's
`Writer` instead of a child-local one. `NodeResult` therefore may replace or
clear only the state produced in that shared writer during helper execution.
The entry guard prevents it from replacing or clearing state that predated the
generated call.

This includes the unusual but existing cases where a raw `return sentinel`
replaces a write error or a raw `return nil` clears it and permits later parent
writes. Those cases are pinned rather than silently "fixed" in this work.

The method is exported only because generated external packages call the root
runtime. It introduces no second `Node` representation and no compatibility
adapter.

Adding this exception also requires correcting the exported `Writer` and `Err`
comments. Ordinary write helpers retain the first write error and no-op while
it is present. `Node` applies a child result only when the parent state is
clear. After a generated helper uses the shared writer, generated code uses
`NodeResult` to apply that helper's result, which may replace or clear state
created during the helper. `Err` therefore reports the current render/write
state, not an immutable first error.

## Generated Shape

Only a plain-function GSX component targeted by at least one eligible local
call is split. Its body exists once:

```go
func Card(r data.Row, attrs gsx.Attrs) gsx.Node {
	return gsx.Func(func(ctx context.Context, w io.Writer) error {
		_gsxgw := gsx.W(w)
		return _gsxrenderCard(ctx, _gsxgw, r, attrs)
	})
}

func _gsxrenderCard(
	ctx context.Context,
	_gsxgw *gsx.Writer,
	r data.Row,
	attrs gsx.Attrs,
) error {
	if err := _gsxgw.Err(); err != nil {
		return err
	}
	// Existing generated Card body, emitted by the shared body emitter.
	return _gsxgw.Err()
}
```

An eligible call becomes:

```go
_gsxgw.NodeResult(_gsxrenderCard(ctx, _gsxgw, r, nil))
```

Go evaluates all helper arguments before the helper entry guard, matching
evaluation of `Card(args...)` before `Writer.Node` checks its current error.
The guard skips child-body work after that point. Parent control flow continues
after `NodeResult`, just as it does after `Node`.

Components without eligible callers keep their current closure body. A split
component's public factory calls the helper, so markup generation is never
duplicated.

## Generic and Variadic Signatures

A direct helper repeats the authored type-parameter declaration and the public
wrapper passes every declared type parameter explicitly, including a named
parameter used only in its constraint. Direct call sites reuse the already
validated and printed positional type arguments.

```go
func Item[T any](value T, children gsx.Node, attrs ...gsx.Attr) gsx.Node
func _gsxrenderItem[T any](
	ctx context.Context,
	_gsxgw *gsx.Writer,
	value T,
	children gsx.Node,
	attrs ...gsx.Attr,
) error
```

The wrapper forwards `attrs...`. Direct calls reuse the positional planner's
existing scalar, children, attrs, zero, adapter, tuple, and variadic forms.

The first implementation uses an exact conservative fallback when a wrapper
cannot faithfully spell a helper call:

- an unnamed or blank value parameter;
- a blank type parameter;
- a type parameter named `ctx`, because the current generated body gives the
  implicit render context that name;
- a non-colliding type parameter in the reserved `_gsx` namespace, such as
  `_gsxT`, as a conservative first-slice fallback.

Grouped and constraint-only named type parameters are supported and tested.
Fallback is declaration-driven, not textual guessing at call sites.
An exact collision with a required generated alias or binding, such as
`_gsxrt`, `_gsxctx`, or `_gsxio`, is not a compilable public factory today and
must remain a positioned declaration diagnostic rather than being described as
a successful fallback.

Variant families share semantic identity and one helper name, but they do not
share authored identifier spellings. For alpha-equivalent variants such as
`Child[T any]` and `Child[U any]`, each declaration's wrapper and helper use its
own `T` or `U`. Type-parameter names, value-parameter names, and variadic
forwarding facts are stored per declaration; only eligibility and the allocated
helper name are family-owned.

## Exact Eligibility

A call is direct only when all of these are true:

- target discovery resolved `targetPackageFunc`;
- the target object's package path equals the generated package;
- exact declaration provenance identifies the object as an authored GSX
  plain-function component;
- its declaration passed the signature-forwarding checks above;
- its declaration family has an allocated direct-helper name;
- positional planning completed normally.

Imported package functions, package function variables, bound methods, method
components, plain Go functions returning `gsx.Node`, unresolved targets, and
dynamic `gsx.Node` values keep `_gsxgw.Node(ctx, target(...))`.

`componentTargetDeclarationProvenance` gains explicit direct metadata while
walking authoritative `*ast.Component` declarations. The call emitter consumes
that metadata; it never infers eligibility from capitalization, selector text,
tag presentation, or result type.

## Exact Helper Naming

Helper names live in the reserved `_gsx` namespace. A deterministic allocator:

1. gathers package declarations from all authored GSX files;
2. identifies the exact generated output path paired with each active GSX input
   and excludes only those owned outputs, preventing self-reservation on
   regeneration;
3. parses every other sibling `.go` file, including handwritten and orphaned
   `.x.go` files that the Go compiler still sees;
4. includes same-package `_test.go` declarations and excludes external
   `package name_test` files by their parsed package clause;
5. groups component build variants by the finalized logical key;
6. visits logical keys in sorted order;
7. allocates `_gsxrender<Name>`, then the first free numeric suffix;
8. reserves each output for the rest of the run.

The Go-file inventory is part of the same authoritative source view used by
generation and persistent-cache classification. Normal source-backed modules
snapshot every owned `.go` input module-wide, including inactive build variants
and same-package tests, then expose only the siblings of directories that are
packages in the effective GSX view. Keeping the wider saved snapshot is
necessary when a first unsaved `.gsx` override turns a previously Go-only
directory into a GSX package; it does not select or activate those Go files for
compilation. Editor overrides and captured present/absent saved states replace
that snapshot exactly. `SourceOnly` bundle generation has no host Go-file
inventory and must never inspect the filesystem.
The persistent source projection hashes every non-owned Go file whose bytes,
membership, parse result, or package clause can affect helper allocation. This
keeps a cache hit from restoring output named against an older inactive,
test-only, handwritten, or orphaned declaration. Exact paired generated output
paths remain excluded from both allocation and this helper-name cache identity.

Scanning all non-owned Go build variants is deliberately conservative and
build-oblivious: it prevents a helper generated under one build configuration
from colliding under another. It uses Go/GSX syntax declarations, not a simple
text heuristic. Multi-file GSX declarations, mutually exclusive variants,
same-package tests, handwritten/orphan `.x.go` declarations, and other sibling
Go declarations therefore receive stable names. Compile-backed tests prove both
that an owned paired output is ignored on regeneration and that a non-owned
`.x.go` collision receives a suffix and passes `go test`.

## Semantic Boundaries

### Ordering and errors

The positional lowerer remains the sole argument planner. Source-order
temporaries, tuple unwrapping, pipeline errors, attrs normalization, adapters,
and child closures are unchanged.

The helper returns `error` because raw GSX Go blocks may return directly:

- normal fallthrough returns `_gsxgw.Err()`;
- a raw return is handed unchanged to `NodeResult`;
- prior parent errors are returned by the helper guard and reassigned unchanged;
- later component arguments still evaluate, while their guarded bodies skip;
- panics are not intercepted.

Tests must cover raw non-nil and nil returns after every failing child write
boundary. Testing only ordinary fallthrough would miss the rejected
first-error-latch bug.

### Children, imported values, and dynamic values

Child/body arguments retain their `gsx.Node` representation. Rendering a child
prop, interpolated node, node slice, imported component, method component,
package variable, or dynamic node continues through `Writer.Node`.

### Source maps

The public declaration keeps its authored component `//line` anchor. The helper
body uses the same body emitter and node-level anchors as today. Generated
boilerplate must not displace authored diagnostics or LSP facts; physical
generated source remains parseable and gofmt-stable.

## Verification Strategy

Runtime tests pin `Writer.NodeResult`, including nil assignment, non-nil
replacement, a prior-error guarded call, and raw sentinel/nil returns following
write failure.

Codegen tests pin:

- exact target provenance and the full fallback matrix;
- grouped, constraint-only, blank, and `ctx` type parameters;
- scalar/variadic children and attrs forwarding;
- shared body emission and public wrapper behavior;
- multi-file and variant-family naming, including alpha-renamed type parameters
  compiled through a common caller under both build-tag selections;
- handwritten Go, orphan/handwritten `.x.go`, and same-package `_test.go`
  helper-name collisions with compile-backed fixtures;
- imported, method, package-variable, plain-Go, and dynamic fallbacks;
- generated source positions.

Semantic corpus probes cover byte output, escaping contexts, source-order side
effects, `(T, error)` short-circuiting, prior writer errors, raw returns,
concurrent rendering, and race detection. Goldens are regenerated from `.gsx`;
generated files are never hand-edited.

Before the candidate base, `gsx-bench` adds stable pooled and discard
benchmarks for imported, method-component, and dynamic-node boundaries. These
fixtures remain on both sides of the comparison and prove those fallback paths
do not regress. Table gains the missing discard destination. Generated-shape
and output tests pin both direct and fallback calls.

## Measurement and Decision

Measure detached base and candidate worktrees in ten distinct process pairs,
odd pairs base/candidate and even pairs candidate/base, using Go 1.26.1,
`GOMAXPROCS=32`, the pinned benchstat revision, and recorded module/dependency
fingerprints.

Retain only if every gate passes:

1. `BenchmarkTableGSXPooled` and `BenchmarkTableGSXDiscard` each improve at
   least 10% with `p < 0.05`.
2. Both Table destinations improve bytes and allocations by at least 90%, each
   with `p < 0.05`.
3. Page pooled and ForwardedAttrs pooled/discard have no significant regression
   at or above 7%; Page parallel uses 12%.
4. Document, List, Stats, Piped, Comments, Buttons, imported, method, and
   dynamic paths have no significant regression at or above 7%, including
   every available Builder destination.
5. Correctness, corpus, race, `make ci`, `make lint`, fixed-point regeneration,
   and adversarial throwaway probes all pass.

There is no waiver between Gates 1 and 2. A near miss rejects the candidate.
On rejection, revert every post-base candidate commit in both repositories,
verify each complete working tree against its saved base and require clean
status (including untracked files), then commit only the measured decision
record. On retention, keep the real public `Node` factories; they remain the
Go/external/value API rather than a compatibility shim.

## Non-goals

- No direct imported, method, package-variable, renderer, dynamic-node, element
  value, or child-prop rendering in this slice.
- No escaping, attribute, style, parser, formatter, or LSP semantic change.
- No `one-learning` validation; it is not a GSX consumer.
- No publishing, pull request, or merge as part of the experiment.
