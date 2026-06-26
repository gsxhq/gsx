package interp

import (
	yaegi "github.com/traefik/yaegi/interp"

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/playground/playbundle"
)

// Diagnostic is a JS-friendly projection of a gsx diagnostic.
type Diagnostic struct {
	Severity string
	Message  string
	Line     int
	Column   int
}

// Result is one playground transform: the generated Go, the rendered HTML (empty
// when generation failed to type-check or rendering errored), and diagnostics.
type Result struct {
	Code        string
	HTML        string
	Diagnostics []Diagnostic
}

// Playground is a reusable, subprocess-free gsx transform+render engine for the
// browser: one embedded-bundle resolver (built once) plus a fresh yaegi
// interpreter per call (so definitions never leak between requests).
type Playground struct {
	resolver *gen.CachedResolver
}

// New builds a Playground from the embedded type bundle — no packages.Load, no
// subprocess. Safe to call in a js/wasm build.
func New() (*Playground, error) {
	r, err := playbundle.NewResolver()
	if err != nil {
		return nil, err
	}
	return &Playground{resolver: r}, nil
}

// Transform generates Go from src and, when generation type-checks cleanly,
// interprets the component expression invoke and renders it to HTML. invoke is a
// Go expression yielding a gsx.Node, e.g. `Greeting(GreetingProps{Name: "World"})`.
func (p *Playground) Transform(src, invoke string) Result {
	res, _ := p.resolver.GenerateSource("source.gsx", []byte(src))
	var code string
	for _, b := range res.Files {
		code = string(b) // single virtual source -> single output
	}
	out := Result{Code: code, Diagnostics: toDiags(res.Diags)}
	if hasError(res.Diags) {
		return out // don't run code that did not type-check
	}
	html, err := interpretRender(code, invoke)
	if err != nil {
		out.Diagnostics = append(out.Diagnostics, Diagnostic{Severity: "error", Message: "render: " + err.Error()})
		return out
	}
	out.HTML = html
	return out
}

// interpretRender interprets generated Go and renders the component expression
// invoke to HTML entirely in-process (no Go toolchain). A fresh interpreter is
// used so prior definitions never leak in.
func interpretRender(generatedGo, invoke string) (string, error) {
	i := yaegi.New(yaegi.Options{})
	// Symbols holds ONLY the playground allowlist (stdlib subset) plus the gsx
	// runtime and std filters — not yaegi's full stdlib — which keeps the wasm
	// binary deployable. The interpreted code can only import the allowlist.
	if err := i.Use(Symbols); err != nil {
		return "", err
	}
	if _, err := i.Eval(generatedGo); err != nil {
		return "", err
	}
	// context is already imported (and in scope) from the generated file; strings
	// is the only new import the render shim needs.
	shim := "import \"strings\"\n\n" +
		"func __gsxRender() string {\n" +
		"\tvar b strings.Builder\n" +
		"\t(" + invoke + ").Render(context.Background(), &b)\n" +
		"\treturn b.String()\n}\n"
	if _, err := i.Eval(shim); err != nil {
		return "", err
	}
	v, err := i.Eval("__gsxRender()")
	if err != nil {
		return "", err
	}
	return v.String(), nil
}

func hasError(ds []diag.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == diag.Error {
			return true
		}
	}
	return false
}

func toDiags(ds []diag.Diagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(ds))
	for _, d := range ds {
		out = append(out, Diagnostic{
			Severity: d.Severity.String(),
			Message:  d.Message,
			Line:     d.Start.Line,
			Column:   d.Start.Column,
		})
	}
	return out
}
