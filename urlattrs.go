package gsx

import "github.com/gsxhq/gsx/internal/htmlattr"

// AttrSinks carries a project's OWN attribute-classification delta for a spread:
// the url_attrs rules from gsx.toml as name lists, plus any preset they enabled
// as a flag. The built-in
// floor and its tag scoping are not repeated here — Spread applies those itself
// from the table above — so a project that configures nothing passes the zero
// value and generated code stays free of the built-in name list.
//
// Nav/Image/Srcset/Refresh are sets of lowercase attribute names naming the sink
// they leave through. Prefixes and Suffixes match a name's start or end instead,
// and always take the strict navigational sink (a project rule never earns the
// image-sink allowance).
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
	Suffixes []string // name suffixes → URLVal (strict)
	// Presets are the url presets the project enabled. A preset is a predicate
	// compiled into the runtime (see URLPreset), so generated code carries only
	// its flag: no name list to serialize per spread site, and no drift between
	// what codegen classified statically and what the leaf sanitizes.
	Presets URLPreset
}

// URLPreset is a bit set of the named url presets (gsx.toml url_presets). Each
// flag selects a predicate over attribute names; a matching name takes the
// strict navigational sink, exactly like a project's own url_attrs rule. Flags
// are compile-time identities, so generated code cannot name a preset the
// runtime does not know.
type URLPreset uint8

const (
	// PresetHTMX is the "htmx" preset: htmlattr.HTMXURL — the htmx request-URL
	// attributes (hx-get/post/put/delete/patch, hx-query, hx-action) in every
	// spelling htmx 4 reads (plain, :inherited, :append, :inherited:append).
	PresetHTMX URLPreset = 1 << iota
)

// matches reports whether key is a URL attribute under any enabled preset.
func (p URLPreset) matches(key string) bool {
	return p&PresetHTMX != 0 && htmlattr.HTMXURL(key)
}

// sinkFor returns the sink for key on tag: the built-in floor first (it is the
// safety floor and a user rule must not be able to downgrade it), then the
// project's own delta, then its prefix rules.
func (s AttrSinks) sinkFor(tag, key string) htmlattr.URLSink {
	if sink := htmlattr.Sink(tag, key); sink != htmlattr.SinkNone {
		return sink
	}
	switch {
	case attrNameExcluded(key, s.Image):
		return htmlattr.SinkImage
	case attrNameExcluded(key, s.Srcset):
		return htmlattr.SinkSrcset
	case attrNameExcluded(key, s.Refresh):
		return htmlattr.SinkRefresh
	case attrNameExcluded(key, s.Nav) ||
		URLPrefixMatch(key, s.Prefixes) || URLSuffixMatch(key, s.Suffixes) ||
		s.Presets.matches(key):
		return htmlattr.SinkNav
	}
	return htmlattr.SinkNone
}
