<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Row(id string, n int) {
	<div data-key=f`row-@{id}-@{n}` title=f`Item @{id |> upper}`>Row</div>
}
```

Renders:

```html
<div data-key="row-a&amp;b-5" title="Item A&amp;B">Row</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgUm93KGlkIHN0cmluZywgbiBpbnQpIHtcblx0XHUwMDNjZGl2IGRhdGEta2V5PWZgcm93LUB7aWR9LUB7bn1gIHRpdGxlPWZgSXRlbSBAe2lkIHxcdTAwM2UgdXBwZXJ9YFx1MDAzZVJvd1x1MDAzYy9kaXZcdTAwM2Vcbn1cbiIsImkiOiJSb3coXCJhXHUwMDI2YlwiLCA1KSJ9)
