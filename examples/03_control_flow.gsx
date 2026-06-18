// 03_control_flow.gsx — if / for / switch, fragments & the {{ }} escape hatch
//
// Demonstrates:
//   - component X(…) { … } emission body — markup IS the result, no `return`
//   - { if … } / { else if … } / { else … } contributing children
//   - { for … } over slices, with index, and C-style
//   - { switch … } with cases
//   - nested control flow (for containing if)
//   - <>…</> fragments for multiple roots
//   - the `?` try-marker for (val, err) — implicit error propagation
//   - {{ stmt }} escape hatch — its remaining STRONG use cases
//
// The three brace forms are disambiguated by the leading token:
//   { expr }            -> interpolation (auto HTML-escaped)
//   { if|for|switch … } -> control flow (markup bodies become children)
//   {{ stmt }}          -> pure Go statements, no output (between markup siblings)
//
// A component has NO return type and NO `return`. If its body uses `?` (or a
// {{ }} that returns an error), it is generated with an implicit error return.

package examples

import (
	"context"
	"fmt"

	"github.com/gsxhq/gsx"
)

type Item struct {
	ID, Name string
	Active   bool
}

// if / else-if / else, each branch yielding markup.
component StatusHeading(autoImport bool, errorCount int) {
	<header>
		{ if autoImport && errorCount > 0 {
			<h2 class="text-red-500">Import Failed — Validation Errors</h2>
		} else if autoImport {
			<h2 class="text-emerald-500">Validation Passed — Importing</h2>
		} else {
			<h2 class="text-gray-800">Validation Complete</h2>
		} }
	</header>
}

// for-range, for-with-index, and nested if inside the loop.
component ItemList(items []Item) {
	<ul>
		{ for i, it := range items {
			<li class="item" data-index={fmt.Sprint(i)}>
				{ if it.Active {
					<strong>{it.Name}</strong>
				} else {
					{it.Name}
				} }
			</li>
		} }
	</ul>
}

// switch.
component Badge(kind string) {
	<span class="badge">
		{ switch kind {
		case "warning":
			<span class="text-amber-600">⚠ Warning</span>
		case "error":
			<span class="text-red-600">✕ Error</span>
		default:
			<span class="text-gray-600">Info</span>
		} }
	</span>
}

// Fragments group multiple roots without a wrapper element.
component Toasts(messages []string) {
	<>
		{ for _, m := range messages {
			<div class="toast">{m}</div>
		} }
	</>
}

// ─── (val, err) handling: the `?` try-marker vs. the {{ }} escape hatch ───────

// PREFERRED: the `?` try-marker unwraps a (T, error) call inline, using T and
// propagating err as this component's (implicit) error return. No pre-computing
// URLs up front, no manual `if err != nil { return … }`. Because the body uses
// `?`, this component is generated with an error return.
component RemoveFilterLink(page gsx.Node, paramName string) {
	<a class="filter">
		<span
			hx-get={ routeURL(ctx, page, map[string]string{paramName: ""})? }
			hx-push-url="true"
		>Remove {paramName}</span>
	</a>
}

// STILL VALID: the {{ }} escape hatch for the same (val, err) case. Use it when
// you need to inspect/transform the value or branch on the error before
// rendering, rather than straight-line propagation. `?` is the tidy default;
// {{ }} is the explicit form when you need the extra statements.
component RemoveFilterLinkExplicit(page gsx.Node, paramName string) {
	<a class="filter">
		{{
			removeURL, err := routeURL(ctx, page, map[string]string{paramName: ""})
			if err != nil {
				return err
			}
		}}
		<span hx-get={removeURL} hx-push-url="true">Remove {paramName}</span>
	</a>
}

// ─── {{ }} escape-hatch strong use cases (no `?` equivalent) ──────────────────

// STRONG USE CASE: a derived value reused by several following siblings —
// compute once, reference in multiple places without recomputation.
component UserChip(email, fullName string) {
	<div class="chip" data-email={email}>
		{{ initials := getInitials(fullName) }}
		<span class="avatar" aria-label={fullName}>{initials}</span>
		<span class="name">{fullName}</span>
		<span class="badge">{initials}</span>
	</div>
}

// STRONG USE CASE: loop-local computation that doesn't fit a single expression.
component Mentions(page gsx.Node, emails []string) {
	<div class="mentions">
		{ for i, email := range emails {
			{{ search := buildSearch(emails, i) }}
			<a href={search} data-email={email}>{email}</a>
		} }
	</div>
}

// Helpers (ordinary Go, would live in a normal .go file).
func getInitials(name string) string            { return name[:1] }
func buildSearch(emails []string, i int) string { return emails[i] }
func routeURL(ctx context.Context, page gsx.Node, q map[string]string) (string, error) {
	return "/x", nil
}
