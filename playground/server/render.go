package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var pkgLine = regexp.MustCompile(`(?m)^package\s+\w+`)

// allowedImports is the deny-by-default allowlist for the generated views
// package. It is the union of imports gsx codegen emits and a curated set of
// capability-free stdlib packages safe for a public template playground.
// Anything not listed (net*, os*, os/exec, syscall, unsafe, runtime, "C", ...)
// is rejected, removing the network/exec/filesystem vectors before the program
// is ever built or run.
var allowedImports = map[string]bool{
	"context": true, "io": true, "strconv": true, "fmt": true,
	"strings": true, "time": true, "sort": true, "errors": true,
	"math": true, "math/rand": true, "unicode": true, "unicode/utf8": true,
	"html": true,
	"github.com/gsxhq/gsx":     true,
	"github.com/gsxhq/gsx/std": true,
}

// checkImports parses every generated *.x.go in viewDir for its imports and
// returns a diagnostic naming the first import not on the allowlist, or nil if
// all are allowed. Parsing the GENERATED code is comprehensive: all visitor
// Go-chunk imports flow into the .x.go. Rejecting unsafe also blocks
// //go:linkname; rejecting "C" blocks cgo.
func checkImports(viewDir string) *diagnostic {
	entries, err := os.ReadDir(viewDir)
	if err != nil {
		return &diagnostic{Severity: "error", Message: "playground: cannot inspect generated code: " + err.Error()}
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".x.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(viewDir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			return &diagnostic{Severity: "error", Message: "playground: cannot parse generated code: " + err.Error()}
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !allowedImports[path] {
				p := fset.Position(imp.Pos())
				return &diagnostic{
					Severity: "error",
					Message:  "import " + strconv.Quote(path) + " is not allowed in the playground",
					Line:     p.Line, Column: p.Column,
				}
			}
		}
	}
	return nil
}

type workspace struct {
	play    string
	viewDir string
}

type pool struct {
	gsxBin  string
	gocache string
	free    chan *workspace
}

type renderReq struct {
	GSX    string `json:"gsx"`
	Invoke string `json:"invoke"`
}

type diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

type renderResp struct {
	HTML        string       `json:"html"`
	GeneratedGo string       `json:"generatedGo"`
	Diagnostics []diagnostic `json:"diagnostics"`
	Error       string       `json:"error"`
	Ms          int64        `json:"ms"`
}

// newPool builds gsx once, sets up `size` prepared workspaces sharing one
// GOCACHE, and pre-warms the build cache. Workspaces are handed out per request.
func newPool(gsxMod, work string, size int) (p *pool, err error) {
	created := work == ""
	if created {
		work, err = os.MkdirTemp("", "gsxpool-")
		if err != nil {
			return nil, err
		}
	}
	defer func() { if err != nil && created { os.RemoveAll(work) } }()
	gsxBin := filepath.Join(work, "gsx")
	// one-time gsx bootstrap build; uses the host GOCACHE
	if out, buildErr := run(context.Background(), gsxMod, nil, "go", "build", "-o", gsxBin, "./cmd/gsx"); buildErr != nil {
		return nil, fmt.Errorf("build gsx: %v: %s", buildErr, out)
	}
	gocache := filepath.Join(work, "gocache")
	if err = os.MkdirAll(gocache, 0o755); err != nil {
		return nil, err
	}
	env := []string{
		"GOCACHE=" + gocache,
		"GOPROXY=off",
		"GOFLAGS=-mod=mod",
		"CGO_ENABLED=0",
	}
	p = &pool{gsxBin: gsxBin, gocache: gocache, free: make(chan *workspace, size)}
	for i := 0; i < size; i++ {
		ws := &workspace{play: filepath.Join(work, fmt.Sprintf("play%d", i))}
		ws.viewDir = filepath.Join(ws.play, "views")
		if err = os.MkdirAll(ws.viewDir, 0o755); err != nil {
			return nil, err
		}
		writeFile(filepath.Join(ws.play, "go.mod"), fmt.Sprintf("module gsxplay\n\ngo 1.23\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n", gsxMod))
		writeFile(filepath.Join(ws.play, "main.go"), "package main\n\nimport (\n\t\"context\"\n\t\"os\"\n\n\t_ \"github.com/gsxhq/gsx\"\n\t\"gsxplay/views\"\n)\n\nfunc main() {\n\tif err := views.Render(context.Background(), os.Stdout); err != nil {\n\t\tpanic(err)\n\t}\n}\n")
		writeFile(filepath.Join(ws.viewDir, "comp.gsx"), "package views\n\ncomponent Hello() {\n\t<p>hi</p>\n}\n")
		writeShim(ws.viewDir, "Hello(HelloProps{})")
		var out string
		if out, err = run(context.Background(), ws.play, env, "go", "mod", "tidy"); err != nil {
			return nil, fmt.Errorf("mod tidy: %v: %s", err, out)
		}
		if out, err = run(context.Background(), ws.play, env, gsxBin, "generate", "./views"); err != nil {
			return nil, fmt.Errorf("seed generate: %v: %s", err, out)
		}
		if out, err = run(context.Background(), ws.play, env, "go", "build", "-o", filepath.Join(ws.play, "play-bin"), "."); err != nil {
			return nil, fmt.Errorf("warm build: %v: %s", err, out)
		}
		p.free <- ws
	}
	return p, nil
}

func (p *pool) render(in renderReq) renderResp {
	ws := <-p.free // block until a workspace is free (back-pressure)
	defer func() { p.free <- ws }()
	return renderIn(p.gsxBin, p.gocache, ws, in)
}

// writeShim writes the render shim Go file into viewDir.
func writeShim(viewDir, invoke string) {
	imp := ""
	if strings.Contains(invoke, "gsx.") {
		imp = "\t\"github.com/gsxhq/gsx\"\n"
	}
	writeFile(filepath.Join(viewDir, "render_shim.go"),
		"package views\n\nimport (\n\t\"context\"\n\t\"io\"\n"+imp+")\n\nfunc Render(ctx context.Context, w io.Writer) error {\n\treturn ("+invoke+").Render(ctx, w)\n}\n")
}

// renderIn performs one render cycle in the given workspace.
func renderIn(gsxBin, gocache string, ws *workspace, in renderReq) renderResp {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	env := []string{
		"GOCACHE=" + gocache,
		"GOPROXY=off",
		"GOFLAGS=-mod=mod",
		"CGO_ENABLED=0",
	}

	// Reset and write the user's component + shim.
	os.RemoveAll(ws.viewDir)
	if err := os.MkdirAll(ws.viewDir, 0o755); err != nil {
		return renderResp{Error: "reset workspace: " + err.Error()}
	}
	writeFile(filepath.Join(ws.viewDir, "comp.gsx"), pkgLine.ReplaceAllString(in.GSX, "package views"))
	writeShim(ws.viewDir, strings.TrimSpace(in.Invoke))

	ms := func() int64 { return time.Since(start).Milliseconds() }

	// 1) authentic codegen with structured diagnostics.
	genOut, genErr := run(ctx, ws.play, env, gsxBin, "generate", "--json", "./views")
	diags := parseDiags(genOut)
	if genErr != nil {
		// Errors are reported as diagnostics; if none parsed, surface raw stderr.
		resp := renderResp{Diagnostics: diags, Ms: ms()}
		if len(diags) == 0 {
			resp.Error = oneline(genOut)
		}
		return resp
	}

	generatedGo := readGenerated(ws.viewDir)

	// 1a) import allowlist: reject disallowed imports before build/run.
	if d := checkImports(ws.viewDir); d != nil {
		return renderResp{GeneratedGo: generatedGo, Diagnostics: []diagnostic{*d}, Ms: ms()}
	}

	// 2) authentic build + run.
	runOut, runErr := run(ctx, ws.play, env, "go", "run", ".")
	if runErr != nil {
		return renderResp{GeneratedGo: generatedGo, Diagnostics: diags, Error: "render: " + oneline(runOut), Ms: ms()}
	}
	return renderResp{HTML: runOut, GeneratedGo: generatedGo, Diagnostics: diags, Ms: ms()}
}

// parseDiags decodes `gsx generate --json` output (a JSON array of diagnostics).
func parseDiags(out string) []diagnostic {
	out = strings.TrimSpace(out)
	i := strings.Index(out, "[")
	if i < 0 {
		return nil
	}
	var raw []struct {
		Severity string `json:"severity"`
		Code     string `json:"code"`
		Message  string `json:"message"`
		Range    struct {
			Start struct {
				Line int `json:"line"`
				Col  int `json:"col"`
			} `json:"start"`
		} `json:"range"`
	}
	if err := json.Unmarshal([]byte(out[i:]), &raw); err != nil {
		return nil
	}
	var ds []diagnostic
	for _, r := range raw {
		msg := r.Message
		if r.Code != "" {
			msg = r.Code + ": " + msg
		}
		ds = append(ds, diagnostic{Severity: r.Severity, Message: msg, Line: r.Range.Start.Line, Column: r.Range.Start.Col})
	}
	return ds
}

func readGenerated(viewDir string) string {
	entries, _ := os.ReadDir(viewDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".x.go") {
			b, _ := os.ReadFile(filepath.Join(viewDir, e.Name()))
			return string(b)
		}
	}
	return ""
}

func run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFile(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Printf("write %s: %v", path, err)
	}
}

func defaultGsxMod() string {
	// This file lives at <gsxmod>/playground/server/render.go.
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}
