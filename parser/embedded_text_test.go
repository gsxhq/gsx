package parser

import (
	"go/token"
	"strings"
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

// embeddedText concatenates all *ast.Text segment values in ea, in order.
func embeddedText(ea *ast.EmbeddedAttr) string {
	var b strings.Builder
	for _, s := range ea.Segments {
		if t, ok := s.(*ast.Text); ok {
			b.WriteString(t.Value)
		}
	}
	return b.String()
}

func TestEmbeddedTextEscapedHole(t *testing.T) {
	src := "package p\ncomponent C(v string) { <span data-x=`lit \\@{ not a hole } @{v}`>h</span> }\n"
	f, err := ParseFile(token.NewFileSet(), "in.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ea := firstEmbeddedAttr(t, f)
	// exactly one real hole (@{v}); the \@{ is literal text
	holes := 0
	for _, s := range ea.Segments {
		if _, ok := s.(*ast.Interp); ok {
			holes++
		}
	}
	if holes != 1 {
		t.Fatalf("holes = %d, want 1 (\\@{ must be literal)", holes)
	}
	// literal text must contain "@{ not a hole }" with the backslash removed
	got := embeddedText(ea)
	if strings.Contains(got, "\\@{") {
		t.Fatalf("literal text %q still contains the escaping backslash; \\@{ must unescape to @{", got)
	}
	if want := "lit @{ not a hole } "; got != want {
		t.Fatalf("literal text = %q, want %q", got, want)
	}
}
