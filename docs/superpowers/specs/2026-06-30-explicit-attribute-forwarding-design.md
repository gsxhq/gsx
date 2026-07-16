# Decision: Explicit component attribute forwarding

> **Partly superseded:** Explicit placement remains, but implicit `attrs`
> synthesis does not. See `2026-07-14-verbatim-component-signatures-design.md`.

**Date:** 2026-06-30
**Status:** DECIDED
**Supersedes:** the automatic root-fallthrough behavior in
`2026-06-18-gsx-templating-design.md` §3 and
`2026-06-20-gsx-attr-fallthrough-design.md`

## Decision

gsx does not automatically apply undeclared component attributes to the
component's root element.

A component accepts and forwards undeclared attributes only when its body
references the implicit `attrs` bag. Placement is explicit:

```go
component Button(variant string) {
	<button class="btn" data-variant={variant} { attrs... }>
		{children}
	</button>
}
```

```go
<Button variant="primary" class="w-full" data-test="save">Save</Button>
```

Referencing `attrs` synthesizes `Attrs gsx.Attrs` in generated props. The
component author can spread the bag with `{ attrs... }`, inspect it, split it, or
place different derived bags on different elements. A component that does not
reference `attrs` has no synthesized `Attrs` field, so undeclared call-site
attributes fail type checking instead of being silently rendered.

Explicit spread keeps its existing positional precedence:

- ordinary attributes before `{ attrs... }` can be overridden by the bag;
- ordinary attributes after `{ attrs... }` override the bag;
- `class` and `style` merge through the existing merge rules.

## Rationale

Automatic fallthrough was introduced on 2026-06-18 as a Vue-inspired convenience
to avoid rest-attribute boilerplate. The follow-up design on 2026-06-20 made a
component's single HTML root the implicit destination.

That default is surprising outside Vue and creates an implicit API dependency on
component implementation structure:

- a misspelled prop can become an HTML attribute;
- adding a declared prop changes where an existing call-site attribute goes;
- changing or wrapping the root element changes externally visible behavior;
- accessibility, event, HTMX, Alpine, and test attributes can land on the wrong
  element;
- merely referencing `attrs` previously switched the entire component from
  automatic to manual mode.

React, Solid, Svelte, and Python/Jinja-style component composition use explicit
rest-prop capture and placement. Explicit `{ attrs... }` matches that expectation,
makes the public forwarding contract visible in source, and lets the compiler
reject undeclared attributes when forwarding was not intended.

## Consequences

- Remove single-root detection and automatic root injection from code generation.
- Synthesize `Attrs gsx.Attrs` only when the component body references `attrs`.
- Preserve explicit `{ attrs... }` behavior and `gsx.Attrs` utilities.
- Remove the automatic “Attribute fallthrough” user documentation and example.
- Existing components relying on automatic fallthrough must add `{ attrs... }` at
  the intended element.
