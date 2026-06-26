// internal/printer/script_test.go
package printer

import (
	"strings"
	"testing"
)

func TestScriptBodyReindented(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>\nfunction f() {\nreturn 1\n}\n\t</script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	// Re-indented to tabs under the tag depth (component=1, body=2, inside fn=3).
	if !strings.Contains(out, "\t\tfunction f() {") || !strings.Contains(out, "\t\t\treturn 1") {
		t.Fatalf("script body not re-indented:\n%s", out)
	}
}

func TestScriptHolePreserved(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>const u = @{ user.ID }</script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "@{ user.ID }") || strings.Contains(out, "__gsxhole") {
		t.Fatalf("hole not preserved / sentinel leaked:\n%s", out)
	}
}

func TestDataIslandScriptLeftVerbatim(t *testing.T) {
	// type="application/json" is NOT executable JS — left verbatim.
	src := "package p\n\ncomponent C() {\n\t<script type=\"application/json\">  {\"a\":1}  </script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  {\"a\":1}  ") {
		t.Fatalf("data-island script should be verbatim:\n%s", out)
	}
}

func TestScriptIdempotent(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>\nif(x){\nf()\n}\n\t</script>\n}\n"
	once, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := normPrint(t, once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Fatalf("script fmt not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}
