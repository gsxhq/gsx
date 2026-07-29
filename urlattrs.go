package gsx

import "strings"

// URLSink names the sanitizer an attribute's value must leave through. It is a
// property of the (element, attribute-name) pair, not of the value, so both
// codegen and the Spread leaf can reach the same verdict from the same table —
// which is why the table lives here, beside the sanitizers it selects, rather
// than only in the generator. BoolRendersBare is the same arrangement.
type URLSink int

const (
	// URLSinkNone means the attribute carries no URL: an ordinary
	// attribute-escaped value.
	URLSinkNone URLSink = iota
	// URLSinkNav is the default, navigational-strict sink: only the standard
	// http/https/mailto/tel allow-list; no data:. Covers href, action, script
	// src, iframe src, object data, media src, etc. → Writer.URL / URLVal.
	URLSinkNav
	// URLSinkImage is an image-rendering resource sink where data:image/* (raster
	// + svg) is safe: <img src>, <source src>, <input src>, <video poster>, and
	// the legacy background attribute. Browsers render these as inert images (SVG
	// in restricted mode), so no script executes. → URLImage / URLImageVal.
	URLSinkImage
	// URLSinkSrcset is a comma-separated image-candidate list, sanitized per
	// candidate as an image resource. → Srcset / SrcsetVal.
	URLSinkSrcset
	// URLSinkRefresh is the <meta> content sink: a compound value that may carry
	// a redirect URL after a `<seconds>;url=` prefix, sanitized in place. Like
	// URLSinkSrcset it is a URL sink over a COMPOUND value rather than a bare
	// URL. → RefreshContent / RefreshContentVal.
	URLSinkRefresh
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

// BuiltinURLAttr reports whether name is a URL attribute on tag through the
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
func BuiltinURLAttr(tag, name string) bool {
	ln := strings.ToLower(name)
	if builtinURLAttrs[ln] {
		return true
	}
	return ln == "content" && strings.EqualFold(tag, "meta")
}

// URLAttrSink returns the sink an attribute's value must leave through on tag,
// or URLSinkNone when the name carries no URL there. It consults ONLY the
// built-in floor; a name a project added through its own url_attrs rules is not
// known here and is routed by the caller (AttrSinks for the bag leaf).
//
// The image set is intentionally narrow and tag-specific: `src` is an image sink
// on <img>/<source>/<input> but strict on <script>/<iframe>/<embed>/<video>
// (where a data: URL is a live document or executable). `poster` is image-only
// on <video>. `background` (legacy) is an image sink on any tag.
func URLAttrSink(tag, name string) URLSink {
	lt := strings.ToLower(tag)
	ln := strings.ToLower(name)
	switch ln {
	case "srcset", "imagesrcset":
		return URLSinkSrcset
	case "content":
		if lt == "meta" {
			return URLSinkRefresh
		}
		return URLSinkNone
	case "src":
		switch lt {
		case "img", "source", "input":
			return URLSinkImage
		}
	case "poster":
		if lt == "video" {
			return URLSinkImage
		}
	case "background":
		return URLSinkImage
	}
	if builtinURLAttrs[ln] {
		return URLSinkNav
	}
	return URLSinkNone
}

// AttrSinks carries a project's OWN attribute-classification delta for a spread:
// the url_attrs rules from gsx.toml, plus any preset they enabled. The built-in
// floor and its tag scoping are not repeated here — Spread applies those itself
// from the table above — so a project that configures nothing passes the zero
// value and generated code stays free of the built-in name list.
//
// Every field is a set of lowercase attribute names naming the sink they leave
// through, except Prefixes, which are name PREFIXES and always take the strict
// navigational sink (a user rule never gets the image-sink allowance).
//
// The zero value adds nothing to the floor. A new sink is a new field: existing
// generated code, which writes only the fields it needs, keeps compiling and
// keeps rendering identically.
type AttrSinks struct {
	Nav      []string // → URLVal
	Image    []string // → URLImageVal
	Srcset   []string // → SrcsetVal
	Refresh  []string // → RefreshContentVal
	Prefixes []string // name prefixes → URLVal (strict)
}

// sinkFor returns the sink for key on tag: the built-in floor first (it is the
// safety floor and a user rule must not be able to downgrade it), then the
// project's own delta, then its prefix rules.
func (s AttrSinks) sinkFor(tag, key string) URLSink {
	if sink := URLAttrSink(tag, key); sink != URLSinkNone {
		return sink
	}
	switch {
	case attrNameExcluded(key, s.Image):
		return URLSinkImage
	case attrNameExcluded(key, s.Srcset):
		return URLSinkSrcset
	case attrNameExcluded(key, s.Refresh):
		return URLSinkRefresh
	case attrNameExcluded(key, s.Nav) || URLPrefixMatch(key, s.Prefixes):
		return URLSinkNav
	}
	return URLSinkNone
}
