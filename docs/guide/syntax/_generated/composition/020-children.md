<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Card(title string) {
	<article class="card">
		<h3>{ title }</h3>
		<div class="card__body">{ children }</div>
	</article>
}

component Page() {
	<Card title="Hello">
		<em>composed</em>
	</Card>
}
```

Renders:

```html
<article class="card"><h3>Hello</h3><div class="card__body"><em>composed</em></div></article>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQ2FyZCh0aXRsZSBzdHJpbmcpIHtcblx0XHUwMDNjYXJ0aWNsZSBjbGFzcz1cImNhcmRcIlx1MDAzZVxuXHRcdFx1MDAzY2gzXHUwMDNleyB0aXRsZSB9XHUwMDNjL2gzXHUwMDNlXG5cdFx0XHUwMDNjZGl2IGNsYXNzPVwiY2FyZF9fYm9keVwiXHUwMDNleyBjaGlsZHJlbiB9XHUwMDNjL2Rpdlx1MDAzZVxuXHRcdTAwM2MvYXJ0aWNsZVx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZSgpIHtcblx0XHUwMDNjQ2FyZCB0aXRsZT1cIkhlbGxvXCJcdTAwM2Vcblx0XHRcdTAwM2NlbVx1MDAzZWNvbXBvc2VkXHUwMDNjL2VtXHUwMDNlXG5cdFx1MDAzYy9DYXJkXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
