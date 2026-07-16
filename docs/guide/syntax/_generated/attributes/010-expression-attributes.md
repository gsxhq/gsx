<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Link(url string, label string, count int) {
	<a href={url} data-count={count}>{ label }</a>
}
```

Renders:

```html
<a href="/p?q=a&amp;b" data-count="42">Docs</a>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgTGluayh1cmwgc3RyaW5nLCBsYWJlbCBzdHJpbmcsIGNvdW50IGludCkge1xuXHRcdTAwM2NhIGhyZWY9e3VybH0gZGF0YS1jb3VudD17Y291bnR9XHUwMDNleyBsYWJlbCB9XHUwMDNjL2FcdTAwM2Vcbn1cbiIsImkiOiJMaW5rKFwiL3A/cT1hXHUwMDI2YlwiLCBcIkRvY3NcIiwgNDIpIn0=)
