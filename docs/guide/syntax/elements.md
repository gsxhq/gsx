# Elements

HTML elements in gsx follow the same rules as HTML: tag names are lowercase (or hyphenated for custom elements), attributes appear in the opening tag, and children nest inside. The only additions are dynamic attribute values (`{expr}`) and the component capitalization rule explained in [Basic Syntax](./basic-syntax#element-vs-component).

## Tags and nesting

Any valid HTML element tag is written directly — `<div>`, `<section>`, `<p>`, `<ul>`, `<li>`, and so on. Elements nest the same way they do in HTML: children appear between the opening and closing tag.

```gsx
package views

component Card(title string, body string) {
	<article>
		<h2>{title}</h2>
		<p>{body}</p>
	</article>
}
```

Attributes accept either a static string literal (`type="text"`) or a dynamic Go expression (`disabled={disabled}`). Boolean attributes — ones whose mere presence means `true` — can be written bare (`required`) or driven by a Go expression whose value must be `bool` (`disabled={on}`). A bare boolean attribute is always emitted; a `{bool}` attribute is emitted only when the expression is `true`.

## Void / self-closing elements

Void elements (`<br>`, `<hr>`, `<img>`, `<input>`, `<link>`, `<meta>`, and the others defined by the HTML spec) have no children and no closing tag. In gsx they are written with a trailing `/>`, and they render without a closing tag.

<!--@include: ./_generated/elements/020-void-elements.md-->

A `disabled={disabled}` attribute on the `<input/>` renders as `disabled` when the value is `true`, and is omitted entirely when `false`. Bare boolean attributes like `required` are always rendered.

## Raw-text elements

`<script>` and `<style>` are **raw-text elements**: the parser reads their bodies as literal text, never as markup. There are no nested child elements and `<` is not treated as a tag start — it passes through to the output exactly as written, never HTML-escaped. The one exception is `@{ expr }` interpolation, which is supported inside both elements for dynamic values.

<!--@include: ./_generated/elements/030-raw-text-elements.md-->

By default, raw-text bodies are emitted without minification. Configure
`css_minifier` or `js_minifier` to minify `<style>` or `<script>` output.

Both `<script>` and `<style>` support `@{ expr }` interpolation for dynamic values — see [Style blocks](./styling) and [JavaScript](./javascript) for details.

## Documents and DOCTYPE

A component can render a full HTML page — `<!DOCTYPE html>`, the `<html>` element, and everything inside. There is no special document mode; gsx treats `<!DOCTYPE html>` as a node in the markup and emits it verbatim.

<!--@include: ./_generated/elements/040-full-html-document.md-->

## Elements as values

A `<tag>…</tag>` isn't limited to a `component` body — it can appear anywhere a Go **expression** is expected in a `.gsx` file: a `var` initializer, a `return` statement, a function-call argument, a struct-literal field, or a slice/map element. The tag lowers to an ordinary `gsx.Node` value.

```gsx
package demo

var help = <a href="/help" class="text-blue-600">?</a>

component Uses() {
	<div>{ help }</div>
}
```

`help` is inferred as `gsx.Node`, exactly as if it had been returned from a component, and interpolates like any other node value. Interpolations inside the element resolve against the surrounding Go scope — `<span class={ cls }>{ label }</span>` reads whatever `cls`/`label` are in scope where the element is written, including local variables inside a function, which the generated closure captures the same way ordinary Go code would.

The same form works as a call argument — the shape `RenderComponent(<Foo/>)` (for example, `structpages.RenderComponent`) uses:

```gsx
component Foo() {
	<p>Foo body</p>
}

var wrapped = Wrap(<Foo/>)
```

as a `return` value:

```gsx
package demo

import "github.com/gsxhq/gsx"

component Noop() {
	<span/>
}

func Help() gsx.Node {
	return <div>hi</div>
}
```

The `component Noop()` here isn't decoration: when a `.gsx` file's only package-level `import` exists solely to spell `gsx.Node`, the file needs at least one `component` declaration for the import to hoist correctly. An element-containing top-level Go region (like `func Help`) is emitted as a single unit, and without a preceding `component` boundary the `import` ends up after generated code — `imports must appear before other declarations`. A `component` splits the file into separate chunks and resolves it; this is a current limitation of element literals in top-level Go regions.

Element literals also appear as a struct-literal field whose declared type is `gsx.Node` — a nav-item table can bake its own icon inline instead of pointing at a separate component:

```gsx
var item = NavItem{Label: "Home", Icon: <svg class="w-5 h-5">
	<path d="M0 0"/>
</svg>}
```

Component tags work in expression position too, lowering through the same attr→prop path a component body's child tags already use:

```gsx
component Badge(count int) {
	<span class="badge">{ count }</span>
}

var badge = <Badge count={12}/>
```

### Element, not component

A `<tag>` in expression position is always an **Element** — the baked result of applying a tag, not the component itself. A bare identifier `Badge` is the component (a function you can still call and pass attributes to); `<Badge …/>` is the node that results from applying it, with its attributes already baked in:

| Form | Is a | Type | Attrs apply at render? |
|---|---|---|:--:|
| `Badge` (bare name) | Component | `func(...) gsx.Node` | yes |
| `<Badge count={12}/>` (tag) | **Element** | `gsx.Node` | no — baked in |

Because the element is baked, attributes can't be injected into it later — there is no render-site `class`/`attrs` binding on a stored element the way there is on a live component call. Put whatever is constant across every use directly on the literal:

```gsx
// class is constant across every nav item, so a baked element is enough
var item = NavItem{Label: "Dashboard", Icon: <HomeIcon class="w-5 h-5"/>}
```

When attributes must vary per site, don't store the element — write the tag fresh where it's rendered, so each site supplies its own attributes: `<HomeIcon class={ dynamicClass }/>`.

The main payoff is removing throwaway single-use `component` declarations: markup that exists only to be handed to a function or stored in a field can be written where the value is needed, without a separate declaration above it.
