package tailwindmerge_test

import (
	"context"
	"strings"
	"testing"

	gsx "github.com/gsxhq/gsx"
	"github.com/gsxhq/gsx/examples/tailwind-merge/views"
)

func TestTailwindMergeFallthrough(t *testing.T) {
	// caller passes conflicting px-8; tailwind merge must drop px-4.
	var sb strings.Builder
	node := views.Card(views.CardProps{Attrs: gsx.Attrs{{Key: "class", Value: "px-8"}}, Children: gsx.Raw("x")})
	if err := node.Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.Contains(got, `class="py-2 px-8"`) {
		t.Fatalf("want merged class py-2 px-8 (px-4 dropped), got: %s", got)
	}
}
