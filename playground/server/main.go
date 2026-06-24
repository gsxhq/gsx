// Command gsxplayground is a local prototype of the gsx docs render API (the
// future Cloud Run /render handler). It runs the AUTHENTIC gsx pipeline:
// write the edited .gsx into a prepared fixed module, run `gsx generate --json`
// (structured diagnostics) and `go run`, and return the rendered HTML + the
// generated Go.
//
// Safety model (mirrors the planned deployment): a FIXED module — the user only
// supplies a component body and an invoke expression; go.mod is fixed (gsx +
// stdlib), so `go list` runs over a known, safe dependency set. Requests are
// handled in isolated workspaces from a bounded pool and are time-bounded.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

var (
	addr        = flag.String("addr", ":8080", "listen address")
	gsxMod      = flag.String("gsxmod", defaultGsxMod(), "path to the gsx module (used via replace)")
	workIn      = flag.String("work", "", "work dir (default: a temp dir)")
	concurrency = flag.Int("concurrency", 4, "number of parallel workspaces in the pool")
)

func main() {
	flag.Parse()
	poolSize := *concurrency
	p, err := newPool(*gsxMod, *workIn, poolSize)
	if err != nil {
		log.Fatalf("setup: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/render", makeRenderHandler(p))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	log.Printf("gsx playground on %s (gsxmod=%s, pool=%d)", *addr, p.gsxBin, poolSize)
	log.Fatal(http.ListenAndServe(*addr, cors(mux)))
}

func makeRenderHandler(p *pool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
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
		writeJSON(w, p.render(in))
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
