<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type UsersPage struct {
	Title string
	Sort  string
}

component (p UsersPage) Page() {
	<div><p.Grid sort={p.Sort}/></div>
}

component (p UsersPage) Grid(sort string) {
	<span>{ sort }-{ p.Title }</span>
}
```

Renders:

```html
<div><span>name-Team</span></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIFVzZXJzUGFnZSBzdHJ1Y3Qge1xuXHRUaXRsZSBzdHJpbmdcblx0U29ydCAgc3RyaW5nXG59XG5cbmNvbXBvbmVudCAocCBVc2Vyc1BhZ2UpIFBhZ2UoKSB7XG5cdFx1MDAzY2Rpdlx1MDAzZVx1MDAzY3AuR3JpZCBzb3J0PXtwLlNvcnR9L1x1MDAzZVx1MDAzYy9kaXZcdTAwM2Vcbn1cblxuY29tcG9uZW50IChwIFVzZXJzUGFnZSkgR3JpZChzb3J0IHN0cmluZykge1xuXHRcdTAwM2NzcGFuXHUwMDNleyBzb3J0IH0teyBwLlRpdGxlIH1cdTAwM2Mvc3Bhblx1MDAzZVxufVxuIiwiaSI6IihVc2Vyc1BhZ2V7VGl0bGU6IFwiVGVhbVwiLCBTb3J0OiBcIm5hbWVcIn0pLlBhZ2UoKSJ9)
