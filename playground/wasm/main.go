//go:build js && wasm

// Command gsx-wasm is the client-side playground engine. It runs the gsx
// transform (gsx -> Go) and the gsx formatter entirely in the browser — no go
// list, no subprocess — exposing two JS functions:
//
//	gsxTransform(source) -> {files: [{name, code}], diagnostics}
//	gsxFormat(source)    -> {formatted} | {error}
//
// Rendering the generated Go to HTML is NOT done here (a browser has no Go
// compiler, and an interpreter can't handle component composition); the site
// posts the generated files to the playground server's /run instead.
//
// Build: GOOS=js GOARCH=wasm go build -o gsx.wasm ./playground/wasm
package main

import (
	"path/filepath"
	"sort"
	"strings"
	"syscall/js"

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/playground/playbundle"
)

var resolver *gen.CachedResolver

func main() {
	r, err := playbundle.NewResolver()
	if err != nil {
		panic("gsx-wasm: build resolver: " + err.Error())
	}
	resolver = r

	js.Global().Set("gsxTransform", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return transform(stringArg(args, 0))
	}))
	js.Global().Set("gsxFormat", js.FuncOf(func(_ js.Value, args []js.Value) any {
		out, ferr := gen.Format("playground.gsx", []byte(stringArg(args, 0)))
		if ferr != nil {
			return map[string]any{"error": ferr.Error()}
		}
		return map[string]any{"formatted": string(out)}
	}))

	if ready := js.Global().Get("gsxReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}

// transform splits a possibly multi-file playground source, generates Go for all
// files of the package, and returns {files: [{name, code}], diagnostics}.
func transform(src string) any {
	res, _ := resolver.GenerateSources(splitSources(src))

	type genFile struct{ name, code string }
	gens := make([]genFile, 0, len(res.Files))
	for path, b := range res.Files {
		gens = append(gens, genFile{name: filepath.Base(path), code: string(b)})
	}
	sort.Slice(gens, func(i, j int) bool { return gens[i].name < gens[j].name })

	files := make([]any, len(gens))
	for i, g := range gens {
		files[i] = map[string]any{"name": g.name, "code": g.code}
	}
	return map[string]any{"files": files, "diagnostics": jsDiags(res.Diags)}
}

func jsDiags(ds []diag.Diagnostic) []any {
	out := make([]any, len(ds))
	for i, d := range ds {
		out[i] = map[string]any{
			"severity": d.Severity.String(),
			"code":     d.Code,
			"message":  d.Message,
			"help":     d.Help,
			"line":     d.Start.Line,
			"column":   d.Start.Column,
		}
	}
	return out
}

// splitSources splits the Go-Playground-style `-- file.gsx --` multi-file format
// into name -> source. A source with no separators is a single "source.gsx".
func splitSources(src string) map[string][]byte {
	files := map[string][]byte{}
	cur := ""
	var buf []string
	flush := func() {
		if cur != "" {
			files[cur] = []byte(strings.Join(buf, "\n"))
		}
	}
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "-- ") && strings.HasSuffix(t, " --") {
			flush()
			cur = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, "-- "), " --"))
			buf = nil
			continue
		}
		buf = append(buf, ln)
	}
	flush()
	if len(files) == 0 {
		files["source.gsx"] = []byte(src)
	}
	return files
}

func stringArg(args []js.Value, i int) string {
	if i < len(args) && args[i].Type() == js.TypeString {
		return args[i].String()
	}
	return ""
}
