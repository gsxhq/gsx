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

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgSW5ib3gobmFtZSBzdHJpbmcsIGNvdW50IGludCkge1xuXHRcdTAwM2NzZWN0aW9uXHUwMDNlXG5cdFx0XHUwMDNjaDFcdTAwM2VIaSB7IG5hbWUgfVx1MDAzYy9oMVx1MDAzZVxuXHRcdHsgaWYgY291bnQgXHUwMDNlIDAge1xuXHRcdFx0XHUwMDNjcCBjbGFzcz1cImJhZGdlXCJcdTAwM2V7IGNvdW50IH0gbmV3XHUwMDNjL3BcdTAwM2Vcblx0XHR9IGVsc2Uge1xuXHRcdFx0XHUwMDNjcFx1MDAzZWFsbCBjYXVnaHQgdXBcdTAwM2MvcFx1MDAzZVxuXHRcdH0gfVxuXHRcdTAwM2Mvc2VjdGlvblx1MDAzZVxufVxuIiwiaSI6IkluYm94KFwiV29ybGRcIiwgMikifQ==)
