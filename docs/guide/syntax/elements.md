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

`help` is inferred as `gsx.Node` and interpolates like any other node. Interpolations inside the element resolve against the surrounding Go scope, capturing locals just as ordinary Go would.

It works the same as a call argument (e.g. `structpages.RenderComponent(<Foo/>)`) or a `return` value:

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

> **Gotcha.** A top-level Go region containing an element (like `func Help`) is emitted as one unit, so a file whose only `import` exists to spell `gsx.Node` needs at least one `component` declaration (`Noop` above) for the import to hoist ahead of generated code.

As a struct-literal field of type `gsx.Node` — baking an icon inline instead of pointing at a separate component — and as a component tag in expression position:

```gsx
var item = NavItem{Label: "Home", Icon: <svg class="w-5 h-5">
	<path d="M0 0"/>
</svg>}

var badge = <Badge count={12}/>
```

### Fragments as values

A fragment (`<>…</>`) works in every one of those same expression positions — `var`, `return`, a call argument, a struct field, a slice/map element — and lowers to a `gsx.Node` the same way a tagged element does, just without a wrapping tag. The Go-side runtime equivalent is `gsx.Fragment(nodes...)`.

The driving use case is returning a *list* of sibling elements from a plain Go function: an element literal can only ever wrap one tag, but a fragment's children can be a `{ for … }` loop that emits many top-level siblings.

```gsx
package views

import "github.com/gsxhq/gsx"

component Noop() {
	<span/>
}

func Items(xs []string) gsx.Node {
	return <>{ for _, s := range xs { <li>{ s }</li> } }</>
}

component Host() {
	<ul>{ Items([]string{"a", "b"}) }</ul>
}
```

An empty fragment, `<></>`, renders nothing — it's the render-nothing nop, the same role `templ.NopComponent` plays in templ, or a `gsx.Fragment()` call with no arguments.

Fragments take no attributes — there's no tag to attach them to. And a fragment's children must be explicitly wrapped in the `<>…</>` delimiters; bare adjacent siblings (`<A/><B/>`) are not legal on their own in expression position, the same "explicit wrapping" rule element expressions already follow.

### Inside interpolations

An element or fragment literal also works as an operand *inside* a `{ … }`
interpolation — e.g. a call argument — not just at a top-level
`var`/`return`/field position. `wrap` below is a plain Go function taking a
`gsx.Node`:

```gsx
package demo

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }

component Uses() {
	<div>{ wrap(<><b>hi</b></>) }</div>
}
```

renders `<div><b>hi</b></div>`. A component tag composes the same way, and its
own props and interpolations resolve against the *enclosing* component's
scope by ordinary closure capture — not `wrap`'s:

```gsx
component Badge(count int) {
	<span>{ count }</span>
}

component Uses(n int) {
	<div>{ wrap(<Badge count={n}/>) }</div>
}
```

`Uses(UsesProps{N: 7})` renders `<div><span>7</span></div>` — `n` is `Uses`'s
own param, captured exactly as a hand-written `gsx.Func` closure would capture
it.

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
