package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// firstEmbeddedAttr walks f and returns the first *ast.EmbeddedAttr found,
// failing the test if none is present.
func firstEmbeddedAttr(t *testing.T, f *ast.File) *ast.EmbeddedAttr {
	t.Helper()
	var found *ast.EmbeddedAttr
	ast.Inspect(f, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if ea, ok := n.(*ast.EmbeddedAttr); ok {
			found = ea
			return false
		}
		return true
	})
	if found == nil {
		t.Fatalf("no *ast.EmbeddedAttr found in file")
	}
	return found
}

func TestParseEmbeddedTextAttr(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class=`badge-@{v} x`>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	if ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText (%d)", ea.Lang, ast.EmbeddedText)
	}
	// segments: Text("badge-"), Interp(v), Text(" x")
	if len(ea.Segments) != 3 {
		t.Fatalf("segments = %d, want 3: %#v", len(ea.Segments), ea.Segments)
	}
	if _, ok := ea.Segments[1].(*ast.Interp); !ok {
		t.Fatalf("segment[1] = %T, want *ast.Interp", ea.Segments[1])
	}
}

func TestParseEmbeddedTextBraced(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span class={`badge-@{v}`}>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ea := firstEmbeddedAttr(t, f); ea.Lang != ast.EmbeddedText {
		t.Fatalf("Lang = %d, want EmbeddedText", ea.Lang)
	}
}
