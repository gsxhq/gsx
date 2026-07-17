package main

import (
	"bytes"
	"context"
	"fmt"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/playground/playbundle"
)

var pkgLine = regexp.MustCompile(`(?m)^package\s+\w+`)

// splitSources interprets the playground source as a Go-Playground-style txtar:
// if it contains `-- name.gsx --` markers, each file becomes its own entry;
// otherwise the whole source is a single comp.gsx. Every file's package line is
// normalized to `package views`. File names must be a bare `*.gsx` (no `/`, no
// `..`) so writes stay inside the views dir.
func splitSources(gsxSrc string) (map[string][]byte, error) {
	files := parseTxtarFiles([]byte(gsxSrc))
	out := map[string][]byte{}
	if len(files) == 0 {
		out["comp.gsx"] = []byte(pkgLine.ReplaceAllString(gsxSrc, "package views"))
		return out, nil
	}
	for name, data := range files {
		if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
			return nil, fmt.Errorf("invalid file name %q: must be a bare *.gsx", name)
		}
		if !strings.HasSuffix(name, ".gsx") {
			return nil, fmt.Errorf("invalid file name %q: must end in .gsx", name)
		}
		out[name] = []byte(pkgLine.ReplaceAllString(string(data), "package views"))
	}
	return out, nil
}

// parseTxtarFiles parses a txtar-format archive and returns a map of filename
// to file contents. Returns nil if there are no file markers (single-file path).
// This is a minimal inline implementation matching golang.org/x/tools/txtar.
func parseTxtarFiles(data []byte) map[string][]byte {
	// Find first marker; if none, signal the single-file path.
	_, firstName, remaining := txtarFindMarker(data)
	if firstName == "" {
		return nil
	}
	out := map[string][]byte{}
	currentName := firstName
	for {
		fileData, nextName, afterNext := txtarFindMarker(remaining)
		out[currentName] = fileData
		if nextName == "" {
			break
		}
		currentName = nextName
		remaining = afterNext
	}
	return out
}

// txtarFindMarker finds the next `-- name --` marker in data.
// Returns (data before marker, marker name, data after marker line).
// If no marker, returns (data, "", nil).
func txtarFindMarker(data []byte) (before []byte, name string, after []byte) {
	var b []byte
	for len(data) > 0 {
		var line []byte
		if before0, after0, ok := bytes.Cut(data, []byte{'\n'}); ok {
			line, data = before0, after0
		} else {
			line, data = data, nil
		}
		s := string(line)
		if strings.HasPrefix(s, "-- ") && strings.HasSuffix(s, " --") {
			n := strings.TrimSpace(s[3 : len(s)-3])
			if n != "" {
				return b, n, data
			}
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	return b, "", nil
}

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
	"html":                     true,
	"github.com/gsxhq/gsx":     true,
	"github.com/gsxhq/gsx/std": true,
}

// checkImportsSource parses the user-supplied GSX source (as Go) for imports
// and returns a diagnostic naming the first import not on the allowlist, or nil
// if all are allowed. This runs BEFORE Generate so that disallowed imports
// produce the friendly "not allowed" message rather than an opaque infra error
// from the cached importer.
func checkImportsSource(src string) *diagnostic {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "comp.gsx", src, parser.ImportsOnly)
	if err != nil {
		// Parse errors here are fine; the real parse happens in Generate.
		return nil
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
	return nil
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
	resolver *gen.BundledResolver
	gocache  string
	free     chan *workspace
	cache    *respCache
}

type renderReq struct {
	GSX    string `json:"gsx"`
	Invoke string `json:"invoke"`
}

type diagnostic struct {
	Severity  string `json:"severity"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Help      string `json:"help,omitempty"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"endLine,omitempty"`
	EndColumn int    `json:"endColumn,omitempty"`
}

type renderResp struct {
	HTML        string       `json:"html"`
	GeneratedGo string       `json:"generatedGo"`
	Diagnostics []diagnostic `json:"diagnostics"`
	Error       string       `json:"error"`
	Ms          int64        `json:"ms"`
	Cached      bool         `json:"cached"`
	GenMs       int64        `json:"genMs"` // gsx generate (type resolution) time
	RunMs       int64        `json:"runMs"` // go build + run time
}

// toDiagnostics maps the diagnostics in a gen.Result to the server's
// diagnostic type. It avoids importing internal/diag by operating on the
// Result value directly; the internal type's fields are accessible via the
// returned slice elements.
func toDiagnostics(res gen.Result) []diagnostic {
	if len(res.Diags) == 0 {
		return nil
	}
	out := make([]diagnostic, 0, len(res.Diags))
	for _, d := range res.Diags {
		out = append(out, diagnostic{
			Severity:  d.Severity.String(),
			Code:      d.Code,
			Message:   d.Message,
			Help:      d.Help,
			Line:      d.Start.Line,
			Column:    d.Start.Column,
			EndLine:   d.End.Line,
			EndColumn: d.End.Column,
		})
	}
	return out
}

// newPool builds the embedded resolver once, sets up `size` prepared workspaces
// sharing one GOCACHE, and pre-warms the build cache. Workspaces are handed out
// per request, but none supplies source or module state to the resolver.
func newPool(gsxMod, work string, size int) (p *pool, err error) {
	created := work == ""
	if created {
		work, err = os.MkdirTemp("", "gsxpool-")
		if err != nil {
			return nil, err
		}
	}
	defer func() {
		if err != nil && created {
			os.RemoveAll(work)
		}
	}()

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
	p = &pool{gocache: gocache, free: make(chan *workspace, size), cache: newRespCache(512)}
	p.resolver, err = playbundle.NewResolver()
	if err != nil {
		return nil, fmt.Errorf("build embedded resolver: %v", err)
	}

	for i := range size {
		ws := &workspace{play: filepath.Join(work, fmt.Sprintf("play%d", i))}
		ws.viewDir = filepath.Join(ws.play, "views")
		if err = os.MkdirAll(ws.viewDir, 0o755); err != nil {
			return nil, err
		}
		writeFile(filepath.Join(ws.play, "go.mod"), fmt.Sprintf("module gsxplay\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n", gsxMod))
		writeFile(filepath.Join(ws.play, "main.go"), "package main\n\nimport (\n\t\"context\"\n\t\"os\"\n\n\t_ \"github.com/gsxhq/gsx\"\n\t\"gsxplay/views\"\n)\n\nfunc main() {\n\tif err := views.Render(context.Background(), os.Stdout); err != nil {\n\t\tpanic(err)\n\t}\n}\n")
		writeFile(filepath.Join(ws.viewDir, "comp.gsx"), "package views\n\ncomponent Hello() {\n\t<p>hi</p>\n}\n")

		// Seed the workspace from the embedded external type universe. The
		// in-memory source set is complete; codegen does not inspect this
		// workspace's module or source files.
		seedSrc := map[string][]byte{
			"comp.gsx": []byte("package views\n\ncomponent Hello() {\n\t<p>hi</p>\n}\n"),
		}
		seedRes, seedErr := p.resolver.GenerateSources(seedSrc)
		if seedErr != nil {
			return nil, fmt.Errorf("seed generate: %v", seedErr)
		}
		for path, b := range seedRes.Files {
			// Virtual output names are mapped into this disposable workspace for
			// the authentic Go build that follows.
			if !filepath.IsAbs(path) {
				path = filepath.Join(ws.viewDir, filepath.Base(path))
			}
			writeFile(path, string(b))
		}
		writeShim(ws.viewDir, "Hello()")
		var out string
		if out, err = run(context.Background(), ws.play, env, "go", "mod", "tidy"); err != nil {
			return nil, fmt.Errorf("mod tidy: %v: %s", err, out)
		}

		if out, err = run(context.Background(), ws.play, env, "go", "build", "-o", filepath.Join(ws.play, "play-bin"), "."); err != nil {
			return nil, fmt.Errorf("warm build: %v: %s", err, out)
		}
		p.free <- ws
	}
	return p, nil
}

func (p *pool) render(in renderReq) renderResp {
	key := cacheKey(in)
	if r, ok := p.cache.get(key); ok {
		r.Cached = true
		r.Ms = 0 // a cache hit is effectively instant; don't report the original render's time
		return r
	}
	ws := <-p.free // block until a workspace is free (back-pressure)
	defer func() { p.free <- ws }()
	resp := renderIn(p.resolver, p.gocache, ws, in)
	if cacheable(resp) {
		p.cache.put(key, resp)
	}
	return resp
}

// cacheable reports whether a render result is safe to cache: a deterministic
// success (HTML) or a real diagnostic result. Transient failures — timeouts,
// operational errors, or empty output — must NOT be cached, or they would
// poison the cache and serve blank forever.
func cacheable(r renderResp) bool {
	return r.Error == "" && (r.HTML != "" || len(r.Diagnostics) > 0)
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
func renderIn(resolver *gen.BundledResolver, gocache string, ws *workspace, in renderReq) renderResp {
	start := time.Now()
	// 25s leaves headroom under Cloud Run's 30s request timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	env := []string{
		"GOCACHE=" + gocache,
		"GOPROXY=off",
		"GOFLAGS=-mod=mod",
		"CGO_ENABLED=0",
	}

	// Reset the build workspace. GSX source remains solely in memory; only the
	// generated Go and render shim are written below.
	os.RemoveAll(ws.viewDir)
	if err := os.MkdirAll(ws.viewDir, 0o755); err != nil {
		return renderResp{Error: "reset workspace: " + err.Error()}
	}
	ms := func() int64 { return time.Since(start).Milliseconds() }

	srcFiles, splitErr := splitSources(in.GSX)
	if splitErr != nil {
		return renderResp{Error: splitErr.Error(), Ms: ms()}
	}

	// 0) Pre-flight import check on each user source file.
	for _, b := range srcFiles {
		if d := checkImportsSource(string(b)); d != nil {
			return renderResp{Diagnostics: []diagnostic{*d}, Ms: ms()}
		}
	}

	// 1) In-process codegen from the complete in-memory source set and embedded
	// external type universe (no host source discovery or per-render go list).
	genStart := time.Now()
	res, gerr := resolver.GenerateSources(srcFiles)
	genMs := time.Since(genStart).Milliseconds()

	diags := toDiagnostics(res)
	if gerr != nil {
		resp := renderResp{Diagnostics: diags, Ms: ms(), GenMs: genMs}
		if len(diags) == 0 {
			resp.Error = "generate: " + gerr.Error()
		}
		return resp
	}

	// Write generated .x.go files to disk for the build step.
	for path, b := range res.Files {
		if !filepath.IsAbs(path) {
			path = filepath.Join(ws.viewDir, filepath.Base(path))
		}
		writeFile(path, string(b))
	}
	writeShim(ws.viewDir, strings.TrimSpace(in.Invoke))

	generatedGo := readGenerated(ws.viewDir)

	// 1a) Belt-and-suspenders: check generated .x.go for disallowed imports
	// (catches any import the codegen emits that slipped past the source check).
	if d := checkImports(ws.viewDir); d != nil {
		return renderResp{GeneratedGo: generatedGo, Diagnostics: []diagnostic{*d}, Ms: ms(), GenMs: genMs}
	}

	// 2) Authentic build + run.
	runStart := time.Now()
	runOut, runErr := run(ctx, ws.play, env, "go", "run", ".")
	runMs := time.Since(runStart).Milliseconds()
	if runErr != nil {
		return renderResp{GeneratedGo: generatedGo, Diagnostics: diags, Error: "render: " + oneline(runOut), Ms: ms(), GenMs: genMs, RunMs: runMs}
	}
	return renderResp{HTML: runOut, GeneratedGo: generatedGo, Diagnostics: diags, Ms: ms(), GenMs: genMs, RunMs: runMs}
}

func readGenerated(viewDir string) string {
	entries, _ := os.ReadDir(viewDir)
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".x.go") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var sb strings.Builder
	for i, n := range names {
		if i > 0 {
			sb.WriteString("\n")
		}
		b, _ := os.ReadFile(filepath.Join(viewDir, n))
		sb.Write(b)
	}
	return sb.String()
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
