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
// (no sort) so callers control attribute order (e.g. Datastar data-* directives).
//
// Security contract: keys are HTML attribute NAMES emitted (after a validity check,
// see Spread) without entity-encoding — they must come from generated code or trusted
// developer input, never from untrusted strings. Values are HTML-attribute-escaped but
// NOT URL-sanitized: a URL-typed attribute (href, src, action, formaction, …) carrying
// an untrusted value must be written with gw.URL, not passed through Spread.
type Attrs []Attr

// AttrMap is the map form of an attribute bag. It is an ALIAS for map[string]any, so a
// bare map[string]any, a gsx.AttrMap{…} literal, and a map returned from user code are
// the same type — and gsx auto-converts any of them to Attrs (via AttrsFromMap) at a
// component bag-prop binding or an element spread. A map has no order, so the
// conversion SORTS keys, keeping map-sourced attributes deterministic.
type AttrMap = map[string]any

// AttrsFromMap converts a map bag to an ordered bag with keys sorted ascending. This is
// the implicit AttrMap→Attrs coercion gsx inserts at bag boundaries; it is exported for
// explicit use too. An empty/nil map yields a nil bag.
func AttrsFromMap(m map[string]any) Attrs {
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

// Get returns the value for key and whether it was present. DUPLICATE-KEY RULE: FIRST
// occurrence wins (matches browser "first attribute wins").
func (a Attrs) Get(key string) (any, bool) {
	for _, kv := range a {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return nil, false
}

// Has reports whether key is present (first-occurrence scan).
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

// Take returns Get(key)'s first value and a copy of a without ALL occurrences of key.
func (a Attrs) Take(key string) (any, Attrs) {
	v, _ := a.Get(key)
	return v, a.Without(key)
}

// Merge returns a new bag combining a and other, preserving order. For each pair in
// other: a "class"/"style" value is CONCATENATED onto the first such pair already in
// the result (or appended if none) — so a directly-spread merged bag never carries a
// duplicate class/style. Any other key OVERWRITES the first existing occurrence in
// place (other wins, position preserved), or is appended if absent.
func (a Attrs) Merge(other Attrs) Attrs {
	out := make(Attrs, len(a))
	copy(out, a)
	for _, kv := range other {
		idx := -1
		for i := range out {
			if out[i].Key == kv.Key {
				idx = i
				break
			}
		}
		switch {
		case idx < 0:
			out = append(out, kv)
		case kv.Key == "class" || kv.Key == "style":
			out[idx].Value = joinAttrStrings(kv.Key, toStr(out[idx].Value), toStr(kv.Value))
		default:
			out[idx].Value = kv.Value
		}
	}
	return out
}

// AttrsCond selects one of two attribute-bag thunks for a conditional component
// attribute: it calls and returns then() when cond is true, otherwise els(). The
// branches are THUNKS so the untaken branch is never evaluated — mirroring a real Go
// if/else, where the untaken block's expressions (e.g. u.Name when u == nil) never run.
// els may be nil (no else branch); a false cond then yields a nil bag (a nil Attrs
// merges as empty). Generated code chains this into a bag-building .Merge(...).
func AttrsCond(cond bool, then, els func() Attrs) Attrs {
	if cond {
		if then != nil {
			return then()
		}
	} else if els != nil {
		return els()
	}
	return nil
}

// Spread renders the bag in SLICE ORDER (no sort). A bool value uses boolean-attribute
// semantics (true → bare attribute, false → omitted); everything else is written as
// key="value" with attribute escaping. A key that is not a structurally valid HTML
// attribute name (see validAttrName) is SKIPPED rather than emitted. Values are
// attribute-escaped but NOT URL-sanitized (see Attrs). ctx is reserved for
// forward-compatibility.
func (gw *Writer) Spread(ctx context.Context, a Attrs) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	for _, kv := range a {
		if !validAttrName(kv.Key) {
			continue // unsafe/invalid attribute name — drop it
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
