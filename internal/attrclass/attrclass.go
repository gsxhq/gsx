// Package attrclass classifies HTML attribute names into security/escaping
// contexts (JS, URL, CSS, plain). The built-in set is the safety floor; users
// extend it additively via declarative Rules, wired through gen.Main. Rules are
// DECLARATIVE on purpose: classification must be fully enumerable, because the
// set has to travel to the spread leaf as data and has to be hashable into the
// codegen cache key. The same Classifier is consulted by the parser (JS facet,
// to split @{ } holes) and by codegen (all facets, for context-aware escaping).
package attrclass

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/gsxhq/gsx/internal/htmlattr"
)

// Context is the escaping context implied by an attribute name.
type Context int

const (
	CtxPlain Context = iota
	CtxJS
	CtxURL
	CtxCSS
)

// RuleSet matches attribute names three ways. Matching is case-insensitive
// (HTML attribute names fold), and the three are checked in the order below;
// they are alternatives, not a conjunction.
//
// Suffix exists because the convention it serves is real — a project that names
// every link attribute `*-url` (data-cancel-url, data-submit-url) cannot express
// that as a prefix.
type RuleSet struct {
	Names    []string `json:"names,omitempty"`
	Prefixes []string `json:"prefixes,omitempty"`
	Suffixes []string `json:"suffixes,omitempty"`
}

// Empty reports whether the set matches nothing.
func (r RuleSet) Empty() bool {
	return len(r.Names) == 0 && len(r.Prefixes) == 0 && len(r.Suffixes) == 0
}

// Valid rejects an entry that would match everything or nothing — an empty
// string as a name, prefix or suffix. A caller's typo should be a config error,
// not a rule that silently classifies every attribute as a URL.
func (r RuleSet) Valid() error {
	for _, group := range []struct {
		field string
		vals  []string
	}{{"names", r.Names}, {"prefixes", r.Prefixes}, {"suffixes", r.Suffixes}} {
		for _, v := range group.vals {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("attrclass: %s contains an empty entry", group.field)
			}
		}
	}
	return nil
}

// matches reports whether the already-lowercased lname matches this set.
func (r RuleSet) matches(lname string) bool {
	for _, n := range r.Names {
		if lname == strings.ToLower(n) {
			return true
		}
	}
	for _, p := range r.Prefixes {
		if strings.HasPrefix(lname, strings.ToLower(p)) {
			return true
		}
	}
	for _, sfx := range r.Suffixes {
		if strings.HasSuffix(lname, strings.ToLower(sfx)) {
			return true
		}
	}
	return false
}

// Merge returns the union of r and other, preserving r's entries first. Rules
// are additive, so merging is how a preset, a config file and a programmatic
// option compose over one another.
func (r RuleSet) Merge(other RuleSet) RuleSet {
	return RuleSet{
		Names:    append(append([]string(nil), r.Names...), other.Names...),
		Prefixes: append(append([]string(nil), r.Prefixes...), other.Prefixes...),
		Suffixes: append(append([]string(nil), r.Suffixes...), other.Suffixes...),
	}
}

// lowerSorted returns vals lowercased, deduplicated and sorted — the stable form
// generated code embeds, minus anything drop reports as already covered.
func lowerSorted(vals []string, drop func(string) bool) []string {
	if len(vals) == 0 {
		return nil
	}
	set := make(map[string]bool, len(vals))
	for _, v := range vals {
		lv := strings.ToLower(v)
		if drop != nil && drop(lv) {
			continue
		}
		set[lv] = true
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// Rules groups a project's classification rules by context. URLTags scopes URL
// rules to ONE element, which is how a project expresses what the built-in floor
// expresses for `content` on <meta>: a name that carries a URL on one element and
// nothing anywhere else. Only URL is tag-scopable — a JS or CSS attribute's
// meaning does not turn on its element.
type Rules struct {
	JS      RuleSet            `json:"js,omitzero"`
	URL     RuleSet            `json:"url,omitzero"`
	CSS     RuleSet            `json:"css,omitzero"`
	URLTags map[string]RuleSet `json:"urlTags,omitempty"`
}

// Valid checks every set, naming the context so a config error points at the
// offending table.
func (r Rules) Valid() error {
	for _, group := range []struct {
		ctx string
		set RuleSet
	}{{"url_attrs", r.URL}, {"js", r.JS}, {"css", r.CSS}} {
		if err := group.set.Valid(); err != nil {
			return fmt.Errorf("%s: %w", group.ctx, err)
		}
	}
	for tag, set := range r.URLTags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("url_attrs.tags: empty element name")
		}
		if err := set.Valid(); err != nil {
			return fmt.Errorf("url_attrs.tags.%s: %w", tag, err)
		}
	}
	return nil
}

// Classifier resolves an attribute name to a Context. Built-ins are the safety
// floor and are checked first; user rules are additive and can only widen it.
type Classifier struct {
	rules Rules
}

// Builtin returns a Classifier with only gsx's built-in classification — no user
// rules. Its decisions are identical to the historical
// attrjs.IsJSAttr + urlAttrs + style logic.
func Builtin() *Classifier { return &Classifier{} }

// New layers user rules over the built-ins.
func New(user Rules) *Classifier {
	return &Classifier{rules: user}
}

// Context classifies name on element tag. Priority (union semantics):
//  1. built-ins (safety floor), including the tag-scoped ones
//  2. user declarative rules (URL, then CSS, then JS — mirrors built-in order)
//
// tag may be empty for a caller with no element in hand; only the tag-scoped
// built-ins consult it, so every other answer is unaffected.
func (c *Classifier) Context(tag, name string) Context {
	ln := strings.ToLower(name)

	// 1. Built-ins, in the historical attrContext order: URL, CSS, JS.
	if htmlattr.BuiltinURL(tag, ln) {
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

	// 2. User declarative rules — the global sets, plus the URL rules this
	// element scopes.
	if c.rules.URL.matches(ln) || c.rules.URLTags[strings.ToLower(tag)].matches(ln) {
		return CtxURL
	}
	if c.rules.CSS.matches(ln) {
		return CtxCSS
	}
	if c.rules.JS.matches(ln) {
		return CtxJS
	}
	return CtxPlain
}

// Rules returns the user rules (built-ins excluded). Used to serialize the
// manifest delta; built-ins are compiled into every consumer.
func (c *Classifier) Rules() Rules {
	if c == nil {
		return Rules{}
	}
	return c.rules
}

// Fingerprint is a stable hash of the user rules. It feeds the codegen cache key
// so changing rules invalidates cached output. Because rules are declarative
// data, this hash covers the classifier COMPLETELY — there is no escape hatch
// whose behaviour could change without changing the fingerprint.
func (c *Classifier) Fingerprint() string {
	type fp struct {
		Rules Rules `json:"rules"`
	}
	b, _ := json.Marshal(fp{Rules: c.Rules()})
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

// UserURLRules returns the project's own URL rules as they apply to element
// tag: the global set merged with that element's scoped set, lowercased,
// deduplicated and sorted, with any name the built-in floor already claims
// dropped. This is the delta codegen ships to the Spread leaf; the floor itself
// lives in internal/htmlattr and is applied there, so it is never repeated in
// generated code and a project rule can only ADD a name, never move one to a
// laxer sink.
//
// Resolving the tag scope HERE, at generate time, is why the leaf needs no
// tag-matching machinery of its own: codegen knows the element.
func (c *Classifier) UserURLRules(tag string) RuleSet {
	if c == nil {
		return RuleSet{}
	}
	set := c.rules.URL.Merge(c.rules.URLTags[strings.ToLower(tag)])
	return RuleSet{
		Names:    lowerSorted(set.Names, func(n string) bool { return htmlattr.BuiltinURL(tag, n) }),
		Prefixes: lowerSorted(set.Prefixes, nil),
		Suffixes: lowerSorted(set.Suffixes, nil),
	}
}

// presets maps a named opt-in ruleset to the classification Rules it contributes.
// Presets compose additively over the built-in floor, exactly like user rules;
// they are enabled via gen.WithURLPreset / gsx.toml url_presets.
//
// "htmx": the five htmx method attributes as URL rules, matched by EXACT name.
// A "hx-" prefix would be wrong — it would also classify hx-swap/hx-target/
// hx-trigger (and every other hx-* attribute), none of which carry URLs.
var presets = map[string]Rules{
	"htmx": {URL: RuleSet{Names: []string{
		"hx-get", "hx-post", "hx-put", "hx-delete", "hx-patch",
	}}},
}

// Preset returns the classification Rules contributed by the named preset and
// true, or the zero Rules and false when no preset by that name exists. Callers
// (gen config, corpus harness) surface an unknown name as a clear config error.
func Preset(name string) (Rules, bool) {
	r, ok := presets[name]
	return r, ok
}

// PresetNames returns the known preset names, sorted — for listing valid choices
// in an unknown-preset config error.
func PresetNames() []string {
	out := make([]string, 0, len(presets))
	for n := range presets {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
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
