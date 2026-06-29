<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type UsersPage struct {
	Title string
	Sort  string
}

component (p UsersPage) Grid(sort string) {
	<div>{ sort }-{ p.Title }</div>
}
```

Renders:

```html
<div>name-Team</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIFVzZXJzUGFnZSBzdHJ1Y3Qge1xuXHRUaXRsZSBzdHJpbmdcblx0U29ydCAgc3RyaW5nXG59XG5cbmNvbXBvbmVudCAocCBVc2Vyc1BhZ2UpIEdyaWQoc29ydCBzdHJpbmcpIHtcblx0XHUwMDNjZGl2XHUwMDNleyBzb3J0IH0teyBwLlRpdGxlIH1cdTAwM2MvZGl2XHUwMDNlXG59XG4iLCJpIjoiKFVzZXJzUGFnZXtUaXRsZTogXCJUZWFtXCJ9KS5HcmlkKFVzZXJzUGFnZUdyaWRQcm9wc3tTb3J0OiBcIm5hbWVcIn0pIn0=)
