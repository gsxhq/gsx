package gsx

import (
	"context"
	"io"
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

