package diag

import (
	"bytes"
	"go/token"
	"strings"
	"testing"
)

func pos(file string, line, col int) token.Position {
	return token.Position{Filename: file, Line: line, Column: col}
}

func TestSeverityString(t *testing.T) {
	for s, want := range map[Severity]string{Error: "error", Warning: "warning", Info: "info", Hint: "hint"} {
		if got := s.String(); got != want {
			t.Errorf("Severity(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func TestBagErrorfResolvesAndHasErrors(t *testing.T) {
	fset := token.NewFileSet()
	f := fset.AddFile("views.gsx", fset.Base(), 100)
	for i := range 100 {
		if i == 20 || i == 40 {
			f.AddLine(i)
		}
	}
	b := NewBag(fset)
	if b.HasErrors() {
		t.Fatal("new bag should have no errors")
	}
	start := f.Pos(25)
	end := f.Pos(28)
	b.Errorf(start, end, "reserved-param", "param name %q is reserved", "ctx")
	if !b.HasErrors() {
		t.Fatal("bag should report errors after Errorf")
	}
	d := b.Sorted()[0]
	if d.Severity != Error || d.Code != "reserved-param" || d.Source != "codegen" {
		t.Errorf("unexpected diag fields: %+v", d)
	}
	if d.Message != `param name "ctx" is reserved` {
		t.Errorf("message = %q", d.Message)
	}
	if d.Start.Filename != "views.gsx" || d.Start.Line == 0 || d.Start.Column == 0 {
		t.Errorf("start not resolved: %+v", d.Start)
	}
}

func TestSortedByFileLineColumn(t *testing.T) {
	b := &Bag{}
	b.Add(Diagnostic{Start: pos("b.gsx", 1, 1), Severity: Error, Message: "b1"})
	b.Add(Diagnostic{Start: pos("a.gsx", 2, 5), Severity: Error, Message: "a2"})
	b.Add(Diagnostic{Start: pos("a.gsx", 2, 1), Severity: Error, Message: "a1"})
	got := b.Sorted()
	order := []string{got[0].Message, got[1].Message, got[2].Message}
	want := []string{"a1", "a2", "b1"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("sort order = %v, want %v", order, want)
		}
	}
}

func TestRenderCompact(t *testing.T) {
	var buf bytes.Buffer
	RenderCompact(&buf, []Diagnostic{
		{Start: pos("views.gsx", 3, 13), Severity: Error, Code: "reserved-param", Message: "param name \"ctx\" is reserved", Source: "codegen"},
		{Start: pos("views.gsx", 5, 2), Severity: Error, Message: "no code here", Source: "parser"},
	})
	out := buf.String()
	if !strings.Contains(out, "views.gsx:3:13: error[reserved-param]: param name \"ctx\" is reserved\n") {
		t.Errorf("compact with code wrong:\n%s", out)
	}
	if !strings.Contains(out, "views.gsx:5:2: error: no code here\n") {
		t.Errorf("compact without code wrong:\n%s", out)
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, []Diagnostic{
		{Start: pos("views.gsx", 3, 13), End: pos("views.gsx", 3, 16), Severity: Error, Code: "reserved-param", Message: "m", Help: "rename it", Source: "codegen"},
	}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	for _, want := range []string{
		`"file":"views.gsx"`, `"start":{"line":3,"col":13}`, `"end":{"line":3,"col":16}`,
		`"severity":"error"`, `"code":"reserved-param"`, `"help":"rename it"`, `"source":"codegen"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("json missing %s:\n%s", want, s)
		}
	}
	// help/code omitted when empty
	buf.Reset()
	_ = RenderJSON(&buf, []Diagnostic{{Start: pos("a.gsx", 1, 1), Severity: Error, Message: "m"}})
	if strings.Contains(buf.String(), `"help"`) || strings.Contains(buf.String(), `"code"`) {
		t.Errorf("empty help/code must be omitted:\n%s", buf.String())
	}
}
