package printer

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// firstElement parses src (a component body's markup wrapped for parsing),
// normalizes, and returns the first element of the first component's body.
func firstElement(t *testing.T, src string) *ast.Element {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.gsx", []byte("package p\n\ncomponent T() {\n"+src+"\n}\n"), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			for _, m := range c.Body {
				if e, ok := m.(*ast.Element); ok {
					return e
				}
			}
		}
	}
	t.Fatal("no element found")
	return nil
}

func TestAtomDoc(t *testing.T) {
	p := &printer{width: 80, tabWidth: 2}
	atoms := []struct {
		src  string
		flat string
	}{
		{`<code>.gsx</code>`, `<code>.gsx</code>`},
		{`<code class="x">y</code>`, `<code class="x">y</code>`},
		{`<a href="/d"><code>x</code></a>`, `<a href="/d"><code>x</code></a>`},
		{`<br/>`, `<br/>`},
		{`<code>{ v }</code>`, `<code>{ v }</code>`},
		{`<a href={ u }>x</a>`, `<a href={u}>x</a>`},
		{`<span class={ cls }>y</span>`, `<span class={ cls }>y</span>`},
	}
	for _, c := range atoms {
		e := firstElement(t, c.src)
		doc, ok := p.atomDoc(e)
		if !ok {
			t.Errorf("%s: expected atom", c.src)
			continue
		}
		if got := pretty.Print(doc, 10, 2); got != c.flat {
			// width 10 on purpose: an atom must render flat even when the
			// width budget is absurdly small.
			t.Errorf("%s: flat = %q, want %q", c.src, got, c.flat)
		}
	}
	notAtoms := []string{
		`<div>x</div>`,                   // not an inline tag
		`<Card>x</Card>`,                 // component
		`<span><div>x</div></span>`,      // block child
		`<code>{ if v { <b/> } }</code>`, // control-flow child
		"<code>\n\tx\n</code>",           // author ChildrenMultiline wins
		`<textarea>x</textarea>`,         // preserve tag, never inline
		`<script>x</script>`,             // preserve tag, never inline
	}
	for _, src := range notAtoms {
		e := firstElement(t, src)
		if _, ok := p.atomDoc(e); ok {
			t.Errorf("%s: expected NOT atom", src)
		}
	}
}

func TestAtomDocPreserveContext(t *testing.T) {
	p := &printer{width: 80, tabWidth: 2, preserve: true}
	if _, ok := p.atomDoc(firstElement(t, `<code>x</code>`)); ok {
		t.Error("inside a preserve subtree nothing is an atom (text is verbatim)")
	}
}
