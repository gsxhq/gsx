package gsx

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

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
