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
<div>
	<button @click="toggle()">Toggle</button>
	<script type="application/json" id="cfg">{"Env":"prod","Beta":true}</script>
</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIENvbmZpZyBzdHJ1Y3Qge1xuXHRFbnYgIHN0cmluZ1xuXHRCZXRhIGJvb2xcbn1cblxuY29tcG9uZW50IFdpZGdldChjZmcgQ29uZmlnKSB7XG5cdTAwM2NkaXZcdTAwM2Vcblx0XHUwMDNjYnV0dG9uIEBjbGljaz1qc2B0b2dnbGUoKWBcdTAwM2VUb2dnbGVcdTAwM2MvYnV0dG9uXHUwMDNlXG5cdFx1MDAzY3NjcmlwdCB0eXBlPVwiYXBwbGljYXRpb24vanNvblwiIGlkPVwiY2ZnXCJcdTAwM2VAeyBjZmcgfVx1MDAzYy9zY3JpcHRcdTAwM2Vcblx1MDAzYy9kaXZcdTAwM2Vcbn1cbiIsImkiOiJXaWRnZXQoQ29uZmlne0VudjogXCJwcm9kXCIsIEJldGE6IHRydWV9KSJ9)
