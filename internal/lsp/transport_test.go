package lsp

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestConnRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(strings.NewReader(""), &buf)
	if err := c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": "hi"}); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "Content-Length: ") || !strings.Contains(got, "\r\n\r\n") {
		t.Fatalf("missing framing headers: %q", got)
	}

	// Now read the framed bytes back.
	rc := newConn(strings.NewReader(got), io.Discard)
	f, err := rc.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if f.Method != "hi" {
		t.Fatalf("method = %q, want hi", f.Method)
	}
}

func TestConnReadEOF(t *testing.T) {
	c := newConn(strings.NewReader(""), io.Discard)
	if _, err := c.read(); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestConnReadParamsAndID(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"x":1}}`
	framed := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	c := newConn(strings.NewReader(framed), io.Discard)
	f, err := c.read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(f.ID) != "7" || f.Method != "initialize" {
		t.Fatalf("id=%s method=%s", f.ID, f.Method)
	}
	var p struct{ X int }
	if err := json.Unmarshal(f.Params, &p); err != nil || p.X != 1 {
		t.Fatalf("params: %v %+v", err, p)
	}
}
