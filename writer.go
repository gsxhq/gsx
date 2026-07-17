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

// URLImage writes s as an image-resource-sanitized, escaped URL attribute value.
// It permits data:image/* (raster + svg) in addition to the standard URL()
// allow-list; codegen emits it only for image-rendering sinks (<img src>,
// <source src>, <video poster>, background), never for navigational or script
// sinks.
func (gw *Writer) URLImage(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeURLImage(gw.w, s)
}

// URLVal writes v as a navigational-URL attribute value: a gsx.RawURL is the
// author's vouch and is emitted verbatim (still attribute-escaped); any other
// value is stringified and scheme-sanitized like URL. Generated code uses it
// for URL-classified bag attributes, where the value is dynamic (any).
func (gw *Writer) URLVal(v any) {
	if gw.err != nil {
		return
	}
	if r, ok := v.(RawURL); ok {
		gw.err = writeHTML(gw.w, string(r))
		return
	}
	s, _, ok := anyRenderVal(v)
	if !ok {
		gw.err = fmt.Errorf("gsx: URL attribute: unsupported dynamic type %T", v)
		return
	}
	gw.err = writeURL(gw.w, s)
}

// URLImageVal is URLVal for image-resource sinks (data:image/* permitted).
func (gw *Writer) URLImageVal(v any) {
	if gw.err != nil {
		return
	}
	if r, ok := v.(RawURL); ok {
		gw.err = writeHTML(gw.w, string(r))
		return
	}
	s, _, ok := anyRenderVal(v)
	if !ok {
		gw.err = fmt.Errorf("gsx: image URL attribute: unsupported dynamic type %T", v)
		return
	}
	gw.err = writeURLImage(gw.w, s)
}

// Srcset writes s as a sanitized, escaped srcset attribute value: a
// comma-separated image-candidate list, each candidate URL sanitized as an
// image resource. Codegen emits it for srcset/imagesrcset attributes.
func (gw *Writer) Srcset(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeSrcset(gw.w, s)
}

// SrcsetVal is Srcset for a dynamically-typed bag value: a gsx.RawURL is the
// author's whole-value vouch and is emitted verbatim (still attribute-escaped);
// any other value is stringified then sanitized.
func (gw *Writer) SrcsetVal(v any) {
	if gw.err != nil {
		return
	}
	if r, ok := v.(RawURL); ok {
		gw.err = writeHTML(gw.w, string(r))
		return
	}
	s, _, ok := anyRenderVal(v)
	if !ok {
		gw.err = fmt.Errorf("gsx: srcset attribute: unsupported dynamic type %T", v)
		return
	}
	gw.err = writeSrcset(gw.w, s)
}

// RefreshContent writes a meta refresh content value with any embedded redirect
// URL sanitized, then HTML-escapes the complete attribute value.
func (gw *Writer) RefreshContent(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeHTML(gw.w, refreshContentSanitize(s))
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

// AttrString converts a dynamically typed renderable value to the same raw
// string used by AttrAny before HTML attribute escaping.
func AttrString(v any) (string, error) {
	s, _, ok := anyRenderVal(v)
	if !ok {
		return "", fmt.Errorf("gsx: AttrString: unsupported dynamic type %T", v)
	}
	return s, nil
}

// TextAny writes v as escaped text, dispatching on its dynamic type. Codegen
// emits it for interpolations whose type is a type parameter with a MIXED
// non-tilde constraint whose terms are all runtime-dispatchable (e.g. T
// string | int) — classify proves every term has a matching case in
// anyRenderString at generate time, so the dispatch is total for generated
// code. See gsx.Val for the named-types-not-matched contract this mirrors.
func (gw *Writer) TextAny(v any) {
	s, err := AttrString(v)
	if err != nil {
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
	s, err := AttrString(v)
	if err != nil {
		if gw.err == nil {
			gw.err = fmt.Errorf("gsx: AttrAny: unsupported dynamic type %T", v)
		}
		return
	}
	gw.AttrValue(s)
}

// AttrAnyToggle writes one complete attribute whose name IS a boolean attribute
// (codegen resolved the list at generate time) but whose value type is known only
// at runtime — a mixed type parameter such as T string | bool. A bool-kinded
// value writes presence (` name` or nothing); any other value writes
// ` name="escaped"`. It owns the whole span — the leading space, the name, and
// the optional ="…" — which is what lets it omit a name codegen would otherwise
// have baked into a static string.
func (gw *Writer) AttrAnyToggle(name string, v any) {
	if gw.err != nil {
		return
	}
	s, k, ok := anyRenderVal(v)
	if !ok {
		gw.err = fmt.Errorf("gsx: AttrAnyToggle: unsupported dynamic type %T", v)
		return
	}
	if k == kindBool {
		gw.BoolAttr(name, s == "true")
		return
	}
	gw.writeStr(" ")
	gw.writeStr(name)
	gw.writeStr(`="`)
	gw.AttrValue(s)
	gw.writeStr(`"`)
}
