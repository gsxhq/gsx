<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

**components.gsx**

```gsx
package views

import "github.com/gsxhq/gsx"

component Button(label string) {
	<button class="btn">{ label }</button>
}

component Card(title string, children gsx.Node) {
	<section class="card">
		<h2>{ title }</h2>
		{ children }
	</section>
}
```

**page.gsx**

```gsx
package views

type HomePage struct {
	Title string
}

component (p HomePage) Render() {
	<main>
		<Card title={p.Title}>
			<Button label="Save"/>
		</Card>
	</main>
}
```

Renders:

```html
<main><section class="card"><h2>Dashboard</h2><button class="btn">Save</button></section></main>
```

[▶ Open in Playground](/playground#try=eyJzIjoiLS0gY29tcG9uZW50cy5nc3ggLS1cbnBhY2thZ2Ugdmlld3NcblxuaW1wb3J0IFwiZ2l0aHViLmNvbS9nc3hocS9nc3hcIlxuXG5jb21wb25lbnQgQnV0dG9uKGxhYmVsIHN0cmluZykge1xuXHRcdTAwM2NidXR0b24gY2xhc3M9XCJidG5cIlx1MDAzZXsgbGFiZWwgfVx1MDAzYy9idXR0b25cdTAwM2Vcbn1cblxuY29tcG9uZW50IENhcmQodGl0bGUgc3RyaW5nLCBjaGlsZHJlbiBnc3guTm9kZSkge1xuXHRcdTAwM2NzZWN0aW9uIGNsYXNzPVwiY2FyZFwiXHUwMDNlXG5cdFx0XHUwMDNjaDJcdTAwM2V7IHRpdGxlIH1cdTAwM2MvaDJcdTAwM2Vcblx0XHR7IGNoaWxkcmVuIH1cblx0XHUwMDNjL3NlY3Rpb25cdTAwM2Vcbn1cbi0tIHBhZ2UuZ3N4IC0tXG5wYWNrYWdlIHZpZXdzXG5cbnR5cGUgSG9tZVBhZ2Ugc3RydWN0IHtcblx0VGl0bGUgc3RyaW5nXG59XG5cbmNvbXBvbmVudCAocCBIb21lUGFnZSkgUmVuZGVyKCkge1xuXHRcdTAwM2NtYWluXHUwMDNlXG5cdFx0XHUwMDNjQ2FyZCB0aXRsZT17cC5UaXRsZX1cdTAwM2Vcblx0XHRcdFx1MDAzY0J1dHRvbiBsYWJlbD1cIlNhdmVcIi9cdTAwM2Vcblx0XHRcdTAwM2MvQ2FyZFx1MDAzZVxuXHRcdTAwM2MvbWFpblx1MDAzZVxufVxuIiwiaSI6IihIb21lUGFnZXtUaXRsZTogXCJEYXNoYm9hcmRcIn0pLlJlbmRlcigpIn0=)
