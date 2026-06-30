<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

// User input is HTML-escaped by construction — no XSS.
component Comment(body string) {
	<blockquote>{ body }</blockquote>
}
```

Renders:

```html
<blockquote>&lt;img src=x onerror=alert(1)&gt;</blockquote>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG4vLyBVc2VyIGlucHV0IGlzIEhUTUwtZXNjYXBlZCBieSBjb25zdHJ1Y3Rpb24g4oCUIG5vIFhTUy5cbmNvbXBvbmVudCBDb21tZW50KGJvZHkgc3RyaW5nKSB7XG5cdFx1MDAzY2Jsb2NrcXVvdGVcdTAwM2V7IGJvZHkgfVx1MDAzYy9ibG9ja3F1b3RlXHUwMDNlXG59XG4iLCJpIjoiQ29tbWVudChDb21tZW50UHJvcHN7Qm9keTogXCJcdTAwM2NpbWcgc3JjPXggb25lcnJvcj1hbGVydCgxKVx1MDAzZVwifSkifQ==)
