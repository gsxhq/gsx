<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Badge(featured bool) {
	<span
		{ if featured {
			class="featured"
		} }
	>
		content
	</span>
}
```

Renders:

```html
<span class="featured">content</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQmFkZ2UoZmVhdHVyZWQgYm9vbCkge1xuXHRcdTAwM2NzcGFuXG5cdFx0eyBpZiBmZWF0dXJlZCB7XG5cdFx0XHRjbGFzcz1cImZlYXR1cmVkXCJcblx0XHR9IH1cblx0XHUwMDNlXG5cdFx0Y29udGVudFxuXHRcdTAwM2Mvc3Bhblx1MDAzZVxufVxuIiwiaSI6IkJhZGdlKHRydWUpIn0=)
