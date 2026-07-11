package gsx

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Attr is one ordered attribute pair. Value is rendered like any attribute value:
// a bool toggles a bare boolean attribute; anything else is stringified (toStr) and
// attribute-escaped.
type Attr struct {
	Key   string
	Value any
}

// Attrs is gsx's attribute bag: an insertion-ordered, duplicate-tolerant slice of
// pairs. It is the type of the implicit fallthrough bag, every declared bag prop, the
// {{ "k": v }} literal, and conditional-attr bags. Spread renders it in SLICE
// ORDER (no sort) so callers control attribute order (e.g. Datastar data-* directives);
// duplicate scalar keys are last-wins, matching JSX-style override order.
//
// Security contract: keys are HTML attribute NAMES emitted (after a validity check,
// see Spread) without entity-encoding — they must come from generated code or
// trusted developer input, never from untrusted strings. Values are HTML-attribute-
// escaped.
//
// URL sanitization happens at EVERY element spread `{ x... }` — top-level, derived (e.g.
// a.Without(...)), or nested inside a conditional-attr group — regardless of the bag's
// provenance (the implicit fallthrough bag, a byo component's declared Attrs field, a
// generated component's own named Attrs param, a local variable, a function call's
// result, or any other gsx.Attrs value). Generated code lowers every such spread to one
// Spread call that, in a single ordered walk, routes each URL-classified
// attribute name (built-in urlAttrs table + gsx.toml rules + gen.WithURLAttrs, resolved
// at generate time) through the same tag-aware sink a static attribute of that name would
// use (URLVal for navigational, URLImageVal for image resources) and writes everything
// else as a plain attribute-escaped value — see Spread for the full algorithm.
// A gsx.RawURL value is the author's per-value vouch and is passed through the sink
// verbatim; there is no other opt-out and no unsanitizing spread primitive — every spread
// goes through Spread. See composition.md §Precedence for the full
// forwarding-element rule.
type Attrs []Attr

// AttrMap is a map-form attribute bag for ergonomic Go literals; convert it to Attrs
// explicitly with ToAttrs before passing/spreading in templates. A map has no order, so
// ToAttrs sorts keys ascending to keep output deterministic.
type AttrMap map[string]any

// ToAttrs converts m to an ordered Attrs slice with keys sorted ascending.
func (m AttrMap) ToAttrs() Attrs {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(Attrs, 0, len(keys))
	for _, k := range keys {
		out = append(out, Attr{Key: k, Value: m[k]})
	}
	return out
}

// Class returns the bag's class string. DUPLICATE-KEY RULE: it AGGREGATES — the values
// of ALL "class" pairs are joined (space-separated, each trimmed), so no class is
// silently dropped. It does NOT merge/dedupe tokens; the single outer codegen-emitted
// class site applies the configured merger exactly once over this plus the root's parts.
func (a Attrs) Class() string {
	var out string
	for _, kv := range a {
		if kv.Key == "class" {
			out = joinAttrStrings("class", out, strings.TrimSpace(toStr(kv.Value)))
		}
	}
	return out
}

// Style returns the bag's style declaration. DUPLICATE-KEY RULE: AGGREGATES — the
// values of ALL "style" pairs are joined ("; "-separated).
func (a Attrs) Style() string {
	var out string
	for _, kv := range a {
		if kv.Key == "style" {
			out = joinAttrStrings("style", out, toStr(kv.Value))
		}
	}
	return out
}

// Get returns the value for key and whether it was present. DUPLICATE-KEY RULE: LAST
// occurrence wins, matching JSX-style override order.
func (a Attrs) Get(key string) (any, bool) {
	for i := len(a) - 1; i >= 0; i-- {
		if a[i].Key == key {
			return a[i].Value, true
		}
	}
	return nil, false
}

// Has reports whether key is present.
func (a Attrs) Has(key string) bool {
	_, ok := a.Get(key)
	return ok
}

// GetFold is Get with ASCII-case-insensitive key matching (last occurrence
// wins). key must already be lowercase. Exported for any caller — hand-written
// bag manipulation, tests — that needs to look up a bag key the same
// case-insensitive way a sanitizing sink does (a case-variant key like HREF
// must not smuggle an unsanitized value past it); Spread itself
// folds case via its own attrNameExcluded helper rather than calling GetFold.
func (a Attrs) GetFold(key string) (any, bool) {
	for i := len(a) - 1; i >= 0; i-- {
		if strings.EqualFold(a[i].Key, key) {
			return a[i].Value, true
		}
	}
	return nil, false
}

// Without returns a copy of a without ANY pair whose key is in keys (a is not mutated);
// the order of the rest is preserved. An empty result (or empty input) yields nil.
func (a Attrs) Without(keys ...string) Attrs {
	if len(a) == 0 {
		return nil
	}
	out := make(Attrs, 0, len(a))
	for _, kv := range a {
		if !slices.Contains(keys, kv.Key) {
			out = append(out, kv)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// WithoutFold is Without with ASCII-case-insensitive key matching: it drops any
// pair whose key case-folds to one of keys (which must already be lowercase),
// preserving the order of the rest. No generated code calls it — a forwarding
// element's URL-classified keys render in place via Spread rather
// than being extracted and dropped from the bag first — but it remains public
// API for hand-written bag manipulation that needs the same fold semantics.
func (a Attrs) WithoutFold(keys ...string) Attrs {
	return a.WithoutFunc(func(k string) bool {
		return slices.ContainsFunc(keys, func(want string) bool {
			return strings.EqualFold(k, want)
		})
	})
}

// WithoutFunc returns a copy of a dropping every pair whose key satisfies drop
// (a is not mutated); the order of the rest is preserved. An empty result (or
// empty input) yields nil.
func (a Attrs) WithoutFunc(drop func(key string) bool) Attrs {
	if len(a) == 0 {
		return nil
	}
	out := make(Attrs, 0, len(a))
	for _, kv := range a {
		if !drop(kv.Key) {
			out = append(out, kv)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// URLPrefixMatch reports whether key (ASCII-case-folded) begins with any of the
// prefixes, which must already be lowercase. It is the single source of truth
// for URL prefix-rule matching, used by Spread to route a prefix-matched
// bag key through the strict navigational URL sink.
func URLPrefixMatch(key string, prefixes []string) bool {
	lk := strings.ToLower(key)
	for _, p := range prefixes {
		if strings.HasPrefix(lk, p) {
			return true
		}
	}
	return false
}

// Take returns Get(key)'s last value and a copy of a without ALL occurrences of key.
func (a Attrs) Take(key string) (any, Attrs) {
	v, _ := a.Get(key)
	return v, a.Without(key)
}

// Merge returns a new bag combining a and other, preserving order. For each pair in
// other: a "class"/"style" value is CONCATENATED onto the first such pair already in
// the result (or appended if none). Any other key OVERWRITES the last existing
// occurrence in place and drops earlier duplicates, so the incoming bag wins under the
// last-wins scalar rule; absent keys append.
//
// Merge is for userland eager composition, where you want duplicates resolved
// immediately rather than at render time. Generated call sites use ConcatAttrs instead
// (one allocation, no eager scan) because Spread resolves duplicates at render
// time anyway; see ConcatAttrs for why the two are observably equivalent there.
func (a Attrs) Merge(other Attrs) Attrs {
	out := make(Attrs, len(a))
	copy(out, a)
	for _, kv := range other {
		if kv.Key == "class" || kv.Key == "style" {
			out = mergeClassStyleAttr(out, kv)
			continue
		}
		out = mergeScalarAttr(out, kv)
	}
	return out
}

// ConcatAttrs concatenates bags in order into one new bag, preserving every
// pair (duplicates included). It does NOT dedupe or class-merge: rendering
// resolves duplicates at the leaf (Spread is last-wins on scalar
// keys and aggregates class/style), and Get/Has are last-wins by contract — so
// concatenation is observably equivalent to eager Merge for every consumer
// of the documented Attrs semantics. Generated call sites use it instead of
// .Merge() chains (one allocation instead of one per link). nil segments are
// skipped; a zero-entry result is nil.
func ConcatAttrs(bags ...Attrs) Attrs {
	n := 0
	for _, b := range bags {
		n += len(b)
	}
	if n == 0 {
		return nil
	}
	out := make(Attrs, 0, n)
	for _, b := range bags {
		out = append(out, b...)
	}
	return out
}

// AttrsCond selects one of two attribute-bag thunks for a conditional component
// attribute: it calls and returns then() when cond is true, otherwise els(). The
// branches are THUNKS so the untaken branch is never evaluated — mirroring a real
// Go if/else, where the untaken block's expressions (e.g. u.Name when u == nil)
// never run. The thunks return (Attrs, error) so a branch body may hoist
// (T, error) values (e.g. a pipeline stage that can fail) and propagate the
// error; the generated call site unwraps it like any other (T, error) value.
// els may be nil (no else branch); an untaken or nil branch yields (nil, nil).
func AttrsCond(cond bool, then, els func() (Attrs, error)) (Attrs, error) {
	if cond {
		if then != nil {
			return then()
		}
	} else if els != nil {
		return els()
	}
	return nil, nil
}

// Spread is gsx's sole spread primitive: the single-pass writer for
// EVERY element spread `{ x... }`, in one ordered walk it renders the plain
// attributes AND routes every URL-classified key through its sanitizing sink.
// There is no separate unsanitizing spread — a bag's provenance (forwarding
// field, local variable, function result, derived via Without, nested in a
// conditional-attr group, …) never changes this. It replaces the older
// unrolled per-name GetFold extraction + prefix-matched URL pass + residual
// spread write that only covered the forwarding bag. Generated code emits
// exactly one call per element spread, after the class/style merge site.
//
// It walks a in slice order, honoring lastValidAttrIndexes (scalar last-wins) and
// validAttrName (structurally unsafe names dropped). excluded carries the names a
// FORCED root attr owns at this element (class/style — merged separately; static
// forced names — always; a post-spread conditional's names — only when its branch
// was taken, which is why codegen passes the runtime drop slice); such a key is
// SKIPPED so the owning site is the sole value. For each surviving key, matching
// case-insensitively (HTML attr names fold, so a smuggled HREF/SRC cannot slip
// past the sink):
//   - a name in imageNames → URLImageVal (image-resource sink; data:image/* ok).
//     Checked FIRST so a name that is both nav- and image-classified (e.g. src)
//     takes the image sink.
//   - a name in navNames, OR a key matching a URL prefix rule (URLPrefixMatch) →
//     URLVal (strict navigational sink; prefix rules are user rules, always strict).
//   - anything else → a plain attribute write (a non-excluded class/style key
//     aggregates via a.Class()/a.Style(); bool → BoolAttr; else key="value"
//     attribute-escaped).
//
// navNames, imageNames and prefixes must already be lowercase. A RawURL value is
// the author's vouch and is emitted verbatim (still attribute-escaped) by the URL
// sinks. URL keys render IN their bag position — not hoisted ahead of the residual
// as the old unrolled extraction did — so the bag's authored attribute order is
// preserved. ctx is reserved for forward-compatibility. The URL classification
// policy lives in these caller-supplied name sets, not in this method: a
// hand-written caller passing nil navNames/imageNames/prefixes gets NO URL
// sanitization at all (every key falls through to the plain-attribute write) —
// generated code always supplies the built-in + configured name sets, so only a
// caller bypassing codegen needs to worry about this.
func (gw *Writer) Spread(ctx context.Context, a Attrs, navNames, imageNames, prefixes, excluded []string) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	last := lastValidAttrIndexes(a)
	for i, kv := range a {
		if !validAttrName(kv.Key) || last[kv.Key] != i {
			continue
		}
		if attrNameExcluded(kv.Key, excluded) {
			continue // class/style/forced/dropVar owns this name
		}
		switch {
		case attrNameExcluded(kv.Key, imageNames):
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.URLImageVal(kv.Value)
			gw.writeStr(`"`)
		case attrNameExcluded(kv.Key, navNames) || URLPrefixMatch(kv.Key, prefixes):
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.URLVal(kv.Value)
			gw.writeStr(`"`)
		default:
			// A non-excluded class/style key aggregates over the whole bag
			// (a.Class()/a.Style()). The forwarding-residual caller excludes
			// class/style (owned by the merge site), so this only fires for a
			// standalone spread that carries them (e.g. a nested cond-attr spread).
			switch kv.Key {
			case "class":
				kv.Value = a.Class()
			case "style":
				kv.Value = a.Style()
			}
			if b, ok := kv.Value.(bool); ok {
				gw.BoolAttr(kv.Key, b)
				continue
			}
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.AttrValue(toStr(kv.Value))
			gw.writeStr(`"`)
		}
	}
}

// attrNameExcluded reports whether key matches any name in excluded, comparing
// ASCII-case-insensitively (HTML attribute names fold), so a force-owned name
// suppresses a case-variant bag key. Spread uses it both for the
// excluded (force-owned) set and, via the same fold, for its URL-classified
// name sets — the same fold semantics GetFold/WithoutFold expose publicly.
func attrNameExcluded(key string, excluded []string) bool {
	for _, e := range excluded {
		if strings.EqualFold(key, e) {
			return true
		}
	}
	return false
}

func mergeScalarAttr(out Attrs, kv Attr) Attrs {
	idx := -1
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Key == kv.Key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return append(out, kv)
	}
	out[idx].Value = kv.Value
	return removeAttrBefore(out, kv.Key, idx)
}

func mergeClassStyleAttr(out Attrs, kv Attr) Attrs {
	idx := -1
	for i := range out {
		if out[i].Key == kv.Key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return append(out, kv)
	}
	for i := idx + 1; i < len(out); i++ {
		if out[i].Key == kv.Key {
			out[idx].Value = joinAttrStrings(kv.Key, toStr(out[idx].Value), toStr(out[i].Value))
		}
	}
	out[idx].Value = joinAttrStrings(kv.Key, toStr(out[idx].Value), toStr(kv.Value))
	return removeAttrAfter(out, kv.Key, idx)
}

func removeAttrBefore(attrs Attrs, key string, keep int) Attrs {
	out := attrs[:0]
	for i, kv := range attrs {
		if kv.Key == key && i < keep {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func removeAttrAfter(attrs Attrs, key string, keep int) Attrs {
	out := attrs[:0]
	for i, kv := range attrs {
		if kv.Key == key && i > keep {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func lastValidAttrIndexes(attrs Attrs) map[string]int {
	last := make(map[string]int, len(attrs))
	for i, kv := range attrs {
		if validAttrName(kv.Key) {
			last[kv.Key] = i
		}
	}
	return last
}

// validAttrName reports whether k is a structurally safe HTML attribute name: non-empty
// and free of whitespace, control bytes, and the characters that could break out of the
// tag or the name (" ' < > = / &). Names like "hx-on::click", ":class", "@click.away",
// "data-x", and "_" pass.
func validAttrName(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c <= ' ' || c == 0x7f {
			return false
		}
		switch c {
		case '"', '\'', '<', '>', '=', '/', '&':
			return false
		}
	}
	return true
}

// toStr renders an attribute/class value to a string.
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		return strings.Join(t, " ")
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// joinAttrStrings concatenates two non-empty class/style values with the right
// separator (space for class, "; " for style).
func joinAttrStrings(key, a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	if key == "style" {
		return a + "; " + b
	}
	return a + " " + b
}
