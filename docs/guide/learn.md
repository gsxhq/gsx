# Learn gsx

These seven patterns cover the normal authoring model.

## 1. A component is Go plus markup

Declare a component in a `.gsx` file, then write its HTML directly in the body.

```gsx
package views

component Greeting(name string) {
	<p>Hello, {name}</p>
}
```

Code inside `{ ... }` is an ordinary Go expression. Its output is escaped for
the surrounding HTML context; see [Escaping](./syntax/escaping.md).

## 2. Inputs are typed

Declare inputs as Go parameters and pass them as component attributes. The
authored parameter list is also the generated Go function signature.

```gsx
component Meter(value int) {
	<meter min={0} max={100} value={value}>{value}%</meter>
}

component Dashboard() {
	<Meter value={72} />
}
```

Go reports invalid input names or values at build time.

## 3. Components compose with children

Declare `children gsx.Node`, then use `{children}` where a component should
render its nested content.

```gsx
import "github.com/gsxhq/gsx"

component Panel(title string, children gsx.Node) {
	<section class="panel">
		<h2>{title}</h2>
		<div>{children}</div>
	</section>
}

component Home() {
	<Panel title="Dashboard">
		<p>Welcome back.</p>
	</Panel>
}
```

See [Composition](./syntax/composition.md) for slots, generics, and forwarding.

## 4. Attributes are explicit

Write static values like HTML and dynamic values as Go expressions.

```gsx
component SaveButton(label string, disabled bool) {
	<button type="submit" disabled={disabled}>{label}</button>
}
```

Boolean attributes are omitted when their value is false. See
[Attributes](./syntax/attributes.md) for conditionals, spreads, and merge order.

## 5. Style and script stay close to HTML

Put component-specific CSS and JavaScript beside the markup that uses them.

```gsx
component Notice(message string) {
	<p class="notice">{message}</p>
	<style>
		.notice { padding: 0.75rem; border: 1px solid #ccc; }
	</style>
	<script>
		console.log("notice mounted")
	</script>
}
```

See [Styling](./syntax/styling.md) and
[JavaScript](./syntax/javascript.md) for their interpolation rules.

## 6. Markup can be a value

Bind markup to a package-level `var` and it infers as `gsx.Node` — reuse it
across components without a wrapper. A fragment (`<>…</>`) groups sibling nodes
with no surrounding element.

```gsx
package views

var footer = <><hr/><small>Built with gsx</small></>

component Page(children gsx.Node) {
	<main>{children}</main>
	{footer}
}
```

See [Fragments](./syntax/fragments.md) for more.

## 7. Save and reload

Run the starter's development server once:

```sh
npm run dev
```

Save a `.gsx` file and gsx regenerates it, rebuilds the server, and reloads the
browser. Errors appear in the browser while you fix them.

## Next steps

- Browse the [syntax reference](./syntax.md) by task.
- Use the [playground](/playground) to try small components.
- Read the [development loop](./dev-loop.md) when you need failure or file-change behavior.
- Configure the dev loop, filters, minification, and class merging in [`gsx.toml`](./config.md).
