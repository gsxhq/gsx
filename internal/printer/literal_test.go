package printer

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestLiteralContentEveryContext(t *testing.T) {
	const literal = "<><span> a\tb\n\t\t c </span><strong>__gsxSignature0__</strong></>"
	contexts := map[string]string{
		"component":     "package p\ncomponent C() { " + literal + " }\n",
		"top-level":     "package p\nvar n = " + literal + "\n",
		"function":      "package p\nfunc F() { n := " + literal + "; _ = n }\n",
		"block":         "package p\ncomponent C() { {{ n := " + literal + "; _ = n }} }\n",
		"interpolation": "package p\ncomponent C() { { Wrap(" + literal + ") } }\n",
		"attribute":     "package p\ncomponent C() { <Card node={ Wrap(" + literal + ") }/> }\n",
		"slot":          "package p\ncomponent C() { <Card node={ " + literal + " }/> }\n",
		"control":       "package p\ncomponent C() { { if ok { " + literal + " } } }\n",
		"case":          "package p\ncomponent C() { { switch x { case 1: " + literal + " } } }\n",
		"fragment":      "package p\ncomponent C() { <>" + literal + "</> }\n",
		"region":        "package p\ncomponent C() { <?start name=\"r\">" + literal + "<?end> }\n",
	}
	for name, src := range contexts {
		t.Run(name, func(t *testing.T) {
			for _, width := range []int{20, 40, 80, 120} {
				t.Run(fmt.Sprint(width), func(t *testing.T) {
					got := fmtSourceWidth(t, src, width)
					if !strings.Contains(got, literal) {
						t.Fatalf("literal bytes changed:\n%q", got)
					}
					if !reflect.DeepEqual(contentAST(t, src), contentAST(t, got)) {
						t.Fatal("literal AST changed")
					}
					if again := fmtSourceWidth(t, got, width); again != got {
						t.Fatalf("second pass changed output:\n%s", again)
					}
				})
			}
		})
	}
}

// The faithfulness oracle must detect a lost tab/newline in embedded markup
// without formatting both sides through the implementation it is checking.
func TestLiteralOracleDetectsEmbeddedTextLoss(t *testing.T) {
	contexts := []string{
		"package p\nvar n = <span>a\t\nb</span>\n",
		"package p\ncomponent C() { {{ n := <span>a\t\nb</span>; _ = n }} }\n",
		"package p\ncomponent C() { { Wrap(<span>a\t\nb</span>) } }\n",
		"package p\ncomponent C() { <Card node={ Wrap(<span>a\t\nb</span>) }/> }\n",
	}
	for _, src := range contexts {
		changed := strings.Replace(src, "a\t\nb", "a b", 1)
		if reflect.DeepEqual(contentAST(t, src), contentAST(t, changed)) {
			t.Fatalf("oracle masked literal change in %q", src)
		}
	}
}
