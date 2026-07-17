package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPositionalConditionalAttrsPreserveContributorEvaluationOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-run render test in -short mode")
	}
	tmp := tempModule(t, "example.com/branchorder")
	viewsDir := filepath.Join(tmp, "views")
	if err := os.MkdirAll(viewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, viewsDir, "views.gsx", `package views

import (
	"fmt"
	"strings"
	"github.com/gsxhq/gsx"
)

var trace []string

func ResetTrace() { trace = nil }
func Trace() string { return strings.Join(trace, ",") }

func mark(label string) string {
	trace = append(trace, label)
	return label
}

func pair(label string) (string, error) {
	return mark(label), nil
}

func fail(label string) (string, error) {
	mark(label)
	return "", fmt.Errorf("branch failed")
}

component Sink(attrs gsx.Attrs) {
	<div {attrs...}></div>
}

component JSOrder() {
	<Sink { if true {
		data-first={mark("first")}
		onclick=js"call(@{mark("hole")})"
		data-last={mark("last")}
	} }/>
}

component CSSOrder() {
	<Sink { if true {
		data-first={mark("first")}
		style=css"color:@{mark("css")}"
		data-last={mark("last")}
	} }/>
}

component TupleOrder() {
	<Sink { if true {
		data-first={mark("first")}
		data-tuple={pair("tuple")}
		data-last={mark("last")}
	} }/>
}

component ErrorOrder() {
	<Sink { if true {
		data-first={mark("first")}
		data-error={fail("error")}
		data-last={mark("last")}
	} }/>
}
`)

	result, err := GenerateDirs(tmp, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics := result[viewsDir].Diags; len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	for sourcePath, generated := range result[viewsDir].Files {
		base := strings.TrimSuffix(filepath.Base(sourcePath), ".gsx")
		writeFile(t, viewsDir, base+".x.go", string(generated))
	}
	writeFile(t, tmp, "main.go", `package main

import (
	"context"
	"fmt"
	"io"

	"github.com/gsxhq/gsx"
	"example.com/branchorder/views"
)

func run(n gsx.Node) {
	err := n.Render(context.Background(), io.Discard)
	fmt.Printf("%s|%v\n", views.Trace(), err)
	views.ResetTrace()
}

func main() {
	run(views.JSOrder())
	run(views.CSSOrder())
	run(views.TupleOrder())
	run(views.ErrorOrder())
}
`)

	got := goRun(t, tmp)
	want := "first,hole,last|<nil>\n" +
		"first,css,last|<nil>\n" +
		"first,tuple,last|<nil>\n" +
		"first,error|branch failed\n"
	if got != want {
		t.Fatalf("render = %q, want %q", got, want)
	}
}
