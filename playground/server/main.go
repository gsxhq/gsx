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
	"os"
	"strings"
	"time"
)

var (
	addr        = flag.String("addr", ":8080", "listen address")
	gsxMod      = flag.String("gsxmod", defaultGsxMod(), "path to the gsx module (used via replace)")
	workIn      = flag.String("work", "", "work dir (default: a temp dir)")
	concurrency = flag.Int("concurrency", 4, "number of parallel workspaces in the pool")
	prewarm     = flag.Bool("prewarm", false, "build pool (warm GOCACHE + work dir) and exit 0 without serving")
)

func main() {
	flag.Parse()

	if *prewarm {
		if _, err := newPool(*gsxMod, *workIn, *concurrency); err != nil {
			log.Fatalf("prewarm: %v", err)
		}
		log.Println("prewarm complete")
		return
	}

	poolSize := *concurrency
	p, err := newPool(*gsxMod, *workIn, poolSize)
	if err != nil {
		log.Fatalf("setup: %v", err)
	}

	// PORT env is set by Cloud Run; fall back to -addr flag.
	listenAddr := *addr
	if port := os.Getenv("PORT"); port != "" {
		listenAddr = ":" + port
	}

	// Warm the response cache with the default examples in the background so the
	// server starts listening immediately; presets become instant shortly after.
	go p.seedPresets()

	sem := make(chan struct{}, *concurrency)

	mux := http.NewServeMux()
	mux.HandleFunc("/render", makeRenderHandler(p))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	log.Printf("gsx playground on %s (gsxmod=%s, pool=%d)", listenAddr, *gsxMod, poolSize)
	log.Fatal(http.ListenAndServe(listenAddr, loggingMiddleware(cors(withLimits(mux, 64*1024, sem)))))
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
			if strings.Contains(err.Error(), "request body too large") {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
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

func withLimits(next http.Handler, maxBody int64, sem chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func cors(h http.Handler) http.Handler {
	origin := os.Getenv("ALLOWED_ORIGIN")
	if origin == "" {
		origin = "*"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		h.ServeHTTP(w, r)
	})
}

// loggingMiddleware wraps an http.Handler with structured per-request logging.
func loggingMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h.ServeHTTP(rw, r)
		log.Printf("method=%s path=%s status=%d ms=%d",
			r.Method, r.URL.Path, rw.code, time.Since(start).Milliseconds())
	})
}

// statusRecorder captures the status code written to a ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func oneline(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ") }
