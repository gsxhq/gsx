// 11_struct_methods.gsx — method components for PAGE COMPOSITION (Style B)
//
// Method components are for app page composition + partial rendering (structpages),
// NOT for reusable component libraries. Reusable families (Card, Dialog, Table…)
// are PACKAGES of function components (Style A) — see 07_realworld_dialog.gsx
// (`package dialog`) and the component-styles design doc.
//
// Declared `component (recv T) Name(params) { … }`, invoked via a dotted tag whose
// left identifier is a local var/receiver: <p.Content/>. gsx tells <p.Content/>
// (method) from <ui.Button/> (package) by parsed scope — `p` is a local var, `ui`
// is an import. Key points:
//   - the RECEIVER STRUCT IS THE PAGE DATA (p.Field, built once in Go)
//   - `ctx` is ambient (never declared)
//   - method PARAMS (if any) → generated <Receiver><Method>Props; referenced by
//     bare name. Page data via p.Field; params via bare name.

package examples

import (
	"github.com/gsxhq/gsx/examples/structpages"
	"github.com/gsxhq/gsx/examples/ui"
)

// The page struct carries the route-scoped data — it IS the props.
type UsersPage struct {
	Title string
	Users []User
	Sort  string
}

// Page() wraps Content() in the shell. Nullary method → no props struct.
component (p UsersPage) Page() {
	<ui.AppShell title={p.Title}>
		<p.Content/>
	</ui.AppShell>
}

// Content() is a swappable HTMX partial, rendered from the receiver's data.
component (p UsersPage) Content() {
	<div
		id={ structpages.ID(ctx, UsersList{}) }
		hx-get={ structpages.URLFor(ctx, UsersList{})? }
		hx-trigger="ListChangedEvent from:body"
		hx-swap="outerHTML"
	>
		<h1>{p.Title}</h1>
		<p.Grid sort={p.Sort}/> {/* method calling a sibling method, with a param */}
	</div>
}

// A method WITH PARAMS: `sort` becomes a generated UsersPageGridProps{Sort},
// referenced by bare name. Page data (p.Users) still comes from the receiver.
component (p UsersPage) Grid(sort string) {
	<table class="w-full text-sm">
		<tbody>
			{ for _, u := range sortUsers(p.Users, sort) {
				<p.Row user={u}/>
			} }
		</tbody>
	</table>
}

// Per-row data passed as a param (→ UsersPageRowProps{User}).
component (p UsersPage) Row(user User) {
	<tr data-row-id={user.ID}>
		<td>{user.Email}</td>
		<td>{user.Role}</td>
	</tr>
}

// Pointer receiver works identically (<f.Field/> → (*EditForm).Field). Here the
// method mixes receiver data (f.Errors) with params (name, label).
type EditForm struct {
	Action string
	Errors map[string]string
}

component (f *EditForm) Field(name string, label string) {
	<div class="space-y-1">
		<label for={name}>{label}</label>
		<input id={name} name={name} { if f.Errors[name] != "" { aria-invalid="true" } }/>
		{ if msg := f.Errors[name]; msg != "" { <p class="text-red-500">{msg}</p> } }
	</div>
}

component (f *EditForm) View() {
	<form action={f.Action} method="post">
		<f.Field name="email" label="Email"/>
		<f.Field name="name" label="Full name"/>
		<ui.Button variant="primary">Save</ui.Button>
	</form>
}

func sortUsers(u []User, sort string) []User { return u }

type (
	User      struct{ ID, Email, Role string }
	UsersList struct{}
)
