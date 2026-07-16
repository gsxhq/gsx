<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Card(title string, featured bool, count int) {
	<div class={ "card", "card-featured": featured }>
		<h2>{ title }</h2>
		<span>{ count }</span>
	</div>
}

component Page(t string, n int) {
	<Card title={t} featured count={n}/>
}
```

Renders:

```html
<div class="card card-featured"><h2>Hi</h2><span>3</span></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQ2FyZCh0aXRsZSBzdHJpbmcsIGZlYXR1cmVkIGJvb2wsIGNvdW50IGludCkge1xuXHRcdTAwM2NkaXYgY2xhc3M9eyBcImNhcmRcIiwgXCJjYXJkLWZlYXR1cmVkXCI6IGZlYXR1cmVkIH1cdTAwM2Vcblx0XHRcdTAwM2NoMlx1MDAzZXsgdGl0bGUgfVx1MDAzYy9oMlx1MDAzZVxuXHRcdFx1MDAzY3NwYW5cdTAwM2V7IGNvdW50IH1cdTAwM2Mvc3Bhblx1MDAzZVxuXHRcdTAwM2MvZGl2XHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKHQgc3RyaW5nLCBuIGludCkge1xuXHRcdTAwM2NDYXJkIHRpdGxlPXt0fSBmZWF0dXJlZCBjb3VudD17bn0vXHUwMDNlXG59XG4iLCJpIjoiUGFnZShcIkhpXCIsIDMpIn0=)
