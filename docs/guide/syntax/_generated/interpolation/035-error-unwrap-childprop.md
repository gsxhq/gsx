<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

func lookup(k string) (string, error) { return "val-" + k, nil }

component Row(label string) {
	<span>{ label }</span>
}

component Page(k string) {
	<Row label={lookup(k)}/>
}
```

Renders:

```html
<span>val-item</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5mdW5jIGxvb2t1cChrIHN0cmluZykgKHN0cmluZywgZXJyb3IpIHsgcmV0dXJuIFwidmFsLVwiICsgaywgbmlsIH1cblxuY29tcG9uZW50IFJvdyhsYWJlbCBzdHJpbmcpIHtcblx0XHUwMDNjc3Bhblx1MDAzZXsgbGFiZWwgfVx1MDAzYy9zcGFuXHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKGsgc3RyaW5nKSB7XG5cdFx1MDAzY1JvdyBsYWJlbD17bG9va3VwKGspfS9cdTAwM2Vcbn1cbiIsImkiOiJQYWdlKFwiaXRlbVwiKSJ9)
