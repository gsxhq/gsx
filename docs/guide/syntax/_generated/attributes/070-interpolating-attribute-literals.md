<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Row(id string, n int) {
	<div data-key=`row-@{id}-@{n}` title=`Item @{id |> upper}`>Row</div>
}
```

Renders:

```html
<div data-key="row-a&amp;b-5" title="Item A&amp;B">Row</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgUm93KGlkIHN0cmluZywgbiBpbnQpIHtcblx0XHUwMDNjZGl2IGRhdGEta2V5PWByb3ctQHtpZH0tQHtufWAgdGl0bGU9YEl0ZW0gQHtpZCB8XHUwMDNlIHVwcGVyfWBcdTAwM2VSb3dcdTAwM2MvZGl2XHUwMDNlXG59XG4iLCJpIjoiUm93KFJvd1Byb3Bze0lkOiBcImFcdTAwMjZiXCIsIE46IDV9KSJ9)
