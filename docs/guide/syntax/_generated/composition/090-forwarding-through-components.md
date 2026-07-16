<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

component Icon(name string, attrs gsx.Attrs) {
	<span class="icon" data-name={name} { attrs... }>i</span>
}

component SearchIcon(attrs gsx.Attrs) {
	<Icon name="search" class="w-5 h-5" { attrs... }/>
}

component Page() {
	<SearchIcon class="text-red" aria-label="Search"/>
}
```

Renders:

```html
<span class="icon w-5 h-5 text-red" data-name="search" aria-label="Search">i</span>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbmNvbXBvbmVudCBJY29uKG5hbWUgc3RyaW5nLCBhdHRycyBnc3guQXR0cnMpIHtcblx0XHUwMDNjc3BhbiBjbGFzcz1cImljb25cIiBkYXRhLW5hbWU9e25hbWV9IHsgYXR0cnMuLi4gfVx1MDAzZWlcdTAwM2Mvc3Bhblx1MDAzZVxufVxuXG5jb21wb25lbnQgU2VhcmNoSWNvbihhdHRycyBnc3guQXR0cnMpIHtcblx0XHUwMDNjSWNvbiBuYW1lPVwic2VhcmNoXCIgY2xhc3M9XCJ3LTUgaC01XCIgeyBhdHRycy4uLiB9L1x1MDAzZVxufVxuXG5jb21wb25lbnQgUGFnZSgpIHtcblx0XHUwMDNjU2VhcmNoSWNvbiBjbGFzcz1cInRleHQtcmVkXCIgYXJpYS1sYWJlbD1cIlNlYXJjaFwiL1x1MDAzZVxufVxuIiwiaSI6IlBhZ2UoKSJ9)
