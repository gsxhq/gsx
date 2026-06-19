// 04_components.gsx — the `component` declaration, inline params, slots
//
// Demonstrates:
//   - component X(inline params) { … }  — templ-style decl, JSX-style body
//   - NO return type, NO `return` keyword (emission body)
//   - inline params -> generated XProps (gsx owns the field names)
//   - implicit {children}  (referencing it adds a Children gsx.Node field)
//   - implicit {attrs} / rest collection (referencing it adds Attrs gsx.Attrs)
//   - components that reference NEITHER -> extra children/attrs are skipped
//   - named slots = ordinary gsx.Node params, passed as attributes
//   - zero-param components, composition, cross-package <ui.Button/>
//
// Mapping reminder (no symbol resolver — generated code is plain Go):
//   <Card title="Hi" featured>…</Card>
//     -> Card(CardProps{Title:"Hi", Featured:true, Children:…})

package examples

import (
	"github.com/gsxhq/gsx"
	"github.com/gsxhq/gsx/examples/ui"
)

// Inline params become CardProps{Title, Featured}. The body references
// `children`, so a Children gsx.Node field is added implicitly.
component Card(title string, featured bool) {
	<section class={ "card", "card-featured": featured }>
		<h2>{title}</h2>
		{ if featured { <span class="badge">Featured</span> } }
		<div class="body">{children}</div>
	</section>
}

// Single root, no `{...attrs}` written: undeclared call-site attributes
// (class, data-*, hx-*) AUTO-FALL-THROUGH to the <div>; `class` merges. (See
// 12_children_attrs.gsx for fallthrough, override, and the ambiguity rules.)
component Box(padded bool) {
	<div class={ "box", "p-4": padded }>
		{children}
	</div>
}

// A self-contained icon: it never places {children}. Passing children to it is a
// COMPILE ERROR (content would vanish). Stray attrs fall through to the <svg>.
component Spinner(size string) {
	<svg class={ "animate-spin", size } viewBox="0 0 24 24"></svg>
}

// Named slots are plain gsx.Node params, passed as attributes. `children` is
// still implicit alongside them.
component Panel(header gsx.Node, footer gsx.Node) {
	<div class="panel">
		<div class="panel-head">{header}</div>
		<div class="panel-body">{children}</div>
		<div class="panel-foot">{footer}</div>
	</div>
}

// Composition: everything together. A component body is markup-only — any Go
// statements go in a {{ }} block (here, a derived heading), never bare.
component Dashboard(items []Item) {
	{{ heading := "Reports" }}
	<main>
		<Panel header={ <h1>{heading}</h1> } footer={ <small>v2</small> }>
			{/* nested markup -> the Panel's Children */}
			<Card title="Recent" featured>
				<ul>
					{ for _, it := range items { <li>{it.Name}</li> } }
				</ul>
			</Card>

			{/* Box collects class/data-* into its Attrs and forwards them */}
			<Box padded class="mt-4" data-test="recent">
				<Spinner size="h-5 w-5"/>
			</Box>

			{/* cross-package component: <ui.Button/> -> ui.Button(ui.ButtonProps{…}) */}
			<ui.Button variant="primary" size="lg">Save</ui.Button>
		</Panel>
	</main>
}

type Item struct{ ID, Name string }
