package codegen

import (
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
)

// componentSignature returns a canonical string of a component's CALLER
// signature — what a `<Comp .../>` invocation type-checks against. Two
// components with the same componentKey that share this signature are drop-in
// build-tag variants (same name, different body); one with a different
// signature is a genuine conflict. The string is comparison-only (not parsed);
// it is order-independent over props (attrs map to fields by name) and ignores
// the receiver VARIABLE name.
func componentSignature(c *gsxast.Component) string {
	var b strings.Builder

	// Receiver: type (parseRecv's recvType already carries the pointer-ness,
	// e.g. "*Form" vs "Form" — a method vs func component, and the owning
	// type, are caller-visible; the receiver var name is not).
	if c.Recv != "" {
		if _, recvType, _, err := parseRecv(c.Recv); err == nil {
			b.WriteString("recv:")
			b.WriteString(recvType)
		} else {
			b.WriteString("recv:<unparsable>")
		}
	}
	b.WriteByte('|')

	// Generic type params: normalized source.
	b.WriteString("tp:")
	b.WriteString(strings.Join(strings.Fields(c.TypeParams), " "))
	b.WriteByte('|')

	// Props: sorted "FieldName type" entries, plus synthesized Children/Attrs.
	var fields []string
	if params, err := parseParams(c.Params); err == nil {
		for _, p := range params {
			fields = append(fields, fieldName(p.name)+" "+strings.Join(strings.Fields(p.typ), " "))
		}
	}
	if usesChildren(c.Body) {
		fields = append(fields, "Children gsx.Node")
	}
	if usesAttrs(c.Body) {
		fields = append(fields, "Attrs gsx.Attrs")
	}
	sort.Strings(fields)
	b.WriteString("props:")
	b.WriteString(strings.Join(fields, ";"))
	return b.String()
}
