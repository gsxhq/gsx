package gsx

import (
	"bytes"
	"context"
	"errors"
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

func TestTextAnyUnsupportedSetsErr(t *testing.T) {
	type named string // named type: NOT matched, mirroring gsx.Val's contract
	var buf bytes.Buffer
	gw := W(&buf)
	gw.TextAny(named("x"))
	if gw.Err() == nil {
		t.Fatal("want error for named type through TextAny")
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
