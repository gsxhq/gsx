// 05_attributes.gsx — the full attribute system
//
// Demonstrates:
//   - dynamic attrs:        name={ expr }
//   - boolean attrs:        bare `open`, and type-driven `disabled={ cond }`
//   - conditional attrs:    in-tag { if … } statements (form 1)
//   - spread attrs:         {...attrs} on elements (form 2)  — and the two combined
//   - implicit rest:        referencing `attrs` adds an `Attrs gsx.Attrs` field;
//                           undeclared call-site attrs collect into it
//   - implicit children:    referencing `children` adds a `Children gsx.Node` field
//   - class composition:    the composable `class={ a, b, … }` comma-list + `"cls": cond` sugar
//   - special attr names:   data-*, aria-*, @click, :class, hx-on::click, x-data, _
//
// Boolean rule: a bool-typed value renders bare when true, omitted when false.
//
// class/style are special composable attributes: their { } value is a
// comma-separated list of contributions (string, []string, `"cls": cond` conditional, …),
// flattened and joined. A pluggable merger post-processes the result — install a
// Tailwind-aware merger globally to resolve conflicting utilities; no per-call
// wrapper needed.

package examples

// Inline params become ButtonProps{ID, Class, Variant, Disabled}. The body
// references `attrs` (so an Attrs gsx.Attrs field is added — undeclared call-site
// attributes collect there) and `children` (so a Children gsx.Node field is added).
component Button(id string, class string, variant string, disabled bool) {
	<button
		type="button"
		// conditional attribute (form 1): only emitted when id is set
		{ if id != "" { id={id} } }
		// composable class list; the caller-provided `class` wins last, and the
		// installed merger resolves any conflicting Tailwind utilities.
		class={
			"inline-flex items-center rounded-md px-4 py-2 text-sm font-medium",
			variantClass(variant),
			"opacity-50 cursor-not-allowed": disabled,
			class,
		}
		// type-driven boolean attribute
		disabled={disabled}
		// spread the collected rest attrs (form 2)
		{...attrs}
	>
		{children}
	</button>
}

// Usage: `class`, `data-test`, `@click`, `hx-get` map to declared/implicit fields:
// `class` -> the Class param; the rest (data-test, hx-post, hx-target) are
// undeclared, so they collect into Attrs and forward to <button>.
component Toolbar() {
	<div class="toolbar">
		<Button
			variant="primary"
			class="w-full"
			data-test="save"
			hx-post="/save"
			hx-target="#form"
			disabled={false}
		>
			Save
		</Button>
	</div>
}

// The composable class list also serves simple conditional toggles — no wrapper,
// just the comma-separated contributions with `"cls": cond` for the conditional ones.
component NavLink(href string, isActive bool) {
	<a
		href={href}
		class={
			"group flex gap-x-3 rounded-md p-2 text-sm font-medium",
			"bg-gray-100 text-blue-600": isActive,
			"text-gray-700 hover:bg-gray-50": !isActive,
		}
	>
		Link
	</a>
}

// Framework attributes with punctuation in their names: Alpine (@, :, x-),
// HTMX (hx-, hx-on::), hyperscript (_). All lexed as opaque names, emitted as-is.
component InteractiveCard(targetID string) {
	<div
		x-data="{ open: false }"
		x-init="$watch('open', v => console.log(v))"
		@click.away="open = false"
		@keydown.escape.window="open = false"
		:class="open ? 'ring-2' : ''"
		hx-get="/api/items"
		hx-trigger="revealed"
		hx-on::after-request="this.classList.add('loaded')"
		_="on click toggle .hidden on #menu"
		data-target={targetID}
	>
		<button :aria-expanded="open" @click="open = !open">Menu</button>
	</div>
}

func variantClass(v string) string {
	switch v {
	case "primary":
		return "bg-blue-600 text-white hover:bg-blue-700"
	case "ghost":
		return "bg-transparent hover:bg-gray-100"
	default:
		return "bg-gray-200 text-gray-900"
	}
}
