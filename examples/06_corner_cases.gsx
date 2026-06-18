// 06_corner_cases.gsx — the hard parsing cases the generator MUST get right
//
// These are deliberately adversarial. Each block notes what the parser must do.
// All markup-producing declarations are `component`s (emission body: the markup
// IS the result, no return type, no `return`). Plain helpers that return non-
// markup values (e.g. getValue) stay ordinary `func`.
//
// The governing rule for markup-vs-Go-expression is POSITIONAL (the Babel rule):
// inside `{ … }`, a `<` only starts markup when it is in *expression-start*
// position and is followed by a tag-name letter, `/`, or `>`. Otherwise `<` is a
// Go operator.

package examples

import (
	"fmt"

	"github.com/gsxhq/gsx"
)

// CASE 1: comparison operators are NOT markup. `a < b && c > d` is a bool expr.
component Comparisons(a, b, c, d int) {
	<div
		data-cmp={ fmt.Sprint(a < b && c > d) }
	>
		{ if a < b {
			<span>less</span>
		} }
	</div>
}

// CASE 2: string/backtick/rune literals containing '<' '>' '{' '}' are opaque.
component StringsWithMarkup() {
	<div
		data-a={ "something with <tag> inside" }
		data-b={ `multiline
<div>not parsed as markup</div>` }
		data-c={ "text with \"quotes\" and <braces> {x}" }
	>
		{ "literal <div> in a Go string, rendered as text" }
	</div>
}

// CASE 3: nested braces in Go expressions (composite literals) must balance,
// without being mistaken for the markup `{ }` delimiters.
component NestedBraces() {
	<div
		data-map={ fmt.Sprint(map[string]int{"a": 1, "b": 2}) }
		style={ gsx.JSON(map[string]any{"color": "red", "n": 10}) }
	></div>
}

// CASE 4: an inline-component / markup attribute value distinguished from a Go
// expression. `child={ <div/> }` is markup; `value={ f() }` is a Go expression.
type WrapperProps struct {
	Child gsx.Node
	Value string
}

component InlineMarkupAttr() {
	<Wrapper
		child={ <div data-value={getValue()}>content</div> } // markup value
		value={ getValue() }                                  // Go expression
	/>
}

// CASE 5: ternary-looking Go is NOT supported (Go has no ?:) — make sure the
// parser doesn't get confused by `?` and `:` appearing in Alpine expressions
// that live inside *string* attributes.
component AlpineTernaryString(open bool) {
	<div :class="open ? 'block' : 'hidden'" data-open={fmt.Sprint(open)}></div>
}

// CASE 6: raw-text elements. <script> and <style> bodies are NOT markup; braces
// inside them are literal. { expr } interpolation is still allowed (JS/CSS-escaped).
component RawTextElements(nonce, accent string) {
	<head>
		<style>
			.box { color: {accent}; }
			.box:hover { color: #000; }
		</style>
		<script nonce={nonce}>
			const config = { retries: 3, nested: { ok: true } };
			console.log("a < b is fine here", config);
		</script>
	</head>
}

// CASE 7: a fragment returning multiple roots with mixed text and elements.
component MixedFragment(name string) {
	<>
		<h1>Hi {name}</h1>
		plain text between elements
		<p>more</p>
	</>
}

// getValue returns a plain string (not markup), so it stays an ordinary func.
func getValue() string { return "v" }
