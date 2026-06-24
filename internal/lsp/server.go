package lsp

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

// Analyzer computes diagnostics for the package in dir, using override (abs
// .gsx path -> buffer bytes) in place of on-disk content for open documents.
type Analyzer interface {
	Analyze(dir string, override map[string][]byte) (*Package, error)
}

// Server is a stdio LSP server that publishes gsx diagnostics. It owns the
// protocol; code analysis is delegated to an injected Analyzer.
type Server struct {
	conn     *conn
	docs     *docStore
	analyzer Analyzer
	pkgs     map[string]*Package // dir → latest analyzed package
	enc      encoding
	shutdown bool
	exited   bool
}

// NewServer builds a Server reading requests from r and writing responses and
// notifications to w. The default position encoding is UTF-16 until initialize
// negotiates otherwise.
func NewServer(r io.Reader, w io.Writer, a Analyzer) *Server {
	return &Server{conn: newConn(r, w), docs: newDocStore(), analyzer: a, pkgs: map[string]*Package{}, enc: encUTF16}
}

// Run reads and dispatches messages until the stream closes or an `exit`
// notification is received.
func (s *Server) Run() error {
	for {
		f, err := s.conn.read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.handle(f); err != nil {
			return err
		}
		if s.exited {
			return nil
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
	for _, e := range p.Capabilities.General.PositionEncodings {
		if e == "utf-8" {
			s.enc = encUTF8
			encName = "utf-8"
			break
		}
	}
	return s.reply(f.ID, initializeResult{Capabilities: serverCapabilities{
		PositionEncoding:   encName,
		TextDocumentSync:   1, // full document sync
		DefinitionProvider: true,
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

func (s *Server) handleDidOpen(f frame) error {
	var p didOpenParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	s.docs.open(p.TextDocument.URI, p.TextDocument.Text, p.TextDocument.Version)
	uri := p.TextDocument.URI
	if strings.HasSuffix(uriToPath(uri), ".go") {
		s.analyzeOnly(uri) // no diagnostics for .go; gopls owns those
		return nil
	}
	return s.analyzeAndPublish(uri)
}

func (s *Server) handleDidChange(f frame) error {
	var p didChangeParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	if len(p.ContentChanges) == 0 {
		return nil
	}
	// Full-document sync: the last change carries the whole new text.
	text := p.ContentChanges[len(p.ContentChanges)-1].Text
	s.docs.update(p.TextDocument.URI, text, p.TextDocument.Version)
	uri := p.TextDocument.URI
	if strings.HasSuffix(uriToPath(uri), ".go") {
		s.analyzeOnly(uri) // no diagnostics for .go; gopls owns those
		return nil
	}
	return s.analyzeAndPublish(uri)
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
	s.docs.close(p.TextDocument.URI)
	// Clear diagnostics for the now-closed document.
	return s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: p.TextDocument.URI, Diagnostics: []Diagnostic{}})
}

// analyzeAndPublish re-analyzes the package containing changedURI and publishes
// diagnostics for every open document in that directory (empty list when a
// document is now clean, so stale squiggles never linger). Diagnostics that
// carry no filename are attached to changedURI at the file start.
func (s *Server) analyzeAndPublish(changedURI string) error {
	dir := filepath.Dir(uriToPath(changedURI))
	openDocs := s.docs.openInDir(dir) // abs path -> text
	override := make(map[string][]byte, len(openDocs))
	for path, text := range openDocs {
		override[path] = []byte(text)
	}

	pkg, err := s.analyzer.Analyze(dir, override)
	if err != nil || pkg == nil {
		// Analysis failure (e.g. no go.mod): do not crash the session. Clear the
		// changed file's diagnostics and move on.
		return s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: changedURI, Diagnostics: []Diagnostic{}})
	}
	s.pkgs[dir] = pkg
	diags := pkg.Diags

	// Group diagnostics by absolute filename; positionless/foreign ones go to
	// the changed document.
	changedPath := uriToPath(changedURI)
	byPath := map[string][]diag.Diagnostic{}
	for _, d := range diags {
		key := d.Start.Filename
		if key == "" {
			key = changedPath
		}
		byPath[key] = append(byPath[key], d)
	}

	// Publish for every open doc in the dir (clearing clean ones), plus any file
	// that has diagnostics even if not currently open.
	targets := map[string]bool{}
	for path := range openDocs {
		targets[path] = true
	}
	for path := range byPath {
		targets[path] = true
	}

	for path := range targets {
		text := openDocs[path] // "" if not open; positions still map (best effort)
		lineAt := lineAtFunc(text)
		ds := byPath[path]
		out := make([]Diagnostic, 0, len(ds))
		for _, d := range ds {
			out = append(out, convertDiag(d, lineAt, s.enc))
		}
		if err := s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: pathToURI(path), Diagnostics: out}); err != nil {
			return err
		}
	}
	return nil
}
