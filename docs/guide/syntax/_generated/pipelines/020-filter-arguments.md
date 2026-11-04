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
<p>hello</p>
<p>42 comments</p>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgU3RhdHMocyBzdHJpbmcsIGNvdW50IGludCkge1xuXHUwMDNjcFx1MDAzZXsgcyB8XHUwMDNlIHRyaW0gfFx1MDAzZSB0cnVuY2F0ZSg1KSB9XHUwMDNjL3BcdTAwM2Vcblx1MDAzY3BcdTAwM2V7IGNvdW50IHxcdTAwM2UgcHJpbnRmKFwiJWQgY29tbWVudHNcIikgfVx1MDAzYy9wXHUwMDNlXG59XG4iLCJpIjoiU3RhdHMoXCIgIGhlbGxvIHdvcmxkICBcIiwgNDIpIn0=)
