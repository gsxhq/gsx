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
<span class="tag tag--active">
	stable
</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgVGFnKGxhYmVsIHN0cmluZywgYWN0aXZlIGJvb2wpIHtcblx1MDAzY3NwYW4gY2xhc3M9eyBcInRhZ1wiLCBcInRhZy0tYWN0aXZlXCI6IGFjdGl2ZSB9XHUwMDNlXG5cdHsgbGFiZWwgfVxuXHUwMDNjL3NwYW5cdTAwM2Vcbn1cbiIsImkiOiJUYWcoXCJzdGFibGVcIiwgdHJ1ZSkifQ==)
