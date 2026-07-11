package gsx

import (
	"context"
	"io"
	"strings"
	"testing"
)

// card mirrors the walkthrough's generated Card.
func card(title string, featured bool, children Node) Node {
	return Func(func(ctx context.Context, w io.Writer) error {
		gw := W(w)
		gw.S(`<section class="`)
		gw.Class(DefaultClassMerge, Class("card"), ClassIf("card-featured", featured))
		gw.S(`"><h2>`)
		gw.Text(title)
		gw.S(`</h2>`)
		gw.Node(ctx, children)
		gw.S(`</section>`)
		return gw.Err()
	})
}

// box mirrors the walkthrough's generated Box (conditional attr + bool + spread).
func box(padded, disabled bool, attrs Attrs, children Node) Node {
	return Func(func(ctx context.Context, w io.Writer) error {
		gw := W(w)
		gw.S(`<div class="`)
		gw.Class(DefaultClassMerge, Class("box"), ClassIf("p-4", padded))
		gw.S(`"`)
		gw.BoolAttr("disabled", disabled)
		if !padded {
			gw.S(` data-tight`)
		}
		gw.Spread(ctx, attrs, nil, nil, nil, nil, nil)
		gw.S(`>`)
		gw.Node(ctx, children)
		gw.S(`</div>`)
		return gw.Err()
	})
}

func render(t *testing.T, n Node) string {
	t.Helper()
	var b strings.Builder
	if err := n.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestIntegrationCard(t *testing.T) {
	got := render(t, card("Hi & <Bye>", true, Raw(`<p>kid</p>`)))
	want := `<section class="card card-featured"><h2>Hi &amp; &lt;Bye&gt;</h2><p>kid</p></section>`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestIntegrationBox(t *testing.T) {
	got := render(t, box(false, true, Attrs{{Key: "aria-hidden", Value: true}, {Key: "id", Value: "b1"}}, Raw("x")))
	// not padded -> data-tight + box class only; disabled bool; spread in slice order (aria-hidden, id)
	want := `<div class="box" disabled data-tight aria-hidden id="b1">x</div>`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
