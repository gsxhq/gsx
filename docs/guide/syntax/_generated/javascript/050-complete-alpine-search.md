<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component AlpineSearch(maxWidth int) {
	<div
		x-data=js`{
			search: '',
			items: ['foo', 'bar', 'baz'],
			get filteredItems() {
				return this.items.filter(i => i.startsWith(this.search))
			}
		}`
		style={ css`max-width:@{maxWidth}px` }
	>
		<input x-model=js`search` placeholder="Search..."/>
		<ul>
			<template x-for=js`item in filteredItems` :key=js`item`>
				<li x-text=js`item`></li>
			</template>
		</ul>
	</div>
}
```

Renders:

```html
<div x-data="{
			search: &#39;&#39;,
			items: [&#39;foo&#39;, &#39;bar&#39;, &#39;baz&#39;],
			get filteredItems() {
				return this.items.filter(i =&gt; i.startsWith(this.search))
			}
		}" style="max-width:320px"><input x-model="search" placeholder="Search..."/><ul><template x-for="item in filteredItems" :key="item"><li x-text="item"></li></template></ul></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQWxwaW5lU2VhcmNoKG1heFdpZHRoIGludCkge1xuXHRcdTAwM2NkaXZcblx0XHR4LWRhdGE9anNge1xuXHRcdFx0c2VhcmNoOiAnJyxcblx0XHRcdGl0ZW1zOiBbJ2ZvbycsICdiYXInLCAnYmF6J10sXG5cdFx0XHRnZXQgZmlsdGVyZWRJdGVtcygpIHtcblx0XHRcdFx0cmV0dXJuIHRoaXMuaXRlbXMuZmlsdGVyKGkgPVx1MDAzZSBpLnN0YXJ0c1dpdGgodGhpcy5zZWFyY2gpKVxuXHRcdFx0fVxuXHRcdH1gXG5cdFx0c3R5bGU9eyBjc3NgbWF4LXdpZHRoOkB7bWF4V2lkdGh9cHhgIH1cblx0XHUwMDNlXG5cdFx0XHUwMDNjaW5wdXQgeC1tb2RlbD1qc2BzZWFyY2hgIHBsYWNlaG9sZGVyPVwiU2VhcmNoLi4uXCIvXHUwMDNlXG5cdFx0XHUwMDNjdWxcdTAwM2Vcblx0XHRcdFx1MDAzY3RlbXBsYXRlIHgtZm9yPWpzYGl0ZW0gaW4gZmlsdGVyZWRJdGVtc2AgOmtleT1qc2BpdGVtYFx1MDAzZVxuXHRcdFx0XHRcdTAwM2NsaSB4LXRleHQ9anNgaXRlbWBcdTAwM2VcdTAwM2MvbGlcdTAwM2Vcblx0XHRcdFx1MDAzYy90ZW1wbGF0ZVx1MDAzZVxuXHRcdFx1MDAzYy91bFx1MDAzZVxuXHRcdTAwM2MvZGl2XHUwMDNlXG59XG4iLCJpIjoiQWxwaW5lU2VhcmNoKEFscGluZVNlYXJjaFByb3Bze01heFdpZHRoOiAzMjB9KSJ9)
