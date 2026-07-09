<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Counter(signals gsx.Attrs) {
	<button { signals... }>{ children }</button>
}

component Page() {
	<Counter signals={{ "data-signals": "{count:0}", "data-text": "$count", "data-on-click": "$count++" }}>Count</Counter>
}
```

Renders:

```html
<button data-signals="{count:0}" data-text="$count" data-on-click="$count++">Count</button>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBDb3VudGVyKHNpZ25hbHMgZ3N4LkF0dHJzKSB7XG5cdFx1MDAzY2J1dHRvbiB7IHNpZ25hbHMuLi4gfVx1MDAzZXsgY2hpbGRyZW4gfVx1MDAzYy9idXR0b25cdTAwM2Vcbn1cblxuY29tcG9uZW50IFBhZ2UoKSB7XG5cdFx1MDAzY0NvdW50ZXIgc2lnbmFscz17eyBcImRhdGEtc2lnbmFsc1wiOiBcIntjb3VudDowfVwiLCBcImRhdGEtdGV4dFwiOiBcIiRjb3VudFwiLCBcImRhdGEtb24tY2xpY2tcIjogXCIkY291bnQrK1wiIH19XHUwMDNlQ291bnRcdTAwM2MvQ291bnRlclx1MDAzZVxufVxuIiwiaSI6IlBhZ2UoKSJ9)
