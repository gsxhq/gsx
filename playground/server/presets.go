package main

// presets mirror the playground's default examples in the frontend
// (gsxhq.github.io .vitepress/theme/GsxPlayground.vue). They are rendered into
// the response cache at startup (in the background) so the "default examples"
// every visitor loads are served instantly. Keep this list in sync with the
// frontend presets (the corpus cases under internal/corpus/testdata/cases/
// playground mirror them too).
var presets = []renderReq{
	{
		GSX: "package views\n\ncomponent Greeting(name string, count int) {\n\t<p>Hello, {name}! You have {count} messages.</p>\n}\n",
		Invoke: `Greeting(GreetingProps{Name: "World", Count: 3})`,
	},
	{
		GSX: "package views\n\ncomponent Inbox(name string, count int) {\n\t<section>\n\t\t<h1>Hi {name}</h1>\n\t\t{ if count > 0 {\n\t\t\t<p class=\"badge\">{count} new</p>\n\t\t} else {\n\t\t\t<p>all caught up</p>\n\t\t} }\n\t</section>\n}\n",
		Invoke: `Inbox(InboxProps{Name: "World", Count: 2})`,
	},
	{
		GSX: "package views\n\ncomponent Tag(label string, active bool) {\n\t<span class={ \"tag\", \"tag--active\": active }>\n\t\t{label}\n\t</span>\n}\n",
		Invoke: `Tag(TagProps{Label: "stable", Active: true})`,
	},
	{
		GSX: "package views\n\n// User input is HTML-escaped by construction — no XSS.\ncomponent Comment(body string) {\n\t<blockquote>{body}</blockquote>\n}\n",
		Invoke: `Comment(CommentProps{Body: "<script>alert(1)</script>"})`,
	},
	{
		GSX: "package views\n\ncomponent Card(title string) {\n\t<article class=\"card\">\n\t\t<h3>{title}</h3>\n\t\t<div class=\"card__body\">{children}</div>\n\t</article>\n}\n",
		Invoke: `Card(CardProps{Title: "Hello", Children: gsx.Raw("<em>composed</em>")})`,
	},
}

// seedPresets renders each preset once so it is warm in the response cache.
// Intended to run in a background goroutine after the server starts listening,
// so startup is not blocked by the (slow) first renders.
func (p *pool) seedPresets() {
	for _, pr := range presets {
		p.render(pr)
	}
}
