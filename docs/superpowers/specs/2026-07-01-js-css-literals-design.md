# Explicit JS/CSS Literals for Attribute Values

## Context

gsx currently treats some attributes as JavaScript or CSS contexts by
attribute-name classification. This was necessary when the language had no
value-site way to say "this value is JavaScript" or "this value is CSS".

That model is safe, but it is not ergonomic enough for Alpine, HTMX, Vue-style
directives, or project-specific attributes. Authors either rely on a registry of
known JS/CSS attribute names, or fall back to Go string construction:

```gsx
<div x-data={ fmt.Sprintf("{ checked: %t }", checked) }>
```

The desired model is explicit at the value site, safe by default, and familiar
to both JavaScript and Go authors.

## Goals

- Make JavaScript and CSS attribute values explicit where they are written.
- Keep Go interpolation in embedded languages safe and context-aware.
- Remove framework-specific JS/CSS attribute classification from the authoring
  model.
- Remove JS/CSS attribute classification config and options from the
  user-facing API.
- Keep ordinary HTML attributes simple: quoted literals or Go expressions.
- Preserve URL attribute safety as an HTML concern.
- Avoid overloading `{{ }}` with another meaning.

## Non-Goals

- Do not add a general JavaScript parser to the gsx parser.
- Do not make `{{ }}` mean JavaScript object construction.
- Do not require configuration for common Alpine/HTMX/custom directive use.

## Syntax

Add two embedded-language literal forms usable as dynamic attribute values:

```gsx
<button @click=js`checked = !checked`>Toggle</button>
<button @click={js`checked = !checked`}>Toggle</button>

<div x-data=js`
	{
		touched: @{wasSet},
		checked: @{checked},
		toggle() {
			this.checked = !this.checked
		}
	}
`>
	...
</div>

<div style=css`
	width: @{pct}%;
	color: @{color};
`>
	...
</div>
```

The attribute RHS has three source forms:

- `name="literal"` for static string attributes.
- `name={dynamic}` for general dynamic values.
- `name=js\`...\`` and `name=css\`...\`` for embedded-language literals.

The direct RHS form is preferred for embedded JS/CSS because it removes
syntactic noise and is unambiguous after `=`. The braced form remains valid
because `js\`...\`` and `css\`...\`` are expression-like dynamic values:

```gsx
<button @click=js`save(@{id})`>Save</button>
```

## Interpolation and Escaping

Inside `js\`...\`` and `css\`...\``, `@{expr}` is the only Go interpolation
syntax.

`@{}` means "evaluate this Go expression and escape it for the surrounding
embedded-language context." It is not string concatenation.

Examples:

```gsx
<button @click={js`save(@{id})`}>Save</button>
```

If `id` is an integer, the rendered JavaScript receives a numeric literal. If
`id` is a string, the rendered JavaScript receives a quoted JS string literal,
then the result is HTML-attribute escaped because it is inside an HTML
attribute.

For JavaScript literals, reuse the existing JS context engine:

- value position -> `JSValAttr`
- string position -> `JSStrAttr`
- template literal position -> `JSTmplAttr`
- regexp position -> `JSRegexpAttr`
- unsafe identifier/binding position -> diagnostic

For CSS literals, reuse the existing CSS escaping/filtering behavior used for
`<style>` holes and CSS attribute contexts.

## Relationship to `<script>` and `<style>`

`js\`...\`` and `css\`...\`` are for attribute-local embedded source.

Use `<script>` and `<style>` for page-level, named, shared, or larger blocks:

```gsx
<script>
	function formData() {
		return {
			status: @{status},
			init() {
				this.$watch('status', value => ...)
			}
		}
	}
</script>

<form x-data=js`formData()`>
	...
</form>
```

Both mechanisms share the same interpolation marker and escaping model:
`@{expr}` in embedded JS/CSS is context-aware.

## Attribute Context Model

The new authoring model is:

- Plain attributes are HTML attributes by default.
- URL attributes remain URL contexts by name because URL safety is part of HTML
  semantics.
- JavaScript is explicit with `js\`...\``.
- CSS is explicit with `css\`...\``.

Examples:

```gsx
<a href={nextURL}>Next</a>                 // URL-sanitized + attr-escaped
<div title={name}>...</div>                // attr-escaped
<div x-data="{ open: false }">...</div>    // static literal string
<div x-data=js`{ open: false }`>...</div>   // explicit JS source
```

Framework-specific JS/CSS attribute classification should not be required for
correct authoring. In particular, `x-data`, `@click`, `x-show`, `:class`,
`hx-on:*`, `wire:*`, and similar attributes do not need to be configured before
authors can safely write JavaScript in them.

## Configuration Model

Remove JS/CSS attribute classification from config and public options:

- remove `jsAttrs` and `cssAttrs` from `gsx.toml`;
- remove `gen.WithJSAttrs`;
- remove `gen.WithCSSAttrs`;
- remove or replace `gen.WithAttrClassifier` so users cannot add custom JS/CSS
  contexts by attribute name;
- remove generated cache-key, manifest, `gsx info --json`, and docs references
  to JS/CSS attr-classification rules.

URL classification remains the one attribute-context extension point because
URLs are an HTML safety concern:

```toml
[[urlAttrs]]
name = "data-href"
```

and:

```go
gen.WithURLAttrs(gen.Rule{Name: "data-href"})
```

If a low-level predicate API remains, it should be URL-only. It must not be a
general "classify this attr as JS/CSS" escape hatch.

## `{{ }}` Decision

Keep `{{ }}` reserved for ordered attribute-bag construction:

```gsx
<Counter signals={{ "data-signals": signals, "data-text": label }} />
```

Do not add this as JavaScript object sugar:

```gsx
<!-- Not part of this design. -->
<div x-data={{ open: false, checked: checked }}>
```

The explicit JS literal is slightly longer but keeps one clear mechanism for
embedded JavaScript:

```gsx
<div x-data=js`{ open: false, checked: @{checked} }`>
```

## Formatting and Editor Behavior

Formatters and editor integrations should treat the contents of `js\`...\`` as
JavaScript and `css\`...\`` as CSS, with `@{}` holes carved out and formatted
as Go expressions.

The embedded-language literal body should be preserved as source text except
for indentation normalization and formatting decisions made by the JS/CSS
formatter. The gsx parser should not try to parse JavaScript or CSS grammar
itself.

## Parser Shape

The parser should recognize `js\`...\`` and `css\`...\`` in two attribute RHS
positions:

- directly after `=`, as in `@click=js\`...\``;
- inside a braced dynamic value, as in `@click={js\`...\`}`.

The literal body is raw embedded-language text split on `@{}` holes, using the
same Go-aware hole parsing already used for `<script>` and `<style>`.

Backticks inside the embedded language need a concrete escaping rule. The
initial design supports backslash-escaped backticks, e.g.
`` js`const s = \`x\`` ``. A longer delimiter form can be designed later if
real-world JavaScript template-literal usage makes escaping too noisy.

## Codegen Shape

For an attribute such as:

```gsx
<button @click=js`save(@{id})`>Save</button>
```

Codegen emits:

1. the opening attribute text;
2. static embedded-language text after HTML-attribute escaping;
3. each `@{}` hole through the JS/CSS attr escaper selected for its context;
4. the closing quote.

The output is still an HTML attribute string. `js\`...\`` and `css\`...\`` do
not bypass HTML attribute escaping.

## Diagnostics

Diagnostics should be explicit:

- `js\`...\`` outside an attribute value: unsupported until a later design
  defines expression-position behavior.
- `css\`...\`` outside an attribute value: unsupported until a later design
  defines expression-position behavior.
- `@{}` in unsafe JS identifier/binding position: fail closed with the existing
  JS-context diagnostic style.
- unclosed embedded literal: point at the opening `js\`` or `css\``.
- invalid Go expression inside `@{}`: use normal Go-expression diagnostics.

## Migration Impact

This design removes most cases that currently need Go string construction for
Alpine/HTMX/client-side expressions:

```gsx
// Before
<div x-show={ fmt.Sprintf("activeTab === '%s'", tab.ID) }>

// After
<div x-show=js`activeTab === @{tab.ID}`>
```

```gsx
// Before
<button @click={ fmt.Sprintf("$dispatch('chart-fullscreen', {chartId: '%s', title: '%s'})", chartID, title) }>

// After
<button @click=js`$dispatch('chart-fullscreen', { chartId: @{chartID}, title: @{title} })`>
```

```gsx
// Before
<form x-data={ fmt.Sprintf(`{
	activeTab: location.hash.slice(1) || 'opportunity',
	berjayaWorkflow: %t,
}`, berjayaWorkflow) }>

// After
<form x-data=js`
	{
		activeTab: location.hash.slice(1) || 'opportunity',
		berjayaWorkflow: @{berjayaWorkflow},
	}
`>
```

## Implementation Notes

The implementation plan can choose the internal AST representation:

- separate JS/CSS attr node types; or
- one generalized embedded-language attr node with a language field.

The language behavior is not open:

- Existing quoted JS-attribute `@{}` support should be removed before release
  so quoted attributes remain static literal strings.
- `css\`...\`` is allowed on any attribute where the author explicitly marks
  CSS. The author, not an attribute-name registry, supplies the CSS context.
- The existing JS/CSS attr classifier should be removed from parser/codegen
  behavior. If editor code keeps any legacy classifier helpers temporarily, they
  must not affect escaping or code generation.
- Documentation should remove the JS/CSS attr-classification extension workflow
  and teach `js\`...\`` / `css\`...\`` instead.

## Decision Summary

Adopt explicit embedded-language attribute literals:

- `js\`...\`` for JavaScript source.
- `css\`...\`` for CSS source.
- `@{expr}` for escaped Go interpolation inside those literals.
- Allow embedded-language literals directly after `=` and inside `{ }`; prefer
  the direct form for attribute-local JS/CSS.
- Keep `{{ }}` exclusively for ordered attribute-bag construction.
- Keep URL attribute classification in core.
- Remove JS/CSS attr configuration and options; keep URL attr configuration as
  the only attr-context knob.
