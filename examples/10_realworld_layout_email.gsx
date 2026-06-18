// 10_realworld_layout_email.gsx — app-shell layout with slots, and an email
//
// Real-world patterns:
//   - a Layout component wrapping page content via implicit {children}, plus a
//     global overlay slot that survives HTMX swaps (lives outside the swap region)
//   - a sidebar nav rendered from grouped metadata with active-state classes,
//     using the composable `class={ a, "cls": cond, … }` list (no wrapper call)
//   - an HTML email built with inline styles and a sanitized download URL
//
// Demonstrates:
//   - component X(inline params) { … } — no return type, no `return` (emission)
//   - implicit {children} (referencing it adds a Children gsx.Node field)
//   - the composable `class` attribute: a comma list of contributions, each a
//     string / `"classes": cond` conditional / etc., flattened + merged (no wrapper call)
//   - nested loops over grouped data, type-safe URLs
//   - the SAME language renders both app chrome and emails; href is auto-sanitized

package examples

import (
	"context"
	"fmt"

	"github.com/gsxhq/gsx/examples/structpages"
)

// References `children` -> a Children gsx.Node field. Page content binds there.
component Layout(title string) {
	<>
		<!DOCTYPE html>
		<html lang="en">
			<head>
				<meta charset="UTF-8"/>
				<title>{title}</title>
			</head>
			<body hx-boost="true">
				<div id="global-progress"></div>
				<AppShell>
					// page content swaps here on navigation
					<div id="content">{children}</div>
				</AppShell>
				// overlay slot: dialogs fetch over HTMX and swap here, surviving
				// content reloads because it sits outside #content.
				<div id={structpages.ID(ctx, OverlayMount{})}></div>
			</body>
		</html>
	</>
}

type NavSection struct {
	Label string
	Items []NavItem
}

type NavItem struct {
	Label, Href string
	Active      bool
}

// References `children` -> Children gsx.Node field. The active-link classes use
// the composable `class` list: contributions split at depth 0, a `"classes": cond`
// entry emits its classes only when its condition holds, then the whole list is merged.
component AppShell() {
	<div class="flex min-h-full" x-data="{ sidebarOpen: false }">
		<aside class="w-60 shrink-0 border-r">
			<nav class="space-y-6 p-3">
				{ for _, sec := range groupedNav() {
					<div>
						<p class="px-2 text-xs uppercase text-gray-400">{sec.Label}</p>
						{ for _, item := range sec.Items {
							<a
								href={item.Href}
								class={
									"flex items-center gap-2 rounded-md px-2 py-1.5 text-sm",
									"bg-gray-100 text-blue-600": item.Active,
									"text-gray-700 hover:bg-gray-50": !item.Active,
								}
							>
								{item.Label}
							</a>
						} }
					</div>
				} }
			</nav>
		</aside>
		<main class="flex-1">{children}</main>
	</div>
}

// An HTML email — inline styles everywhere, sanitized download URL. Same gsx.
component ExportReadyEmail(userName string, appURL string, downloadToken string, recordCount int) {
	<html>
		<head><meta charset="UTF-8"/></head>
		<body style="font-family: system-ui; max-width: 600px; padding: 20px;">
			<div style="background: #f8f9fa; padding: 30px; margin-bottom: 20px;">
				<h2 style="color: #1f2937; margin: 0 0 20px;">Your Export is Ready</h2>
				<p>Hello {userName},</p>
				<p>
					Your export of
					<strong>{fmt.Sprintf("%d records", recordCount)}</strong>
					is ready to download.
				</p>
				<a
					href={appURL + "/datasync/download/" + downloadToken}
					style="display: inline-block; background: #3b82f6; color: white; padding: 12px 24px; border-radius: 6px; text-decoration: none;"
				>
					Download Export
				</a>
				<p style="font-size: 12px; color: #9ca3af;">Link expires in 24 hours.</p>
			</div>
		</body>
	</html>
}

type OverlayMount struct{}

func groupedNav() []NavSection {
	return []NavSection{
		{Label: "Main", Items: []NavItem{{Label: "Home", Href: "/", Active: true}}},
	}
}
