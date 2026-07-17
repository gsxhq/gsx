<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type cardData struct {
	Title string
}

type pageData struct {
	Heading string
}

type Home struct{}

component Card(d cardData) {
	<div>{ d.Title }</div>
}

component Page(d pageData) {
	<Card d={cardData{Title: d.Heading}}/>
}

component (p Home) Content(pd pageData) {
	<h1>{ pd.Heading }</h1>
}

component (p Home) Shell(pd pageData) {
	<p.Content pd={pd}/>
}
```

Renders:

```html
<h1>Hi</h1>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIGNhcmREYXRhIHN0cnVjdCB7XG5cdFRpdGxlIHN0cmluZ1xufVxuXG50eXBlIHBhZ2VEYXRhIHN0cnVjdCB7XG5cdEhlYWRpbmcgc3RyaW5nXG59XG5cbnR5cGUgSG9tZSBzdHJ1Y3R7fVxuXG5jb21wb25lbnQgQ2FyZChkIGNhcmREYXRhKSB7XG5cdFx1MDAzY2Rpdlx1MDAzZXsgZC5UaXRsZSB9XHUwMDNjL2Rpdlx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZShkIHBhZ2VEYXRhKSB7XG5cdFx1MDAzY0NhcmQgZD17Y2FyZERhdGF7VGl0bGU6IGQuSGVhZGluZ319L1x1MDAzZVxufVxuXG5jb21wb25lbnQgKHAgSG9tZSkgQ29udGVudChwZCBwYWdlRGF0YSkge1xuXHRcdTAwM2NoMVx1MDAzZXsgcGQuSGVhZGluZyB9XHUwMDNjL2gxXHUwMDNlXG59XG5cbmNvbXBvbmVudCAocCBIb21lKSBTaGVsbChwZCBwYWdlRGF0YSkge1xuXHRcdTAwM2NwLkNvbnRlbnQgcGQ9e3BkfS9cdTAwM2Vcbn1cbiIsImkiOiIoSG9tZXt9KS5TaGVsbChwYWdlRGF0YXtIZWFkaW5nOiBcIkhpXCJ9KSJ9)
