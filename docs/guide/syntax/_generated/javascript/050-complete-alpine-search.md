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
		}" style="max-width:320px"><input x-model="search" placeholder="Search..."/><ul><template x-for="item in filteredItems" :key="item"><li x-text="item"></li></template></ul></div>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgQWxwaW5lU2VhcmNoKG1heFdpZHRoIGludCkge1xuXHRcdTAwM2NkaXZcblx0XHR4LWRhdGE9anNge1xuXHRcdFx0c2VhcmNoOiAnJyxcblx0XHRcdGl0ZW1zOiBbJ2ZvbycsICdiYXInLCAnYmF6J10sXG5cdFx0XHRnZXQgZmlsdGVyZWRJdGVtcygpIHtcblx0XHRcdFx0cmV0dXJuIHRoaXMuaXRlbXMuZmlsdGVyKGkgPVx1MDAzZSBpLnN0YXJ0c1dpdGgodGhpcy5zZWFyY2gpKVxuXHRcdFx0fVxuXHRcdH1gXG5cdFx0c3R5bGU9e2Nzc2BtYXgtd2lkdGg6QHttYXhXaWR0aH1weGB9XG5cdFx1MDAzZVxuXHRcdFx1MDAzY2lucHV0IHgtbW9kZWw9anNgc2VhcmNoYCBwbGFjZWhvbGRlcj1cIlNlYXJjaC4uLlwiL1x1MDAzZVxuXHRcdFx1MDAzY3VsXHUwMDNlXG5cdFx0XHRcdTAwM2N0ZW1wbGF0ZSB4LWZvcj1qc2BpdGVtIGluIGZpbHRlcmVkSXRlbXNgIDprZXk9anNgaXRlbWBcdTAwM2Vcblx0XHRcdFx0XHUwMDNjbGkgeC10ZXh0PWpzYGl0ZW1gXHUwMDNlXHUwMDNjL2xpXHUwMDNlXG5cdFx0XHRcdTAwM2MvdGVtcGxhdGVcdTAwM2Vcblx0XHRcdTAwM2MvdWxcdTAwM2Vcblx0XHUwMDNjL2Rpdlx1MDAzZVxufVxuIiwiaSI6IkFscGluZVNlYXJjaChBbHBpbmVTZWFyY2hQcm9wc3tNYXhXaWR0aDogMzIwfSkifQ==)
