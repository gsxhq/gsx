<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

type Props struct {
	Variant  string
	Children gsx.Node
	Attrs    gsx.Attrs
}

component Button(p Props) {
	<button class={ "btn", p.Variant } { p.Attrs... }>{ p.Children }</button>
}

component Page() {
	<Button variant="primary" data-test="save">Save</Button>
}
```

Renders:

```html
<button class="btn primary" data-test="save">Save</button>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbnR5cGUgUHJvcHMgc3RydWN0IHtcblx0VmFyaWFudCAgc3RyaW5nXG5cdENoaWxkcmVuIGdzeC5Ob2RlXG5cdEF0dHJzICAgIGdzeC5BdHRyc1xufVxuXG5jb21wb25lbnQgQnV0dG9uKHAgUHJvcHMpIHtcblx0XHUwMDNjYnV0dG9uIGNsYXNzPXsgXCJidG5cIiwgcC5WYXJpYW50IH0geyBwLkF0dHJzLi4uIH1cdTAwM2V7IHAuQ2hpbGRyZW4gfVx1MDAzYy9idXR0b25cdTAwM2Vcbn1cblxuY29tcG9uZW50IFBhZ2UoKSB7XG5cdFx1MDAzY0J1dHRvbiB2YXJpYW50PVwicHJpbWFyeVwiIGRhdGEtdGVzdD1cInNhdmVcIlx1MDAzZVNhdmVcdTAwM2MvQnV0dG9uXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
