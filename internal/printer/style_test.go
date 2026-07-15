// internal/printer/style_test.go
package printer

import (
	"strings"
	"testing"
)

func TestStyleBodyReindented(t *testing.T) {
	// A well-indented <style> body is preserved and re-based under the tag depth.
	src := "package p\n\ncomponent C() {\n\t<style>\n.a {\n\tcolor: red;\n}\n\t</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\t\t.a {") || !strings.Contains(out, "\t\t\tcolor: red;") {
		t.Fatalf("style body not re-indented to tabs:\n%s", out)
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

func TestStyleUnterminatedStringFallsBackVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<style>.a{content:\"oops}</style>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ".a{content:\"oops}") {
		t.Fatalf("unterminated-string CSS should be left verbatim:\n%s", out)
	}
}
