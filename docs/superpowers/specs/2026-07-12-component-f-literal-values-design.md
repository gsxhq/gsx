# Component `f`-literal string values

**Date:** 2026-07-12  
**Status:** DECIDED

## Problem

An `f`-literal is already defined as a string value, but attribute-position
codegen currently assumes every `ast.EmbeddedAttr` on a component is a
fallthrough HTML attribute. A hole-bearing literal is consequently rejected:

```gsx
<PageHeader subtitle=f`@{props.Count} tickets`/>
```

This surfaced while dogfooding GSX in
`/Users/jackieli/work/one-learning-gsx/ui/feedback_pages.gsx`. `subtitle` is the
declared `string` prop `PageHeaderProps.Subtitle`, so the literal should cross
the component boundary as a string rather than use element-attribute emission.

## Decision

An `f`-literal always evaluates to a Go `string`. Component field matching only
chooses where that string is stored:

- if the attribute name maps to a declared prop, assign the string to that prop;
- otherwise, place the string in the component's explicit `Attrs` bag;
- if the component has no `Attrs` field, retain the existing undeclared-attribute
  type error.

For the motivating example, codegen emits the equivalent of:

```go
PageHeader(PageHeaderProps{
	Subtitle: strconv.FormatInt(int64(props.Count), 10) + " tickets",
})
```

Static segments and typed holes use the existing `embeddedValueExpr` string
assembly rules. Per-hole pipelines, registered renderers, `fmt.Stringer`,
numeric conversion, and `(T, error)` propagation retain their established
semantics. Whole-literal pipelines operate on the assembled string as they do in
other value positions.

`js` and `css` literals are not changed by this decision. They remain contextual
embedded-language attributes forwarded through `Attrs`; they do not bind
declared component props.

## Sink-dependent lowering

The value semantics do not require one universal emission strategy.

On a leaf HTML tag, codegen owns the final sink. For an ordinary non-URL
attribute with no whole-literal pipeline, it retains the direct-write
optimization: static segments go directly to `_gsxgw.S`, string holes go to the
attribute writer, and numeric holes use the shared `_gsxnum` scratch buffer via
`IntInto`, `UintInto`, or `FloatInto`. No intermediate string is materialized.
URL attributes continue to assemble the whole value before sanitization.

On a component tag, the value must cross the component boundary. Codegen
therefore materializes a Go string expression and stores it in either the
declared props field or an `Attrs` entry. The child component's eventual sink is
not assumed at the call site.

## Codegen shape

Parser and AST behavior remain unchanged. `childPropsLiteral` gains explicit
`EmbeddedText` handling before the existing `js`/`css` fallthrough path:

1. Apply the same field-name matching and validation used by static and
   expression attributes.
2. In the type-check probe, use a string-valued field expression while retaining
   the existing per-hole probes.
3. In emission, assemble the literal through the shared typed-hole string
   lowering.
4. Preserve source-order evaluation with sibling props and ordered-attrs values,
   including when a hole or pipeline returns `(T, error)`.
5. Wrap the assembled string with `gsx.Text` when the matched prop is a
   `gsx.Node`, matching quoted-string prop behavior.
6. When no declared field matches, assemble the same string and store it as the
   fallthrough bag value.

The existing element path is not routed through this component-value lowering.

## Verification

The canonical corpus will pin:

- the exact `PageHeader` integer-hole case and rendered output;
- a matched `gsx.Node` prop;
- an unmatched `f`-literal forwarded through `Attrs`;
- source-order evaluation when formatted props and tuple-returning values are
  interleaved;
- whole-literal/per-hole pipeline and error propagation behavior;
- unchanged rejection/forwarding semantics for `js` and `css` component attrs;
- unchanged leaf-tag direct writes, including numeric `IntInto` scratch-buffer
  emission.

After core verification, regenerate the one-learning `ui` package with its local
GSX replacement and confirm the motivating tag generates a populated
`PageHeaderProps.Subtitle`. Remove the temporary handwritten `PageHeader(...)`
fallback so only the direct GSX tag remains.

Review `docs/ROADMAP.md` while landing the change and replace the stale statement
that all hole-bearing component embedded literals are rejected with the narrower
remaining `js`/`css` restriction.
