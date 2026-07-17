<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Badge(variant string, attrs gsx.Attrs) {
	<span class=f`badge-@{variant}` { attrs... }>Hi</span>
}
```

Renders:

```html
<span class="badge-x hl" id="a">Hi</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBCYWRnZSh2YXJpYW50IHN0cmluZywgYXR0cnMgZ3N4LkF0dHJzKSB7XG5cdFx1MDAzY3NwYW4gY2xhc3M9ZmBiYWRnZS1Ae3ZhcmlhbnR9YCB7IGF0dHJzLi4uIH1cdTAwM2VIaVx1MDAzYy9zcGFuXHUwMDNlXG59XG4iLCJpIjoiQmFkZ2UoXCJ4XCIsIGdzeC5BdHRyc3t7S2V5OiBcImNsYXNzXCIsIFZhbHVlOiBcImhsXCJ9LCB7S2V5OiBcImlkXCIsIFZhbHVlOiBcImFcIn19KSJ9)
