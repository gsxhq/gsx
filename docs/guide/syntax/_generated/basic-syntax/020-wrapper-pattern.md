<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component div(children gsx.Node, attrs gsx.Attrs) {
	<div { attrs... }>{ children }</div>
}

component Page() {
	<div class="card">Hello</div>
}
```

Renders:

```html
<div class="card">Hello</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBkaXYoY2hpbGRyZW4gZ3N4Lk5vZGUsIGF0dHJzIGdzeC5BdHRycykge1xuXHRcdTAwM2NkaXYgeyBhdHRycy4uLiB9XHUwMDNleyBjaGlsZHJlbiB9XHUwMDNjL2Rpdlx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZSgpIHtcblx0XHUwMDNjZGl2IGNsYXNzPVwiY2FyZFwiXHUwMDNlSGVsbG9cdTAwM2MvZGl2XHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
