# Native Style Documentation and Coverage Design

## Goal

Make GSX's native `style={...}` model easy to discover and understand, while
recording why an object-like `style={{ "property": value }}` form is deferred.

## User-facing syntax

GSX styles are ordered declaration contributions:

```gsx
style={
    "display: block",
    "color: " + color,
    "opacity: 0": hidden,
    if active { "font-weight: bold" } else { "font-weight: normal" },
}
```

Contributions may be static or dynamic, guarded with `: condition`, or selected
exclusively with value-form `if` and `switch`. They evaluate from left to right.
Dynamic values continue to use the existing CSS safety filtering, while trusted
CSS can use `gsx.RawCSS`.

## Documentation changes

Expand the composable `class` and `style` section in
`docs/guide/syntax.md` with focused `style` examples covering:

- static and dynamic declarations;
- additive guarded declarations;
- exclusive `if`/`else` and `switch` selection;
- left-to-right contribution evaluation;
- the distinction between declaration contributions and a property/value object.

Add a short note explaining that GSX does not currently support:

```gsx
style={{ "color": color }}
```

The object-like form is deferred because it would introduce a second way to
express inline styles. Current usage has not shown enough repeated dynamic
property construction to justify additional grammar, formatter, code generator,
documentation, and maintenance cost.

## Roadmap

Add a deferred roadmap item for ordered style property bags. Reconsider it when
real projects repeatedly construct many dynamic declarations and string
composition becomes a material usability problem. If reconsidered, the initial
design should prefer quoted, native CSS property names rather than JSX camelCase
conversion or automatic numeric units.

## Test coverage

PR 15 already covers:

- value-form switch selection in
  `internal/corpus/testdata/cases/style/value_switch.txtar`;
- left-to-right evaluation in
  `internal/corpus/testdata/cases/style/value_form_evaluation_order.txtar`.

Add `internal/corpus/testdata/cases/style/value_if_else.txtar` for style-specific
`if`/`else` parity. It should combine a static contribution with an exclusive
conditional contribution and verify the rendered declaration order:

```gsx
style={
    "display: block",
    if active { "color: green" } else { "color: gray" },
}
```

The corpus case should include generated output and render goldens and be added
to the coverage manifest.

## Non-goals

- No parser, AST, formatter, code generator, or runtime changes.
- No object-like style property bag syntax.
- No bare CSS keys, camelCase conversion, automatic `px`, or computed keys.
- No duplicate test of the existing switch or evaluation-order scenarios.
