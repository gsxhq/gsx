# Expression-Valued `js``/`css`` Literals Requirements

**Status:** Proposed
**Date:** 2026-07-13

## Problem

GSX supports `js`` and `css`` literals in attribute positions, but rejects them when a Go expression is expected. This forces authors to build JavaScript/CSS strings manually or wrap them with `gsx.RawJS`/`gsx.RawCSS`, especially when assembling attribute maps:

```gsx
{{
    containerAttrs := gsx.Attrs{
        "@suggest-datetime.window": gsx.RawJS("suggest($event.detail)"),
    }
}}
```

The literal should be usable directly as a typed Go expression:

```gsx
{{
    containerAttrs := gsx.Attrs{
        "@suggest-datetime.window": js`suggest($event.detail)`,
    }
}}
```

This is not string interpolation shorthand. It must retain GSX's lexical-context analysis and escaping guarantees.

## Grounding in the Current Implementation

The building blocks already exist; this feature composes them rather than inventing new machinery:

- The runtime exposes pure string escapers separate from the streaming writer: `EscapeJSVal`, `EscapeJSStr`, `EscapeJSTmpl`, `EscapeJSRegexp`, `StyleValue`, `FilterCSS`. None of them can fail: `EscapeJSVal` defuses JSON marshal errors into a ` /* err */null ` comment (the `html/template` port), and CSS filtering fails safe to `ZgotmplZ`.
- The JS lexical classifier already runs per-hole for attribute literals (see `jsattr/escaped_backtick_literal.txtar` lowering a hole to `JSTmplAttr`). CSS attribute holes route through renderer-then-`StyleValue`-then-`AttrValue`.
- `f`` literals already lower in Go-expression positions — top-level `var`/`func` Go regions and `{ }` interpolation bodies — as plain string concatenation (`goexpr-f-literal/f_value_is_escaped_string.txtar`). **Body `{{ }}` Go blocks are NOT such a position today**: `GoBlock.Code` is captured verbatim with no literal splitting, so even `f`` fails there (verified by live probe, 2026-07-13). This slice adds the split treatment to `{{ }}` for all three literal kinds.
- Spread already emits a `RawJS` attrs value as HTML-escaped JavaScript *source*, never as a JSON-quoted string (`toStr` → `AttrValue`, `attrs.go`). This requirement is pinned by test, not built new. An explicit `case RawJS`/`case RawCSS` in `toStr` may be added for clarity but must not change behavior.

The natural lowering is therefore plain concatenation wrapped in the trusted type, e.g.

```go
gsx.RawJS("select(" + gsx.EscapeJSVal(x) + ")")
gsx.RawCSS("width:" + gsx.StyleValue(w) + "px")
```

which satisfies the evaluation requirements by construction (left-to-right, exactly once, no writer, no error return shape).

## Goals

1. Allow `js`` and `css`` literals anywhere GSX accepts a Go expression — the positions `f`` supports today (top-level `var`/`func` Go regions, `{ }` interpolation bodies) **plus body `{{ }}` Go blocks**, which gain embedded-literal support for all three kinds in this slice (the motivating example lives in one). Element literals inside `{{ }}` stay out of scope: positioned diagnostic, ROADMAP entry.
2. Give an expression-valued `js`` literal the static Go type `gsx.RawJS`, and `css`` the type `gsx.RawCSS`.
3. Preserve lexical-context analysis across literal text and interpolations (JavaScript contexts for `js``; the CSS declaration-value context for `css``).
4. Escape/filter each interpolated value for its exact lexical position.
5. Evaluate interpolation expressions once, from left to right.
6. Keep existing attribute-local `js``/`css`` behavior unchanged — and prove it: the expression form and the attribute-local form must render byte-identical output for the same literal and inputs.
7. Produce precise compile-time diagnostics rather than silently falling back to plain string concatenation.

## Non-Goals

- Treating `js``/`css`` as plain `string`.
- Raw concatenation of interpolation values.
- Changing existing attribute semantics.
- Rendering `gsx.RawJS`/`gsx.RawCSS` as raw HTML or as a `gsx.Node`.
- A new interpolation marker. Interpolation is `@{expr}` — the one rule. `${` appearing in literal text is hostile *input* the template-literal escaper must neutralize, never GSX syntax.
- CSS `url()` lexical classification inside `css`` literals. That remains follow-up #82; until then `url()` holes are subject to the same declaration-value filter as today's attribute form (hostile content fails safe to `ZgotmplZ`).

## Required Syntax

An expression-valued literal uses the existing literal syntax with `@{}` interpolation:

```gsx
{{ handler := js`selectItem(@{props.Item.ID})` }}
```

Backticks inside the literal are escaped as `\``, exactly as in the attribute form (`jsattr/escaped_backtick_literal.txtar`).

It must work in all Go-expression positions supported by GSX, including:

```gsx
{{
    local := js`selectItem(@{props.Item.ID})`

    attrs := gsx.Attrs{
        "@click": local,
        "style":  css`width:@{props.Width}px`,
    }

    values := []gsx.RawJS{
        js`first()`,
        js`second(@{props.Value})`,
    }
}}

{ consume(js`submit(@{props.FormID})`) }
<Widget Handler={ js`open(@{props.ID})` } />
```

Functions may return the value as the trusted type:

```gsx
{{
    makeHandler := func(id string) gsx.RawJS {
        return js`open(@{id})`
    }
}}
```

## Type Contract

The result of an expression-valued `js`` literal is `gsx.RawJS`; of a `css`` literal, `gsx.RawCSS`. Not `string`.

This distinction is required so downstream GSX APIs can preserve the author's explicit trust decision. Assigning the value to `any`, storing it in `gsx.Attrs`, or passing it through component props must not erase that provenance.

Normal Go assignability rules apply. Code requiring a `string` must convert explicitly if that conversion is valid and intentional.

(`f`` remains plain `string`: its output is escaped later at the sink, so it carries no trust decision. `js``/`css`` escape at construction, so the type is the provenance.)

## Interpolation Semantics

Two layers cooperate, and the spec keeps them distinct:

**Static lexical classification (codegen time).** The existing JS lexer classifies each hole's position across literal text; the CSS side has a single declaration-value context today. The classification selects the escaper:

| Lexical position (static) | Required behavior |
|---|---|
| JavaScript value | `gsx.EscapeJSVal` semantics |
| Single- or double-quoted JS string | Escape as JS string content (`gsx.EscapeJSStr`) |
| JS template literal | Escape as template-literal content (`gsx.EscapeJSTmpl`) |
| JS regular expression | Escape as regexp content (`gsx.EscapeJSRegexp`) |
| CSS declaration value | Filter via `gsx.StyleValue` semantics (fails safe to `ZgotmplZ`) |

**Runtime trusted-type dispatch.** Independently of the static classification, the escapers type-switch at runtime:

- A `gsx.RawJS` interpolated in a **JavaScript value position** passes through verbatim (`EscapeJSVal` already does this). This passthrough applies to value positions **only**: inside quoted-string, template-literal, or regexp content, a `RawJS` is escaped as content like any other string — "trusted as JS source" does not mean "trusted as string content", and passthrough there would reintroduce the breakout the escapers exist to close.
- A `gsx.RawCSS` interpolated in a CSS hole passes through verbatim (`StyleValue` already does this at every CSS hole).

Example — three holes in three different lexical positions, which must not share a generic string-concatenation lowering:

```gsx
{{
    valueExpr := js`select(@{props.Value})`
    stringExpr := js`select("@{props.Value}")`
    templateExpr := js`select(\`@{props.Value}\`)`
}}
```

HTML escaping still applies when the resulting value is eventually emitted inside an HTML attribute. JavaScript/CSS trust does not imply raw HTML trust.

## Evaluation Requirements

Interpolation expressions must:

1. Evaluate exactly once.
2. Evaluate from left to right; observable evaluation order must match source order (temporaries are fine as long as order is preserved).
3. **Reject error-returning pipe stages with a positioned diagnostic** in this slice. A hole like `@{x |> filter}` where a stage returns `(T, error)` has no error-return shape available in an arbitrary Go expression position (`gsx.Attrs{...}` inside a `{{ }}` block cannot propagate). Attribute-local literals keep their existing pipe-error behavior unchanged.
4. **Reject ctx-taking filters and renderers** the same way: a hole whose filter or registered renderer takes the ambient render `ctx` has no `ctx` at a Go-expression position with no render closure (a top-level value), so it is rejected with the same positioned diagnostic rather than lowered to an undefined-`ctx` call. Positions that DO bind `ctx` (an interpolation, a `{{ }}` block, an attribute) keep threading it unchanged.

## Interaction With Attributes

An expression-valued literal stored in an attribute map must retain its trusted type:

```gsx
{{
    attrs := gsx.Attrs{
        "x-data": js`dialog(@{props.InitialOpen})`,
        "@click": js`select(@{props.ID})`,
        "style":  css`color:@{props.Color}`,
    }
}}

<button {attrs...}>Select</button>
```

When spread, a `RawJS` value must be emitted as JavaScript source through the normal HTML attribute escaping layer — never serialized as a quoted JavaScript string value. (This is current `toStr` → `AttrValue` behavior; pin it.) A `RawCSS` value under a `style` key must compose with the existing class/style merge machinery via `StyleValue` passthrough.

Existing inline attribute syntax remains valid and unchanged:

```gsx
<button @click={ js`select(@{props.ID})` }>Select</button>
```

## Body/Text Position

- A `js``/`css`` **literal written directly** in a markup body position (`<div>{ js`alert(1)` }</div>`) is a positioned compile-time error — rendering JavaScript/CSS source as visible text is an authoring mistake.
- An **indirect** `RawJS`/`RawCSS` value reaching a text position (e.g. `{ someFunc() }` returning `gsx.RawJS`) keeps the existing safe behavior: HTML-escaped as ordinary text, never raw HTML. Pin with a corpus case.

## Renderers

The expression form must match the existing attribute-hole renderer behavior: a registered renderer for a hole's type applies first, and its string result feeds the same escaper chain any other string would. A corpus case must pin renderer interaction for at least one `js`` and one `css`` expression-valued hole, and one case must pin what happens when a renderer is registered for `gsx.RawJS`/`gsx.RawCSS` itself (must match the existing whole-value attribute behavior, whatever the current registry semantics dictate — no new special case).

## Diagnostics

GSX must report a positioned compile-time error when:

- the literal is malformed;
- an interpolation appears in an unsupported lexical position;
- contextual escaping cannot be determined safely;
- a hole contains an error-returning pipe stage, or a ctx-taking / error-returning filter or renderer (expression positions only — no ambient render context exists there);
- the literal appears directly in a markup body/text position;
- generated Go cannot satisfy the required trusted-type expression contract.

There must be no fallback to plain strings, unescaped concatenation, or best-effort output.

## Formatter And Tooling

`gsx fmt` must preserve expression-valued `js``/`css`` literals and format surrounding Go expressions consistently.

The implementation must verify all affected user-facing surfaces:

- parser and AST;
- code generation (including `_gsx`-alias import need-tracking: the lowering introduces `gsx.RawJS`/`gsx.EscapeJS*`/`gsx.StyleValue` references into pass-through code that may not otherwise import gsx);
- semantic corpus;
- formatter corpus;
- LSP diagnostics, hover, and navigation where applicable;
- tree-sitter grammar and highlighting;
- VS Code extension highlighting;
- documentation-site Shiki highlighting;
- playground CodeMirror highlighting and WASM generation.

If the existing grammar already recognizes the literal in these positions, sibling changes may be unnecessary, but each surface must still be verified.

## Required Tests

The semantic corpus must cover, for **both** `js`` and `css`` where the row applies:

1. Local variable assignment.
2. Function return.
3. Function argument.
4. Struct and map literal values.
5. Component prop values.
6. `gsx.Attrs` values followed by attribute spread (including `RawCSS` under a `style` key composing with class/style merge).
7. JS value, quoted-string, template-literal, and regexp interpolation positions; the CSS declaration-value position.
8. Hostile interpolation values containing quotes, backticks, `${`, newlines, HTML delimiters, script-closing text, and CSS breakout attempts (expecting `ZgotmplZ`).
9. `gsx.RawJS` passthrough in a JS value position, **and** `RawJS` escaped-as-content in a quoted-string position; `gsx.RawCSS` passthrough in a CSS hole.
10. Left-to-right, exactly-once evaluation.
11. Static result type compatibility with `gsx.RawJS`/`gsx.RawCSS`.
12. **Differential equivalence**: `@click=js`X`` (attribute-local, streaming) versus `@click={ js`X` }` (expression form, construct-then-emit) render byte-identical output for the same literal and inputs; likewise `style=css`X`` versus `style={ css`X` }`. This is the correctness-first rule applied directly — two lowerings, one proven behavior.
13. A literal inside a plain func in a `.gsx` file that previously needed no gsx import (pins alias/import need-tracking).
14. Renderer interaction per the Renderers section.
15. Precise diagnostics for malformed literals, unsupported positions, error-returning pipe stages, and direct body-position literals.

The existing rejection case at:

```text
internal/corpus/testdata/cases/goexpr-f-literal/js_value_unsupported.txtar
```

must be replaced by accepted expression-valued coverage for both literal kinds.

Formatter corpus coverage must include multiline Go expressions containing `js``/`css`` literals and nested interpolation expressions.

## Acceptance Criteria

The feature is complete when:

- the motivating `gsx.Attrs` example compiles without `gsx.RawJS(...)`/`gsx.RawCSS(...)` or manual concatenation;
- rendered JavaScript/CSS is correct in every supported lexical context;
- hostile-value tests demonstrate contextual escaping/filtering rather than raw concatenation;
- trusted-type provenance survives assignment, argument passing, props, and attribute maps;
- the differential-equivalence cases prove attribute-local behavior unchanged;
- corpus, formatter, runtime, LSP, examples, and authoritative CI checks pass;
- documentation clearly distinguishes JavaScript/CSS trust from HTML trust.
