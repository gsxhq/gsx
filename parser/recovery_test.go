package parser

import (
	"go/token"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
)

// errorf accumulates a positioned Error and the public ParseFile still returns
// the SAME "line:col: message" text (back-compat for Task 1's pure refactor).
func TestParseFileBackCompatErrorText(t *testing.T) {
	src := "package p\n\ncomponent X() { <div>hi</span> }\n"
	_, err := ParseFile(token.NewFileSet(), "c.gsx", []byte(src), 0)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if !strings.Contains(err.Error(), "mismatched close tag </span>, expected </div>") {
		t.Errorf("message text changed: %q", err.Error())
	}
	// Position prefix preserved in the formatted error.
	if !strings.HasPrefix(err.Error(), "3:") {
		t.Errorf("expected a 3:<col>: position prefix, got %q", err.Error())
	}
}

// The parser accumulates a structured, positioned Error (in-package test can see errs).
func TestErrorfAccumulatesPositioned(t *testing.T) {
	fset := token.NewFileSet()
	// ParseFileWithClassifier now returns (*ast.File, []Error).
	_, errs := ParseFileWithClassifier(fset, "c.gsx", []byte("package p\n\ncomponent X() { <div>hi</span> }\n"), 0, attrclass.Builtin())
	if len(errs) == 0 || !strings.Contains(errs[0].Msg, "mismatched close tag") {
		t.Fatalf("expected positioned error, got %v", errs)
	}
	pos := fset.Position(errs[0].Pos)
	if !pos.IsValid() || pos.Line != 3 {
		t.Fatalf("expected error on line 3, got %v", pos)
	}
}

func TestComponentBoundaryRecovery(t *testing.T) {
	// Two broken components: each has a mismatched close tag. Recovery must
	// report BOTH (one per component), not just the first.
	src := "package p\n\n" +
		"component A() { <div>hi</span> }\n\n" +
		"component B() { <p>yo</b> }\n"
	_, errs := ParseFileWithClassifier(token.NewFileSet(), "c.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) != 2 {
		t.Fatalf("expected 2 recovered errors, got %d: %+v", len(errs), errs)
	}
}

func TestRecoveryKeepsValidComponents(t *testing.T) {
	// A broken component followed by a valid one: the valid one must still be
	// in the returned AST, and exactly one error reported.
	src := "package p\n\n" +
		"component Bad() { <div>hi</span> }\n\n" +
		"component Good() { <p>ok</p> }\n"
	f, errs := ParseFileWithClassifier(token.NewFileSet(), "c.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	var names []string
	for _, d := range f.Decls {
		if c, ok := d.(*ast.Component); ok {
			names = append(names, c.Name)
		}
	}
	if len(names) != 1 || names[0] != "Good" {
		t.Errorf("expected only Good component in AST, got %v", names)
	}
}

func TestParseFileStillReturnsSingleError(t *testing.T) {
	_, err := ParseFile(token.NewFileSet(), "c.gsx", []byte("package p\n\ncomponent X() { <div>hi</span> }\n"), 0)
	if err == nil || !strings.Contains(err.Error(), "3:") {
		t.Fatalf("ParseFile must still return one formatted error, got %v", err)
	}
}
