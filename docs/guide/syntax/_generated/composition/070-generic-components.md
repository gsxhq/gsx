<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Badge[T string | int](value T) {
	<span class="badge">{ value }</span>
}

component Page() {
	<Badge value={"new"}/>
	<Badge value={42}/>
}
```

Renders:

```html
<span class="badge">new</span><span class="badge">42</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQmFkZ2VbVCBzdHJpbmcgfCBpbnRdKHZhbHVlIFQpIHtcblx0XHUwMDNjc3BhbiBjbGFzcz1cImJhZGdlXCJcdTAwM2V7IHZhbHVlIH1cdTAwM2Mvc3Bhblx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZSgpIHtcblx0XHUwMDNjQmFkZ2UgdmFsdWU9e1wibmV3XCJ9L1x1MDAzZVxuXHRcdTAwM2NCYWRnZSB2YWx1ZT17NDJ9L1x1MDAzZVxufVxuIiwiaSI6IlBhZ2UoKSJ9)
