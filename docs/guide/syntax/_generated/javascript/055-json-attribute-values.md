<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component EntityFilter(entityType string, opts map[string]string) {
	<input
		type="checkbox"
		hx-post="/filter"
		hx-vals=js`{"entity_type": @{entityType}, "opts": @{opts}}`
	/>
}
```

Renders:

```html
<input type="checkbox" hx-post="/filter" hx-vals="{&#34;entity_type&#34;: &#34;opportunity&#34;, &#34;opts&#34;: {&#34;page&#34;:&#34;1&#34;}}"/>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgRW50aXR5RmlsdGVyKGVudGl0eVR5cGUgc3RyaW5nLCBvcHRzIG1hcFtzdHJpbmddc3RyaW5nKSB7XG5cdFx1MDAzY2lucHV0XG5cdFx0dHlwZT1cImNoZWNrYm94XCJcblx0XHRoeC1wb3N0PVwiL2ZpbHRlclwiXG5cdFx0aHgtdmFscz1qc2B7XCJlbnRpdHlfdHlwZVwiOiBAe2VudGl0eVR5cGV9LCBcIm9wdHNcIjogQHtvcHRzfX1gXG5cdC9cdTAwM2Vcbn1cbiIsImkiOiJFbnRpdHlGaWx0ZXIoXCJvcHBvcnR1bml0eVwiLCBtYXBbc3RyaW5nXXN0cmluZ3tcInBhZ2VcIjogXCIxXCJ9KSJ9)
