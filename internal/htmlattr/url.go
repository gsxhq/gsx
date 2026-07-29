package htmlattr

import "strings"

// URLSink names the sanitizer an attribute's value must leave through. It is a
// property of the (element, attribute-name) pair, not of the value, so both
// codegen and the Spread leaf can reach the same verdict from the same table —
// which is why the table lives here, beside the sanitizers it selects, rather
// than only in the generator. RendersBare is the same arrangement.
type URLSink int

const (
	// SinkNone means the attribute carries no URL: an ordinary
	// attribute-escaped value.
	SinkNone URLSink = iota
	// SinkNav is the default, navigational-strict sink: only the standard
	// http/https/mailto/tel allow-list; no data:. Covers href, action, script
	// src, iframe src, object data, media src, etc. → Writer.URL / URLVal.
	SinkNav
	// SinkImage is an image-rendering resource sink where data:image/* (raster
	// + svg) is safe: <img src>, <source src>, <input src>, <video poster>, and
	// the legacy background attribute. Browsers render these as inert images (SVG
	// in restricted mode), so no script executes. → URLImage / URLImageVal.
	SinkImage
	// SinkSrcset is a comma-separated image-candidate list, sanitized per
	// candidate as an image resource. → Srcset / SrcsetVal.
	SinkSrcset
	// SinkRefresh is the <meta> content sink: a compound value that may carry
	// a redirect URL after a `<seconds>;url=` prefix, sanitized in place. Like
	// SinkSrcset it is a URL sink over a COMPOUND value rather than a bare
	// URL. → RefreshContent / RefreshContentVal.
	SinkRefresh
)

// builtinURLAttrs is the URL-context attribute set that holds on ANY element —
// the safety floor. Keys are lowercase. The htmx method attributes are NOT here;
// they are an opt-in preset, so the floor stays pure-HTML and projects that
// never touch htmx don't sanitize hx-* by default.
var builtinURLAttrs = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true, "poster": true,
	"cite": true, "ping": true, "data": true, "background": true, "manifest": true,
	"xlink:href": true, "srcset": true, "imagesrcset": true,
}

// BuiltinURL reports whether name is a URL attribute on tag through the
// built-in floor — the element-independent set above, plus the names that carry
// a URL only on a particular element.
//
// `content` on <meta> is that whole tag-scoped set: with http-equiv="refresh"
// its value is `<seconds>;url=<URL>`, a navigation the page performs on its own.
// Membership deliberately does NOT depend on a sibling http-equiv. That is
// html/template's rule (transition.go: `c.element == elementMeta && attrName ==
// "content"`), and it is what makes the sink unevadable: reading http-equiv
// would miss every shape it cannot be seen in — a dynamic value, or one
// arriving through a spread bag. It also costs nothing, because
// refreshContentSanitize returns any value that is not a refresh directive
// unchanged, so an ordinary `name="description"` content passes through
// byte-for-byte.
func BuiltinURL(tag, name string) bool {
	ln := strings.ToLower(name)
	if builtinURLAttrs[ln] {
		return true
	}
	return ln == "content" && strings.EqualFold(tag, "meta")
}

// Sink returns the sink an attribute's value must leave through on tag,
// or SinkNone when the name carries no URL there. It consults ONLY the
// built-in floor; a name a project added through its own url_attrs rules is not
// known here and is routed by the caller (AttrSinks for the bag leaf).
//
// The image set is intentionally narrow and tag-specific: `src` is an image sink
// on <img>/<source>/<input> but strict on <script>/<iframe>/<embed>/<video>
// (where a data: URL is a live document or executable). `poster` is image-only
// on <video>. `background` (legacy) is an image sink on any tag.
func Sink(tag, name string) URLSink {
	lt := strings.ToLower(tag)
	ln := strings.ToLower(name)
	switch ln {
	case "srcset", "imagesrcset":
		return SinkSrcset
	case "content":
		if lt == "meta" {
			return SinkRefresh
		}
		return SinkNone
	case "src":
		switch lt {
		case "img", "source", "input":
			return SinkImage
		}
	case "poster":
		if lt == "video" {
			return SinkImage
		}
	case "background":
		return SinkImage
	}
	if builtinURLAttrs[ln] {
		return SinkNav
	}
	return SinkNone
}
