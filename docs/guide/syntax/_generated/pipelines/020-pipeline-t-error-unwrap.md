<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

func greet(name string) (string, error) { return "Hi " + name, nil }

component Card(name string) {
	<p>{ greet(name) }</p>
}
```

Renders:

```html
<p>Hi Al</p>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5mdW5jIGdyZWV0KG5hbWUgc3RyaW5nKSAoc3RyaW5nLCBlcnJvcikgeyByZXR1cm4gXCJIaSBcIiArIG5hbWUsIG5pbCB9XG5cbmNvbXBvbmVudCBDYXJkKG5hbWUgc3RyaW5nKSB7XG5cdFx1MDAzY3BcdTAwM2V7IGdyZWV0KG5hbWUpIH1cdTAwM2MvcFx1MDAzZVxufVxuIiwiaSI6IkNhcmQoQ2FyZFByb3Bze05hbWU6IFwiQWxcIn0pIn0=)
