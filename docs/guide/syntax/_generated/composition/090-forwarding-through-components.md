<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Icon(name string) {
	<span class="icon" data-name={name} { attrs... }>i</span>
}

component SearchIcon() {
	<Icon name="search" class="w-5 h-5" { attrs... }/>
}

component Page() {
	<SearchIcon class="text-red" aria-label="Search"/>
}
```

Renders:

```html
<span class="icon w-5 h-5 text-red" data-name="search" aria-label="Search">i</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgSWNvbihuYW1lIHN0cmluZykge1xuXHRcdTAwM2NzcGFuIGNsYXNzPVwiaWNvblwiIGRhdGEtbmFtZT17bmFtZX0geyBhdHRycy4uLiB9XHUwMDNlaVx1MDAzYy9zcGFuXHUwMDNlXG59XG5cbmNvbXBvbmVudCBTZWFyY2hJY29uKCkge1xuXHRcdTAwM2NJY29uIG5hbWU9XCJzZWFyY2hcIiBjbGFzcz1cInctNSBoLTVcIiB7IGF0dHJzLi4uIH0vXHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKCkge1xuXHRcdTAwM2NTZWFyY2hJY29uIGNsYXNzPVwidGV4dC1yZWRcIiBhcmlhLWxhYmVsPVwiU2VhcmNoXCIvXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
