<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type UsersPage struct {
	Title string
	Sort  string
}

component (p UsersPage) Page() {
	<div>
		<p.Grid sort={p.Sort}/>
	</div>
}

component (p UsersPage) Grid(sort string) {
	<span>{ sort }-{ p.Title }</span>
}
```

Renders:

```html
<div><span>name-Team</span></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIFVzZXJzUGFnZSBzdHJ1Y3Qge1xuXHRUaXRsZSBzdHJpbmdcblx0U29ydCAgc3RyaW5nXG59XG5cbmNvbXBvbmVudCAocCBVc2Vyc1BhZ2UpIFBhZ2UoKSB7XG5cdFx1MDAzY2Rpdlx1MDAzZVxuXHRcdFx1MDAzY3AuR3JpZCBzb3J0PXtwLlNvcnR9L1x1MDAzZVxuXHRcdTAwM2MvZGl2XHUwMDNlXG59XG5cbmNvbXBvbmVudCAocCBVc2Vyc1BhZ2UpIEdyaWQoc29ydCBzdHJpbmcpIHtcblx0XHUwMDNjc3Bhblx1MDAzZXsgc29ydCB9LXsgcC5UaXRsZSB9XHUwMDNjL3NwYW5cdTAwM2Vcbn1cbiIsImkiOiIoVXNlcnNQYWdle1RpdGxlOiBcIlRlYW1cIiwgU29ydDogXCJuYW1lXCJ9KS5QYWdlKCkifQ==)
