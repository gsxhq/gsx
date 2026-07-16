<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Field(label string, attrs gsx.Attrs) {
	<div class="field">
		<label>{ label }</label>
		<input class="control" { attrs... }/>
	</div>
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
<form method="post" action="/login"><div class="field"><label>Email</label><input class="control" type="email" name="email" required/></div><div class="field"><label>Password</label><input class="control" type="password" name="password" required/></div><button type="submit">Sign in</button></form>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBGaWVsZChsYWJlbCBzdHJpbmcsIGF0dHJzIGdzeC5BdHRycykge1xuXHRcdTAwM2NkaXYgY2xhc3M9XCJmaWVsZFwiXHUwMDNlXG5cdFx0XHUwMDNjbGFiZWxcdTAwM2V7IGxhYmVsIH1cdTAwM2MvbGFiZWxcdTAwM2Vcblx0XHRcdTAwM2NpbnB1dCBjbGFzcz1cImNvbnRyb2xcIiB7IGF0dHJzLi4uIH0vXHUwMDNlXG5cdFx1MDAzYy9kaXZcdTAwM2Vcbn1cblxuY29tcG9uZW50IExvZ2luRm9ybSgpIHtcblx0XHUwMDNjZm9ybSBtZXRob2Q9XCJwb3N0XCIgYWN0aW9uPVwiL2xvZ2luXCJcdTAwM2Vcblx0XHRcdTAwM2NGaWVsZCBsYWJlbD1cIkVtYWlsXCIgdHlwZT1cImVtYWlsXCIgbmFtZT1cImVtYWlsXCIgcmVxdWlyZWQvXHUwMDNlXG5cdFx0XHUwMDNjRmllbGQgbGFiZWw9XCJQYXNzd29yZFwiIHR5cGU9XCJwYXNzd29yZFwiIG5hbWU9XCJwYXNzd29yZFwiIHJlcXVpcmVkL1x1MDAzZVxuXHRcdFx1MDAzY2J1dHRvbiB0eXBlPVwic3VibWl0XCJcdTAwM2VTaWduIGluXHUwMDNjL2J1dHRvblx1MDAzZVxuXHRcdTAwM2MvZm9ybVx1MDAzZVxufVxuIiwiaSI6IkxvZ2luRm9ybSgpIn0=)
