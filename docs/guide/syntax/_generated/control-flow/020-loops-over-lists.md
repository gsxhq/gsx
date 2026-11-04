<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type Item struct {
	Name  string
	Count int
}

component List(items []Item) {
	<ul>
		{ for _, it := range items {
			<li>{ it.Name }: { it.Count }</li>
		} }
	</ul>
}
```

Renders:

```html
<ul><li>alpha: 1</li><li>beta: 2</li></ul>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIEl0ZW0gc3RydWN0IHtcblx0TmFtZSAgc3RyaW5nXG5cdENvdW50IGludFxufVxuXG5jb21wb25lbnQgTGlzdChpdGVtcyBbXUl0ZW0pIHtcblx0XHUwMDNjdWxcdTAwM2Vcblx0XHR7IGZvciBfLCBpdCA6PSByYW5nZSBpdGVtcyB7XG5cdFx0XHRcdTAwM2NsaVx1MDAzZXsgaXQuTmFtZSB9OiB7IGl0LkNvdW50IH1cdTAwM2MvbGlcdTAwM2Vcblx0XHR9IH1cblx0XHUwMDNjL3VsXHUwMDNlXG59XG4iLCJpIjoiTGlzdChbXUl0ZW17e05hbWU6IFwiYWxwaGFcIiwgQ291bnQ6IDF9LCB7TmFtZTogXCJiZXRhXCIsIENvdW50OiAyfX0pIn0=)
