<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Tracker() {
	<script>
		const threshold = 5;
		if (count < threshold) { track("below"); }
	</script>
}
```

Renders:

```html
<script>const threshold = 5;
if (count < threshold) { track("below"); }</script>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgVHJhY2tlcigpIHtcblx0XHUwMDNjc2NyaXB0XHUwMDNlXG5cdFx0Y29uc3QgdGhyZXNob2xkID0gNTtcblx0XHRpZiAoY291bnQgXHUwMDNjIHRocmVzaG9sZCkgeyB0cmFjayhcImJlbG93XCIpOyB9XG5cdFx1MDAzYy9zY3JpcHRcdTAwM2Vcbn1cbiIsImkiOiJUcmFja2VyKCkifQ==)
