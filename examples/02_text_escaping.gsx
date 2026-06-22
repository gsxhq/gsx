// 02_text_escaping.gsx — automatic context-aware escaping
//
// gsx escapes by CONTEXT automatically (like html/template), determined at codegen
// from where the value sits. You write the value; gsx picks the right escaper.
// Helpers are OPT-OUTS for trusted values, never required for safety.
//
// Demonstrates:
//   - { expr } in text         -> auto HTML-escaped
//   - { expr } in an attribute -> auto attribute-escaped
//   - href/src/action/…        -> auto URL-sanitized (no templ.URL-style wrapper!)
//   - opt-outs: gsx.Raw (trusted HTML), gsx.RawURL (trusted URL — skip scheme check)
//   - HTML entities pass through literally
//
// Corner cases: a string literal containing '<' must NOT be parsed as markup
// (it's inside a Go expression); escaped quotes inside attribute expressions.

package examples

import (
	"fmt"

	"github.com/gsxhq/gsx"
)

// Interpolation is auto-escaped: if name is `<script>`, it renders escaped.
component Greeting(name string, count int) {
	<p>
		Hello, {name}! You have {fmt.Sprint(count)} new messages.
		gsx allows {"strings"} to be included in sentences &nbsp; with entities.
	</p>
}

// gsx.Raw injects trusted HTML without escaping (e.g. sanitized rich text).
component Article(bodyHTML string) {
	<article class="prose">
		{gsx.Raw(bodyHTML)}
	</article>
}

// URL attributes are sanitized AUTOMATICALLY by context (e.g. a javascript: URL is
// neutralised) — no wrapper needed. gsx.RawURL is the explicit OPT-OUT for a URL
// you already trust (e.g. from a type-safe router): it skips the scheme check but
// is still attribute-escaped, so it can't break out of the quotes.
component Links(userURL, trustedURL string) {
	<div>
		<a href={userURL}>user-supplied link</a> {/* auto-sanitized */}
		<a href={gsx.RawURL(trustedURL)}>trusted link</a> {/* opt out of scheme check */}
		{/* A string literal containing '<' is a Go expression, never markup: */}
		<span title={"comparisons like a < b are safe here"}>tooltip</span>
	</div>
}

// JSON-valued attributes (HTMX hx-vals, Alpine x-data) via a helper.
component DataAttrs(entityType string) {
	<input
		x-bind="searchInput"
		hx-vals={gsx.JSON(map[string]string{"entity_type": entityType})}
		data-note={"text with \"quotes\" and <tags> stays literal"}
	/>
}

// <style> interpolation: dynamic values are CSS-value-filtered automatically;
// gsx.RawCSS opts out for author-controlled CSS.
// Note: the CSS filter rejects values containing '(' or '/' (so dynamic rgb(...)/calc(...)/url(...)
// collapse to a safe placeholder — use a string literal or gsx.RawCSS for those).
// The <style> static CSS is auto-minified at build time (source formatting / gsx fmt unaffected).
component ThemedCard(width int, accent string) {
	<style>
		.themed {
			width: @{ width }px;
			color: @{ accent };
		}
	</style>
}
