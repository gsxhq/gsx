// Package attrclass classifies HTML attribute names into security/escaping
// contexts (JS, URL, CSS, plain). The built-in set is the safety floor; users
// extend it additively via declarative Rules and an optional predicate, wired
// through gen.Main. The same Classifier is consulted by the parser (JS facet,
// to split @{ } holes) and by codegen (all facets, for context-aware escaping).
package attrclass

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Context is the escaping context implied by an attribute name.
type Context int

const (
	CtxPlain Context = iota
	CtxJS
	CtxURL
	CtxCSS
)

// Rule matches an attribute name by exact Name (case-insensitive) OR by Prefix.
// Exactly one field is set; the other is empty (see Valid).
type Rule struct {
	Name   string `json:"name,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

// Valid reports whether exactly one of Name/Prefix is set.
func (r Rule) Valid() error {
	switch {
	case r.Name != "" && r.Prefix != "":
		return fmt.Errorf("attrclass.Rule: set only one of Name/Prefix, got both (%q, %q)", r.Name, r.Prefix)
	case r.Name == "" && r.Prefix == "":
		return fmt.Errorf("attrclass.Rule: set exactly one of Name/Prefix, got neither")
	default:
		return nil
	}
}

// matches reports whether the already-lowercased lname matches this rule.
func (r Rule) matches(lname string) bool {
	if r.Name != "" {
		return lname == strings.ToLower(r.Name)
	}
	if r.Prefix != "" {
		return strings.HasPrefix(lname, strings.ToLower(r.Prefix))
	}
	return false
}

// Rules groups user-supplied classification rules by context.
type Rules struct {
	JS  []Rule `json:"js,omitempty"`
	URL []Rule `json:"url,omitempty"`
	CSS []Rule `json:"css,omitempty"`
}

// Classifier resolves an attribute name to a Context. Built-ins are the safety
// floor and are checked first; user rules and the predicate are additive.
type Classifier struct {
	rules     Rules
	predicate func(name string) (Context, bool)
}

// Builtin returns a Classifier with only gsx's built-in classification — no user
// rules, no predicate. Its decisions are identical to the historical
// attrjs.IsJSAttr + urlAttrs + style logic.
func Builtin() *Classifier { return &Classifier{} }

// New layers user rules and an optional predicate over the built-ins. predicate
// may be nil.
func New(user Rules, predicate func(name string) (Context, bool)) *Classifier {
	return &Classifier{rules: user, predicate: predicate}
}

// Context classifies name. Priority (union semantics):
//  1. built-ins (safety floor)
//  2. user declarative rules (URL, then CSS, then JS — mirrors built-in order)
//  3. user predicate (only for names no rule matched; CtxPlain results ignored)
func (c *Classifier) Context(name string) Context {
	ln := strings.ToLower(name)

	// 1. Built-ins, in the historical attrContext order: URL, CSS, JS.
	if builtinURL[ln] {
		return CtxURL
	}
	if ln == "style" {
		return CtxCSS
	}
	if builtinJS(ln) {
		return CtxJS
	}

	if c == nil {
		return CtxPlain
	}

	// 2. User declarative rules.
	for _, r := range c.rules.URL {
		if r.matches(ln) {
			return CtxURL
		}
	}
	for _, r := range c.rules.CSS {
		if r.matches(ln) {
			return CtxCSS
		}
	}
	for _, r := range c.rules.JS {
		if r.matches(ln) {
			return CtxJS
		}
	}

	// 3. Predicate escape hatch (receives the original name, not lowercased).
	if c.predicate != nil {
		if ctx, ok := c.predicate(name); ok && ctx != CtxPlain {
			return ctx
		}
	}
	return CtxPlain
}

// HasPredicate reports whether a predicate escape hatch is registered. The
// manifest records this so offline tools can warn that predicate-classified
// attributes are not available without a live build.
func (c *Classifier) HasPredicate() bool { return c != nil && c.predicate != nil }

// Rules returns the user rules (built-ins excluded). Used to serialize the
// manifest delta; built-ins are compiled into every consumer.
func (c *Classifier) Rules() Rules {
	if c == nil {
		return Rules{}
	}
	return c.rules
}

// Fingerprint is a stable hash of the user rules plus whether a predicate is
// present. It feeds the codegen cache key so changing rules invalidates cached
// output. NOTE: predicate *bodies* are not hashed (closures aren't inspectable),
// matching the existing treatment of WithCSSMinifier/WithJSMinifier — document
// that changing a predicate's logic requires `gsx clean --cache`.
func (c *Classifier) Fingerprint() string {
	type fp struct {
		Rules        Rules `json:"rules"`
		HasPredicate bool  `json:"hasPredicate"`
	}
	b, _ := json.Marshal(fp{Rules: c.Rules(), HasPredicate: c.HasPredicate()})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

// URLExactNames returns every exact-name URL-classified attribute — the
// built-in set unioned with the user's exact-Name URL rules — lowercased,
// deduplicated, and sorted. Codegen enumerates these into per-name
// Get-extraction blocks at forwarding elements so a URL attribute smuggled
// through a fallthrough bag is sanitized at the leaf; the deterministic sort
// keeps generated code stable.
func (c *Classifier) URLExactNames() []string {
	set := make(map[string]bool, len(builtinURL))
	for n := range builtinURL {
		set[n] = true
	}
	if c != nil {
		for _, r := range c.rules.URL {
			if r.Name != "" {
				set[strings.ToLower(r.Name)] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// URLPrefixes returns the user's URL prefix rules, lowercased, deduplicated and
// sorted. Prefix rules cannot be enumerated into Get blocks; codegen consults
// them with a runtime matcher in the residual spread, and only when this is
// non-empty. Built-ins contribute no prefixes.
func (c *Classifier) URLPrefixes() []string {
	if c == nil {
		return nil
	}
	var out []string
	for _, r := range c.rules.URL {
		if r.Prefix != "" {
			p := strings.ToLower(r.Prefix)
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return out
}

// builtinURL is the URL-context attribute set (ported verbatim from
// codegen.urlAttrs). Keys are lowercase.
var builtinURL = map[string]bool{
	"href": true, "src": true, "action": true, "formaction": true, "poster": true,
	"cite": true, "ping": true, "data": true, "background": true, "manifest": true,
	"xlink:href": true, "hx-get": true, "hx-post": true, "hx-put": true,
	"hx-delete": true, "hx-patch": true,
}

// SinkClass distinguishes URL attribute sinks that differ in what schemes are
// safe. It is only meaningful for attributes already classified CtxURL.
type SinkClass int

const (
	// SinkStrict is the default, navigational-strict sink: only the standard
	// http/https/mailto/tel allow-list; no data:. Covers href, action, script
	// src, iframe src, object data, media src, etc.
	SinkStrict SinkClass = iota
	// SinkImage is an image-rendering resource sink where data:image/* (raster +
	// svg) is safe: <img src>, <source src>, <input src>, <video poster>, and the
	// legacy background attribute. Browsers render these as inert images (SVG in
	// restricted mode), so no script executes.
	SinkImage
)

// URLSink classifies a tag+attribute pair (both matched case-insensitively) as
// an image-rendering resource sink or the strict default. The caller must have
// already established Context(name) == CtxURL; URLSink assumes it.
//
// The image set is intentionally narrow and tag-specific: `src` is an image
// sink on <img>/<source>/<input> but strict on <script>/<iframe>/<embed>/<video>
// (where a data: URL is a live document or executable). `poster` is image-only
// on <video>. `background` (legacy) is an image sink on any tag.
func URLSink(tag, name string) SinkClass {
	lt := strings.ToLower(tag)
	ln := strings.ToLower(name)
	switch ln {
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
	return SinkStrict
}

// builtinJS reports whether the lowercased attribute name n is a JS-context
// attribute. Ported verbatim from the historical attrjs.IsJSAttr (input is
// already lowercased by the caller).
func builtinJS(n string) bool {
	switch {
	case strings.HasPrefix(n, "@"): // Alpine @click shorthand for x-on:
		return true
	case strings.HasPrefix(n, "hx-on"): // HTMX hx-on:*
		return true
	case strings.HasPrefix(n, "on") && len(n) > 2 && n[2] >= 'a' && n[2] <= 'z': // onclick…
		return true
	case n == "x-data" || n == "x-init" || n == "x-show" || n == "x-if" || n == "x-effect":
		return true
	case strings.HasPrefix(n, "x-on:"): // Alpine x-on:click
		return true
	case strings.HasPrefix(n, ":") && n != ":": // Alpine :class / x-bind shorthand
		return true
	default:
		return false
	}
}
