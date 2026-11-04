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
	style={css`max-width:@{maxWidth}px`}
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
}" style="max-width:320px">
	<input x-model="search" placeholder="Search...">
	<ul>
		<template x-for="item in filteredItems" :key="item">
			<li x-text="item"></li>
		</template>
	</ul>
</div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQWxwaW5lU2VhcmNoKG1heFdpZHRoIGludCkge1xuXHUwMDNjZGl2XG5cdHgtZGF0YT1qc2B7XG5cdFx0c2VhcmNoOiAnJyxcblx0XHRpdGVtczogWydmb28nLCAnYmFyJywgJ2JheiddLFxuXHRcdGdldCBmaWx0ZXJlZEl0ZW1zKCkge1xuXHRcdFx0cmV0dXJuIHRoaXMuaXRlbXMuZmlsdGVyKGkgPVx1MDAzZSBpLnN0YXJ0c1dpdGgodGhpcy5zZWFyY2gpKVxuXHRcdH1cblx0fWBcblx0c3R5bGU9e2Nzc2BtYXgtd2lkdGg6QHttYXhXaWR0aH1weGB9XG5cdTAwM2Vcblx0XHUwMDNjaW5wdXQgeC1tb2RlbD1qc2BzZWFyY2hgIHBsYWNlaG9sZGVyPVwiU2VhcmNoLi4uXCIvXHUwMDNlXG5cdFx1MDAzY3VsXHUwMDNlXG5cdFx0XHUwMDNjdGVtcGxhdGUgeC1mb3I9anNgaXRlbSBpbiBmaWx0ZXJlZEl0ZW1zYCA6a2V5PWpzYGl0ZW1gXHUwMDNlXG5cdFx0XHRcdTAwM2NsaSB4LXRleHQ9anNgaXRlbWBcdTAwM2VcdTAwM2MvbGlcdTAwM2Vcblx0XHRcdTAwM2MvdGVtcGxhdGVcdTAwM2Vcblx0XHUwMDNjL3VsXHUwMDNlXG5cdTAwM2MvZGl2XHUwMDNlXG59XG4iLCJpIjoiQWxwaW5lU2VhcmNoKDMyMCkifQ==)
