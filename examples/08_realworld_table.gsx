// 08_realworld_table.gsx — data table with column metadata, loops & selection
//
// Real-world pattern (both projects): a DataGrid driven by a []Column metadata
// slice, with sticky headers, per-row checkbox selection wired to Alpine state,
// and pagination.
//
// Demonstrates:
//   - component X(inline params) { … }  — templ-style decl, emission body
//     (NO return type, NO `return`; the markup IS the result)
//   - inline params -> generated XProps (gsx owns the field names)
//   - loops { for … } producing both headers and rows; conditional columns
//   - the composable `class` attribute: a comma-list of contributions
//     (strings + `"cls": cond` conditionals) flattened, joined, then run through the
//     configured class merger so conflicting Tailwind utilities resolve
//   - Alpine attribute strings (:class, :checked, @change) built in Go

package examples

import (
	"fmt"
)

type Column struct {
	Key, Label string
	Sortable   bool
	Sticky     bool
}

type Selection int

const (
	SelectionNone Selection = iota
	SelectionMulti
)

type User struct {
	ID, Email, Role string
}

// Inline params become UsersGridProps{Columns, Users, Selection}.
component UsersGrid(columns []Column, users []User, selection Selection) {
	// Alpine state object built in Go and injected as a string attribute.
	<div x-data="{ selected: {}, selectAll(v){ /* … */ } }">
		<table class="w-full text-sm">
			<thead>
				<tr>
					{ if selection == SelectionMulti {
						<th class="sticky top-0 w-8 bg-muted">
							<input type="checkbox" @change="selectAll($event.target.checked)"/>
						</th>
					} }
					{ for _, c := range columns {
						<th
							data-column={c.Key}
							class={
								"sticky top-0 z-10 bg-muted px-3 py-2 text-left font-medium",
								"left-0 z-[11]": c.Sticky,
							}
						>
							{ if c.Sortable {
								<button class="inline-flex items-center gap-1">{c.Label}</button>
							} else {
								{c.Label}
							} }
						</th>
					} }
				</tr>
			</thead>
			<tbody>
				{ for _, u := range users {
					<UsersRow user={u} selection={selection} columns={columns}/>
				} }
			</tbody>
		</table>
	</div>
}

component UsersRow(user User, selection Selection, columns []Column) {
	<tr
		data-row-id={user.ID}
		:class={ "{ 'bg-primary/10': selected['" + user.ID + "'] }" }
	>
		{ if selection == SelectionMulti {
			<td class="w-8">
				<input
					type="checkbox"
					:checked={ "!!selected['" + user.ID + "']" }
					@change={ "selected['" + user.ID + "'] = $event.target.checked" }
				/>
			</td>
		} }
		{ for _, c := range columns {
			<td class="px-3 py-2">{cellValue(user, c.Key)}</td>
		} }
	</tr>
}

// Pagination computed from a window function; negative index = ellipsis.
component Pagination(current int, total int, href func(int) string) {
	<nav class="flex gap-1" aria-label="Pagination">
		{ for _, ix := range pageWindow(current, total) {
			{ if ix < 0 {
				<span class="px-2">…</span>
			} else {
				<a
					href={href(ix)}
					class={ "px-3 py-1 rounded", "bg-primary text-white": ix == current }
				>
					{fmt.Sprintf("%d", ix)}
				</a>
			} }
		} }
	</nav>
}

func cellValue(u User, key string) string {
	switch key {
	case "email":
		return u.Email
	case "role":
		return u.Role
	default:
		return ""
	}
}

func pageWindow(current, total int) []int { return []int{1, 2, -1, total} }
