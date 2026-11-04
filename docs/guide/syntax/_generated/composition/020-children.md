<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Card(title string, children gsx.Node) {
<article class="card">
	<h3>{ title }</h3>
	<div class="card__body">{ children }</div>
</article>
}

component Page() {
	<Card title="Hello"><em>composed</em></Card>
}
```

Renders:

```html
<article class="card">
	<h3>Hello</h3>
	<div class="card__body"><em>composed</em></div>
</article>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBDYXJkKHRpdGxlIHN0cmluZywgY2hpbGRyZW4gZ3N4Lk5vZGUpIHtcblx1MDAzY2FydGljbGUgY2xhc3M9XCJjYXJkXCJcdTAwM2Vcblx0XHUwMDNjaDNcdTAwM2V7IHRpdGxlIH1cdTAwM2MvaDNcdTAwM2Vcblx0XHUwMDNjZGl2IGNsYXNzPVwiY2FyZF9fYm9keVwiXHUwMDNleyBjaGlsZHJlbiB9XHUwMDNjL2Rpdlx1MDAzZVxuXHUwMDNjL2FydGljbGVcdTAwM2Vcbn1cblxuY29tcG9uZW50IFBhZ2UoKSB7XG5cdFx1MDAzY0NhcmQgdGl0bGU9XCJIZWxsb1wiXHUwMDNlXHUwMDNjZW1cdTAwM2Vjb21wb3NlZFx1MDAzYy9lbVx1MDAzZVx1MDAzYy9DYXJkXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
