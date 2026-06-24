// Command gsxplayground is a local prototype of the gsx docs render API (the
// future Cloud Run /render handler). It runs the AUTHENTIC gsx pipeline:
// write the edited .gsx into a prepared fixed module, run `gsx generate --json`
// (structured diagnostics) and `go run`, and return the rendered HTML + the
// generated Go.
//
// Safety model (mirrors the planned deployment): a FIXED module — the user only
// supplies a component body and an invoke expression; go.mod is fixed (gsx +
// stdlib), so `go list` runs over a known, safe dependency set. Requests are
// serialized (one shared work dir) and time-bounded.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	addr   = flag.String("addr", ":8080", "listen address")
	gsxMod = flag.String("gsxmod", defaultGsxMod(), "path to the gsx module (used via replace)")
	workIn = flag.String("work", "", "work dir (default: a temp dir)")
)

func defaultGsxMod() string {
	// This file lives at <gsxmod>/playground/server/main.go.
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

var pkgLine = regexp.MustCompile(`(?m)^package\s+\w+`)

// renderer owns the prepared module and serializes requests against it.
type renderer struct {
	mu      sync.Mutex
	work    string // work dir
	play    string // prepared render module dir
	gsxBin  string // built gsx binary
	viewDir string
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

func main() {
	flag.Parse()
	r, err := newRenderer()
	if err != nil {
		log.Fatalf("setup: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/render", r.handleRender)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	log.Printf("gsx playground on %s (gsxmod=%s, work=%s)", *addr, r.gsxBin, r.work)
	log.Fatal(http.ListenAndServe(*addr, cors(mux)))
}

func newRenderer() (*renderer, error) {
	work := *workIn
	if work == "" {
		var err error
		work, err = os.MkdirTemp("", "gsxplay-")
		if err != nil {
			return nil, err
		}
	}
	r := &renderer{work: work, play: filepath.Join(work, "play")}
	r.viewDir = filepath.Join(r.play, "views")
	r.gsxBin = filepath.Join(work, "gsx")

	// Build the gsx binary once.
	if out, err := run(context.Background(), *gsxMod, "go", "build", "-o", r.gsxBin, "./cmd/gsx"); err != nil {
		return nil, fmt.Errorf("build gsx: %v: %s", err, out)
	}

	// Prepare the fixed render module.
	if err := os.MkdirAll(r.viewDir, 0o755); err != nil {
		return nil, err
	}
	writeFile(filepath.Join(r.play, "go.mod"), fmt.Sprintf("module gsxplay\n\ngo 1.23\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => %s\n", *gsxMod))
	writeFile(filepath.Join(r.play, "main.go"), "package main\n\nimport (\n\t\"context\"\n\t\"os\"\n\n\t_ \"github.com/gsxhq/gsx\"\n\t\"gsxplay/views\"\n)\n\nfunc main() {\n\tif err := views.Render(context.Background(), os.Stdout); err != nil {\n\t\tpanic(err)\n\t}\n}\n")
	// Seed component so tidy + warm build succeed.
	writeFile(filepath.Join(r.viewDir, "comp.gsx"), "package views\n\ncomponent Hello() {\n\t<p>hi</p>\n}\n")
	r.writeShim("Hello(HelloProps{})")
	if out, err := run(context.Background(), r.play, "go", "mod", "tidy"); err != nil {
		return nil, fmt.Errorf("mod tidy: %v: %s", err, out)
	}
	if out, err := run(context.Background(), r.play, r.gsxBin, "generate", "./views"); err != nil {
		return nil, fmt.Errorf("seed generate: %v: %s", err, out)
	}
	// Warm GOCACHE so later builds are incremental.
	if out, err := run(context.Background(), r.play, "go", "build", "-o", filepath.Join(work, "play-bin"), "."); err != nil {
		return nil, fmt.Errorf("warm build: %v: %s", err, out)
	}
	return r, nil
}

func (r *renderer) writeShim(invoke string) {
	imp := ""
	if strings.Contains(invoke, "gsx.") {
		imp = "\t\"github.com/gsxhq/gsx\"\n"
	}
	writeFile(filepath.Join(r.viewDir, "render_shim.go"),
		"package views\n\nimport (\n\t\"context\"\n\t\"io\"\n"+imp+")\n\nfunc Render(ctx context.Context, w io.Writer) error {\n\treturn ("+invoke+").Render(ctx, w)\n}\n")
}

func (r *renderer) handleRender(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in renderReq
	if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
		writeJSON(w, renderResp{Error: "bad request: " + err.Error()})
		return
	}
	if len(in.GSX) > 64*1024 {
		writeJSON(w, renderResp{Error: "source too large"})
		return
	}
	if strings.TrimSpace(in.Invoke) == "" {
		in.Invoke = "Hello(HelloProps{})"
	}
	writeJSON(w, r.render(in))
}

func (r *renderer) render(in renderReq) renderResp {
	r.mu.Lock()
	defer r.mu.Unlock()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// Reset and write the user's component + shim.
	os.RemoveAll(r.viewDir)
	os.MkdirAll(r.viewDir, 0o755)
	writeFile(filepath.Join(r.viewDir, "comp.gsx"), pkgLine.ReplaceAllString(in.GSX, "package views"))
	r.writeShim(strings.TrimSpace(in.Invoke))

	ms := func() int64 { return time.Since(start).Milliseconds() }

	// 1) authentic codegen with structured diagnostics.
	genOut, genErr := run(ctx, r.play, r.gsxBin, "generate", "--json", "./views")
	diags := parseDiags(genOut)
	if genErr != nil {
		// Errors are reported as diagnostics; if none parsed, surface raw stderr.
		resp := renderResp{Diagnostics: diags, Ms: ms()}
		if len(diags) == 0 {
			resp.Error = oneline(genOut)
		}
		return resp
	}

	generatedGo := readGenerated(r.viewDir)

	// 2) authentic build + run.
	runOut, runErr := run(ctx, r.play, "go", "run", ".")
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

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeFile(path, content string) {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Printf("write %s: %v", path, err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		h.ServeHTTP(w, r)
	})
}

func oneline(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ") }
