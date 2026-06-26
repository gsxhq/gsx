// internal/printer/style_test.go
package printer

import (
	"strings"
	"testing"
)

func TestStyleBodyFormatted(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>.a{color:red;background:blue}</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "color: red;") || !strings.Contains(out, "background: blue;") {
		t.Fatalf("style body not formatted:\n%s", out)
	}
}

func TestStyleBodyHolePreserved(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>.a{color:@{ fg }}</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "@{ fg }") {
		t.Fatalf("hole not preserved:\n%s", out)
	}
	if strings.Contains(out, "__gsxhole") {
		t.Fatalf("sentinel leaked:\n%s", out)
	}
}

func TestStyleBodyIdempotent(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>h1,h2{margin:0}.a{color:@{ fg }}</style>\n}\n"
	once, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normPrint(t, once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatalf("style fmt not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestStyleMalformedFallsBackVerbatim(t *testing.T) {
	// Unbalanced CSS → cssfmt errors → verbatim fallback (body unchanged).
	src := "package p\n\ncomponent C() {\n\t<style>.a{color:red</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ".a{color:red") {
		t.Fatalf("malformed CSS should be left verbatim:\n%s", out)
	}
}
