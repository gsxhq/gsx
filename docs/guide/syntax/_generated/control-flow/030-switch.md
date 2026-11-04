<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Badge(kind string) {
	<span>{ switch kind {
	case "warn":
		<b>warning</b>
	case "err":
		<b>error</b>
	default:
		<b>info</b>
	} }</span>
}
```

Renders:

```html
<span><b>warning</b></span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQmFkZ2Uoa2luZCBzdHJpbmcpIHtcblx0XHUwMDNjc3Bhblx1MDAzZXsgc3dpdGNoIGtpbmQge1xuXHRjYXNlIFwid2FyblwiOlxuXHRcdFx1MDAzY2JcdTAwM2V3YXJuaW5nXHUwMDNjL2JcdTAwM2Vcblx0Y2FzZSBcImVyclwiOlxuXHRcdFx1MDAzY2JcdTAwM2VlcnJvclx1MDAzYy9iXHUwMDNlXG5cdGRlZmF1bHQ6XG5cdFx0XHUwMDNjYlx1MDAzZWluZm9cdTAwM2MvYlx1MDAzZVxuXHR9IH1cdTAwM2Mvc3Bhblx1MDAzZVxufVxuIiwiaSI6IkJhZGdlKFwid2FyblwiKSJ9)
