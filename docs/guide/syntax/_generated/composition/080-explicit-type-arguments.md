<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

component Price[T int | float64](amount T, currency string) {
	<b>{ currency }{ amount }</b>
}

component Page() {
<Price amount={9.99} currency="$"/>
<Price amount={42} currency="€"/>
<Price[float64] amount={4} currency="£"/>
}
```

Renders:

```html
<b>$9.99</b>
<b>€42</b>
<b>£4</b>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5jb21wb25lbnQgUHJpY2VbVCBpbnQgfCBmbG9hdDY0XShhbW91bnQgVCwgY3VycmVuY3kgc3RyaW5nKSB7XG5cdFx1MDAzY2JcdTAwM2V7IGN1cnJlbmN5IH17IGFtb3VudCB9XHUwMDNjL2JcdTAwM2Vcbn1cblxuY29tcG9uZW50IFBhZ2UoKSB7XG5cdTAwM2NQcmljZSBhbW91bnQ9ezkuOTl9IGN1cnJlbmN5PVwiJFwiL1x1MDAzZVxuXHUwMDNjUHJpY2UgYW1vdW50PXs0Mn0gY3VycmVuY3k9XCLigqxcIi9cdTAwM2Vcblx1MDAzY1ByaWNlW2Zsb2F0NjRdIGFtb3VudD17NH0gY3VycmVuY3k9XCLCo1wiL1x1MDAzZVxufVxuIiwiaSI6IlBhZ2UoKSJ9)
