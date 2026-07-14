package codegen

import (
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
	"go/token"
	"go/types"
)

// mustParseComponent parses a single-component .gsx source and returns the
// first *gsxast.Component declaration.
func mustParseComponent(t *testing.T, src string) *gsxast.Component {
	t.Helper()
	fset := token.NewFileSet()
	file, errs := parser.ParseFileWithClassifier(fset, "input.gsx", []byte(src), 0, nil)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			return c
		}
	}
	t.Fatal("no component")
	return nil
}

func TestComponentSignature(t *testing.T) {
	// Same declaration, different body → SAME signature (drop-in variant).
	a := mustParseComponent(t, "package v\ncomponent Icon(name string) {\n\t<span>{ name }</span>\n}\n")
	b := mustParseComponent(t, "package v\ncomponent Icon(name string) {\n\t<b>{ name }</b>\n}\n")
	if componentSignature(a) != componentSignature(b) {
		t.Fatalf("same-declaration variants must share a signature:\n a=%q\n b=%q", componentSignature(a), componentSignature(b))
	}

	// Different prop type → DIFFERENT signature.
	c := mustParseComponent(t, "package v\ncomponent Icon(name int) {\n\t<span>{ name }</span>\n}\n")
	if componentSignature(a) == componentSignature(c) {
		t.Fatalf("different prop type must differ: %q", componentSignature(a))
	}

	// Verbatim parameter order is part of the Go contract.
	d := mustParseComponent(t, "package v\ncomponent Icon(x string, y string) { <i/> }\n")
	e := mustParseComponent(t, "package v\ncomponent Icon(y string, x string) { <i/> }\n")
	if componentSignature(d) == componentSignature(e) {
		t.Fatal("parameter order must affect the verbatim component contract")
	}

	// Grouping is not part of the logical parameter declaration.
	f := mustParseComponent(t, "package v\ncomponent Icon(x, y string) { <i/> }\n")
	if componentSignature(d) != componentSignature(f) {
		t.Fatal("grouped and ungrouped logical parameters must match")
	}

	// Name, variadic position, receiver type, and constraint spelling are all
	// component-contract identity.
	rename := mustParseComponent(t, "package v\ncomponent Icon(label string) { <i/> }\n")
	if componentSignature(a) == componentSignature(rename) {
		t.Fatal("parameter rename must change the contract")
	}
	slice := mustParseComponent(t, "package v\ncomponent Icon(values []string) { <i/> }\n")
	variadic := mustParseComponent(t, "package v\ncomponent Icon(values ...string) { <i/> }\n")
	if componentSignature(slice) == componentSignature(variadic) {
		t.Fatal("slice and variadic parameters must differ")
	}
	recvA := mustParseComponent(t, "package v\ncomponent (a A) Icon(name string) { <i/> }\n")
	recvB := mustParseComponent(t, "package v\ncomponent (b B) Icon(name string) { <i/> }\n")
	if componentSignature(recvA) == componentSignature(recvB) {
		t.Fatal("receiver type must change the contract")
	}
	recvRename := mustParseComponent(t, "package v\ncomponent (other A) Icon(name string) { <i/> }\n")
	if componentSignature(recvA) != componentSignature(recvRename) {
		t.Fatal("receiver variable name must not change the contract")
	}
	constraintA := mustParseComponent(t, "package v\ncomponent Icon[T ~int](value T) { <i/> }\n")
	constraintB := mustParseComponent(t, "package v\ncomponent Icon[T int](value T) { <i/> }\n")
	if componentSignature(constraintA) == componentSignature(constraintB) {
		t.Fatal("constraint spelling must change the contract")
	}
	typeParamsGrouped := mustParseComponent(t, "package v\ncomponent Icon[T, U any](left T, right U) { <i/> }\n")
	typeParamsUngrouped := mustParseComponent(t, "package v\ncomponent Icon[T any, U any](left T, right U) { <i/> }\n")
	if componentSignature(typeParamsGrouped) != componentSignature(typeParamsUngrouped) {
		t.Fatal("grouped and ungrouped logical type parameters must match")
	}

	// Reserved roles come only from the declaration, never body free-use.
	bodyChildren := mustParseComponent(t, "package v\ncomponent Box() { <div>{ children }</div> }\n")
	bodyPlain := mustParseComponent(t, "package v\ncomponent Box() { <div/> }\n")
	if componentSignature(bodyChildren) != componentSignature(bodyPlain) {
		t.Fatal("body-only changes must not affect declaration identity")
	}
}

func TestComponentSignatureMalformedFallbackIsCollisionSafe(t *testing.T) {
	a := &gsxast.Component{Recv: "a", TypeParams: "bc", Params: ""}
	b := &gsxast.Component{Recv: "ab", TypeParams: "c", Params: ""}
	if componentSignature(a) == componentSignature(b) {
		t.Fatal("raw fallback fields must be length-prefixed")
	}
}

func TestDetectSignatureConflicts(t *testing.T) {
	filesOf := func(srcs map[string]string) map[string]*gsxast.File {
		out := map[string]*gsxast.File{}
		for name, src := range srcs {
			fset := token.NewFileSet()
			f, errs := parser.ParseFileWithClassifier(fset, name, []byte(src), 0, nil)
			if len(errs) > 0 {
				t.Fatalf("%s: %v", name, errs)
			}
			out[name] = f
		}
		return out
	}

	// Same name, same signature, different files → NO conflict (tolerated variant).
	same := filesOf(map[string]string{
		"a.gsx": "package v\ncomponent Icon(name string) { <a>{ name }</a> }\n",
		"b.gsx": "package v\ncomponent Icon(name string) { <b>{ name }</b> }\n",
	})
	if got := detectSignatureConflicts(same); len(got) != 0 {
		t.Fatalf("same-sig variants: want 0 conflicts, got %d", len(got))
	}

	// Same name, DIFFERENT signature, different files → conflict.
	diff := filesOf(map[string]string{
		"a.gsx": "package v\ncomponent Icon(name string) { <a>{ name }</a> }\n",
		"b.gsx": "package v\ncomponent Icon(name int) { <b>{ name }</b> }\n",
	})
	got := detectSignatureConflicts(diff)
	if len(got) != 1 || got[0].key != ".Icon" || len(got[0].comps) != 2 {
		t.Fatalf("diff-sig: want 1 conflict on .Icon with 2 comps, got %+v", got)
	}

	// Same name twice in ONE file → NOT our conflict (within-file; left to raw error).
	within := filesOf(map[string]string{
		"a.gsx": "package v\ncomponent Icon(name string) { <a/> }\ncomponent Icon(name int) { <b/> }\n",
	})
	if got := detectSignatureConflicts(within); len(got) != 0 {
		t.Fatalf("within-file dup: want 0 conflicts, got %d", len(got))
	}
}

func TestSuppressCrossFileRedeclarations(t *testing.T) {
	fset := token.NewFileSet()
	fa := fset.AddFile("a.x.go", -1, 100)
	fb := fset.AddFile("b.x.go", -1, 100)
	posA := fa.Pos(10)
	posB := fb.Pos(10)

	// Cross-file redeclaration of Icon → both dropped.
	// Within-file redeclaration of Dup (both in a.x.go) → both kept.
	// An unrelated type error → kept.
	posA2 := fa.Pos(40)
	posA3 := fa.Pos(60)
	errs := []types.Error{
		{Fset: fset, Pos: posB, Msg: "Icon redeclared in this block"},
		{Fset: fset, Pos: posA, Msg: "other declaration of Icon"},
		{Fset: fset, Pos: posA2, Msg: "Dup redeclared in this block"},
		{Fset: fset, Pos: posA3, Msg: "other declaration of Dup"},
		{Fset: fset, Pos: posA, Msg: "undefined: Whatever"},
	}
	// Facts as the skeleton ASTs would report them: Icon is a pure cross-file
	// variant (one decl in each of a/b); Dup is a within-file duplicate (twice
	// in a.x.go), so its errors must be kept.
	facts := redeclFacts{
		crossFile: map[string]bool{"Icon": true, "Dup": false},
		withinDup: map[string]bool{"Dup": true},
	}
	got := suppressCrossFileRedeclarations(errs, facts)

	var msgs []string
	for _, e := range got {
		msgs = append(msgs, e.Msg)
	}
	// Icon pair gone; Dup pair + undefined kept.
	for _, e := range got {
		if strings.Contains(e.Msg, "Icon") {
			t.Fatalf("cross-file Icon redeclaration should be suppressed, got %q", e.Msg)
		}
	}
	if len(got) != 3 {
		t.Fatalf("want 3 kept (2 Dup + 1 undefined), got %d: %v", len(got), msgs)
	}
}
