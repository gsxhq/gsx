<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "context"

type ctxKey struct{}

// userName reads an authenticated username from the context, or falls
// back to "guest" when the value is absent.
func userName(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return "guest"
}

component Greeting() {
	<p>Hello, { userName(ctx) }</p>
}
```

Renders:

```html
<p>Hello, guest</p>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJjb250ZXh0XCJcblxudHlwZSBjdHhLZXkgc3RydWN0e31cblxuLy8gdXNlck5hbWUgcmVhZHMgYW4gYXV0aGVudGljYXRlZCB1c2VybmFtZSBmcm9tIHRoZSBjb250ZXh0LCBvciBmYWxsc1xuLy8gYmFjayB0byBcImd1ZXN0XCIgd2hlbiB0aGUgdmFsdWUgaXMgYWJzZW50LlxuZnVuYyB1c2VyTmFtZShjdHggY29udGV4dC5Db250ZXh0KSBzdHJpbmcge1xuXHRpZiB2LCBvayA6PSBjdHguVmFsdWUoY3R4S2V5e30pLihzdHJpbmcpOyBvayB7XG5cdFx0cmV0dXJuIHZcblx0fVxuXHRyZXR1cm4gXCJndWVzdFwiXG59XG5cbmNvbXBvbmVudCBHcmVldGluZygpIHtcblx0XHUwMDNjcFx1MDAzZUhlbGxvLCB7IHVzZXJOYW1lKGN0eCkgfVx1MDAzYy9wXHUwMDNlXG59XG4iLCJpIjoiR3JlZXRpbmcoKSJ9)
