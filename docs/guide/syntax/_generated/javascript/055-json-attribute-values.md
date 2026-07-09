<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component EntityFilter(entityType string, opts map[string]string) {
	<input type="checkbox" hx-post="/filter" hx-vals=js`{"entity_type": @{entityType}, "opts": @{opts}}`/>
}
```

Renders:

```html
<input type="checkbox" hx-post="/filter" hx-vals="{&#34;entity_type&#34;: &#34;opportunity&#34;, &#34;opts&#34;: {&#34;page&#34;:&#34;1&#34;}}"/>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgRW50aXR5RmlsdGVyKGVudGl0eVR5cGUgc3RyaW5nLCBvcHRzIG1hcFtzdHJpbmddc3RyaW5nKSB7XG5cdFx1MDAzY2lucHV0IHR5cGU9XCJjaGVja2JveFwiIGh4LXBvc3Q9XCIvZmlsdGVyXCIgaHgtdmFscz1qc2B7XCJlbnRpdHlfdHlwZVwiOiBAe2VudGl0eVR5cGV9LCBcIm9wdHNcIjogQHtvcHRzfX1gL1x1MDAzZVxufVxuIiwiaSI6IkVudGl0eUZpbHRlcihFbnRpdHlGaWx0ZXJQcm9wc3tFbnRpdHlUeXBlOiBcIm9wcG9ydHVuaXR5XCIsIE9wdHM6IG1hcFtzdHJpbmddc3RyaW5ne1wicGFnZVwiOiBcIjFcIn19KSJ9)
