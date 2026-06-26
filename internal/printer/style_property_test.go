// internal/printer/style_property_test.go
package printer

import (
	"reflect"
	"testing"
)

// styleCases are inline <style> bodies exercising the faithfulness + idempotence
// contract for CSS formatting.
var styleCases = []string{
	"package p\n\ncomponent C() {\n\t<style>.a{color:red;background:blue}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>h1,h2,h3{margin:0}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>.a{color:@{ fg };width:@{ w }}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>@media (min-width:600px){.a{color:red}}</style>\n}\n",
	"package p\n\ncomponent C() {\n\t<style>.a{color:red</style>\n}\n", // malformed → verbatim
}

func TestStylePropertyFaithfulAndIdempotent(t *testing.T) {
	for _, src := range styleCases {
		formatted, err := normPrint(t, src)
		if err != nil {
			t.Errorf("fmt failed: %v\n%s", err, src)
			continue
		}
		// Faithfulness: normalized ASTs (with <style> bodies canonicalized) match.
		want := normalizedAST(t, src)
		got := normalizedAST(t, formatted)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("fmt changed normalized AST (not faithful):\n--- src ---\n%s\n--- fmt ---\n%s", src, formatted)
		}
		// Idempotence.
		formatted2, err := normPrint(t, formatted)
		if err != nil {
			t.Errorf("re-fmt failed: %v", err)
			continue
		}
		if formatted != formatted2 {
			t.Errorf("not idempotent:\n--- 1 ---\n%s\n--- 2 ---\n%s", formatted, formatted2)
		}
	}
}
