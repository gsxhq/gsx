<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Button(variant string) {
	<button class="btn" data-variant={variant} { attrs... }>
		{ children }
	</button>
}

component Page() {
	<Button variant="primary" class="w-full" data-test="x" hx-post="/go">
		Save
	</Button>
}
```

Renders:

```html
<button class="btn w-full" data-variant="primary" data-test="x" hx-post="/go">Save</button>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQnV0dG9uKHZhcmlhbnQgc3RyaW5nKSB7XG5cdFx1MDAzY2J1dHRvbiBjbGFzcz1cImJ0blwiIGRhdGEtdmFyaWFudD17dmFyaWFudH0geyBhdHRycy4uLiB9XHUwMDNlXG5cdFx0eyBjaGlsZHJlbiB9XG5cdFx1MDAzYy9idXR0b25cdTAwM2Vcbn1cblxuY29tcG9uZW50IFBhZ2UoKSB7XG5cdFx1MDAzY0J1dHRvbiB2YXJpYW50PVwicHJpbWFyeVwiIGNsYXNzPVwidy1mdWxsXCIgZGF0YS10ZXN0PVwieFwiIGh4LXBvc3Q9XCIvZ29cIlx1MDAzZVxuXHRcdFNhdmVcblx0XHUwMDNjL0J1dHRvblx1MDAzZVxufVxuIiwiaSI6IlBhZ2UoKSJ9)
