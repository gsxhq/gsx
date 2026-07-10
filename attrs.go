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
// {{ "k": v }} literal, and conditional-attr bags. Spread renders it in SLICE ORDER
// (no sort) so callers control attribute order (e.g. Datastar data-* directives);
// duplicate scalar keys are last-wins, matching JSX-style override order.
//
// Security contract: keys are HTML attribute NAMES emitted (after a validity check,
// see Spread) without entity-encoding — they must come from generated code or trusted
// developer input, never from untrusted strings. Values are HTML-attribute-escaped but
// NOT URL-sanitized: a URL-typed attribute (href, src, action, formaction, …) carrying
// an untrusted value must be written with gw.URL, not passed through Spread.
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
// resolves duplicates at the leaf (Spread is last-wins on scalar keys and
// aggregates class/style), and Get/Has are last-wins by contract — so
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

// Spread renders the bag in SLICE ORDER (no sort), with duplicate scalar keys resolved
// last-wins. Duplicate class/style keys aggregate and emit once at their last source
// position. A bool value uses boolean-attribute semantics (true → bare attribute,
// false → omitted); everything else is written as key="value" with attribute escaping.
// A key that is not a structurally valid HTML attribute name (see validAttrName) is
// SKIPPED rather than emitted. Values are attribute-escaped but NOT URL-sanitized (see
// Attrs). ctx is reserved for
// forward-compatibility.
func (gw *Writer) Spread(ctx context.Context, a Attrs) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	last := lastValidAttrIndexes(a)
	for i, kv := range a {
		if !validAttrName(kv.Key) {
			continue // unsafe/invalid attribute name — drop it
		}
		if last[kv.Key] != i {
			continue
		}
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
