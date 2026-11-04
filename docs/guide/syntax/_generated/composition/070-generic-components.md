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
<span class="badge">new</span>
<span class="badge">42</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQmFkZ2VbVCBzdHJpbmcgfCBpbnRdKHZhbHVlIFQpIHtcblx0XHUwMDNjc3BhbiBjbGFzcz1cImJhZGdlXCJcdTAwM2V7IHZhbHVlIH1cdTAwM2Mvc3Bhblx1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZSgpIHtcblx1MDAzY0JhZGdlIHZhbHVlPXtcIm5ld1wifS9cdTAwM2Vcblx1MDAzY0JhZGdlIHZhbHVlPXs0Mn0vXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
