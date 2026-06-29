<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

type AppState struct {
	Tab  string
	Open bool
}

component Shell(state AppState) {
	<script>
		const app = @{ state };
	</script>
}
```

Renders:

```html
<script>
		const app = {"Tab":"settings","Open":true};
	</script>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG50eXBlIEFwcFN0YXRlIHN0cnVjdCB7XG5cdFRhYiAgc3RyaW5nXG5cdE9wZW4gYm9vbFxufVxuXG5jb21wb25lbnQgU2hlbGwoc3RhdGUgQXBwU3RhdGUpIHtcblx0XHUwMDNjc2NyaXB0XHUwMDNlXG5cdFx0Y29uc3QgYXBwID0gQHsgc3RhdGUgfTtcblx0XHUwMDNjL3NjcmlwdFx1MDAzZVxufVxuIiwiaSI6IlNoZWxsKEFwcFN0YXRle1RhYjogXCJzZXR0aW5nc1wiLCBPcGVuOiB0cnVlfSkifQ==)
