package views

import "github.com/gsxhq/gsx"

component Card(children gsx.Node, attrs gsx.Attrs) {
	<section class="px-4 py-2" { attrs... }>{ children }</section>
}
