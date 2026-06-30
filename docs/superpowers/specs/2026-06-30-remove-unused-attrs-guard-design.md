# Remove the Generated Attrs Usage Guard

## Decision

Components synthesize and bind `attrs` only when `usesAttrs` finds an explicit
reference in the component body. Generated render code must therefore emit:

```go
attrs := _gsxp.Attrs
```

It must not emit `_ = attrs`. Every synthesized binding must be consumed by the
author's explicitly detected reference. If detection and lowering drift apart,
the generated Go compile error should expose that defect instead of suppressing
it.

## Scope

- Remove the defensive `_ = attrs` emission.
- Update generated corpus fixtures and example output.
- Add a generator-level regression test rejecting `_ = attrs`.
- Keep analyzer skeleton probes unchanged; they are diagnostic scaffolding, not
  user-facing generated render code.

## Verification

Run the focused code-generation tests, regenerate pinned output, and run the
repository check suite.
