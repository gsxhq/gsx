<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Button(variant string, children gsx.Node, attrs gsx.Attrs) {
	<button class={ "btn", variant } { attrs... }>{ children }</button>
}

component Page() {
	<Button variant="primary" data-test="save">Save</Button>
}
```

Renders:

```html
<button class="btn primary" data-test="save">Save</button>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBCdXR0b24odmFyaWFudCBzdHJpbmcsIGNoaWxkcmVuIGdzeC5Ob2RlLCBhdHRycyBnc3guQXR0cnMpIHtcblx0XHUwMDNjYnV0dG9uIGNsYXNzPXsgXCJidG5cIiwgdmFyaWFudCB9IHsgYXR0cnMuLi4gfVx1MDAzZXsgY2hpbGRyZW4gfVx1MDAzYy9idXR0b25cdTAwM2Vcbn1cblxuY29tcG9uZW50IFBhZ2UoKSB7XG5cdFx1MDAzY0J1dHRvbiB2YXJpYW50PVwicHJpbWFyeVwiIGRhdGEtdGVzdD1cInNhdmVcIlx1MDAzZVNhdmVcdTAwM2MvQnV0dG9uXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
