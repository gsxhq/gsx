package gsx_test

import (
	"strings"
	"testing"

	"github.com/gsxhq/gsx"
)

func TestGeneratedPartConstructorsAreExternallyUsable(t *testing.T) {
	var style strings.Builder
	gsx.W(&style).Style(
		gsx.Style("color:red"),
		gsx.StyleIf("display:none", false),
	)
	if got := style.String(); got != "color:red" {
		t.Fatalf("style = %q, want %q", got, "color:red")
	}

	var class strings.Builder
	gsx.W(&class).Class(
		gsx.DefaultClassMerge,
		gsx.Class("button"),
		gsx.ClassIf("active", true),
	)
	if got := class.String(); got != "button active" {
		t.Fatalf("class = %q, want %q", got, "button active")
	}
}
