<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Badge(kind string) {
	<span>
		{ switch kind {
		case "warn":
			<b>warning</b>
		case "err":
			<b>error</b>
		default:
			<b>info</b>
		} }
	</span>
}
```

Renders:

```html
<span><b>warning</b></span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQmFkZ2Uoa2luZCBzdHJpbmcpIHtcblx0XHUwMDNjc3Bhblx1MDAzZVxuXHRcdHsgc3dpdGNoIGtpbmQge1xuXHRcdGNhc2UgXCJ3YXJuXCI6XG5cdFx0XHRcdTAwM2NiXHUwMDNld2FybmluZ1x1MDAzYy9iXHUwMDNlXG5cdFx0Y2FzZSBcImVyclwiOlxuXHRcdFx0XHUwMDNjYlx1MDAzZWVycm9yXHUwMDNjL2JcdTAwM2Vcblx0XHRkZWZhdWx0OlxuXHRcdFx0XHUwMDNjYlx1MDAzZWluZm9cdTAwM2MvYlx1MDAzZVxuXHRcdH0gfVxuXHRcdTAwM2Mvc3Bhblx1MDAzZVxufVxuIiwiaSI6IkJhZGdlKFwid2FyblwiKSJ9)
