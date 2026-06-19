package gsx

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Attrs is an attribute bag (spread / implicit rest). Values are bool (boolean
// attribute), string, []string, or anything fmt can format.
type Attrs map[string]any

// Has reports whether key is present.
func (a Attrs) Has(key string) bool { _, ok := a[key]; return ok }

// Get returns the value for key and whether it was present.
func (a Attrs) Get(key string) (any, bool) { v, ok := a[key]; return v, ok }

// Without returns a copy of a without the given keys (a is not mutated).
func (a Attrs) Without(keys ...string) Attrs {
	drop := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		drop[k] = struct{}{}
	}
	out := make(Attrs, len(a))
	for k, v := range a {
		if _, skip := drop[k]; skip {
			continue
		}
		out[k] = v
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

// Class returns the merged class string from the bag's "class" entry, or "".
func (a Attrs) Class() string {
	v, ok := a["class"]
	if !ok {
		return ""
	}
	return ClassMerger(classTokens([]ClassPart{Class(toStr(v))}))
}

// Spread renders the bag deterministically (keys sorted). bool values use
// boolean-attribute semantics; everything else is written as key="value" with
// attribute escaping. ctx is reserved for forward-compatibility.
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
