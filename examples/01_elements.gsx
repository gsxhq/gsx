// 01_elements.gsx — basic HTML elements & attributes
//
// Demonstrates:
//   - component X(params) { … } — templ-style decl, emission body (markup IS
//     the result: no return type, no `return`)
//   - lowercase tags  -> raw HTML elements
//   - hyphenated tags -> raw HTML elements (web components / custom elements)
//   - static string attributes, void/self-closing elements
//   - DOCTYPE, HTML comments (pass-through), SVG, namespaced attributes
//
// Corner cases: self-closing void elements, hyphenated tag & attr names,
// xmlns/xlink namespaced attributes, full HTML document with <!DOCTYPE html>.

package examples

import "github.com/gsxhq/gsx"

// A full HTML document. DOCTYPE and comments pass through verbatim.
component Document(title string) {
	<>
		<!DOCTYPE html>
		<html lang="en">
			<head>
				<meta charset="UTF-8"/>
				<meta name="viewport" content="width=device-width, initial-scale=1"/>
				<link rel="stylesheet" href="/assets/app.css"/>
				<title>{title}</title>
				<!-- analytics injected at build time -->
			</head>
			<body>
				<main id="content" class="container"></main>
			</body>
		</html>
	</>
}

// Void elements may be written self-closing or bare; both are fine.
component VoidElements() {
	<div>
		<br/>
		<hr/>
		<img src="/logo.svg" alt="Logo"/>
		<input type="text" name="email"/>
	</div>
}

// Hyphenated tags are HTML elements (e.g. design-system web components).
component WebComponents() {
	<el-dialog open>
		<el-dialog-backdrop class="fixed inset-0 bg-gray-500/75"></el-dialog-backdrop>
		<turbo-frame id="messages">
			<div data-turbo-permanent="true">…</div>
		</turbo-frame>
	</el-dialog>
}

// Inline SVG with namespaced attributes (xmlns, viewBox, stroke).
component FlagIcon(class string) {
	<svg
		class={class}
		xmlns="http://www.w3.org/2000/svg"
		fill="none"
		viewBox="0 0 24 24"
		stroke-width="1.5"
		stroke="currentColor"
	>
		<path stroke-linecap="round" stroke-linejoin="round" d="M3 3v18h18"/>
	</svg>
}
