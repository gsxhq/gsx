<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

func lookup(k string) (string, error) { return k, nil }

component Label(key string) {
	<span>{ lookup(key) }</span>
}
```

Renders:

```html
<span>hello</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5mdW5jIGxvb2t1cChrIHN0cmluZykgKHN0cmluZywgZXJyb3IpIHsgcmV0dXJuIGssIG5pbCB9XG5cbmNvbXBvbmVudCBMYWJlbChrZXkgc3RyaW5nKSB7XG5cdFx1MDAzY3NwYW5cdTAwM2V7IGxvb2t1cChrZXkpIH1cdTAwM2Mvc3Bhblx1MDAzZVxufVxuIiwiaSI6IkxhYmVsKExhYmVsUHJvcHN7S2V5OiBcImhlbGxvXCJ9KSJ9)
