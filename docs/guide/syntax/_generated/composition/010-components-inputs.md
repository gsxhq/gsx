<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Card(title string, featured bool, count int) {
	<div class={ "card", "card-featured": featured }><h2>{ title }</h2><span>{ count }</span></div>
}

component Page(t string, n int) {
	<Card title={t} featured count={n}/>
}
```

Renders:

```html
<div class="card card-featured"><h2>Hi</h2><span>3</span></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQ2FyZCh0aXRsZSBzdHJpbmcsIGZlYXR1cmVkIGJvb2wsIGNvdW50IGludCkge1xuXHRcdTAwM2NkaXYgY2xhc3M9eyBcImNhcmRcIiwgXCJjYXJkLWZlYXR1cmVkXCI6IGZlYXR1cmVkIH1cdTAwM2VcdTAwM2NoMlx1MDAzZXsgdGl0bGUgfVx1MDAzYy9oMlx1MDAzZVx1MDAzY3NwYW5cdTAwM2V7IGNvdW50IH1cdTAwM2Mvc3Bhblx1MDAzZVx1MDAzYy9kaXZcdTAwM2Vcbn1cblxuY29tcG9uZW50IFBhZ2UodCBzdHJpbmcsIG4gaW50KSB7XG5cdFx1MDAzY0NhcmQgdGl0bGU9e3R9IGZlYXR1cmVkIGNvdW50PXtufS9cdTAwM2Vcbn1cbiIsImkiOiJQYWdlKFwiSGlcIiwgMykifQ==)
