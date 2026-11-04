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
<div x-data="{ open: false }">
	<button @click="open = !open">Toggle</button>
	<div x-show="open" @click.outside="open = false">Contents...</div>
</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQWxwaW5lRHJvcGRvd24oKSB7XG5cdTAwM2NkaXYgeC1kYXRhPWpzYHsgb3BlbjogZmFsc2UgfWBcdTAwM2Vcblx0XHUwMDNjYnV0dG9uIEBjbGljaz1qc2BvcGVuID0gIW9wZW5gXHUwMDNlVG9nZ2xlXHUwMDNjL2J1dHRvblx1MDAzZVxuXHRcdTAwM2NkaXYgeC1zaG93PWpzYG9wZW5gIEBjbGljay5vdXRzaWRlPWpzYG9wZW4gPSBmYWxzZWBcdTAwM2VDb250ZW50cy4uLlx1MDAzYy9kaXZcdTAwM2Vcblx1MDAzYy9kaXZcdTAwM2Vcbn1cbiIsImkiOiJBbHBpbmVEcm9wZG93bigpIn0=)
