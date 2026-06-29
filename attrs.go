package gsx

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Attrs is an attribute bag (spread / implicit rest). Values are bool (boolean
// attribute), string, []string, or anything fmt can format.
//
// Security contract: keys are HTML attribute NAMES emitted (after a validity
// check, see Spread) without entity-encoding — they must come from generated
// code or trusted developer input, never from untrusted strings. Values are
// HTML-attribute-escaped but NOT URL-sanitized: a URL-typed attribute (href,
// src, action, formaction, …) carrying an untrusted value must be written with
// gw.URL, not passed through Spread.
type Attrs map[string]any

// Has reports whether key is present.
func (a Attrs) Has(key string) bool { _, ok := a[key]; return ok }

// Get returns the value for key and whether it was present.
func (a Attrs) Get(key string) (any, bool) { v, ok := a[key]; return v, ok }

// Without returns a copy of a without the given keys (a is not mutated).
func (a Attrs) Without(keys ...string) Attrs {
	// Fast path: an empty bag (the common case — no fallthrough attributes) has
	// nothing to filter. This runs on every component root via Spread.
	if len(a) == 0 {
		return nil
	}
	out := make(Attrs, len(a))
	for k, v := range a {
		if !slices.Contains(keys, k) {
			out[k] = v
		}
	}
	return out
}

// Take returns the value for key and a copy of a without it (a is not mutated).
func (a Attrs) Take(key string) (any, Attrs) {
	return a[key], a.Without(key)
}

// Merge returns a new bag combining a and other. For most keys other wins; the
// "class" and "style" values are concatenated (a's then other's).
func (a Attrs) Merge(other Attrs) Attrs {
	out := make(Attrs, len(a)+len(other))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range other {
		if (k == "class" || k == "style") && out[k] != nil {
			out[k] = joinAttrStrings(k, toStr(out[k]), toStr(v))
			continue
		}
		out[k] = v
	}
	return out
}

// AttrsCond selects one of two attribute-bag thunks for a conditional component
// attribute: it calls and returns then() when cond is true, otherwise els().
// The branches are THUNKS so the untaken branch is never evaluated — mirroring a
// real Go `if cond { … } else { … }`, where the untaken block's expressions
// (e.g. `u.Name` when `u == nil`) are not evaluated. els may be nil (no else
// branch), in which case a false cond yields a nil bag — a nil Attrs merges as
// empty. Generated code chains this into a bag-building .Merge(...) expression so
// a conditional attr only contributes its entries when its condition holds.
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

// Class returns the merged class string from the bag's "class" entry, or "".
func (a Attrs) Class() string {
	v, ok := a["class"]
	if !ok {
		return ""
	}
	return ClassMerger(classTokens([]ClassPart{Class(toStr(v))}))
}

// Style returns the bag's "style" declaration string, or "".
func (a Attrs) Style() string {
	v, ok := a["style"]
	if !ok {
		return ""
	}
	return toStr(v)
}

// Spread renders the bag deterministically (keys sorted). bool values use
// boolean-attribute semantics; everything else is written as key="value" with
// attribute escaping. ctx is reserved for forward-compatibility.
//
// A key that is not a structurally valid HTML attribute name (empty, or
// containing whitespace or any of " ' < > = / or a control byte) is SKIPPED
// rather than emitted — such a name cannot be entity-encoded while staying a
// valid name, so emitting it verbatim would allow tag breakout. Values are
// attribute-escaped but NOT URL-sanitized (see Attrs).
func (gw *Writer) Spread(ctx context.Context, a Attrs) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !validAttrName(k) {
			continue // unsafe/invalid attribute name — drop it
		}
		if b, ok := a[k].(bool); ok {
			gw.BoolAttr(k, b)
			continue
		}
		gw.writeStr(" ")
		gw.writeStr(k)
		gw.writeStr(`="`)
		gw.AttrValue(toStr(a[k]))
		gw.writeStr(`"`)
	}
}

// validAttrName reports whether k is a structurally safe HTML attribute name:
// non-empty and free of whitespace, control bytes, and the characters that could
// break out of the tag or the name (" ' < > = / &). Names like "hx-on::click",
// ":class", "@click.away", "data-x", and "_" pass.
func validAttrName(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c <= ' ' || c == 0x7f { // whitespace or control byte
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
