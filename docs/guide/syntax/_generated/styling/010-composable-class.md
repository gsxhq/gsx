<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Tag(label string, active bool) {
	<span class={ "tag", "tag--active": active }>
		{ label }
	</span>
}
```

Renders:

```html
<span class="tag tag--active">stable</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgVGFnKGxhYmVsIHN0cmluZywgYWN0aXZlIGJvb2wpIHtcblx0XHUwMDNjc3BhbiBjbGFzcz17IFwidGFnXCIsIFwidGFnLS1hY3RpdmVcIjogYWN0aXZlIH1cdTAwM2Vcblx0XHR7IGxhYmVsIH1cblx0XHUwMDNjL3NwYW5cdTAwM2Vcbn1cbiIsImkiOiJUYWcoXCJzdGFibGVcIiwgdHJ1ZSkifQ==)
