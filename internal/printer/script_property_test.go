// internal/printer/script_property_test.go
package printer

import (
	"reflect"
	"testing"
)

var scriptCases = []string{
	"package p\n\ncomponent C() {\n\t<script>function f(){return 1}</script>\n}\n",
	"package p\n\ncomponent C() {\n\t<script>\nconst x = 1;\n\nconst y = 2;\n\t</script>\n}\n",
	"package p\n\ncomponent C() {\n\t<script>const u = @{ user.ID };const re = /a\\/b/g</script>\n}\n",
	"package p\n\ncomponent C() {\n\t<script type=\"application/json\">{\"a\":1}</script>\n}\n",
}

func TestScriptPropertyFaithfulAndIdempotent(t *testing.T) {
	for _, src := range scriptCases {
		formatted, err := printSource(t, src)
		if err != nil {
			t.Errorf("fmt failed: %v\n%s", err, src)
			continue
		}
		if !reflect.DeepEqual(contentAST(t, src), contentAST(t, formatted)) {
			t.Errorf("fmt changed content AST (not faithful):\n--- src ---\n%s\n--- fmt ---\n%s", src, formatted)
		}
		formatted2, err := printSource(t, formatted)
		if err != nil {
			t.Errorf("re-fmt failed: %v", err)
			continue
		}
		if formatted != formatted2 {
			t.Errorf("not idempotent:\n--- 1 ---\n%s\n--- 2 ---\n%s", formatted, formatted2)
		}
	}
}
