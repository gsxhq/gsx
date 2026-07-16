// Package lsp implements gsx's language server: a stdio JSON-RPC transport, a
// minimal hand-written subset of the LSP protocol, an in-memory document store,
// and a server loop that publishes gsx diagnostics. It depends only on stdlib
// and internal/diag; the concrete code analysis is injected via Analyzer.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// frame is one decoded JSON-RPC message received from the client. A request has
// Method and ID; a notification has Method and no ID.
type frame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// conn reads and writes Content-Length-framed JSON-RPC messages over a stream.
type conn struct {
	r *bufio.Reader
	w io.Writer
}

func newConn(r io.Reader, w io.Writer) *conn {
	return &conn{r: bufio.NewReader(r), w: w}
}

// read returns the next message frame, or io.EOF when the stream closes between
// messages.
func (c *conn) read() (frame, error) {
	length := -1
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return frame{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line terminates the header block
		}
		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return frame{}, fmt.Errorf("lsp: bad Content-Length %q: %w", v, err)
			}
			length = n
		}
	}
	if length < 0 {
		return frame{}, fmt.Errorf("lsp: message without Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return frame{}, err
	}
	var f frame
	if err := json.Unmarshal(body, &f); err != nil {
		return frame{}, fmt.Errorf("lsp: bad message body: %w", err)
	}
	return f, nil
}

// writeMessage marshals v and writes it as one Content-Length-framed message.
func (c *conn) writeMessage(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.w.Write(body)
	return err
}
