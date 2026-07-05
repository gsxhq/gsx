<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Item(id string) {
	<li>{`item-@{id}` |> upper}</li>
}
```

Renders:

```html
<li>ITEM-A&amp;B</li>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgSXRlbShpZCBzdHJpbmcpIHtcblx0XHUwMDNjbGlcdTAwM2V7YGl0ZW0tQHtpZH1gIHxcdTAwM2UgdXBwZXJ9XHUwMDNjL2xpXHUwMDNlXG59XG4iLCJpIjoiSXRlbShJdGVtUHJvcHN7SWQ6IFwiYVx1MDAyNmJcIn0pIn0=)
