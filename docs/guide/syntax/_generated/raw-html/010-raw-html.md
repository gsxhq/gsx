<!-- GENERATED from examples/*.txtar by cmd/gsx-examples — do not edit. -->

```gsx
package views

import "github.com/gsxhq/gsx"

// ArticleBody renders pre-sanitized HTML from a CMS or Markdown converter.
// { gsx.Raw(html) } emits the string verbatim — no entity encoding.
component ArticleBody(html string) {
	<article>{ gsx.Raw(html) }</article>
}
```

Renders:

```html
<article><em>Hello</em> &amp; <strong>World</strong></article>
```

[▶ Open in Playground](/playground#try=eyJzIjoicGFja2FnZSB2aWV3c1xuXG5pbXBvcnQgXCJnaXRodWIuY29tL2dzeGhxL2dzeFwiXG5cbi8vIEFydGljbGVCb2R5IHJlbmRlcnMgcHJlLXNhbml0aXplZCBIVE1MIGZyb20gYSBDTVMgb3IgTWFya2Rvd24gY29udmVydGVyLlxuLy8geyBnc3guUmF3KGh0bWwpIH0gZW1pdHMgdGhlIHN0cmluZyB2ZXJiYXRpbSDigJQgbm8gZW50aXR5IGVuY29kaW5nLlxuY29tcG9uZW50IEFydGljbGVCb2R5KGh0bWwgc3RyaW5nKSB7XG5cdFx1MDAzY2FydGljbGVcdTAwM2V7IGdzeC5SYXcoaHRtbCkgfVx1MDAzYy9hcnRpY2xlXHUwMDNlXG59XG4iLCJpIjoiQXJ0aWNsZUJvZHkoQXJ0aWNsZUJvZHlQcm9wc3tIdG1sOiBcIlx1MDAzY2VtXHUwMDNlSGVsbG9cdTAwM2MvZW1cdTAwM2UgXHUwMDI2YW1wOyBcdTAwM2NzdHJvbmdcdTAwM2VXb3JsZFx1MDAzYy9zdHJvbmdcdTAwM2VcIn0pIn0=)
