<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Stats(s string, count int) {
	<p>{ s |> trim |> truncate(5) }</p>
	<p>{ count |> printf("%d comments") }</p>
}
```

Renders:

```html
<p>hello</p><p>42 comments</p>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgU3RhdHMocyBzdHJpbmcsIGNvdW50IGludCkge1xuXHRcdTAwM2NwXHUwMDNleyBzIHxcdTAwM2UgdHJpbSB8XHUwMDNlIHRydW5jYXRlKDUpIH1cdTAwM2MvcFx1MDAzZVxuXHRcdTAwM2NwXHUwMDNleyBjb3VudCB8XHUwMDNlIHByaW50ZihcIiVkIGNvbW1lbnRzXCIpIH1cdTAwM2MvcFx1MDAzZVxufVxuIiwiaSI6IlN0YXRzKFwiICBoZWxsbyB3b3JsZCAgXCIsIDQyKSJ9)
