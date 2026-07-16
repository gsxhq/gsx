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
<div><img src="/avatar.png" alt="User"/><br/><input type="text" required disabled/></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQXZhdGFyRmllbGQoc3JjIHN0cmluZywgYWx0IHN0cmluZywgZGlzYWJsZWQgYm9vbCkge1xuXHRcdTAwM2NkaXZcdTAwM2Vcblx0XHRcdTAwM2NpbWcgc3JjPXtzcmN9IGFsdD17YWx0fS9cdTAwM2Vcblx0XHRcdTAwM2Nici9cdTAwM2Vcblx0XHRcdTAwM2NpbnB1dCB0eXBlPVwidGV4dFwiIHJlcXVpcmVkIGRpc2FibGVkPXtkaXNhYmxlZH0vXHUwMDNlXG5cdFx1MDAzYy9kaXZcdTAwM2Vcbn1cbiIsImkiOiJBdmF0YXJGaWVsZChcIi9hdmF0YXIucG5nXCIsIFwiVXNlclwiLCB0cnVlKSJ9)
