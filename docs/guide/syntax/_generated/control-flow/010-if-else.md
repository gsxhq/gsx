<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Inbox(name string, count int) {
	<section>
		<h1>Hi { name }</h1>
		{ if count > 0 {
		<p class="badge">{ count } new</p>
	} else {
		<p>all caught up</p>
	} }
	</section>
}
```

Renders:

```html
<section><h1>Hi World</h1><p class="badge">2 new</p></section>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgSW5ib3gobmFtZSBzdHJpbmcsIGNvdW50IGludCkge1xuXHRcdTAwM2NzZWN0aW9uXHUwMDNlXG5cdFx0XHUwMDNjaDFcdTAwM2VIaSB7IG5hbWUgfVx1MDAzYy9oMVx1MDAzZVxuXHRcdHsgaWYgY291bnQgXHUwMDNlIDAge1xuXHRcdFx1MDAzY3AgY2xhc3M9XCJiYWRnZVwiXHUwMDNleyBjb3VudCB9IG5ld1x1MDAzYy9wXHUwMDNlXG5cdH0gZWxzZSB7XG5cdFx0XHUwMDNjcFx1MDAzZWFsbCBjYXVnaHQgdXBcdTAwM2MvcFx1MDAzZVxuXHR9IH1cblx0XHUwMDNjL3NlY3Rpb25cdTAwM2Vcbn1cbiIsImkiOiJJbmJveChcIldvcmxkXCIsIDIpIn0=)
