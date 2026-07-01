# Learn gsx

This path starts after `gsx init`, when you have a working app and a `.gsx`
file open. Keep the examples small at first: write a component, save, and let
`gsx dev` regenerate the Go code.

## 1. A component is Go plus markup

A `.gsx` file begins like any Go file, then adds `component` declarations. The
component body is markup, so there is no return type and no `return`.

```gsx
package views

component Greeting(name string) {
	<p>Hello, {name}</p>
}
```

Expressions inside `{ ... }` are Go expressions. gsx writes context-aware HTML
escaping into the generated `.x.go` file.

## 2. Props are typed

Component parameters are Go parameters. Use ordinary Go types, imports, and
expressions.

```gsx
package views

import "fmt"

component Meter(value int, color string) {
	<div
		class={ "meter", "meter-full": value >= 100 }
		style={ fmt.Sprintf("width: %d%%", value), "color: " + color }
	/>
}
```

Callers pass values with Go syntax:

```gsx
<Meter value={72} color={"rebeccapurple"} />
```

## 3. Components compose with children

Components receive children explicitly. Place `{children}` where nested content
should render.

```gsx
component Panel(title string) {
	<section class="panel">
		<h2>{title}</h2>
		<div class="panel-body">{children}</div>
	</section>
}

component Home() {
	<Panel title="Dashboard">
		<p>Server-rendered content can be composed like HTML.</p>
	</Panel>
}
```

## 4. Attributes are explicit

Static attributes look like HTML. Dynamic attributes use Go expressions. Boolean
attributes are controlled by their value.

```gsx
component SaveButton(disabled bool, label string) {
	<button type="submit" disabled={disabled}>
		{label}
	</button>
}
```

Use `class={ ... }` and `style={ ... }` lists when values need to compose.

## 5. Style and script stay close to HTML

`<style>` and `<script>` stay in the component tree. Use the syntax reference for
the exact interpolation rules in each context.

```gsx
component InlineExample(message string) {
	<div class="notice">{message}</div>
	<style>
		.notice { padding: 0.75rem; border: 1px solid #ccc; }
	</style>
	<script>
		console.log("notice mounted")
	</script>
}
```

## 6. The development loop is one command

The starter runs the full development loop with:

```sh
npm run dev
```

That script runs `go tool gsx dev`. It watches `.gsx`, `.go`, and `.env` files,
regenerates `.x.go`, rebuilds the Go server, and asks Vite to reload the
browser.

## Next

- Keep the [syntax reference](./syntax) open while writing `.gsx`.
- Use the [playground](/playground) to try small components.
- Configure the dev loop, filters, minification, and class merging in [`gsx.toml`](./config).
- Check [Status](./status) before relying on alpha or deferred features.
