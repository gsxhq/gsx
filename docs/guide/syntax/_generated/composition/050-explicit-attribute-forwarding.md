<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Button(variant string) {
	<button class="btn" data-variant={variant} { attrs... }>{ children }</button>
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

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQnV0dG9uKHZhcmlhbnQgc3RyaW5nKSB7XG5cdFx1MDAzY2J1dHRvbiBjbGFzcz1cImJ0blwiIGRhdGEtdmFyaWFudD17dmFyaWFudH0geyBhdHRycy4uLiB9XHUwMDNleyBjaGlsZHJlbiB9XHUwMDNjL2J1dHRvblx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZSgpIHtcblx0XHUwMDNjQnV0dG9uIHZhcmlhbnQ9XCJwcmltYXJ5XCIgY2xhc3M9XCJ3LWZ1bGxcIiBkYXRhLXRlc3Q9XCJ4XCIgaHgtcG9zdD1cIi9nb1wiXHUwMDNlXG5cdFx0U2F2ZVxuXHRcdTAwM2MvQnV0dG9uXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
