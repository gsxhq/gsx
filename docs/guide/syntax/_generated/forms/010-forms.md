<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Field(label string, attrs gsx.Attrs) {
	<div class="field"><label>{ label }</label><input class="control" { attrs... }/></div>
}

component LoginForm() {
	<form method="post" action="/login">
		<Field label="Email" type="email" name="email" required/>
		<Field label="Password" type="password" name="password" required/>
		<button type="submit">Sign in</button>
	</form>
}
```

Renders:

```html
<form method="post" action="/login"><div class="field"><label>Email</label><input class="control" type="email" name="email" required></div><div class="field"><label>Password</label><input class="control" type="password" name="password" required></div><button type="submit">Sign in</button></form>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBGaWVsZChsYWJlbCBzdHJpbmcsIGF0dHJzIGdzeC5BdHRycykge1xuXHRcdTAwM2NkaXYgY2xhc3M9XCJmaWVsZFwiXHUwMDNlXHUwMDNjbGFiZWxcdTAwM2V7IGxhYmVsIH1cdTAwM2MvbGFiZWxcdTAwM2VcdTAwM2NpbnB1dCBjbGFzcz1cImNvbnRyb2xcIiB7IGF0dHJzLi4uIH0vXHUwMDNlXHUwMDNjL2Rpdlx1MDAzZVxufVxuXG5jb21wb25lbnQgTG9naW5Gb3JtKCkge1xuXHRcdTAwM2Nmb3JtIG1ldGhvZD1cInBvc3RcIiBhY3Rpb249XCIvbG9naW5cIlx1MDAzZVxuXHRcdFx1MDAzY0ZpZWxkIGxhYmVsPVwiRW1haWxcIiB0eXBlPVwiZW1haWxcIiBuYW1lPVwiZW1haWxcIiByZXF1aXJlZC9cdTAwM2Vcblx0XHRcdTAwM2NGaWVsZCBsYWJlbD1cIlBhc3N3b3JkXCIgdHlwZT1cInBhc3N3b3JkXCIgbmFtZT1cInBhc3N3b3JkXCIgcmVxdWlyZWQvXHUwMDNlXG5cdFx0XHUwMDNjYnV0dG9uIHR5cGU9XCJzdWJtaXRcIlx1MDAzZVNpZ24gaW5cdTAwM2MvYnV0dG9uXHUwMDNlXG5cdFx1MDAzYy9mb3JtXHUwMDNlXG59XG4iLCJpIjoiTG9naW5Gb3JtKCkifQ==)
