package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runFile is one generated .x.go file produced by the WASM transform.
type runFile struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

// runReq is the lean render request. The client (WASM) already produced the
// generated Go, so the server only compiles+runs it — no gsx generate. files are
// the generated .x.go (one per source .gsx, all package views); invoke is a Go
// expression yielding the gsx.Node to render (e.g. Page("home")).
type runReq struct {
	Files  []runFile `json:"files"`
	Invoke string    `json:"invoke"`
}

type runResp struct {
	HTML  string `json:"html"`
	Error string `json:"error"`
	Ms    int64  `json:"ms"`
}

func makeRunHandler(p *pool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if req.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var in runReq
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, p.runGenerated(in))
	}
}

func (p *pool) runGenerated(in runReq) runResp {
	if len(in.Files) == 0 || strings.TrimSpace(in.Invoke) == "" {
		return runResp{Error: "files and invoke are required"}
	}
	// Content cache: identical generated Go + invoke renders the same HTML, so
	// the example corpus (run over and over) is served without recompiling. Reuses
	// the LRU shared with /render; "run\x00" keys never collide with /render keys.
	key := runCacheKey(in)
	if r, ok := p.cache.get(key); ok {
		return runResp{HTML: r.HTML} // hit → instant, no build
	}
	ws := <-p.free // back-pressure: block until a workspace frees up
	defer func() { p.free <- ws }()
	resp := runIn(p.gocache, ws, in)
	if resp.Error == "" && resp.HTML != "" { // cache only clean successes
		p.cache.put(key, renderResp{HTML: resp.HTML})
	}
	return resp
}

func runCacheKey(in runReq) string {
	h := sha256.New()
	h.Write([]byte("run\x00"))
	for _, f := range in.Files {
		h.Write([]byte(f.Name))
		h.Write([]byte{0})
		h.Write([]byte(f.Code))
		h.Write([]byte{0})
	}
	h.Write([]byte(in.Invoke))
	return hex.EncodeToString(h.Sum(nil))
}

// runIn compiles and runs the client-supplied generated Go in a warm workspace.
//
// SECURITY: the generated Go is NOT trusted (it comes from the browser, not from
// server-side codegen). checkGeneratedImports is the allowlist boundary — every
// disallowed import (os/exec, net, syscall, …) is rejected before any build. The
// usual sandbox still applies: GOPROXY=off (no network at build), CGO off, a hard
// timeout, and whatever process isolation the deployment provides.
func runIn(gocache string, ws *workspace, in runReq) runResp {
	start := time.Now()
	ms := func() int64 { return time.Since(start).Milliseconds() }

	// SECURITY: validate every submitted file's imports against the allowlist
	// before writing or building anything.
	for _, f := range in.Files {
		if d := checkGeneratedImports(f.Code); d != nil {
			return runResp{Error: d.Message, Ms: ms()}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	env := []string{
		"GOCACHE=" + gocache,
		"GOPROXY=off",
		"GOFLAGS=-mod=mod",
		"CGO_ENABLED=0",
	}

	os.RemoveAll(ws.viewDir)
	if err := os.MkdirAll(ws.viewDir, 0o755); err != nil {
		return runResp{Error: "reset workspace: " + err.Error(), Ms: ms()}
	}
	for i, f := range in.Files {
		// Sanitize to a base .go filename in the views dir; never trust the name.
		name := filepath.Base(f.Name)
		if !strings.HasSuffix(name, ".go") || name == ".go" {
			name = fmt.Sprintf("gen%d.go", i)
		}
		writeFile(filepath.Join(ws.viewDir, name), f.Code)
	}
	writeShim(ws.viewDir, strings.TrimSpace(in.Invoke))

	out, err := run(ctx, ws.play, env, "go", "run", ".")
	if err != nil {
		return runResp{Error: "render: " + oneline(out), Ms: ms()}
	}
	return runResp{HTML: out, Ms: ms()}
}

// checkGeneratedImports is the STRICT allowlist gate for client-submitted code:
// unlike checkImportsSource it rejects on a parse failure rather than allowing it
// through, because nothing downstream re-validates here.
func checkGeneratedImports(src string) *diagnostic {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "submitted.go", src, parser.ImportsOnly)
	if err != nil {
		return &diagnostic{Severity: "error", Message: "playground: cannot parse submitted code: " + err.Error()}
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
