<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

**components.gsx**

```gsx
package views

import "github.com/gsxhq/gsx"

component Button(label string) {
	<button class="btn">{ label }</button>
}

component Card(title string, children gsx.Node) {
	<section class="card"><h2>{ title }</h2>{ children }</section>
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
<main>
	<section class="card"><h2>Dashboard</h2>
		<button class="btn">Save</button>
	</section>
</main>
```

[▶ Open in Playground](/playground#try=eyJzIjoiLS0gY29tcG9uZW50cy5nc3ggLS1cbnBhY2thZ2Ugdmlld3NcblxuaW1wb3J0IFwiZ2l0aHViLmNvbS9nc3hocS9nc3hcIlxuXG5jb21wb25lbnQgQnV0dG9uKGxhYmVsIHN0cmluZykge1xuXHRcdTAwM2NidXR0b24gY2xhc3M9XCJidG5cIlx1MDAzZXsgbGFiZWwgfVx1MDAzYy9idXR0b25cdTAwM2Vcbn1cblxuY29tcG9uZW50IENhcmQodGl0bGUgc3RyaW5nLCBjaGlsZHJlbiBnc3guTm9kZSkge1xuXHRcdTAwM2NzZWN0aW9uIGNsYXNzPVwiY2FyZFwiXHUwMDNlXHUwMDNjaDJcdTAwM2V7IHRpdGxlIH1cdTAwM2MvaDJcdTAwM2V7IGNoaWxkcmVuIH1cdTAwM2Mvc2VjdGlvblx1MDAzZVxufVxuLS0gcGFnZS5nc3ggLS1cbnBhY2thZ2Ugdmlld3NcblxudHlwZSBIb21lUGFnZSBzdHJ1Y3Qge1xuXHRUaXRsZSBzdHJpbmdcbn1cblxuY29tcG9uZW50IChwIEhvbWVQYWdlKSBSZW5kZXIoKSB7XG5cdTAwM2NtYWluXHUwMDNlXG5cdFx1MDAzY0NhcmQgdGl0bGU9e3AuVGl0bGV9XHUwMDNlXG5cdFx0XHUwMDNjQnV0dG9uIGxhYmVsPVwiU2F2ZVwiL1x1MDAzZVxuXHRcdTAwM2MvQ2FyZFx1MDAzZVxuXHUwMDNjL21haW5cdTAwM2Vcbn1cbiIsImkiOiIoSG9tZVBhZ2V7VGl0bGU6IFwiRGFzaGJvYXJkXCJ9KS5SZW5kZXIoKSJ9)
