package lsp

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/gsxhq/gsx/internal/diag"
)

// Analyzer computes diagnostics for the package in dir, using override (abs
// .gsx path -> buffer bytes) in place of on-disk content for open documents.
type Analyzer interface {
	Diagnose(dir string, override map[string][]byte) ([]diag.Diagnostic, error)
}

// Server is a stdio LSP server that publishes gsx diagnostics. It owns the
// protocol; code analysis is delegated to an injected Analyzer.
type Server struct {
	conn     *conn
	docs     *docStore
	analyzer Analyzer
	enc      encoding
	shutdown bool
	exited   bool
}

// NewServer builds a Server reading requests from r and writing responses and
// notifications to w. The default position encoding is UTF-16 until initialize
// negotiates otherwise.
func NewServer(r io.Reader, w io.Writer, a Analyzer) *Server {
	return &Server{conn: newConn(r, w), docs: newDocStore(), analyzer: a, enc: encUTF16}
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
		PositionEncoding: encName,
		TextDocumentSync: 1, // full document sync
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

// Transitional stubs — Task 6 replaces these bodies with real implementations.

func (s *Server) handleDidOpen(f frame) error   { return nil }
func (s *Server) handleDidChange(f frame) error { return nil }
func (s *Server) handleDidClose(f frame) error  { return nil }
