<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type User struct {
	Name string
	Age  int
}

component Profile(user User) {
	<p>{ user.Name } is { user.Age }</p>
}
```

Renders:

```html
<p>Alice is 30</p>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIFVzZXIgc3RydWN0IHtcblx0TmFtZSBzdHJpbmdcblx0QWdlICBpbnRcbn1cblxuY29tcG9uZW50IFByb2ZpbGUodXNlciBVc2VyKSB7XG5cdFx1MDAzY3BcdTAwM2V7IHVzZXIuTmFtZSB9IGlzIHsgdXNlci5BZ2UgfVx1MDAzYy9wXHUwMDNlXG59XG4iLCJpIjoiUHJvZmlsZShVc2Vye05hbWU6IFwiQWxpY2VcIiwgQWdlOiAzMH0pIn0=)
