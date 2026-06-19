// 09_realworld_form_htmx.gsx — forms, HTMX, type-safe URLs & native `?` errors
//
// Real-world pattern (both projects): a form composed of Field sub-components
// with conditional error display, HTMX-driven submission with smart targeting,
// and type-safe route URLs that return (string, error).
//
// Demonstrates:
//   - component X(inline params) { … } — no return type, no `return` (emission)
//   - native (T, error) via the `?` try-marker: `structpages.URLFor(ctx, X{})?`
//     unwraps the value and propagates err as the component's implicit error
//     return — no {{ action, err := …; if err != nil { return nil, err } }} dance
//   - implicit rest: undeclared call-site attrs (hx-get/hx-trigger/…) collect
//     into the component's Attrs because the body references `attrs`
//   - type-driven boolean attrs, conditional `{ if … { attr } }`, spread `{...attrs}`

package examples

import (
	"context"

	"github.com/gsxhq/gsx/examples/structpages"
)

// Inline params become FieldProps{Name, Label, Value, Error, Required}. The body
// references `attrs`, so an Attrs gsx.Attrs field is added implicitly: undeclared
// call-site attributes (hx-*, Alpine, …) collect there and forward to <input>.
component Field(name string, label string, value string, error string, required bool) {
	<div class="space-y-1" id={name + "-group"}>
		<label for={name} class="text-sm font-medium">
			{label}
			{ if required { <span aria-hidden="true" class="text-red-500"> *</span> } }
		</label>
		<input
			id={name}
			name={name}
			value={value}
			// conditional attribute + type-driven boolean + spread, all together
			{ if error != "" { aria-invalid="true" } }
			required={required}
			class="w-full rounded-md border px-3 py-2"
			{...attrs}
		/>
		{ if error != "" {
			<p class="text-sm text-red-500">{error}</p>
		} }
	</div>
}

// A form using type-safe route generation. structpages.URLFor returns
// (string, error); the `?` marker unwraps it inline and propagates the error as
// this component's implicit error return — no explicit (gsx.Node, error) needed.
component CreateUserForm() {
	<form
		hx-post={ structpages.URLFor(ctx, CreateUser{})? }
		hx-target={ structpages.IDTarget(ctx, UsersList{}) }
		hx-swap="beforeend"
		hx-on::after-request="if(event.detail.successful) this.reset()"
		class="space-y-4"
	>
		{/* hx-get/hx-trigger/hx-target are undeclared on Field -> they implicitly
		collect into Field's Attrs (implicit rest) and forward to its <input>. */}
		<Field
			name="email"
			label="Email"
			required
			hx-get={ structpages.URLFor(ctx, CheckEmail{})? }
			hx-trigger="change"
			hx-target="#email-status"
		/>
		<Field name="name" label="Full name"/>
		<div id="email-status"></div>
		<button type="submit" class="bg-blue-600 text-white">Create</button>
	</form>
}

// Removable filter badge: attrs forwarded via rest spread. References `attrs`,
// so the call site's hx-* / href etc. collect into Attrs and spread onto <a>.
component RemovableBadge(label string) {
	<a
		class="inline-flex items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-xs"
		{...attrs}
	>
		{label}
		<span aria-hidden="true">×</span>
	</a>
}

// Marker route types (would be defined by the app, used with structpages).
type (
	CreateUser struct{}
	UsersList  struct{}
	CheckEmail struct{}
)
