<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type Props struct {
	Title string
}

component Greeting(name string) {
	<p>Hi { name }</p>
}

component Card(title string, n int) {
	<div>{ title }: { n }</div>
}

component Panel(p Props) {
	<section>{ p.Title }</section>
}

component Page() {
	<>
		<Greeting name="Ann"/>
		<Card title="T" n={2}/>
		<Panel p={Props{Title: "P"}}/>
	</>
}
```

Renders:

```html
<p>Hi Ann</p><div>T: 2</div><section>P</section>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIFByb3BzIHN0cnVjdCB7XG5cdFRpdGxlIHN0cmluZ1xufVxuXG5jb21wb25lbnQgR3JlZXRpbmcobmFtZSBzdHJpbmcpIHtcblx0XHUwMDNjcFx1MDAzZUhpIHsgbmFtZSB9XHUwMDNjL3BcdTAwM2Vcbn1cblxuY29tcG9uZW50IENhcmQodGl0bGUgc3RyaW5nLCBuIGludCkge1xuXHRcdTAwM2NkaXZcdTAwM2V7IHRpdGxlIH06IHsgbiB9XHUwMDNjL2Rpdlx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFuZWwocCBQcm9wcykge1xuXHRcdTAwM2NzZWN0aW9uXHUwMDNleyBwLlRpdGxlIH1cdTAwM2Mvc2VjdGlvblx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZSgpIHtcblx0XHUwMDNjXHUwMDNlXG5cdFx0XHUwMDNjR3JlZXRpbmcgbmFtZT1cIkFublwiL1x1MDAzZVxuXHRcdFx1MDAzY0NhcmQgdGl0bGU9XCJUXCIgbj17Mn0vXHUwMDNlXG5cdFx0XHUwMDNjUGFuZWwgcD17UHJvcHN7VGl0bGU6IFwiUFwifX0vXHUwMDNlXG5cdFx1MDAzYy9cdTAwM2Vcbn1cbiIsImkiOiJQYWdlKCkifQ==)
