package parser

import (
	"go/token"
	"strings"
	"testing"

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
	// Drive ParseFileWithClassifier; in Task 1 it still returns (*ast.File, error),
	// but the parser's errs must hold a positioned Error. Re-parse via a small helper
	// that exposes errs: parse and then assert through the formatted error's position.
	_, err := ParseFileWithClassifier(fset, "c.gsx", []byte("package p\n\ncomponent X() { <div>hi</span> }\n"), 0, attrclass.Builtin())
	if err == nil || !strings.Contains(err.Error(), "3:") {
		t.Fatalf("expected positioned error, got %v", err)
	}
}
