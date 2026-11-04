<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Counter(signals gsx.Attrs, children gsx.Node) {
	<button { signals... }>{ children }</button>
}

component Page() {
	<Counter
		signals={{ "data-signals": "{count:0}", "data-text": "$count", "data-on-click": js`$count++` }}
	>Count</Counter>
}
```

Renders:

```html
<button data-signals="{count:0}" data-text="$count" data-on-click="$count++">Count</button>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBDb3VudGVyKHNpZ25hbHMgZ3N4LkF0dHJzLCBjaGlsZHJlbiBnc3guTm9kZSkge1xuXHRcdTAwM2NidXR0b24geyBzaWduYWxzLi4uIH1cdTAwM2V7IGNoaWxkcmVuIH1cdTAwM2MvYnV0dG9uXHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKCkge1xuXHRcdTAwM2NDb3VudGVyXG5cdFx0c2lnbmFscz17eyBcImRhdGEtc2lnbmFsc1wiOiBcIntjb3VudDowfVwiLCBcImRhdGEtdGV4dFwiOiBcIiRjb3VudFwiLCBcImRhdGEtb24tY2xpY2tcIjoganNgJGNvdW50KytgIH19XG5cdFx1MDAzZUNvdW50XHUwMDNjL0NvdW50ZXJcdTAwM2Vcbn1cbiIsImkiOiJQYWdlKCkifQ==)
