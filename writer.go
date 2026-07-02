package gsx

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

// Writer streams HTML to an underlying io.Writer, retaining the first write error
// so generated code need not check every write. Once an error is set, every
// helper is a no-op; read it once via Err.
type Writer struct {
	w   io.Writer
	err error
}

// W wraps w. The returned *Writer is always usable.
func W(w io.Writer) *Writer { return &Writer{w: w} }

// Err returns the first write error encountered, or nil.
func (gw *Writer) Err() error { return gw.err }

// writeStr writes s verbatim, threading the first error.
func (gw *Writer) writeStr(s string) {
	if gw.err != nil {
		return
	}
	_, gw.err = io.WriteString(gw.w, s)
}

// S writes trusted static markup verbatim.
func (gw *Writer) S(s string) { gw.writeStr(s) }

// Text writes s as HTML-escaped text content.
func (gw *Writer) Text(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeHTML(gw.w, s)
}

// IntInto writes n in base 10 into the caller-provided scratch buffer and writes
// the digit bytes directly to the output — no intermediate string allocation, and
// no HTML escaping (decimal digits and a leading '-' are always safe in text and
// attribute contexts). Generated code declares one buffer per render and reuses
// it across all numeric interpolations, so a numeric-heavy component allocates
// the scratch at most once (when it escapes) rather than once per number.
func (gw *Writer) IntInto(buf []byte, n int64) {
	if gw.err != nil {
		return
	}
	_, gw.err = gw.w.Write(strconv.AppendInt(buf[:0], n, 10))
}

// UintInto writes n in base 10 (see IntInto).
func (gw *Writer) UintInto(buf []byte, n uint64) {
	if gw.err != nil {
		return
	}
	_, gw.err = gw.w.Write(strconv.AppendUint(buf[:0], n, 10))
}

// FloatInto writes f using strconv's 'g' shortest form (see IntInto). The output
// charset (digits, '.', '-', '+', 'e', and Inf/NaN letters) is always HTML-safe.
func (gw *Writer) FloatInto(buf []byte, f float64) {
	if gw.err != nil {
		return
	}
	_, gw.err = gw.w.Write(strconv.AppendFloat(buf[:0], f, 'g', -1, 64))
}

// AttrValue writes s as an escaped double-quoted attribute value.
func (gw *Writer) AttrValue(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeHTML(gw.w, s)
}

// URL writes s as a sanitized, escaped URL attribute value.
func (gw *Writer) URL(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeURL(gw.w, s)
}

// BoolAttr writes ` name` when on, and nothing otherwise.
func (gw *Writer) BoolAttr(name string, on bool) {
	if !on {
		return
	}
	gw.writeStr(" ")
	gw.writeStr(name)
}

// Node renders a child node to the same writer; a nil node is a no-op. A render
// error is retained.
func (gw *Writer) Node(ctx context.Context, n Node) {
	if gw.err != nil || n == nil {
		return
	}
	gw.err = n.Render(ctx, gw.w)
}

// CSS writes s into a <style> raw-text context, value-filtered so it cannot
// break out of a CSS value. The filter rejects '<', so the result is raw-text
// safe and needs no HTML escaping.
func (gw *Writer) CSS(s string) {
	gw.writeStr(cssValueFilter(s))
}

// anyRenderString converts a dynamically-typed renderable value to its text
// form, matching the static per-category emit paths byte-for-byte (FormatInt
// base 10, FormatFloat 'g' -1 64, FormatBool, Stringer.String, string/[]byte
// verbatim). It matches EXACT predeclared types (string, []byte, the sized
// int/uint/float kinds, bool) plus the fmt.Stringer interface (which also
// matches named Stringer types) — a named scalar with no String() method
// (type Slug string) returns ok=false, mirroring gsx.Val's documented
// contract. codegen (classifyTypeParam) only routes here for type parameters
// whose constraint terms are all non-tilde AND individually dispatchable —
// an unnamed predeclared type, an unnamed []byte, or a Stringer — so every
// term in the constraint has a matching case here and the dispatch is total.
func anyRenderString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	case fmt.Stringer:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	case int:
		return strconv.FormatInt(int64(t), 10), true
	case int8:
		return strconv.FormatInt(int64(t), 10), true
	case int16:
		return strconv.FormatInt(int64(t), 10), true
	case int32:
		return strconv.FormatInt(int64(t), 10), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case uint:
		return strconv.FormatUint(uint64(t), 10), true
	case uint8:
		return strconv.FormatUint(uint64(t), 10), true
	case uint16:
		return strconv.FormatUint(uint64(t), 10), true
	case uint32:
		return strconv.FormatUint(uint64(t), 10), true
	case uint64:
		return strconv.FormatUint(t, 10), true
	case uintptr:
		return strconv.FormatUint(uint64(t), 10), true
	case float32:
		return strconv.FormatFloat(float64(t), 'g', -1, 64), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	}
	return "", false
}

// TextAny writes v as escaped text, dispatching on its dynamic type. Codegen
// emits it for interpolations whose type is a type parameter with a MIXED
// non-tilde constraint whose terms are all runtime-dispatchable (e.g. T
// string | int) — classify proves every term has a matching case in
// anyRenderString at generate time, so the dispatch is total for generated
// code. See gsx.Val for the named-types-not-matched contract this mirrors.
func (gw *Writer) TextAny(v any) {
	s, ok := anyRenderString(v)
	if !ok {
		if gw.err == nil {
			gw.err = fmt.Errorf("gsx: TextAny: unsupported dynamic type %T", v)
		}
		return
	}
	gw.Text(s)
}

// AttrAny is TextAny for attribute-value position (AttrValue escaping).
// See gsx.Val for the named-types-not-matched contract this mirrors.
func (gw *Writer) AttrAny(v any) {
	s, ok := anyRenderString(v)
	if !ok {
		if gw.err == nil {
			gw.err = fmt.Errorf("gsx: AttrAny: unsupported dynamic type %T", v)
		}
		return
	}
	gw.AttrValue(s)
}
