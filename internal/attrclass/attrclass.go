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

	"github.com/gsxhq/gsx"
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
	if gsx.BuiltinURLAttr(tag, ln) {
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

// UserURLExactNames returns the project's own exact-Name URL rules — lowercased,
// deduplicated, sorted, and EXCLUDING anything already in the built-in floor.
// This is the delta codegen ships to the Spread leaf (gsx.AttrSinks); the floor
// itself lives in the runtime (gsx.URLAttrSink), so it is applied there and is
// never repeated in generated code. The deterministic sort keeps that output
// stable.
//
// A user rule can only ADD a name, never reclassify one: a rule naming a
// built-in is dropped here because the floor already claims it, with the sink
// the element dictates (an `src` rule cannot demote <img src> off the image
// sink). This is the same additive-only guarantee Context provides.
func (c *Classifier) UserURLExactNames() []string {
	if c == nil {
		return nil
	}
	set := make(map[string]bool, len(c.rules.URL))
	for _, r := range c.rules.URL {
		if r.Name == "" {
			continue
		}
		ln := strings.ToLower(r.Name)
		// "" asks only the element-independent floor, which is what "already
		// built in on every element" means.
		if gsx.BuiltinURLAttr("", ln) {
			continue
		}
		set[ln] = true
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

// presets maps a named opt-in ruleset to the classification Rules it contributes.
// Presets compose additively over the built-in floor, exactly like user rules;
// they are enabled via gen.WithURLPreset / gsx.toml url_presets.
//
// "htmx": the five htmx method attributes as URL rules, matched by EXACT name.
// A "hx-" prefix would be wrong — it would also classify hx-swap/hx-target/
// hx-trigger (and every other hx-* attribute), none of which carry URLs.
var presets = map[string]Rules{
	"htmx": {URL: []Rule{
		{Name: "hx-get"}, {Name: "hx-post"}, {Name: "hx-put"},
		{Name: "hx-delete"}, {Name: "hx-patch"},
	}},
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
