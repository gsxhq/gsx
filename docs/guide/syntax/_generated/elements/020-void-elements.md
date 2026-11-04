<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component AvatarField(src string, alt string, disabled bool) {
<div>
	<img src={src} alt={alt}/>
	<br/>
	<input type="text" required disabled={disabled}/>
</div>
}
```

Renders:

```html
<div>
	<img src="/avatar.png" alt="User">
	<br>
	<input type="text" required disabled>
</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQXZhdGFyRmllbGQoc3JjIHN0cmluZywgYWx0IHN0cmluZywgZGlzYWJsZWQgYm9vbCkge1xuXHUwMDNjZGl2XHUwMDNlXG5cdFx1MDAzY2ltZyBzcmM9e3NyY30gYWx0PXthbHR9L1x1MDAzZVxuXHRcdTAwM2Nici9cdTAwM2Vcblx0XHUwMDNjaW5wdXQgdHlwZT1cInRleHRcIiByZXF1aXJlZCBkaXNhYmxlZD17ZGlzYWJsZWR9L1x1MDAzZVxuXHUwMDNjL2Rpdlx1MDAzZVxufVxuIiwiaSI6IkF2YXRhckZpZWxkKFwiL2F2YXRhci5wbmdcIiwgXCJVc2VyXCIsIHRydWUpIn0=)
