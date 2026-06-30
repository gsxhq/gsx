//go:build ignore

// recorder is a stand-in for the Vite dev server during the reload probe: it
// records the codegen events and reload pings that `gsx dev` POSTs, so the probe
// script can assert the browser-reload behavior without a real browser/Vite.
//
// Run via the probe script (dev/reload-probe/run.sh); not part of the package
// build (//go:build ignore).
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	port := os.Getenv("REC_PORT")
	mux := http.NewServeMux()
	// gsx dev POSTs the codegen/build event here to drive the error overlay.
	mux.HandleFunc("/__gsx/event", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		ok := "?"
		switch {
		case strings.Contains(s, `"ok":true`):
			ok = "true"
		case strings.Contains(s, `"ok":false`):
			ok = "false"
		}
		fmt.Printf("EVENT ok=%s\n", ok)
		w.WriteHeader(http.StatusNoContent)
	})
	// gsx dev POSTs here to trigger a full browser reload.
	mux.HandleFunc("/__reload", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Println("RELOAD")
		w.WriteHeader(http.StatusNoContent)
	})
	fmt.Fprintf(os.Stderr, "recorder listening on %s\n", port)
	_ = http.ListenAndServe("127.0.0.1:"+port, mux)
}
