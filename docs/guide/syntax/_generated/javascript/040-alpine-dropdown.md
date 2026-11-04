<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component AlpineDropdown() {
	<div x-data=js`{ open: false }`>
		<button @click=js`open = !open`>Toggle</button>
		<div x-show=js`open` @click.outside=js`open = false`>Contents...</div>
	</div>
}
```

Renders:

```html
<div x-data="{ open: false }"><button @click="open = !open">Toggle</button><div x-show="open" @click.outside="open = false">Contents...</div></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQWxwaW5lRHJvcGRvd24oKSB7XG5cdFx1MDAzY2RpdiB4LWRhdGE9anNgeyBvcGVuOiBmYWxzZSB9YFx1MDAzZVxuXHRcdFx1MDAzY2J1dHRvbiBAY2xpY2s9anNgb3BlbiA9ICFvcGVuYFx1MDAzZVRvZ2dsZVx1MDAzYy9idXR0b25cdTAwM2Vcblx0XHRcdTAwM2NkaXYgeC1zaG93PWpzYG9wZW5gIEBjbGljay5vdXRzaWRlPWpzYG9wZW4gPSBmYWxzZWBcdTAwM2VDb250ZW50cy4uLlx1MDAzYy9kaXZcdTAwM2Vcblx0XHUwMDNjL2Rpdlx1MDAzZVxufVxuIiwiaSI6IkFscGluZURyb3Bkb3duKCkifQ==)
