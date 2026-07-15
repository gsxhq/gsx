// internal/printer/script_test.go
package printer

import (
	"strings"
	"testing"
)

func TestScriptBodyReindented(t *testing.T) {
	// Well-indented body: its relative structure is preserved and re-based under
	// the tag depth (component=1, body=2, inside fn=3).
	src := "package p\n\ncomponent C() {\n\t<script>\nfunction f() {\n\treturn 1\n}\n\t</script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\t\tfunction f() {") || !strings.Contains(out, "\t\t\treturn 1") {
		t.Fatalf("script body not re-based:\n%s", out)
	}
}

// TestScriptCallbackSingleIndentEndToEnd guards, through the full printer +
// rawfmt path, the callback pattern that escaped to a user's file: the callback
// body must be exactly ONE level under its `call(args, () => {` line, not two.
func TestScriptCallbackSingleIndentEndToEnd(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>\ndocument.body.addEventListener('x', (e) => {\n\tconsole.log(e);\n});\n\t</script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	// tag <script> is at depth 1 (component) → body base = depth 2; the call line
	// at 2 tabs, the callback body exactly one deeper at 3, the `});` back at 2.
	if !strings.Contains(out, "\t\tdocument.body.addEventListener('x', (e) => {") {
		t.Fatalf("call line wrong indent:\n%s", out)
	}
	if !strings.Contains(out, "\t\t\tconsole.log(e);") {
		t.Fatalf("callback body should be exactly one level deeper (got over/under-indent):\n%s", out)
	}
	if !strings.Contains(out, "\t\t});") {
		t.Fatalf("`});` should dedent back to the call's level:\n%s", out)
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

func TestEmptyScriptStaysInline(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script src=\"https://x.js\"></script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<script src=\"https://x.js\"></script>") {
		t.Fatalf("empty external script should stay inline:\n%s", out)
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

// A multi-line template literal inside a <script> body must be emitted verbatim
// (interior not re-indented) and be idempotent.
func TestScriptTemplateLiteralVerbatim(t *testing.T) {
	src := "package p\n\ncomponent C() {\n\t<script>\nconst t = `<div>\nhi\n</div>`;\n</script>\n}\n"
	out, err := normPrint(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "`<div>\nhi\n</div>`") {
		t.Fatalf("script template-literal interior re-indented:\n%s", out)
	}
	twice, _ := normPrint(t, out)
	if out != twice {
		t.Fatalf("script not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", out, twice)
	}
}
