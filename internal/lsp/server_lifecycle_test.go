package lsp

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// nilAnalyzer satisfies Analyzer and returns nothing.
type nilAnalyzer struct{}

func (nilAnalyzer) Analyze(string, map[string][]byte) (*Package, error) { return &Package{}, nil }
func (nilAnalyzer) AnalyzeModule(string, map[string][]byte) ([]CrossRef, error) { return nil, nil }

// framed wraps a JSON-RPC body in Content-Length framing.
func framed(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

// readFrames splits a server's output stream into decoded frames (by method or,
// for responses, raw bodies).
func readFrames(t *testing.T, out string) []map[string]json.RawMessage {
	t.Helper()
	var msgs []map[string]json.RawMessage
	rest := out
	for {
		i := strings.Index(rest, "Content-Length: ")
		if i < 0 {
			break
		}
		rest = rest[i+len("Content-Length: "):]
		j := strings.Index(rest, "\r\n\r\n")
		if j < 0 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimSpace(rest[:j]))
		body := rest[j+4 : j+4+n]
		rest = rest[j+4+n:]
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func TestServerInitializeNegotiatesUTF8(t *testing.T) {
	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"capabilities": map[string]any{"general": map[string]any{"positionEncodings": []string{"utf-8", "utf-16"}}}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if srv.enc != encUTF8 {
		t.Fatalf("enc = %v, want encUTF8", srv.enc)
	}
	msgs := readFrames(t, out.String())
	// First response is the initialize result; assert positionEncoding.
	var res struct {
		Capabilities serverCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(msgs[0]["result"], &res); err != nil {
		t.Fatalf("init result: %v", err)
	}
	if res.Capabilities.PositionEncoding != "utf-8" || res.Capabilities.TextDocumentSync != 1 {
		t.Fatalf("caps = %+v", res.Capabilities)
	}
}

func TestServerInitializeDefaultsUTF16(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	if srv.enc != encUTF16 {
		t.Fatalf("enc = %v, want encUTF16", srv.enc)
	}
}

func TestServerUnknownRequestMethodNotFound(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "textDocument/unknownMethod", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	msgs := readFrames(t, out.String())
	if len(msgs) == 0 || msgs[0]["error"] == nil {
		t.Fatalf("expected an error response, got %v", msgs)
	}
}
