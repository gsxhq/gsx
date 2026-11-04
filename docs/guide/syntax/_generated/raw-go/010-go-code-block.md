<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component FullName(first string, last string) {
	<div>
		{{ full := first + " " + last }}
		<span>{ full }</span>
	</div>
}
```

Renders:

```html
<div><span>Ada Lovelace</span></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgRnVsbE5hbWUoZmlyc3Qgc3RyaW5nLCBsYXN0IHN0cmluZykge1xuXHRcdTAwM2NkaXZcdTAwM2Vcblx0XHR7eyBmdWxsIDo9IGZpcnN0ICsgXCIgXCIgKyBsYXN0IH19XG5cdFx0XHUwMDNjc3Bhblx1MDAzZXsgZnVsbCB9XHUwMDNjL3NwYW5cdTAwM2Vcblx0XHUwMDNjL2Rpdlx1MDAzZVxufVxuIiwiaSI6IkZ1bGxOYW1lKFwiQWRhXCIsIFwiTG92ZWxhY2VcIikifQ==)
