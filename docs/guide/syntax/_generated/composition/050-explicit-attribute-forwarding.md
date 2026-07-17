<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Button(variant string, children gsx.Node, attrs gsx.Attrs) {
	<button class="btn" data-variant={variant} { attrs... }>{ children }</button>
}

component Page() {
	<Button variant="primary" class="w-full" data-test="x" hx-post="/go">Save</Button>
}
```

Renders:

```html
<button class="btn w-full" data-variant="primary" data-test="x" hx-post="/go">Save</button>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBCdXR0b24odmFyaWFudCBzdHJpbmcsIGNoaWxkcmVuIGdzeC5Ob2RlLCBhdHRycyBnc3guQXR0cnMpIHtcblx0XHUwMDNjYnV0dG9uIGNsYXNzPVwiYnRuXCIgZGF0YS12YXJpYW50PXt2YXJpYW50fSB7IGF0dHJzLi4uIH1cdTAwM2V7IGNoaWxkcmVuIH1cdTAwM2MvYnV0dG9uXHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKCkge1xuXHRcdTAwM2NCdXR0b24gdmFyaWFudD1cInByaW1hcnlcIiBjbGFzcz1cInctZnVsbFwiIGRhdGEtdGVzdD1cInhcIiBoeC1wb3N0PVwiL2dvXCJcdTAwM2VTYXZlXHUwMDNjL0J1dHRvblx1MDAzZVxufVxuIiwiaSI6IlBhZ2UoKSJ9)
