package gsx

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestFuncRenders(t *testing.T) {
	called := false
	var n Node = Func(func(ctx context.Context, w io.Writer) error {
		called = true
		_, err := w.Write([]byte("hi"))
		return err
	})
	var b bytes.Buffer
	if err := n.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !called || b.String() != "hi" {
		t.Fatalf("called=%v out=%q", called, b.String())
	}
}

func TestRawIsVerbatim(t *testing.T) {
	var b bytes.Buffer
	if err := Raw(`<b>bold</b>`).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if b.String() != `<b>bold</b>` { // NOT escaped
		t.Fatalf("got %q", b.String())
	}
	b.Reset()
	if err := Raw("").Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if b.String() != "" {
		t.Fatalf("Raw(\"\") wrote %q", b.String())
	}
}
