<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Field(label string) {
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

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgRmllbGQobGFiZWwgc3RyaW5nKSB7XG5cdFx1MDAzY2RpdiBjbGFzcz1cImZpZWxkXCJcdTAwM2Vcblx0XHRcdTAwM2NsYWJlbFx1MDAzZXsgbGFiZWwgfVx1MDAzYy9sYWJlbFx1MDAzZVxuXHRcdFx1MDAzY2lucHV0IGNsYXNzPVwiY29udHJvbFwiIHsgYXR0cnMuLi4gfS9cdTAwM2Vcblx0XHUwMDNjL2Rpdlx1MDAzZVxufVxuXG5jb21wb25lbnQgTG9naW5Gb3JtKCkge1xuXHRcdTAwM2Nmb3JtIG1ldGhvZD1cInBvc3RcIiBhY3Rpb249XCIvbG9naW5cIlx1MDAzZVxuXHRcdFx1MDAzY0ZpZWxkIGxhYmVsPVwiRW1haWxcIiB0eXBlPVwiZW1haWxcIiBuYW1lPVwiZW1haWxcIiByZXF1aXJlZC9cdTAwM2Vcblx0XHRcdTAwM2NGaWVsZCBsYWJlbD1cIlBhc3N3b3JkXCIgdHlwZT1cInBhc3N3b3JkXCIgbmFtZT1cInBhc3N3b3JkXCIgcmVxdWlyZWQvXHUwMDNlXG5cdFx0XHUwMDNjYnV0dG9uIHR5cGU9XCJzdWJtaXRcIlx1MDAzZVNpZ24gaW5cdTAwM2MvYnV0dG9uXHUwMDNlXG5cdFx1MDAzYy9mb3JtXHUwMDNlXG59XG4iLCJpIjoiTG9naW5Gb3JtKCkifQ==)
