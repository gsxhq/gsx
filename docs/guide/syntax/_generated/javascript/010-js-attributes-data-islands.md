<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type Config struct {
	Env  string
	Beta bool
}

component Widget(cfg Config) {
	<div>
		<button @click=js`toggle()`>Toggle</button>
		<script type="application/json" id="cfg">@{ cfg }</script>
	</div>
}
```

Renders:

```html
<div><button @click="toggle()">Toggle</button><script type="application/json" id="cfg">{"Env":"prod","Beta":true}</script></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIENvbmZpZyBzdHJ1Y3Qge1xuXHRFbnYgIHN0cmluZ1xuXHRCZXRhIGJvb2xcbn1cblxuY29tcG9uZW50IFdpZGdldChjZmcgQ29uZmlnKSB7XG5cdFx1MDAzY2Rpdlx1MDAzZVxuXHRcdFx1MDAzY2J1dHRvbiBAY2xpY2s9anNgdG9nZ2xlKClgXHUwMDNlVG9nZ2xlXHUwMDNjL2J1dHRvblx1MDAzZVxuXHRcdFx1MDAzY3NjcmlwdCB0eXBlPVwiYXBwbGljYXRpb24vanNvblwiIGlkPVwiY2ZnXCJcdTAwM2VAeyBjZmcgfVx1MDAzYy9zY3JpcHRcdTAwM2Vcblx0XHUwMDNjL2Rpdlx1MDAzZVxufVxuIiwiaSI6IldpZGdldChDb25maWd7RW52OiBcInByb2RcIiwgQmV0YTogdHJ1ZX0pIn0=)
