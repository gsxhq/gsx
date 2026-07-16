<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Panel(header gsx.Node, footer gsx.Node) {
	<div class="panel">
		<header>{ header }</header>
		<footer>{ footer }</footer>
	</div>
}

component Page() {
	<Panel header={ <h1>H</h1> } footer={ <>F</> }/>
}
```

Renders:

```html
<div class="panel"><header><h1>H</h1></header><footer>F</footer></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBQYW5lbChoZWFkZXIgZ3N4Lk5vZGUsIGZvb3RlciBnc3guTm9kZSkge1xuXHRcdTAwM2NkaXYgY2xhc3M9XCJwYW5lbFwiXHUwMDNlXG5cdFx0XHUwMDNjaGVhZGVyXHUwMDNleyBoZWFkZXIgfVx1MDAzYy9oZWFkZXJcdTAwM2Vcblx0XHRcdTAwM2Nmb290ZXJcdTAwM2V7IGZvb3RlciB9XHUwMDNjL2Zvb3Rlclx1MDAzZVxuXHRcdTAwM2MvZGl2XHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKCkge1xuXHRcdTAwM2NQYW5lbCBoZWFkZXI9eyBcdTAwM2NoMVx1MDAzZUhcdTAwM2MvaDFcdTAwM2UgfSBmb290ZXI9eyBcdTAwM2NcdTAwM2VGXHUwMDNjL1x1MDAzZSB9L1x1MDAzZVxufVxuIiwiaSI6IlBhZ2UoKSJ9)
