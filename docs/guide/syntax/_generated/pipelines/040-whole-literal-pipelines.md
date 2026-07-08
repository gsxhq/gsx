<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Item(id string) {
	<li>{f`item-@{id}` |> upper}</li>
}
```

Renders:

```html
<li>ITEM-A&amp;B</li>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgSXRlbShpZCBzdHJpbmcpIHtcblx0XHUwMDNjbGlcdTAwM2V7ZmBpdGVtLUB7aWR9YCB8XHUwMDNlIHVwcGVyfVx1MDAzYy9saVx1MDAzZVxufVxuIiwiaSI6Ikl0ZW0oSXRlbVByb3Bze0lkOiBcImFcdTAwMjZiXCJ9KSJ9)
