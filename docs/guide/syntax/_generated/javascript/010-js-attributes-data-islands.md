<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

type Config struct {
	Env  string
	Beta bool
}

component Widget(cfg Config) {
	<div>
		<button @click={gsx.RawJS("toggle()")}>Toggle</button>
		<script type="application/json" id="cfg">@{ cfg }</script>
	</div>
}
```

Renders:

```html
<div><button @click="toggle()">Toggle</button><script type="application/json" id="cfg">{"Env":"prod","Beta":true}</script></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbnR5cGUgQ29uZmlnIHN0cnVjdCB7XG5cdEVudiAgc3RyaW5nXG5cdEJldGEgYm9vbFxufVxuXG5jb21wb25lbnQgV2lkZ2V0KGNmZyBDb25maWcpIHtcblx0XHUwMDNjZGl2XHUwMDNlXG5cdFx0XHUwMDNjYnV0dG9uIEBjbGljaz17Z3N4LlJhd0pTKFwidG9nZ2xlKClcIil9XHUwMDNlVG9nZ2xlXHUwMDNjL2J1dHRvblx1MDAzZVxuXHRcdFx1MDAzY3NjcmlwdCB0eXBlPVwiYXBwbGljYXRpb24vanNvblwiIGlkPVwiY2ZnXCJcdTAwM2VAeyBjZmcgfVx1MDAzYy9zY3JpcHRcdTAwM2Vcblx0XHUwMDNjL2Rpdlx1MDAzZVxufVxuIiwiaSI6IldpZGdldChDb25maWd7RW52OiBcInByb2RcIiwgQmV0YTogdHJ1ZX0pIn0=)
