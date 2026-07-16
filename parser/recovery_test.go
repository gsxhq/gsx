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

func TestPackageClauseRequiresNameOnSameLine(t *testing.T) {
	src := "package\n\ncomponent C() {\n\t<p>hi</p>\n}\n"
	fset := token.NewFileSet()
	_, errs := ParseFileWithClassifier(fset, "badpkg.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) == 0 {
		t.Fatal("expected a package-clause parse error")
	}
	if !strings.Contains(errs[0].Msg, "malformed package clause") {
		t.Fatalf("message = %q, want malformed package clause", errs[0].Msg)
	}
	pos := fset.Position(errs[0].Pos)
	if pos.Line != 1 || pos.Column != 1 {
		t.Fatalf("position = %d:%d, want 1:1", pos.Line, pos.Column)
	}
}

func TestPipelineStageTrailingTextErrorRange(t *testing.T) {
	src := `package views

component Meter(value int, color string) {
	<div
		class={ "meter", "meter-full": value >= 100 }
		style={ value |> printf("width: %d%%"); "color: " + color }
	/>
}
`
	fset := token.NewFileSet()
	_, errs := ParseFileWithClassifier(fset, "meter.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) == 0 {
		t.Fatal("expected a parse error")
	}
	gotStart := fset.Position(errs[0].Pos).Offset
	gotEnd := fset.Position(errs[0].End).Offset
	wantStart := strings.Index(src, `; "color: " + color`)
	wantEnd := strings.Index(src, ` }`+"\n\t/>")
	if gotStart != wantStart || gotEnd != wantEnd {
		t.Fatalf("range offsets = %d..%d, want %d..%d (%q)", gotStart, gotEnd, wantStart, wantEnd, src[wantStart:wantEnd])
	}
}

func TestPipelineStageInvalidNameErrorRange(t *testing.T) {
	src := `package views

component C(value string) {
	<p>{ value |> 123 }</p>
}
`
	fset := token.NewFileSet()
	_, errs := ParseFileWithClassifier(fset, "badpipe.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) == 0 {
		t.Fatal("expected a parse error")
	}
	gotStart := fset.Position(errs[0].Pos).Offset
	gotEnd := fset.Position(errs[0].End).Offset
	wantStart := strings.Index(src, `123`)
	wantEnd := wantStart + len(`123`)
	if gotStart != wantStart || gotEnd != wantEnd {
		t.Fatalf("range offsets = %d..%d, want %d..%d (%q)", gotStart, gotEnd, wantStart, wantEnd, src[wantStart:wantEnd])
	}
}

func TestPipelineStageEmptyErrorPosition(t *testing.T) {
	src := `package views

component C(value string) {
	<p>{ value |> }</p>
}
`
	fset := token.NewFileSet()
	_, errs := ParseFileWithClassifier(fset, "emptypipe.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) == 0 {
		t.Fatal("expected a parse error")
	}
	got := fset.Position(errs[0].Pos).Offset
	want := strings.Index(src, `|>`) + len(`|>`)
	if got != want {
		t.Fatalf("position offset = %d, want %d", got, want)
	}
}

func TestUnterminatedStaticAttrStringErrorRange(t *testing.T) {
	src := `package views

component C() {
	<div title="hi />
}
`
	fset := token.NewFileSet()
	_, errs := ParseFileWithClassifier(fset, "badattr.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) == 0 {
		t.Fatal("expected a parse error")
	}
	gotStart := fset.Position(errs[0].Pos).Offset
	gotEnd := fset.Position(errs[0].End).Offset
	wantStart := strings.Index(src, `"hi />`)
	if gotStart != wantStart || gotEnd <= gotStart {
		t.Fatalf("range offsets = %d..%d, want non-empty range starting at %d", gotStart, gotEnd, wantStart)
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

func TestEmptySpreadRequiresExpression(t *testing.T) {
	cases := []struct {
		name string
		tag  string
	}{
		{name: "component compact", tag: "<C {...}/>"},
		{name: "component spaced", tag: "<C { ... }/>"},
		{name: "element compact", tag: "<div {...}></div>"},
		{name: "element multiline", tag: "<div {\n\t...\n}></div>"},
		{name: "element CRLF multiline", tag: "<div {\r\n\t...\r\n}></div>"},
		{name: "conditional then", tag: "<C { if ok { {\n\t...\n} } }/>"},
		{name: "conditional else", tag: `<div { if ok { id="x" } else { {...} } }></div>`},
		{name: "after valid spread", tag: `<div {attrs...} data-x="..." { ... }></div>`},
		{name: "comment only", tag: `<C { /* no expression */ ... }/>`},
		{name: "line comment only", tag: "<C { // no expression\n ... }/>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\ncomponent Page(ok bool, attrs map[string]any) { " + tc.tag + " }\n"
			fset := token.NewFileSet()
			_, errs := ParseFileWithClassifier(fset, "empty_spread.gsx", []byte(src), 0, attrclass.Builtin())
			if len(errs) != 1 {
				t.Fatalf("errors = %+v, want exactly one empty-spread error", errs)
			}
			if got, want := errs[0].Msg, "spread attribute requires an expression before `...`"; got != want {
				t.Fatalf("message = %q, want %q", got, want)
			}
			start := fset.Position(errs[0].Pos).Offset
			end := fset.Position(errs[0].End).Offset
			wantStart := strings.LastIndex(src, "...")
			if start != wantStart || end != wantStart+3 {
				t.Fatalf("error range = %d..%d, want ellipsis %d..%d", start, end, wantStart, wantStart+3)
			}
		})
	}
}

func TestEmptySpreadRecoverySkipsBrokenComponentOnly(t *testing.T) {
	src := `package p

component Bad() { <div {...}></div> }

func helper() string { return "kept" }

component Good() { <p>ok</p> }
`
	f, errs := ParseFileWithClassifier(token.NewFileSet(), "empty_spread_recovery.gsx", []byte(src), 0, attrclass.Builtin())
	if len(errs) != 1 || errs[0].Msg != "spread attribute requires an expression before `...`" {
		t.Fatalf("errors = %+v, want one precise empty-spread error", errs)
	}
	var componentNames []string
	var goSource strings.Builder
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.Component:
			componentNames = append(componentNames, decl.Name)
		case *ast.GoChunk:
			goSource.WriteString(decl.Src)
		default:
			t.Fatalf("broken component residue survived recovery as %T", decl)
		}
	}
	if len(componentNames) != 1 || componentNames[0] != "Good" {
		t.Fatalf("components = %v, want [Good]", componentNames)
	}
	if !strings.Contains(goSource.String(), `func helper() string { return "kept" }`) {
		t.Fatalf("top-level Go after broken component was swallowed: %q", goSource.String())
	}
}

func TestSpreadExpressionAndPipelineRemainValid(t *testing.T) {
	for _, tag := range []string{
		"<C {attrs...}/>",
		"<C { (attrs |> normalize)... }/>",
		"<C { attrs /* keep this comment */ ... }/>",
		`<C { "/* string content */"... }/>`,
		"<C { `/* raw string content */`... }/>",
		"<C { if ok { {attrs...} } else { { (fallback |> normalize)... } } }/>",
		"<div { attrs... }></div>",
		"<div { attrs |> normalize... }></div>",
	} {
		src := "package p\ncomponent Page() { " + tag + " }\n"
		if _, err := ParseFile(token.NewFileSet(), "spread.gsx", []byte(src), 0); err != nil {
			t.Errorf("%s: %v", tag, err)
		}
	}
}
