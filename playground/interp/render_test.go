package interp

import (
	"strings"
	"testing"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"

	"github.com/gsxhq/gsx/playground/playbundle"
)

// TestInterpretGeneratedComponentToHTML is the HTML-preview feasibility spike:
// transform a gsx snippet to Go (via the embedded bundle, no subprocess), then
// INTERPRET the generated component with yaegi + gsx/std reflection bindings and
// render it to HTML — no Go compiler. If this works, the playground's live
// preview can run entirely client-side in WASM.
func TestInterpretGeneratedComponentToHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping interpreter spike in -short mode")
	}

	// 1. gsx -> generated Go.
	r, err := playbundle.NewResolver()
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	const snippet = `package main

component Greeting(name string) {
	<p>Hello { name |> upper }!</p>
}
`
	res, err := r.GenerateSource("source.gsx", []byte(snippet))
	if err != nil {
		t.Fatalf("transform: %v (diags=%v)", err, res.Diags)
	}
	var generatedGo string
	for _, b := range res.Files {
		generatedGo = string(b)
	}

	// 2. Interpret the generated component and render it.
	i := interp.New(interp.Options{})
	if err := i.Use(stdlib.Symbols); err != nil {
		t.Fatalf("Use(stdlib): %v", err)
	}
	if err := i.Use(Symbols); err != nil {
		t.Fatalf("Use(gsx symbols): %v", err)
	}
	if _, err := i.Eval(generatedGo); err != nil {
		t.Fatalf("eval generated component: %v", err)
	}

	// context is already imported (and in scope) from the generated file's eval;
	// only strings is new.
	const driver = `import "strings"

func renderHTML() string {
	var b strings.Builder
	Greeting(GreetingProps{Name: "World"}).Render(context.Background(), &b)
	return b.String()
}
`
	if _, err := i.Eval(driver); err != nil {
		t.Fatalf("eval driver: %v", err)
	}
	v, err := i.Eval("renderHTML()")
	if err != nil {
		t.Fatalf("call renderHTML: %v", err)
	}
	html := v.String()
	if !strings.Contains(html, "Hello WORLD") {
		t.Fatalf("interpreted HTML = %q, want it to contain 'Hello WORLD'", html)
	}
	t.Logf("interpreted HTML: %s", html)
}
