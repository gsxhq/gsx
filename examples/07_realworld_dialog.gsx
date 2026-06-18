// 07_realworld_dialog.gsx — compound Dialog component family (from his-project)
//
// Real-world pattern: a design-system Dialog split into Content/Header/Body/
// Footer/Banner sub-components, each a `component` with inline params + children.
//
// Demonstrates:
//   - component X(inline params) { … }  — templ-style decl, emission body
//     (NO return type, NO `return`; the markup IS the result)
//   - inline params -> generated XProps; `class` param threads a caller override
//   - implicit {children} (referencing it adds a Children gsx.Node field)
//   - the special composable `class` attribute: a comma-list of contributions
//     (strings, `"cls": cond` conditionals, the caller's `class`) — the configured class merger
//     resolves conflicting Tailwind utilities; no twmerge.Merge wrapper needed
//   - boolean attrs are type-driven (data-state via a Go helper here for clarity)
//   - conditional children via { if … }, switch-on-variant, cross-package <icon.X/>

package dialog

import (
	"github.com/gsxhq/gsx/examples/icon"
)

type Variant int

const (
	VariantInfo Variant = iota
	VariantWarning
	VariantError
	VariantSuccess
)

// Inline params become ContentProps{Open, HideCloseButton, Class}. The body
// references `children`, so a Children gsx.Node field is added implicitly.
// `class` (the caller override) is listed last in the class list so it wins.
component Content(open bool, hideCloseButton bool, class string) {
	<dialog
		data-tui-dialog-content
		data-tui-dialog-open={boolStr(open)}
		class={
			"fixed left-1/2 top-1/2 z-50 m-0 flex max-h-[85dvh] -translate-x-1/2 -translate-y-1/2 flex-col gap-4 rounded-lg bg-white p-6",
			"transition-all duration-200",
			"data-[tui-dialog-open=false]:scale-95 data-[tui-dialog-open=true]:scale-100",
			class,
		}
	>
		{children}
		{ if !hideCloseButton {
			<button data-tui-dialog-close aria-label="Close" class="absolute right-4 top-4">
				<icon.X size="16"/>
			</button>
		} }
	</dialog>
}

// Header / Body / Footer share the same shape but different default classes.
// Each takes a `class` override (last in the list) and implicit {children}.
component Header(class string) {
	<div class={ "shrink-0", class }>{children}</div>
}

component Body(class string) {
	<div class={ "min-h-0 flex-1 overflow-y-auto", class }>{children}</div>
}

component Footer(class string) {
	<div class={ "flex shrink-0 justify-end gap-2", class }>{children}</div>
}

// Variant-driven banner. bannerVariantClass stays an ordinary Go helper and
// contributes one item to the composable class list.
component Banner(variant Variant, hideIcon bool, label string) {
	<div class={
		"-mx-6 -mt-6 mb-2 flex items-center gap-2 border-b px-6 py-3 text-sm font-medium",
		bannerVariantClass(variant),
	}>
		{ if !hideIcon { <BannerIcon variant={variant}/> } }
		<span>{label}</span>
	</div>
}

// switch-on-variant chooses the icon; wrapped in a fragment <>…</>.
component BannerIcon(variant Variant) {
	<>
		{ switch variant {
		case VariantError:
			<icon.XCircle size="16"/>
		case VariantWarning:
			<icon.AlertTriangle size="16"/>
		default:
			<icon.Info size="16"/>
		} }
	</>
}

// ── Usage: composing the family ──────────────────────────────────────────────

component DeleteConfirm(itemName string) {
	<Content open>
		<Banner variant={VariantWarning} label="This action cannot be undone"/>
		<Header><h2 class="text-lg font-semibold">Delete {itemName}?</h2></Header>
		<Body>
			<p>Are you sure you want to permanently delete <strong>{itemName}</strong>?</p>
		</Body>
		<Footer>
			<button data-tui-dialog-close>Cancel</button>
			<button class="bg-red-600 text-white" hx-delete="/items/123">Delete</button>
		</Footer>
	</Content>
}

func bannerVariantClass(v Variant) string {
	switch v {
	case VariantWarning:
		return "bg-amber-50 border-amber-200 text-amber-800"
	case VariantError:
		return "bg-red-50 border-red-200 text-red-800"
	case VariantSuccess:
		return "bg-emerald-50 border-emerald-200 text-emerald-800"
	default:
		return "bg-blue-50 border-blue-200 text-blue-800"
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
