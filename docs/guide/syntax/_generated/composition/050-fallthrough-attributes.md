<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Button(variant string) {
	<button class="btn" data-variant={variant}>{ children }</button>
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

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQnV0dG9uKHZhcmlhbnQgc3RyaW5nKSB7XG5cdFx1MDAzY2J1dHRvbiBjbGFzcz1cImJ0blwiIGRhdGEtdmFyaWFudD17dmFyaWFudH1cdTAwM2V7IGNoaWxkcmVuIH1cdTAwM2MvYnV0dG9uXHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKCkge1xuXHRcdTAwM2NCdXR0b24gdmFyaWFudD1cInByaW1hcnlcIiBjbGFzcz1cInctZnVsbFwiIGRhdGEtdGVzdD1cInhcIiBoeC1wb3N0PVwiL2dvXCJcdTAwM2Vcblx0XHRTYXZlXG5cdFx1MDAzYy9CdXR0b25cdTAwM2Vcbn1cbiIsImkiOiJQYWdlKCkifQ==)
