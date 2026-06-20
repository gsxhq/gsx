# Plan: Codegen method components + `ctx`-in-interp fix (Phase 4)

**Date:** 2026-06-20
**Branch:** `feat/codegen-method-components` off `main`
**Design:** `specs/2026-06-18-gsx-component-styles.md` (Style B / method components),
`specs/2026-06-18-gsx-templating-design.md` §2 (scope disambiguation), example
`examples/11_struct_methods.gsx`.
**Status:** ready for SDD

## Goal

Support **method components** — `component (p UsersPage) Content() { … }` → a Go
method `func (p UsersPage) Content(…) gsx.Node` — and their invocation in markup
(`<p.Content/>`, `<p.Grid sort={p.Sort}/>`). Also fix the **`ctx`-in-interpolation**
gap (tracked debt) that blocks the canonical Style-B body (`{ structpages.ID(ctx,…) }`).

Method components are for page composition (Style B): the **receiver struct is the
page data** (set once in Go), methods render the page / swappable partials. Today
`genComponent` and `buildSkeleton` both SKIP `c.Recv != ""`.

## Key facts (grounded)

- `c.Recv` is the raw receiver INCLUDING parens, e.g. `"(p UsersPage)"`, `"(f *Form)"`.
- Render closure: `func <Name>(_gsxp <Name>Props) gsx.Node { return gsx.Func(func(ctx
  context.Context, _gsxw io.Writer) error { …binds…; _gsxgw := gsx.W(_gsxw); …; return
  _gsxgw.Err() }) }`. The closure param `ctx` IS in scope in the emitted body — only
  the type-resolution SKELETON lacks it.
- Skeleton component func (analyze.go buildSkeleton): `func <Name>(_gsxp <Name>Props)
  _gsxrt.Node { …probes… }` — no `ctx` binding (the gap).
- `isComponentTag` returns true for an uppercase OR dotted tag. `<ui.AppShell/>`
  (package fn) and `<p.Content/>` (method) are both dotted → both currently routed
  to `genChildComponent`, which emits `<tag>(<tag>Props{})` — correct for the
  package case, WRONG for the method case (props type is `<RecvType><Method>Props`,
  not `p.ContentProps`, and a nullary method has no props struct).
- Example 11 disambiguation: every method call is via the **enclosing receiver
  variable** (`p`), while `ui` is an import. So `<X.Y/>` is a METHOD iff `X` equals
  the enclosing method component's receiver variable name; otherwise it's a package
  function (today's behavior). Syntactic, deterministic, covers the example.
- Nullary method (no params, no `{children}`) → NO props struct; signature
  `func (p T) Name() gsx.Node`; call `p.Name()`.

## Tasks

### Task 1: Bind `ctx` in the type-resolution skeleton

The skeleton component func must bind `ctx` so an interp/attr expr referencing it
(`{ f(ctx) }`, `id={ g(ctx) }`) type-checks — matching the emitted closure where
`ctx` is the ambient param.

- In `buildSkeleton` (analyze.go), inside each component func body (before the
  probes), bind a `ctx` of type `context.Context`: e.g.
  `ctx := context.Background()` + `_ = ctx` (or `var ctx context.Context; _ = ctx`).
  Import `context` in the skeleton import block (add `import "context"` — confirm it
  doesn't clash; the skeleton currently imports `_gsxrt` and the file's GoChunk
  imports). Use a real, type-correct binding (NOT a fake type).
- `checkReservedParams` already reserves `ctx` — good, no user param shadows it.
- Test (e2e): a component whose interp calls a ctx-taking func resolves + renders.
  Add a tiny same-file `func fromCtx(ctx context.Context) string { … }` and
  `component C() { <p>{ fromCtx(ctx) }</p> }`; render via the harness (the harness
  passes a context). Confirms `{ f(ctx) }` no longer fails `undefined: ctx`.

Commit: `codegen: bind ctx in the type-resolution skeleton (fixes ctx-in-interp)`.

### Task 2: Method-component declaration

Emit the Go method + its props struct (or none for nullary), in BOTH genComponent
(emit) and buildSkeleton (skeleton). Stop skipping `c.Recv`.

- **Parse the receiver** (`c.Recv`, e.g. `"(p UsersPage)"`) into `(recvVar,
  recvType, recvTypeName)`: use go/parser on a synthesized `func <Recv> _m() {}` and
  read the `ast.FuncDecl.Recv` field (robust; handles `*T`, named/unnamed). For
  `(p UsersPage)` → recvVar `p`, recvType `UsersPage`, recvTypeName `UsersPage`. For
  `(f *Form)` → recvVar `f`, recvType `*Form`, recvTypeName `Form` (strip `*` for the
  props-struct name prefix). Reject an unnamed receiver (`(UsersPage)` / `(*Form)`)
  with a clear error (method components need the receiver var as page-data handle).
- **Props struct name**: `<recvTypeName><MethodName>Props` (e.g.
  `UsersPageGridProps`). A method with params and/or `{children}` gets this struct;
  a NULLARY method (no params, no children) gets NO struct.
- **Emit (genComponent)**:
  - method WITH props: `func <Recv> <Name>(_gsxp <RecvTypeName><Name>Props) gsx.Node
    { return gsx.Func(func(ctx, _gsxw) error { …bind used params from _gsxp, bind
    children if used, _gsxgw := gsx.W(_gsxw); …body…; return _gsxgw.Err() }) }`.
  - nullary: `func <Recv> <Name>() gsx.Node { return gsx.Func(…) }` (no `_gsxp`).
  - the receiver var (`p`) is in scope in the body (it's a method receiver), so
    `p.Field` references in interps/attrs work. usedParams must NOT try to bind the
    receiver var as a prop local (it's the receiver, already in scope).
  - reserved-name: the recvVar must not be `_gsx*`/`ctx`/an emitted import ident —
    add a check (a recvVar named `ctx` or `gsx` would break the body). Keep it small.
- **Skeleton (buildSkeleton)**: mirror exactly — emit the same method signature with
  the receiver, the same props struct (or none), bind `ctx` (Task 1), bind used
  params + children, emit probes. The receiver var is in scope (it's the method
  receiver) so `p.Field` probes type-check against the real receiver type. The
  receiver type (`UsersPage`) must be a real type visible in the package (it is —
  user-declared in the .gsx GoChunk or another file).
- **Tests (e2e)**: a method component invoked DIRECTLY from Go via the harness
  (the harness invocation string can be `views.UsersPage{Title:"T"}.Page()` style —
  check how renderPackage builds the invocation; it takes an expression). Cover:
  nullary method (`(p T) Page()` → `p.Page()`), method with a param
  (`(p T) Grid(sort string)` → props `TGridProps{Sort}`), receiver field access
  (`{p.Title}`), pointer receiver (`(f *Form)`). Confirm the props struct name and
  the receiver-in-scope. (Method-to-method INVOCATION in markup is Task 3 — for now
  test declaration by calling the method from the harness Go.)

Commit: `codegen: method-component declaration (receiver method + <Recv><Name>Props)`.

### Task 3: Method invocation in markup (`<p.Content/>`)

Lower a dotted-tag element whose left identifier is the ENCLOSING component's
receiver variable into a method call; other dotted tags keep the package-function
behavior.

- **Thread the enclosing receiver var** through genComponent → genNode →
  genChildComponent (and the probe path: buildSkeleton's emitProbes). A free
  function component has recvVar `""`.
- **In genChildComponent / the component-tag probe**: split `el.Tag` on the first
  `.` into `(left, method)`. If `left == enclosingRecvVar` (non-empty) → METHOD
  invocation:
  - nullary (no attrs, no children): `_gsxgw.Node(ctx, <left>.<method>())`.
  - with props: `_gsxgw.Node(ctx, <left>.<method>(<RecvTypeName><method>Props{…fields…}))`
    where the props type name uses the enclosing receiver's TYPE name (known from the
    enclosing component's parsed Recv — thread `recvTypeName` too, OR resolve it; the
    enclosing receiver var has a known type from the enclosing Recv, so thread the
    name). Fields built by the existing `childPropsFields` (attr→field). `{children}`
    slot closure as in Phase 3 if the method places children.
  - Probe: `_ = <left>.<method>(…)` mirroring, so the call type-checks against the
    real method signature + props struct.
  - Else (left != recvVar) → existing package-function path unchanged
    (`<tag>(<tag>Props{…})`).
- **usedParams / binding**: a method-invocation's prop exprs reference enclosing
  params/receiver — bound via the existing collectChildPropExprSrc walk (it already
  collects child-component attr exprs; a method invocation is the same shape). The
  receiver var itself is in scope (method receiver), no binding needed.
- **Tests (e2e)**: the full example-11 chain in one file — `(p UsersPage) Page()`
  renders `<p.Content/>`; `Content()` renders `<p.Grid sort={p.Sort}/>`; `Grid`
  renders `<p.Row user={u}/>` in a `{ for }`. Invoke `Page()` from the harness and
  assert the nested output. A nullary `<p.Content/>` and a parameterized
  `<p.Grid sort={x}/>`. A mixed file with BOTH `<ui.AppShell …>` (package, define a
  tiny `ui`-like same-file component or a second package) and `<p.Content/>` (method)
  — confirm each lowers correctly. Order-invariant: a method invocation with slot
  content + interleaved interps (reuse the Phase-3 machinery; method invocation goes
  through the same component-tag recursion).

Commit: `codegen: method-component invocation (<recv.Method/> via enclosing receiver)`.

## After tasks
- Final whole-feature review (adversarial: receiver parse for `*T`/named/edge;
  props-name collisions between methods of different receivers; method-vs-package
  disambiguation incl. a local var that isn't the receiver → package path (Go error,
  documented); ctx binding doesn't mask real errors; order invariant with method
  slots).
- Independent adversarial review with live probing (merge gate).
- Merge `--no-ff`; update ROADMAP (#6 method components done; clear the
  ctx-in-interp debt). Graduate `examples/11_struct_methods.gsx` toward a render
  golden if feasible.

## Scope cuts (deferred, clear errors)
- `<v.Method/>` where `v` is a NON-receiver local of a method-bearing type → treated
  as a package-function call (Go compile error if `v` isn't a package). Documented;
  full local-var-type resolution for arbitrary receivers is a later enhancement.
- Method components with `{children}` + named slots — children work (Phase 3
  machinery); named markup-attr slots stay deferred.
- `?` try-marker on a method-invocation prop / pipeline on a method prop — error
  (same as child props, Task-1 of Phase 3).

## Risks
- **Skeleton/emit method-signature parity.** The receiver, props struct, ctx
  binding, and param bindings must be byte-identical in shape between buildSkeleton
  and genComponent — a mismatch makes resolution disagree with emission. Factor the
  receiver parse + props-name into shared helpers used by both.
- **ctx binding masking errors.** `ctx := context.Background()` in the skeleton must
  not accidentally satisfy a different `ctx` misuse. It's the real type, so type
  errors still surface. Confirm a genuinely wrong ctx use still errors.
- **recvVar == a dotted-tag left for a PACKAGE** (e.g. a receiver named `ui` while
  also importing `ui`) — pathological; checkReservedParams-style guard or document.
