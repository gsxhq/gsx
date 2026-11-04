<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Counter(signals gsx.Attrs, children gsx.Node) {
	<button { signals... }>{ children }</button>
}

component Page() {
	<Counter signals={{ "data-signals": "{count:0}", "data-text": "$count", "data-on-click": js`$count++` }}>
		Count
	</Counter>
}
```

Renders:

```html
<button data-signals="{count:0}" data-text="$count" data-on-click="$count++">Count</button>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBDb3VudGVyKHNpZ25hbHMgZ3N4LkF0dHJzLCBjaGlsZHJlbiBnc3guTm9kZSkge1xuXHRcdTAwM2NidXR0b24geyBzaWduYWxzLi4uIH1cdTAwM2V7IGNoaWxkcmVuIH1cdTAwM2MvYnV0dG9uXHUwMDNlXG59XG5cbmNvbXBvbmVudCBQYWdlKCkge1xuXHRcdTAwM2NDb3VudGVyIHNpZ25hbHM9e3sgXCJkYXRhLXNpZ25hbHNcIjogXCJ7Y291bnQ6MH1cIiwgXCJkYXRhLXRleHRcIjogXCIkY291bnRcIiwgXCJkYXRhLW9uLWNsaWNrXCI6IGpzYCRjb3VudCsrYCB9fVx1MDAzZVxuXHRcdENvdW50XG5cdFx1MDAzYy9Db3VudGVyXHUwMDNlXG59XG4iLCJpIjoiUGFnZSgpIn0=)
