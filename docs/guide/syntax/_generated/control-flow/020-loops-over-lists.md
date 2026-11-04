<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type Item struct {
	Name  string
	Count int
}

component List(items []Item) {
	<ul>{ for _, it := range items {
		<li>{ it.Name }: { it.Count }</li>
	} }</ul>
}
```

Renders:

```html
<ul><li>alpha: 1</li><li>beta: 2</li></ul>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIEl0ZW0gc3RydWN0IHtcblx0TmFtZSAgc3RyaW5nXG5cdENvdW50IGludFxufVxuXG5jb21wb25lbnQgTGlzdChpdGVtcyBbXUl0ZW0pIHtcblx0XHUwMDNjdWxcdTAwM2V7IGZvciBfLCBpdCA6PSByYW5nZSBpdGVtcyB7XG5cdFx0XHUwMDNjbGlcdTAwM2V7IGl0Lk5hbWUgfTogeyBpdC5Db3VudCB9XHUwMDNjL2xpXHUwMDNlXG5cdH0gfVx1MDAzYy91bFx1MDAzZVxufVxuIiwiaSI6Ikxpc3QoW11JdGVte3tOYW1lOiBcImFscGhhXCIsIENvdW50OiAxfSwge05hbWU6IFwiYmV0YVwiLCBDb3VudDogMn19KSJ9)
