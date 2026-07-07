package lsp

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gsxhq/gsx/internal/diag"
)

// defaultDebounce is how long the server waits for typing to settle before
// re-analyzing a changed package. Matches gopls's diagnosticsDelay default.
const defaultDebounce = 250 * time.Millisecond

// Analyzer computes diagnostics for the package in dir, using override (abs
// .gsx path -> buffer bytes) in place of on-disk content for open documents.
type Analyzer interface {
	Analyze(dir string, override map[string][]byte) (*Package, error)
	// AnalyzeModule analyzes every gsx package in the module containing dir and
	// returns one flat cross-reference list (each component once; Refs span the
	// whole module). Used by find-references; failure is non-fatal (the server
	// falls back to the per-package CrossIndex).
	AnalyzeModule(dir string, override map[string][]byte) ([]CrossRef, error)
	// PrintWidth returns the gsx.toml print width for the given directory
	// (default 80). Used by textDocument/formatting.
	PrintWidth(dir string) int
}

// Server is a stdio LSP server that publishes gsx diagnostics. It owns the
// protocol; code analysis is delegated to an injected Analyzer.
//
// Diagnostics are debounced: a burst of edits resets a per-directory timer and
// only the settled text is analyzed, so typing "|> upper" triggers one analysis
// instead of one per keystroke. The settled analysis then runs on a worker
// goroutine so a heavy package type-check never blocks hover/definition; its
// result is published back on the Run goroutine, and a per-directory generation
// counter discards a result that a newer edit has already superseded.
//
// All mutable state (pkgs, timers, lastURI, gen) and all writes to conn happen
// on the Run goroutine. Worker goroutines only call the (pure) Analyzer on a
// snapshot handed to them and send the result over a channel.
type Server struct {
	conn     *conn
	docs     *docStore
	analyzer Analyzer
	pkgs     map[string]*Package // dir → latest analyzed package
	enc      encoding
	shutdown bool
	exited   bool

	moduleRefs      []CrossRef // whole-module cross-reference index (lazy; find-references)
	moduleRefsValid bool       // false ⇒ rebuild on next references request

	debounce time.Duration
	// schedule arms a timer that calls f after d, returning a cancel func. It is a
	// field so tests can drive debouncing deterministically; production uses
	// time.AfterFunc.
	schedule func(d time.Duration, f func()) (cancel func())
	timers   map[string]func() // dir → cancel of the pending debounce timer
	lastURI  map[string]string // dir → most recently edited URI (fallback for positionless diags)
	fireC    chan string       // a dir whose debounce elapsed; drained by Run

	gen      map[string]int      // dir → generation of the latest requested analysis
	resultsC chan analysisResult // completed worker analyses; drained by Run
	doneC    chan struct{}       // closed when Run returns; releases blocked workers
}

// analysisResult is one worker's finished analysis, routed back to the Run
// goroutine for publishing. gen identifies which request it answers, so a result
// superseded by a newer edit can be discarded.
type analysisResult struct {
	dir         string
	gen         int
	fallbackURI string
	snap        map[string]docSnap
	pkg         *Package
	err         error
}

// NewServer builds a Server reading requests from r and writing responses and
// notifications to w. The default position encoding is UTF-16 until initialize
// negotiates otherwise.
func NewServer(r io.Reader, w io.Writer, a Analyzer) *Server {
	return &Server{
		conn:     newConn(r, w),
		docs:     newDocStore(),
		analyzer: a,
		pkgs:     map[string]*Package{},
		enc:      encUTF16,
		debounce: defaultDebounce,
		schedule: func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		},
		timers:   map[string]func(){},
		lastURI:  map[string]string{},
		fireC:    make(chan string, 16),
		gen:      map[string]int{},
		resultsC: make(chan analysisResult, 16),
	}
}

// readResult is one frame (or terminal error) forwarded from the reader
// goroutine to the Run loop.
type readResult struct {
	f   frame
	err error
}

// Run reads and dispatches messages until the stream closes or an `exit`
// notification is received. A reader goroutine forwards frames over a channel so
// the loop can also wake on debounce-timer fires and completed worker analyses;
// all message handling and publishing stay on this single goroutine.
func (s *Server) Run() error {
	frames := make(chan readResult)
	s.doneC = make(chan struct{})
	defer close(s.doneC) // stop the reader and release blocked workers when we return
	go s.readLoop(frames, s.doneC)

	for {
		select {
		case rr := <-frames:
			if errors.Is(rr.err, io.EOF) {
				return nil
			}
			if rr.err != nil {
				return rr.err
			}
			if err := s.handle(rr.f); err != nil {
				return err
			}
			if s.exited {
				return nil
			}
		case dir := <-s.fireC:
			// Debounce elapsed for dir: drop its timer and analyze the settled text
			// on a worker so the loop stays responsive during the type-check.
			delete(s.timers, dir)
			s.launchAnalysis(dir, s.lastURI[dir])
		case res := <-s.resultsC:
			// Discard a result a newer edit has already superseded; else publish it.
			if res.gen != s.gen[res.dir] {
				continue
			}
			if err := s.publishAnalysis(res.dir, res.fallbackURI, res.snap, res.pkg, res.err); err != nil {
				return err
			}
		}
	}
}

// readLoop reads frames until the stream closes or Run signals done, forwarding
// each over out. It is the only goroutine other than Run, and it touches no
// server state.
func (s *Server) readLoop(out chan<- readResult, done <-chan struct{}) {
	for {
		f, err := s.conn.read()
		select {
		case out <- readResult{f: f, err: err}:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) handle(f frame) error {
	switch f.Method {
	case "initialize":
		return s.handleInitialize(f)
	case "initialized":
		return nil
	case "shutdown":
		s.shutdown = true
		return s.reply(f.ID, nil)
	case "exit":
		s.exited = true
		return nil
	case "textDocument/didOpen":
		return s.handleDidOpen(f)
	case "textDocument/didChange":
		return s.handleDidChange(f)
	case "textDocument/didClose":
		return s.handleDidClose(f)
	case "textDocument/definition":
		return s.handleDefinition(f)
	case "textDocument/references":
		return s.handleReferences(f)
	case "textDocument/hover":
		return s.handleHover(f)
	case "textDocument/formatting":
		return s.handleFormatting(f)
	case "textDocument/documentSymbol":
		return s.handleDocumentSymbol(f)
	default:
		if len(f.ID) > 0 {
			return s.replyError(f.ID, -32601, "method not found: "+f.Method)
		}
		return nil // ignore unknown notifications
	}
}

func (s *Server) handleInitialize(f frame) error {
	var p initializeParams
	_ = json.Unmarshal(f.Params, &p) // absent or malformed params -> defaults
	s.enc = encUTF16
	encName := "utf-16"
	if slices.Contains(p.Capabilities.General.PositionEncodings, "utf-8") {
		s.enc = encUTF8
		encName = "utf-8"
	}
	return s.reply(f.ID, initializeResult{Capabilities: serverCapabilities{
		PositionEncoding:           encName,
		TextDocumentSync:           1, // full document sync
		DefinitionProvider:         true,
		ReferencesProvider:         true,
		DocumentFormattingProvider: true,
		HoverProvider:              true,
		DocumentSymbolProvider:     true,
	}})
}

func (s *Server) reply(id json.RawMessage, result any) error {
	return s.conn.writeMessage(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, result})
}

func (s *Server) replyError(id json.RawMessage, code int, msg string) error {
	return s.conn.writeMessage(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{code, msg}})
}

func (s *Server) notify(method string, params any) error {
	return s.conn.writeMessage(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params})
}

// invalidateModuleRefs drops the cached whole-module reference index; the next
// references request rebuilds it. Any document mutation may change references.
func (s *Server) invalidateModuleRefs() {
	s.moduleRefs = nil
	s.moduleRefsValid = false
}

func (s *Server) handleDidOpen(f frame) error {
	var p didOpenParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	s.invalidateModuleRefs()
	s.docs.open(p.TextDocument.URI, p.TextDocument.Text, p.TextDocument.Version)
	uri := p.TextDocument.URI
	dir := filepath.Dir(uriToPath(uri))
	s.lastURI[dir] = uri
	s.gen[dir]++ // supersede any in-flight worker for this dir
	if strings.HasSuffix(uriToPath(uri), ".go") {
		s.analyzeOnly(uri) // no diagnostics for .go; gopls owns those
		return nil
	}
	// Open is not debounced: the file just appeared, so publish promptly. It runs
	// inline (a one-shot, unlike the bursty edit path) but is gen-guarded above so
	// a slower in-flight edit analysis cannot clobber it.
	return s.analyzeAndPublishDir(dir, uri)
}

func (s *Server) handleDidChange(f frame) error {
	var p didChangeParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	if len(p.ContentChanges) == 0 {
		return nil
	}
	s.invalidateModuleRefs()
	// Full-document sync: the last change carries the whole new text.
	text := p.ContentChanges[len(p.ContentChanges)-1].Text
	s.docs.update(p.TextDocument.URI, text, p.TextDocument.Version)
	uri := p.TextDocument.URI
	dir := filepath.Dir(uriToPath(uri))
	s.lastURI[dir] = uri
	if strings.HasSuffix(uriToPath(uri), ".go") {
		s.analyzeOnly(uri) // no diagnostics for .go; gopls owns those
		return nil
	}
	// Debounce: reset the dir's timer so a burst of keystrokes yields one analysis
	// of the settled text, not one per character.
	if cancel := s.timers[dir]; cancel != nil {
		cancel()
	}
	s.timers[dir] = s.schedule(s.debounce, func() { s.fireC <- dir })
	return nil
}

// analyzeOnly analyzes the package for the changed URI and stores the result in
// s.pkgs, WITHOUT publishing diagnostics. Used for .go files (gopls owns .go
// diagnostics) so gsx-LSP can still answer component definition/references.
func (s *Server) analyzeOnly(changedURI string) {
	dir := filepath.Dir(uriToPath(changedURI))
	openDocs := s.docs.openInDir(dir)
	override := make(map[string][]byte, len(openDocs))
	for path, text := range openDocs {
		override[path] = []byte(text)
	}
	if pkg, err := s.analyzer.Analyze(dir, override); err == nil && pkg != nil {
		s.pkgs[dir] = pkg
	}
}

func (s *Server) handleDidClose(f frame) error {
	var p didCloseParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	s.invalidateModuleRefs()
	s.docs.close(p.TextDocument.URI)
	s.gen[filepath.Dir(uriToPath(p.TextDocument.URI))]++ // supersede any in-flight worker
	// Clear diagnostics for the now-closed document.
	return s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: p.TextDocument.URI, Diagnostics: []Diagnostic{}})
}

// snapshotOverride captures dir's open documents (text+version) and the override
// map the analyzer consumes. Both come from one atomic docStore snapshot so the
// version a publish is tagged with matches exactly the bytes that were analyzed.
func (s *Server) snapshotOverride(dir string) (map[string]docSnap, map[string][]byte) {
	snap := s.docs.snapshotDir(dir) // abs path -> {text, version}
	override := make(map[string][]byte, len(snap))
	for path, d := range snap {
		override[path] = []byte(d.text)
	}
	return snap, override
}

// launchAnalysis bumps dir's generation and runs the analysis on a worker so the
// Run loop stays responsive during a heavy type-check. The worker sends its
// result back to Run, which publishes it only if no newer edit superseded it.
func (s *Server) launchAnalysis(dir, fallbackURI string) {
	s.gen[dir]++
	g := s.gen[dir]
	snap, override := s.snapshotOverride(dir)
	go func() {
		pkg, err := s.analyzer.Analyze(dir, override)
		select {
		case s.resultsC <- analysisResult{dir: dir, gen: g, fallbackURI: fallbackURI, snap: snap, pkg: pkg, err: err}:
		case <-s.doneC: // Run has exited; drop the result
		}
	}()
}

// analyzeAndPublishDir analyzes dir inline (on the Run goroutine) and publishes.
// Used for the one-shot didOpen path; the bursty edit path goes through
// launchAnalysis instead.
func (s *Server) analyzeAndPublishDir(dir, fallbackURI string) error {
	snap, override := s.snapshotOverride(dir)
	pkg, err := s.analyzer.Analyze(dir, override)
	return s.publishAnalysis(dir, fallbackURI, snap, pkg, err)
}

// publishAnalysis publishes diagnostics for every open document in dir (empty
// list when a document is now clean, so stale squiggles never linger). Each
// publish carries the open document's version so the editor can drop a
// stale-version result. Diagnostics that carry no filename are attached to
// fallbackURI (the most recently edited file) at its start. A nil pkg or non-nil
// err means analysis failed (e.g. no go.mod): clear the changed file and move on
// rather than crash the session.
func (s *Server) publishAnalysis(dir, fallbackURI string, snap map[string]docSnap, pkg *Package, err error) error {
	if err != nil || pkg == nil {
		return s.publishDiags(fallbackURI, snap, []Diagnostic{})
	}
	s.pkgs[dir] = pkg
	diags := pkg.Diags

	// Group diagnostics by absolute filename; positionless/foreign ones go to the
	// most recently edited document.
	fallbackPath := uriToPath(fallbackURI)
	byPath := map[string][]diag.Diagnostic{}
	for _, d := range diags {
		key := d.Start.Filename
		if key == "" {
			key = fallbackPath
		}
		byPath[key] = append(byPath[key], d)
	}

	// Publish for every open doc in the dir (clearing clean ones), plus any file
	// that has diagnostics even if not currently open.
	targets := map[string]bool{}
	for path := range snap {
		targets[path] = true
	}
	for path := range byPath {
		targets[path] = true
	}

	for path := range targets {
		text := snap[path].text // "" if not open; positions still map (best effort)
		lineAt := lineAtFunc(text)
		ds := byPath[path]
		out := make([]Diagnostic, 0, len(ds))
		for _, d := range ds {
			out = append(out, convertDiag(d, lineAt, s.enc))
		}
		if err := s.publishDiags(pathToURI(path), snap, out); err != nil {
			return err
		}
	}
	return nil
}

// publishDiags sends a publishDiagnostics notification for uri, tagging it with
// the document's version when uri is an open document in snap (so the editor can
// discard a stale-version publish). diags must be non-nil so the wire form is an
// empty array, never JSON null.
func (s *Server) publishDiags(uri string, snap map[string]docSnap, diags []Diagnostic) error {
	var version *int
	if d, ok := snap[uriToPath(uri)]; ok {
		v := d.version
		version = &v
	}
	return s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: uri, Version: version, Diagnostics: diags})
}
