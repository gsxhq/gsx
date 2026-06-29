package gsx

import "context"

// Attr is one ordered attribute pair. Value is rendered like an Attrs value: a
// bool toggles a bare boolean attribute; anything else is stringified (toStr)
// and attribute-escaped.
type Attr struct {
	Key   string
	Value any
}

// OrderedAttrs is an insertion-ordered, duplicate-tolerant attribute bag. Unlike
// Attrs (a map that Spread renders in sorted key order), OrderedAttrs renders in
// slice order — for callers who must control attribute order (e.g. Datastar
// data-* directives). Construct it directly or via the gsx `{{ "k": v }}` literal.
type OrderedAttrs []Attr

// SpreadOrdered writes the pairs of a in slice order. It mirrors Spread's
// per-attribute behavior exactly — the same validAttrName gate (a structurally
// unsafe name is dropped, never emitted), the same bool handling (a true bool is
// a bare attribute, false is omitted), and the same AttrValue escaping — and
// differs ONLY in that it does not sort. An empty/nil bag writes nothing.
// ctx is reserved for future context propagation (mirrors Spread's signature).
func (gw *Writer) SpreadOrdered(ctx context.Context, a OrderedAttrs) {
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
