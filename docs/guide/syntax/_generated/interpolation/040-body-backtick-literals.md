<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Row(id string, n int) {
	<p>{f`row-@{id}-@{n}`}</p>
}
```

Renders:

```html
<p>row-a&amp;b-5</p>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgUm93KGlkIHN0cmluZywgbiBpbnQpIHtcblx0XHUwMDNjcFx1MDAzZXtmYHJvdy1Ae2lkfS1Ae259YH1cdTAwM2MvcFx1MDAzZVxufVxuIiwiaSI6IlJvdyhSb3dQcm9wc3tJZDogXCJhXHUwMDI2YlwiLCBOOiA1fSkifQ==)
