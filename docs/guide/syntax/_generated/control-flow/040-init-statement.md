<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

func loadUser(id string) (string, error) { return "User:" + id, nil }

component Profile(id string) {
	<div>{ if name, err := loadUser(id); err != nil {
		<span class="err">{ err.Error() }</span>
	} else {
		<span>{ name }</span>
	} }</div>
}
```

Renders:

```html
<div><span>User:42</span></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5mdW5jIGxvYWRVc2VyKGlkIHN0cmluZykgKHN0cmluZywgZXJyb3IpIHsgcmV0dXJuIFwiVXNlcjpcIiArIGlkLCBuaWwgfVxuXG5jb21wb25lbnQgUHJvZmlsZShpZCBzdHJpbmcpIHtcblx0XHUwMDNjZGl2XHUwMDNleyBpZiBuYW1lLCBlcnIgOj0gbG9hZFVzZXIoaWQpOyBlcnIgIT0gbmlsIHtcblx0XHRcdTAwM2NzcGFuIGNsYXNzPVwiZXJyXCJcdTAwM2V7IGVyci5FcnJvcigpIH1cdTAwM2Mvc3Bhblx1MDAzZVxuXHR9IGVsc2Uge1xuXHRcdFx1MDAzY3NwYW5cdTAwM2V7IG5hbWUgfVx1MDAzYy9zcGFuXHUwMDNlXG5cdH0gfVx1MDAzYy9kaXZcdTAwM2Vcbn1cbiIsImkiOiJQcm9maWxlKFwiNDJcIikifQ==)
