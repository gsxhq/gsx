// 12_children_attrs.gsx — children placement & attribute fallthrough, by example
//
// Rules demonstrated:
//   1. Attribute FALLTHROUGH (Vue-style): undeclared attributes auto-apply to the
//      component's SINGLE root element — no {...attrs}, no declaration. class/style
//      MERGE into the root's list; other attrs are added. Declaring a `class` prop
//      is no longer needed just to let callers add classes.
//   2. Explicit {...attrs}: reference it to place pass-through attrs yourself (e.g.
//      on a NON-root element). TOUCHING `attrs` AT ALL (spread OR method call)
//      disables auto-fallthrough — you take over placement (cf. Vue inheritAttrs).
//   5. `attrs` is a rich built-in gsx.Attrs: split/read/merge it directly. gsx
//      SHIPS these utilities so nobody hand-rolls classFromAttrs/hasAttr.
//   3. Ambiguous root (fragment / multiple roots): auto-fallthrough has no target,
//      so passing undeclared attrs without an explicit {...attrs} is a COMPILE
//      ERROR — never silently guessed.
//   4. {children}: explicit placement. Passing children to a component that never
//      places {children} is a COMPILE ERROR (the content would vanish).

package examples

import "github.com/gsxhq/gsx"

// ── 1. Auto-fallthrough: the common case ─────────────────────────────────────
// Button declares only `variant`. The caller's class/data-*/hx-*/@click are
// undeclared, so they fall through onto the single root <button>; `class` merges
// into the existing class list. No {...attrs} written anywhere.
component Button(variant string) {
	<button type="button" class={ "btn", variantClass(variant) }>
		{children}
	</button>
}

component Toolbar() {
	<div>
		{/* class -> merges to "btn btn-primary w-full"; data-test/hx-post/@click -> <button> */}
		<Button variant="primary" class="w-full" data-test="save" hx-post="/save" @click="go()">
			Save
		</Button>
	</div>
}

// ── 2. Explicit {...attrs}: redirect fallthrough to a non-root element ────────
// The root is the wrapper <div>, but pass-through attrs belong on the <input>.
// Referencing {...attrs} opts out of auto-fallthrough and places them by hand.
component Field(label string) {
	<div class="field">
		<label>{label}</label>
		<input class="control" {...attrs}/>{/* caller's name/value/hx-* land on <input>, not <div> */}
	</div>
}

component LoginForm() {
	<form>
		{/* label -> prop; name/required/hx-get are placed on the inner <input> via {...attrs} */}
		<Field label="Email" name="email" required hx-get="/check-email"/>
	</form>
}

// ── 3. Ambiguous root: fragment has no single target ─────────────────────────
// A fragment has multiple roots, so auto-fallthrough can't choose. You MUST place
// {...attrs} explicitly; otherwise it's a compile error.
component Stack() {
	<>
		<hr/>
		<section {...attrs}>{children}</section> {/* explicit target for fallthrough attrs */}
		<hr/>
	</>
}
// <Stack data-x="1"/>  with NO {...attrs} in the body  ->  COMPILE ERROR:
//   "attributes have no unambiguous root element; place {...attrs} explicitly"

// ── 4. Children misplacement is a compile error ──────────────────────────────
// Spinner never places {children}. Passing children would silently drop content,
// so gsx rejects it at compile time.
component Spinner(size string) {
	<svg class={ "animate-spin", size } viewBox="0 0 24 24"></svg>
}
// <Spinner size="h-5 w-5">oops</Spinner>  ->  COMPILE ERROR:
//   "Spinner does not accept children (its body never places {children})"

// ── 5. Splitting the attrs bag — gsx SHIPS the utilities ─────────────────────
// When auto-fallthrough isn't enough (route different attrs to different
// elements), `attrs` is a rich built-in gsx.Attrs. This is the templ
// `classFromAttrs` pattern (one-learning/ui/common_components.templ) — but the
// helpers are provided, not hand-written. `class` stays on the wrapper; the rest
// go to the <input>. (Touching `attrs` here disables auto-fallthrough.)
// NOTE: this file is PARSE-ONLY today — LabeledInput's `{{ rest := … }}` GoBlock
// declares a local the emitter does not yet track as used by the `{...rest}` spread
// (a pre-existing GoBlock-local limitation, orthogonal to fallthrough). The auto +
// manual fallthrough core it demonstrates is rendered end-to-end in codegen's
// TestExample12EndToEnd.
component LabeledInput(label string) {
	{{ rest := attrs.Without("class") }}
	<div class={ "field", attrs.Class() }>
		<label>{label}</label>
		<input class="control" {...rest}/>
	</div>
}

// Built-in gsx.Attrs methods (these REPLACE hand-rolled helpers like
// classFromAttrs / hasAttr / extractAttr):
//   attrs.Class()            -> merged class string (string / []string / class value)
//   attrs.Has(key) bool      -> presence            (replaces hasAttr)
//   attrs.Get(key) (any, ok) -> a single value
//   attrs.Without(keys...)   -> the bag minus those keys
//   attrs.Take(key)          -> (value, rest) in one step
//   attrs.Merge(other)       -> combine two bags

func variantClass(v string) string { return "btn-" + v }
