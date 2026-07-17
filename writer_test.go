package gsx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type stubStringer struct{}

func (stubStringer) String() string { return "stub" }

func TestWriterHelpers(t *testing.T) {
	var b strings.Builder
	gw := W(&b)
	gw.S(`<a href="`)
	gw.URL("/path?x=1")
	gw.S(`" data-t="`)
	gw.AttrValue(`a"&b`)
	gw.S(`">`)
	gw.Text(`hi <there>`)
	gw.BoolAttr("hidden", false) // omitted
	gw.BoolAttr("checked", true) // ` checked`
	gw.S(`</a>`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	want := `<a href="/path?x=1" data-t="a&#34;&amp;b">hi &lt;there&gt; checked</a>`
	if b.String() != want {
		t.Fatalf("got  %q\nwant %q", b.String(), want)
	}
}

func TestWriterNodeNilSafe(t *testing.T) {
	var b strings.Builder
	gw := W(&b)
	gw.Node(context.Background(), nil) // no-op, no panic
	gw.Node(context.Background(), Raw("X"))
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if b.String() != "X" {
		t.Fatalf("got %q", b.String())
	}
}

// failingWriter fails on the Nth write.
type failingWriter struct {
	n int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("boom")
	}
	f.n--
	return len(p), nil
}

func TestWriterErrorThreadingShortCircuits(t *testing.T) {
	fw := &failingWriter{n: 1} // allow one write, then fail
	gw := W(fw)
	gw.S("ok")    // succeeds
	gw.S("boom")  // fails, sets err
	gw.Text("xx") // no-op (err already set)
	if gw.Err() == nil {
		t.Fatal("expected threaded error, got nil")
	}
	var _ io.Writer = fw
}

func TestWriterCSS(t *testing.T) {
	cases := []struct {
		name   string
		render func(*Writer)
		want   string
	}{
		{"block safe", func(w *Writer) { w.CSS("10px") }, "10px"},
		{"block breakout", func(w *Writer) { w.CSS("red;}body{x") }, "ZgotmplZ"},
		{"rawcss type is a string", func(w *Writer) { w.S(string(RawCSS("1px solid"))) }, "1px solid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b strings.Builder
			w := W(&b)
			c.render(w)
			if err := w.Err(); err != nil {
				t.Fatalf("Err = %v", err)
			}
			if b.String() != c.want {
				t.Errorf("got %q, want %q", b.String(), c.want)
			}
		})
	}
}

func TestTextAny(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"a<b", "a&lt;b"},
		{[]byte("x&y"), "x&amp;y"},
		{7, "7"},
		{int64(-3), "-3"},
		{uint8(200), "200"},
		{1.5, "1.5"},
		{float32(2.5), "2.5"},
		{true, "true"},
		{stubStringer{}, "stub"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		gw := W(&buf)
		gw.TextAny(c.in)
		if err := gw.Err(); err != nil {
			t.Fatalf("TextAny(%#v): %v", c.in, err)
		}
		if buf.String() != c.want {
			t.Errorf("TextAny(%#v) = %q, want %q", c.in, buf.String(), c.want)
		}
	}
}

// A named scalar renders by its UNDERLYING type, exactly as { x } does inline.
// This previously errored — the exact-type switch could not see through the name,
// which is why `type Flag bool` rendered required="false" through a bag while
// rendering as a bare `required` statically.
func TestTextAnyNamedTypeRendersByUnderlyingType(t *testing.T) {
	type named string
	var buf bytes.Buffer
	gw := W(&buf)
	gw.TextAny(named("x<y"))
	if gw.Err() != nil {
		t.Fatalf("TextAny(named) = %v; want it to render by underlying type", gw.Err())
	}
	if got := buf.String(); got != "x&lt;y" {
		t.Errorf("TextAny(named) wrote %q; want %q (escaped)", got, "x&lt;y")
	}
}

func TestTextAnyUnsupportedSetsErr(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.TextAny(struct{ X int }{X: 1})
	if gw.Err() == nil {
		t.Fatal("want error for a struct through TextAny")
	}
}

func TestAttrAnyEscapes(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.AttrAny(`a"b`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "a&#34;b" {
		t.Errorf("AttrAny = %q", got)
	}
}

func TestAttrString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"text", "text"}, {[]byte("bytes"), "bytes"}, {stubStringer{}, "stub"},
		{true, "true"}, {int(-1), "-1"}, {int8(-2), "-2"}, {int16(-3), "-3"},
		{int32(-4), "-4"}, {int64(-5), "-5"}, {uint(1), "1"}, {uint8(2), "2"},
		{uint16(3), "3"}, {uint32(4), "4"}, {uint64(5), "5"}, {uintptr(6), "6"},
		{float32(1.5), "1.5"}, {float64(2.5), "2.5"},
	}
	for _, c := range cases {
		got, err := AttrString(c.in)
		if err != nil || got != c.want {
			t.Errorf("AttrString(%#v) = %q, %v; want %q, nil", c.in, got, err, c.want)
		}
	}
	_, err := AttrString(struct{ X int }{X: 1})
	if got, want := fmt.Sprint(err), "gsx: AttrString: unsupported dynamic type struct { X int }"; got != want {
		t.Errorf("unsupported error = %q, want %q", got, want)
	}
}

func TestURLVal(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{"https://x/y", "https://x/y"},
		{"javascript:alert(1)", "about:invalid#gsx"},
		{RawURL("app://z"), "app://z"},
		{RawURL(`a"b`), "a&#34;b"}, // RawURL still attribute-escaped
	}
	for _, c := range cases {
		var buf bytes.Buffer
		gw := W(&buf)
		gw.URLVal(c.v)
		if err := gw.Err(); err != nil || buf.String() != c.want {
			t.Fatalf("URLVal(%v) = %q, %v; want %q", c.v, buf.String(), err, c.want)
		}
	}
}

func TestURLImageVal(t *testing.T) {
	var buf bytes.Buffer
	gw := W(&buf)
	gw.URLImageVal("data:image/png;base64,AAAA")
	if got := buf.String(); got != "data:image/png;base64,AAAA" {
		t.Fatalf("got %q", got)
	}
	buf.Reset()
	gw = W(&buf)
	gw.URLVal("data:image/png;base64,AAAA") // nav sink rejects data:
	if got := buf.String(); got != "about:invalid#gsx" {
		t.Fatalf("nav sink must reject data:image, got %q", got)
	}
}

func TestSrcsetSinks(t *testing.T) {
	var sb strings.Builder
	gw := &Writer{w: &sb}
	gw.Srcset("ok.jpg 1x, javascript:alert(1) 2x")
	if got := sb.String(); got != "ok.jpg 1x, about:invalid#gsx" {
		t.Fatalf("Srcset = %q", got)
	}
	// SrcsetVal: RawURL vouch passes verbatim (still attribute-escaped)
	sb.Reset()
	gw = &Writer{w: &sb}
	gw.SrcsetVal(RawURL("javascript:whatever 1x"))
	if got := sb.String(); got != "javascript:whatever 1x" {
		t.Fatalf("SrcsetVal(RawURL) = %q", got)
	}
	// SrcsetVal: non-RawURL string sanitizes
	sb.Reset()
	gw = &Writer{w: &sb}
	gw.SrcsetVal("javascript:alert(1) 1x")
	if got := sb.String(); got != "about:invalid#gsx" {
		t.Fatalf("SrcsetVal(string) = %q", got)
	}
}
