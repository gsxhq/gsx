# gsx LSP — Slice 1 (Walking Skeleton) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a `gsx lsp` subcommand that runs a JSON-RPC language server over stdio and publishes gsx's existing diagnostics live as a `.gsx` file is edited — no gopls, no completion, no hover.

**Architecture:** A new `internal/lsp` package owns the protocol: stdio JSON-RPC transport, a minimal hand-written set of LSP message types, an in-memory document store, encoding-aware position conversion, and a server loop. The server is pure protocol — it depends only on `internal/diag` and an injected `Analyzer` interface, so it is testable with a fake. The `gen` package supplies the concrete analyzer (module-root resolution + a call into the existing `codegen` pipeline with an in-memory source override) and wires the `lsp` subcommand. This injection keeps `internal/lsp` free of any `gen`/`codegen` import, avoiding an import cycle (`gen` imports `internal/lsp`, never the reverse).

**Tech Stack:** Go 1.26.1, stdlib only for the LSP package (`encoding/json`, `bufio`, `go/token`, `unicode/utf16` semantics computed inline). Analysis reuses `internal/codegen` (which already uses `golang.org/x/tools/go/packages`).

## Global Constraints

- Module path: `github.com/gsxhq/gsx`. Go version floor: `go 1.26.1` (from `go.mod`).
- `internal/lsp` MUST NOT import `gen` or `internal/codegen` (cycle avoidance). It may import `internal/diag` and stdlib only.
- The LSP MUST NOT write `.x.go` to disk — it reads only `PackageResult.Diags`, never `.Files`.
- Unexported by default (project rule): types/funcs start lowercase unless they need to be called from another package. Exported surface of `internal/lsp` is only what `gen` calls: `NewServer`, `(*Server).Run`, the `Analyzer` interface, and the `Diagnostic`/`diag`-derived types it returns. Everything else stays unexported.
- Diagnostics positions: `diag.Diagnostic` uses 1-based `token.Position` with **byte** columns. LSP wire positions are 0-based with characters counted in the **negotiated encoding** (UTF-16 default; UTF-8 if the client offers it). All conversion goes through one function (Task 4).
- Each task ends green (`go test ./...` for the touched packages) and is committed.

---

### Task 1: JSON-RPC stdio transport

**Files:**
- Create: `internal/lsp/transport.go`
- Test: `internal/lsp/transport_test.go`

**Interfaces:**
- Produces:
  - `type frame struct { ID json.RawMessage; Method string; Params json.RawMessage }` — a decoded incoming message; request if `ID`+`Method`, notification if `Method` only.
  - `type conn struct { ... }` with `func newConn(r io.Reader, w io.Writer) *conn`, `func (c *conn) read() (frame, error)` (returns `io.EOF` at stream end), `func (c *conn) writeMessage(v any) error`.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"bytes"
	"encoding/json"
	"io"
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
	framed := "Content-Length: " + itoaLen(body) + "\r\n\r\n" + body
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

func itoaLen(s string) string {
	return strings.TrimSpace(string(rune('0'+len(s)/100))+string(rune('0'+len(s)/10%10))+string(rune('0'+len(s)%10)))
}
```

(The `itoaLen` helper is deliberately ugly to avoid importing `strconv` in the test for a 3-digit body; if the body length is not 3 digits, replace with `strconv.Itoa(len(body))` and import `strconv`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestConn -v`
Expected: FAIL — `undefined: newConn`.

- [ ] **Step 3: Write minimal implementation**

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestConn -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/transport.go internal/lsp/transport_test.go
git commit -m "feat(lsp): Content-Length JSON-RPC stdio transport"
```

---

### Task 2: Minimal LSP protocol types

**Files:**
- Create: `internal/lsp/protocol.go`
- Test: `internal/lsp/protocol_test.go`

**Interfaces:**
- Produces (exact field names used by later tasks):
  - `type Position struct { Line int; Character int }` (both JSON `line`/`character`).
  - `type Range struct { Start, End Position }`.
  - `type Diagnostic struct { Range Range; Severity int; Code string; Source string; Message string }` (`severity`/`code`/`source`/`message`; `code`/`source` `omitempty`).
  - `type publishDiagnosticsParams struct { URI string; Diagnostics []Diagnostic }` (`uri`/`diagnostics`).
  - `type initializeParams struct { Capabilities clientCapabilities }` with `clientCapabilities{ General generalCapabilities }`, `generalCapabilities{ PositionEncodings []string }`.
  - `type initializeResult struct { Capabilities serverCapabilities }` with `serverCapabilities{ PositionEncoding string; TextDocumentSync int }`.
  - `type didOpenParams struct { TextDocument textDocumentItem }`, `textDocumentItem{ URI string; Text string; Version int }`.
  - `type didChangeParams struct { TextDocument versionedTextDocumentIdentifier; ContentChanges []contentChange }`, `versionedTextDocumentIdentifier{ URI string; Version int }`, `contentChange{ Text string }` (full-sync: only `text`).
  - `type didCloseParams struct { TextDocument textDocumentIdentifier }`, `textDocumentIdentifier{ URI string }`.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"encoding/json"
	"testing"
)

func TestPublishDiagnosticsParamsJSON(t *testing.T) {
	p := publishDiagnosticsParams{
		URI: "file:///x/page.gsx",
		Diagnostics: []Diagnostic{{
			Range:    Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 9}},
			Severity: 1,
			Code:     "type-error",
			Source:   "types",
			Message:  "undefined: foo",
		}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := `{"uri":"file:///x/page.gsx","diagnostics":[{"range":{"start":{"line":2,"character":5},"end":{"line":2,"character":9}},"severity":1,"code":"type-error","source":"types","message":"undefined: foo"}]}`
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

func TestInitializeParamsParse(t *testing.T) {
	in := `{"capabilities":{"general":{"positionEncodings":["utf-8","utf-16"]}}}`
	var p initializeParams
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Capabilities.General.PositionEncodings) != 2 || p.Capabilities.General.PositionEncodings[0] != "utf-8" {
		t.Fatalf("encodings = %v", p.Capabilities.General.PositionEncodings)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run "TestPublishDiagnosticsParamsJSON|TestInitializeParamsParse" -v`
Expected: FAIL — `undefined: publishDiagnosticsParams`.

- [ ] **Step 3: Write minimal implementation**

```go
package lsp

// Position is a 0-based LSP position; Character is counted in the negotiated
// encoding (UTF-16 by default).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open [Start, End) span.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic is the LSP wire form of one problem.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type initializeParams struct {
	Capabilities clientCapabilities `json:"capabilities"`
}

type clientCapabilities struct {
	General generalCapabilities `json:"general"`
}

type generalCapabilities struct {
	PositionEncodings []string `json:"positionEncodings"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

type serverCapabilities struct {
	PositionEncoding string `json:"positionEncoding"`
	TextDocumentSync int    `json:"textDocumentSync"`
}

type textDocumentItem struct {
	URI     string `json:"uri"`
	Text    string `json:"text"`
	Version int    `json:"version"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type contentChange struct {
	Text string `json:"text"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run "TestPublishDiagnosticsParamsJSON|TestInitializeParamsParse" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/protocol.go internal/lsp/protocol_test.go
git commit -m "feat(lsp): minimal LSP protocol message types"
```

---

### Task 3: Document store + URI/path conversion

**Files:**
- Create: `internal/lsp/documents.go`
- Test: `internal/lsp/documents_test.go`

**Interfaces:**
- Produces:
  - `type docStore struct { ... }`, `func newDocStore() *docStore`.
  - `func (s *docStore) open(uri, text string, version int)`, `func (s *docStore) update(uri, text string, version int)`, `func (s *docStore) close(uri string)`.
  - `func (s *docStore) text(uri string) (string, bool)`.
  - `func (s *docStore) openInDir(dir string) map[string]string` — abs file path → text, for every open doc whose containing dir == `dir`.
  - `func uriToPath(uri string) string`, `func pathToURI(path string) string`.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import "testing"

func TestURIPathRoundTrip(t *testing.T) {
	cases := []string{"/x/page.gsx", "/a b/c.gsx", "/p/ünïcode.gsx"}
	for _, p := range cases {
		if got := uriToPath(pathToURI(p)); got != p {
			t.Fatalf("round trip %q -> %q", p, got)
		}
	}
	if got := pathToURI("/x/page.gsx"); got != "file:///x/page.gsx" {
		t.Fatalf("pathToURI = %q", got)
	}
}

func TestDocStoreOpenInDir(t *testing.T) {
	s := newDocStore()
	s.open(pathToURI("/proj/page.gsx"), "A", 1)
	s.open(pathToURI("/proj/card.gsx"), "B", 1)
	s.open(pathToURI("/other/x.gsx"), "C", 1)
	got := s.openInDir("/proj")
	if len(got) != 2 || got["/proj/page.gsx"] != "A" || got["/proj/card.gsx"] != "B" {
		t.Fatalf("openInDir = %v", got)
	}
	s.update(pathToURI("/proj/page.gsx"), "A2", 2)
	if txt, _ := s.text(pathToURI("/proj/page.gsx")); txt != "A2" {
		t.Fatalf("text after update = %q", txt)
	}
	s.close(pathToURI("/proj/page.gsx"))
	if _, ok := s.text(pathToURI("/proj/page.gsx")); ok {
		t.Fatal("doc still present after close")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run "TestURIPathRoundTrip|TestDocStoreOpenInDir" -v`
Expected: FAIL — `undefined: uriToPath`.

- [ ] **Step 3: Write minimal implementation**

```go
package lsp

import (
	"net/url"
	"path/filepath"
	"strings"
	"sync"
)

type document struct {
	text    string
	version int
}

// docStore holds open document buffers keyed by URI. It is safe for concurrent
// use; slice 1 drives it from a single goroutine but the mutex keeps it honest.
type docStore struct {
	mu   sync.Mutex
	docs map[string]*document
}

func newDocStore() *docStore { return &docStore{docs: map[string]*document{}} }

func (s *docStore) open(uri, text string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &document{text: text, version: version}
}

func (s *docStore) update(uri, text string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[uri] = &document{text: text, version: version}
}

func (s *docStore) close(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, uri)
}

func (s *docStore) text(uri string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[uri]
	if !ok {
		return "", false
	}
	return d.text, true
}

// openInDir returns abs-file-path -> text for every open document whose
// containing directory equals dir.
func (s *docStore) openInDir(dir string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for uri, d := range s.docs {
		p := uriToPath(uri)
		if filepath.Dir(p) == dir {
			out[p] = d.text
		}
	}
	return out
}

// uriToPath converts a file:// URI to a local filesystem path, decoding percent
// escapes. Non-file URIs are returned unchanged.
func uriToPath(uri string) string {
	rest, ok := strings.CutPrefix(uri, "file://")
	if !ok {
		return uri
	}
	if decoded, err := url.PathUnescape(rest); err == nil {
		return decoded
	}
	return rest
}

// pathToURI converts an absolute filesystem path to a file:// URI, percent-
// escaping path segments.
func pathToURI(path string) string {
	u := &url.URL{Scheme: "file", Path: path}
	return u.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run "TestURIPathRoundTrip|TestDocStoreOpenInDir" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/documents.go internal/lsp/documents_test.go
git commit -m "feat(lsp): in-memory document store + file URI conversion"
```

---

### Task 4: Encoding-aware position/diagnostic conversion

**Files:**
- Create: `internal/lsp/convert.go`
- Test: `internal/lsp/convert_test.go`

**Interfaces:**
- Consumes: `internal/diag` (`diag.Diagnostic`, `diag.Severity` constants), `go/token` (`token.Position`).
- Produces:
  - `type encoding int` with `const ( encUTF16 encoding = iota; encUTF8 )`.
  - `func convertDiag(d diag.Diagnostic, lineAt func(line1 int) string, enc encoding) Diagnostic`.
  - `func lineAtFunc(text string) func(line1 int) string` — returns a closure giving the 1-based line's text (without trailing newline); out-of-range lines return "".

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func mkPos(line, col int) token.Position {
	return token.Position{Filename: "f.gsx", Line: line, Column: col}
}

func TestConvertDiagASCII(t *testing.T) {
	src := "line one\n  bad here\n"
	d := diag.Diagnostic{Start: mkPos(2, 3), End: mkPos(2, 6), Severity: diag.Error, Code: "x", Source: "types", Message: "boom"}
	got := convertDiag(d, lineAtFunc(src), encUTF16)
	want := Range{Start: Position{Line: 1, Character: 2}, End: Position{Line: 1, Character: 5}}
	if got.Range != want {
		t.Fatalf("range = %+v, want %+v", got.Range, want)
	}
	if got.Severity != 1 {
		t.Fatalf("severity = %d, want 1 (Error)", got.Severity)
	}
}

func TestConvertDiagUTF16MultiByte(t *testing.T) {
	// "é" is 2 bytes in UTF-8, 1 UTF-16 code unit. Cursor after "héllo " (byte col 8, 1-based).
	src := "héllo world\n"
	d := diag.Diagnostic{Start: mkPos(1, 8), End: mkPos(1, 8), Severity: diag.Warning}
	got := convertDiag(d, lineAtFunc(src), encUTF16)
	// bytes before col 8: "héllo " = h(1) é(2) l(1) l(1) o(1) space(1) = 7 bytes -> 6 UTF-16 units.
	if got.Range.Start.Character != 6 {
		t.Fatalf("utf16 char = %d, want 6", got.Range.Start.Character)
	}
	if got.Severity != 2 {
		t.Fatalf("severity = %d, want 2 (Warning)", got.Severity)
	}
}

func TestConvertDiagUTF8MultiByte(t *testing.T) {
	src := "héllo world\n"
	d := diag.Diagnostic{Start: mkPos(1, 8), End: mkPos(1, 8)}
	got := convertDiag(d, lineAtFunc(src), encUTF8)
	if got.Range.Start.Character != 7 { // byte offset
		t.Fatalf("utf8 char = %d, want 7", got.Range.Start.Character)
	}
}

func TestConvertDiagEmojiUTF16(t *testing.T) {
	// "😀" is 4 bytes UTF-8, 2 UTF-16 code units (surrogate pair). col after it = byte 5.
	src := "😀x\n"
	d := diag.Diagnostic{Start: mkPos(1, 5), End: mkPos(1, 5)}
	got := convertDiag(d, lineAtFunc(src), encUTF16)
	if got.Range.Start.Character != 2 {
		t.Fatalf("emoji utf16 char = %d, want 2", got.Range.Start.Character)
	}
}

func TestConvertDiagPositionless(t *testing.T) {
	d := diag.Diagnostic{Start: token.Position{}, End: token.Position{}, Severity: diag.Error, Message: "no pos"}
	got := convertDiag(d, lineAtFunc(""), encUTF16)
	zero := Range{Start: Position{0, 0}, End: Position{0, 0}}
	if got.Range != zero {
		t.Fatalf("positionless range = %+v, want zero", got.Range)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestConvertDiag -v`
Expected: FAIL — `undefined: convertDiag`.

- [ ] **Step 3: Write minimal implementation**

```go
package lsp

import (
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

type encoding int

const (
	encUTF16 encoding = iota
	encUTF8
)

// lineAtFunc returns a closure giving the text of the 1-based line number in
// src (without the trailing newline). Out-of-range lines yield "".
func lineAtFunc(src string) func(line1 int) string {
	lines := strings.Split(src, "\n")
	return func(line1 int) string {
		if line1 < 1 || line1 > len(lines) {
			return ""
		}
		return lines[line1-1]
	}
}

// convertDiag converts a gsx diagnostic (1-based, byte columns) to an LSP
// Diagnostic (0-based, characters in the negotiated encoding). A positionless
// diagnostic (Line == 0) maps to the zero range at the file start.
func convertDiag(d diag.Diagnostic, lineAt func(line1 int) string, enc encoding) Diagnostic {
	return Diagnostic{
		Range:    Range{Start: convertPos(d.Start, lineAt, enc), End: convertPos(d.End, lineAt, enc)},
		Severity: lspSeverity(d.Severity),
		Code:     d.Code,
		Source:   d.Source,
		Message:  d.Message,
	}
}

func convertPos(p token.Position, lineAt func(line1 int) string, enc encoding) Position {
	if p.Line == 0 {
		return Position{Line: 0, Character: 0}
	}
	return Position{Line: p.Line - 1, Character: charForByteCol(lineAt(p.Line), p.Column, enc)}
}

// charForByteCol converts a 1-based byte column within lineText to a 0-based LSP
// character offset in enc. A column past the line end clamps to the line length.
func charForByteCol(lineText string, col int, enc encoding) int {
	byteOff := col - 1
	if byteOff < 0 {
		byteOff = 0
	}
	if byteOff > len(lineText) {
		byteOff = len(lineText)
	}
	prefix := lineText[:byteOff]
	if enc == encUTF8 {
		return len(prefix)
	}
	return utf16Len(prefix)
}

// utf16Len counts UTF-16 code units in s (chars above U+FFFF take two).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// lspSeverity maps a gsx severity to an LSP DiagnosticSeverity (1=Error,
// 2=Warning, 3=Information, 4=Hint).
func lspSeverity(s diag.Severity) int {
	switch s {
	case diag.Error:
		return 1
	case diag.Warning:
		return 2
	case diag.Info:
		return 3
	case diag.Hint:
		return 4
	default:
		return 1
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestConvertDiag -v`
Expected: PASS (all five).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/convert.go internal/lsp/convert_test.go
git commit -m "feat(lsp): encoding-aware diagnostic position conversion"
```

---

### Task 5: Server lifecycle + dispatch loop

**Files:**
- Create: `internal/lsp/server.go`
- Test: `internal/lsp/server_lifecycle_test.go`

**Interfaces:**
- Consumes: `frame`/`conn` (Task 1), protocol types (Task 2), `docStore` (Task 3), `encoding` (Task 4), `internal/diag`.
- Produces:
  - `type Analyzer interface { Diagnose(dir string, override map[string][]byte) ([]diag.Diagnostic, error) }`.
  - `type Server struct { ... }` with fields `conn *conn`, `docs *docStore`, `analyzer Analyzer`, `enc encoding`, `shutdown bool`, `exited bool`.
  - `func NewServer(r io.Reader, w io.Writer, a Analyzer) *Server` (defaults `enc = encUTF16`).
  - `func (s *Server) Run() error` — read/dispatch loop until EOF or `exit`.
  - `func (s *Server) handle(f frame) error` — dispatch one frame.
  - helpers `reply(id, result)`, `replyError(id, code, msg)`, `notify(method, params)`.
  - `handleInitialize` negotiates encoding (`utf-8` if offered, else `utf-16`) and returns capabilities `{positionEncoding, textDocumentSync: 1}`.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// nilAnalyzer satisfies Analyzer and returns nothing.
type nilAnalyzer struct{}

func (nilAnalyzer) Diagnose(string, map[string][]byte) ([]diag.Diagnostic, error) { return nil, nil }

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
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "textDocument/hover", "params": map[string]any{}})
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run TestServer -v`
Expected: FAIL — `undefined: NewServer`.

- [ ] **Step 3: Write minimal implementation**

```go
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
	_ = json.Unmarshal(f.Params, &p) // absent/å malformed params -> defaults
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
```

NOTE: the comment `// absent/å malformed` contains a stray character — write it as `// absent or malformed params -> defaults`. (Watch for typos; the dispatch references `handleDidOpen`/`handleDidChange`/`handleDidClose`, defined in Task 6 — the package will not compile until Task 6 lands, so Task 5's test is run with those handlers added as no-op stubs in Step 3.)

Add these stubs at the end of `server.go` in this task so the package compiles; Task 6 replaces their bodies:

```go
func (s *Server) handleDidOpen(f frame) error  { return nil }
func (s *Server) handleDidChange(f frame) error { return nil }
func (s *Server) handleDidClose(f frame) error  { return nil }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -run TestServer -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/server.go internal/lsp/server_lifecycle_test.go
git commit -m "feat(lsp): server lifecycle, dispatch loop, initialize negotiation"
```

---

### Task 6: Document sync → analyze → publish

**Files:**
- Modify: `internal/lsp/server.go` (replace the three `handleDid*` stubs; add `analyzeAndPublish`)
- Test: `internal/lsp/server_sync_test.go`

**Interfaces:**
- Consumes: `Analyzer` (Task 5), `docStore.openInDir` (Task 3), `convertDiag`/`lineAtFunc` (Task 4), `pathToURI`/`uriToPath` (Task 3).
- Produces:
  - `handleDidOpen`/`handleDidChange` update the doc then call `analyzeAndPublish(uri)`.
  - `handleDidClose` removes the doc and clears its diagnostics (publishes an empty list).
  - `func (s *Server) analyzeAndPublish(changedURI string) error` — resolves the dir, builds the override from open docs in that dir, calls `analyzer.Diagnose`, groups results by filename, and publishes per open doc in the dir (empty list when clean). Diagnostics with an empty `Filename` are attached to `changedURI` at the zero range.

- [ ] **Step 1: Write the failing test**

```go
package lsp

import (
	"bytes"
	"encoding/json"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// fakeAnalyzer returns one error diagnostic for the file it is told about.
type fakeAnalyzer struct {
	file string // abs .gsx path to attach the diagnostic to
}

func (a fakeAnalyzer) Diagnose(dir string, override map[string][]byte) ([]diag.Diagnostic, error) {
	if _, ok := override[a.file]; !ok {
		return nil, nil // the open buffer must reach the analyzer
	}
	return []diag.Diagnostic{{
		Start:    token.Position{Filename: a.file, Line: 1, Column: 3},
		End:      token.Position{Filename: a.file, Line: 1, Column: 6},
		Severity: diag.Error,
		Code:     "type-error",
		Source:   "types",
		Message:  "undefined: foo",
	}}, nil
}

func TestDidOpenPublishesDiagnostics(t *testing.T) {
	file := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(file)
	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "text": "ab foo cd", "version": 1}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, fakeAnalyzer{file: file})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	msgs := readFrames(t, out.String())
	var found bool
	for _, m := range msgs {
		if string(m["method"]) != `"textDocument/publishDiagnostics"` {
			continue
		}
		var p publishDiagnosticsParams
		if err := json.Unmarshal(m["params"], &p); err != nil {
			t.Fatal(err)
		}
		if p.URI != uri {
			continue
		}
		if len(p.Diagnostics) != 1 {
			t.Fatalf("diagnostics = %d, want 1", len(p.Diagnostics))
		}
		d := p.Diagnostics[0]
		if d.Range.Start != (Position{Line: 0, Character: 2}) || d.Severity != 1 || d.Message != "undefined: foo" {
			t.Fatalf("converted diag = %+v", d)
		}
		found = true
	}
	if !found {
		t.Fatalf("no publishDiagnostics for %s in %q", uri, out.String())
	}
}

func TestDidCloseClearsDiagnostics(t *testing.T) {
	file := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(file)
	in := framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "text": "ab foo cd", "version": 1}},
	})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didClose",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri}},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, fakeAnalyzer{file: file})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	msgs := readFrames(t, out.String())
	// The LAST publishDiagnostics for uri must be empty.
	var last *publishDiagnosticsParams
	for _, m := range msgs {
		if string(m["method"]) != `"textDocument/publishDiagnostics"` {
			continue
		}
		var p publishDiagnosticsParams
		_ = json.Unmarshal(m["params"], &p)
		if p.URI == uri {
			cp := p
			last = &cp
		}
	}
	if last == nil || len(last.Diagnostics) != 0 {
		t.Fatalf("expected final empty publish for %s, got %+v", uri, last)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/lsp/ -run "TestDidOpenPublishesDiagnostics|TestDidCloseClearsDiagnostics" -v`
Expected: FAIL — the stub handlers publish nothing.

- [ ] **Step 3: Write minimal implementation**

Replace the three stub handlers from Task 5 with:

```go
func (s *Server) handleDidOpen(f frame) error {
	var p didOpenParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	s.docs.open(p.TextDocument.URI, p.TextDocument.Text, p.TextDocument.Version)
	return s.analyzeAndPublish(p.TextDocument.URI)
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
	return s.analyzeAndPublish(p.TextDocument.URI)
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

	diags, err := s.analyzer.Diagnose(dir, override)
	if err != nil {
		// Analysis failure (e.g. no go.mod): do not crash the session. Clear the
		// changed file's diagnostics and move on.
		return s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: changedURI, Diagnostics: []Diagnostic{}})
	}

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
```

Add `"path/filepath"` to `server.go`'s imports (alongside `encoding/json`, `errors`, `io`, and `github.com/gsxhq/gsx/internal/diag`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/lsp/ -v`
Expected: PASS (whole package, including earlier tasks).

- [ ] **Step 5: Commit**

```bash
git add internal/lsp/server.go internal/lsp/server_sync_test.go
git commit -m "feat(lsp): document sync, package re-analysis, diagnostic publishing"
```

---

### Task 7: codegen in-memory source override seam

**Files:**
- Modify: `internal/codegen/batch.go` (add `srcOverride` param to `GeneratePackagesWithFilters`; consult it before `os.ReadFile`; update `GeneratePackages`)
- Modify: `gen/cache.go:94`, `gen/cache.go:209` (pass `nil` for the new param)
- Test: `internal/codegen/batch_override_test.go`

**Interfaces:**
- Consumes: existing `GeneratePackagesWithFilters` pipeline.
- Produces: new signature
  `func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error), srcOverride map[string][]byte) (map[string]*PackageResult, error)`.
  `srcOverride` maps an absolute `.gsx` path to in-memory bytes used instead of reading that file from disk; `nil` preserves current disk-only behavior.

- [ ] **Step 1: Write the failing test**

```go
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSrcOverrideReplacesDiskContent: a .gsx on disk is clean, but an override
// buffer introduces a type error; the override must drive the diagnostics.
func TestSrcOverrideReplacesDiskContent(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.26\n")
	gsxPath := filepath.Join(dir, "page.gsx")
	// On-disk version is valid.
	mustWrite(t, gsxPath, "package x\n\ncomponent Page() {\n\t<div>hi</div>\n}\n")

	// Override introduces a reference to an undefined identifier.
	override := map[string][]byte{
		gsxPath: []byte("package x\n\ncomponent Page() {\n\t<div>{ nope }</div>\n}\n"),
	}
	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, override)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no result for %s (keys: %v)", dir, keysOf(out))
	}
	if len(pr.Diags) == 0 {
		t.Fatalf("expected a diagnostic from the override, got none")
	}
	found := false
	for _, d := range pr.Diags {
		if strings.Contains(d.Message, "nope") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags did not mention undefined 'nope': %+v", pr.Diags)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keysOf(m map[string]*PackageResult) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}
```

NOTE: confirm the exact gsx component syntax against an existing `.gsx` fixture in `examples/` or `internal/codegen/testdata` before running — adjust the `package x` / `component Page()` header to match real gsx grammar if it differs. The test's contract (override drives diagnostics) is what matters; the snippet must be valid-enough gsx that the on-disk version produces zero diagnostics and the override produces one mentioning `nope`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen/ -run TestSrcOverrideReplacesDiskContent -v`
Expected: FAIL — too many arguments to `GeneratePackagesWithFilters` (the param does not exist yet).

- [ ] **Step 3: Write minimal implementation**

In `internal/codegen/batch.go`, change the signature (line ~37) to add the final param:

```go
func GeneratePackagesWithFilters(moduleDir string, dirs []string, filterPkgs []string, cls *attrclass.Classifier, cssMin, jsMin func(string) (string, error), srcOverride map[string][]byte) (map[string]*PackageResult, error) {
```

In the parse loop (the `for _, m := range matches {` block, ~line 85), replace the read:

```go
		for _, m := range matches {
			var src []byte
			if ov, ok := srcOverride[m]; ok {
				src = ov
			} else {
				b, err := os.ReadFile(m)
				if err != nil {
					bag.Add(diag.Diagnostic{Severity: diag.Error, Message: err.Error(), Source: "parser"})
					hasErr = true
					break
				}
				src = b
			}
			f, err := gsxparser.ParseFileWithClassifier(fset, m, src, 0, cls)
			// ... unchanged from here ...
```

Update `GeneratePackages` (bottom of the file) to pass `nil`:

```go
func GeneratePackages(moduleDir string, dirs []string) (map[string]*PackageResult, error) {
	return GeneratePackagesWithFilters(moduleDir, dirs, nil, nil, nil, nil, nil)
}
```

In `gen/cache.go`, update both call sites (lines ~94 and ~209) to append `, nil`:

```go
	out, err := codegen.GeneratePackagesWithFilters(root, miss, filterPkgs, cls, cssMin, jsMin, nil)
```
```go
	out, err := codegen.GeneratePackagesWithFilters(root, dirs, filterPkgs, cls, cssMin, jsMin, nil)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen/ -run TestSrcOverrideReplacesDiskContent -v`
Then the full guard against regressions: `go test ./internal/codegen/ ./gen/`
Expected: PASS; existing codegen/gen tests still green.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/batch.go gen/cache.go internal/codegen/batch_override_test.go
git commit -m "feat(codegen): in-memory srcOverride for GeneratePackagesWithFilters"
```

---

### Task 8: gen analyzer + `lsp` subcommand + end-to-end

**Files:**
- Create: `gen/lsp.go` (concrete `Analyzer`; `runLSP`)
- Modify: `gen/main.go` (dispatch `case "lsp"`; usage line)
- Test: `gen/lsp_test.go`

**Interfaces:**
- Consumes: `moduleRoot` (`gen/modroot.go`), `codegen.GeneratePackagesWithFilters` (Task 7), `attrclass.Builtin`, `lsp.NewServer`/`lsp.Analyzer` (Tasks 5–6).
- Produces:
  - `type lspAnalyzer struct{}` implementing `lsp.Analyzer`.
  - `func (lspAnalyzer) Diagnose(dir string, override map[string][]byte) ([]diag.Diagnostic, error)`.
  - `func runLSP(stdin io.Reader, stdout, stderr io.Writer, args []string) int`.

- [ ] **Step 1: Write the failing test**

```go
package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/lsp"
)

func frameMsg(t *testing.T, v any) string {
	t.Helper()
	b, _ := json.Marshal(v)
	return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
}

// TestLSPEndToEndDiagnostics drives the real analyzer through lsp.Server over an
// in-memory stream and asserts a publishDiagnostics for a .gsx with a type error.
func TestLSPEndToEndDiagnostics(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gsxPath := filepath.Join(dir, "page.gsx")
	// Valid on disk so discovery/glob finds it; the open buffer adds the error.
	if err := os.WriteFile(gsxPath, []byte("package x\n\ncomponent Page() {\n\t<div>hi</div>\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := lsp.PathToURIForTest(gsxPath) // see helper note below

	in := frameMsg(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frameMsg(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{
			"uri": uri, "version": 1,
			"text": "package x\n\ncomponent Page() {\n\t<div>{ nope }</div>\n}\n",
		}},
	})
	in += frameMsg(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, nil); code != 0 {
		t.Fatalf("runLSP exit = %d, stderr = %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "publishDiagnostics") || !strings.Contains(out.String(), "nope") {
		t.Fatalf("expected a diagnostic mentioning 'nope'; out:\n%s", out.String())
	}
}
```

Helper note: the test needs `pathToURI`, which is unexported in `internal/lsp`. Rather than export it, build the URI inline in the test with `"file://" + gsxPath` (paths from `t.TempDir()` contain no characters needing escaping), and delete the `lsp.PathToURIForTest` reference:

```go
	uri := "file://" + gsxPath
```

As with Task 7, verify the gsx snippet compiles to a clean on-disk component and an erroring open buffer against real grammar; adjust the header/markup if gsx syntax differs.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./gen/ -run TestLSPEndToEndDiagnostics -v`
Expected: FAIL — `undefined: runLSP`.

- [ ] **Step 3: Write minimal implementation**

Create `gen/lsp.go`:

```go
package gen

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/lsp"
)

// lspAnalyzer is the concrete code analysis behind the language server: it
// resolves the module root for a directory and runs the stock (std-filter)
// codegen pipeline over that one package, returning its diagnostics. It never
// writes .x.go to disk — only PackageResult.Diags is read.
type lspAnalyzer struct{}

func (lspAnalyzer) Diagnose(dir string, override map[string][]byte) ([]diag.Diagnostic, error) {
	root, _, err := moduleRoot(dir)
	if err != nil {
		return nil, err
	}
	out, err := codegen.GeneratePackagesWithFilters(root, []string{dir}, nil, attrclass.Builtin(), nil, nil, override)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if pr := out[abs]; pr != nil {
		return pr.Diags, nil
	}
	return nil, nil
}

// runLSP runs the gsx language server over stdin/stdout (JSON-RPC), logging
// operational failures to stderr. It returns a process exit code.
func runLSP(stdin io.Reader, stdout, stderr io.Writer, _ []string) int {
	srv := lsp.NewServer(stdin, stdout, lspAnalyzer{})
	if err := srv.Run(); err != nil {
		fmt.Fprintf(stderr, "gsx: lsp: %v\n", err)
		return 1
	}
	return 0
}
```

In `gen/main.go`, add the dispatch case (after the `fmt` case, ~line 139):

```go
	case "lsp":
		return runLSP(os.Stdin, stdout, stderr, cmdArgs)
```

And add a line to `printUsage` (in the Commands block):

```
	lsp                   run the language server over stdio (JSON-RPC)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./gen/ -run TestLSPEndToEndDiagnostics -v`
Then the whole tree: `go test ./...`
Expected: PASS; no regressions.

- [ ] **Step 5: Commit**

```bash
git add gen/lsp.go gen/main.go gen/lsp_test.go
git commit -m "feat(gsx): gsx lsp subcommand — in-process diagnostics language server"
```

---

## Out of scope for slice 1 (explicit deferrals)

These are intentionally NOT built here; they are tracked for later slices so their absence is a recorded decision, not an oversight:

- **Debouncing of re-analysis.** Slice 1 analyzes synchronously on every `didChange` (keeps the loop deterministic and testable). A debounce wrapper (coalesce a burst of keystrokes, cancel superseded runs) is a fast-follow once the loop is proven, and is a pure latency optimization — it does not change published results.
- **Incremental / cached type-checking.** Each re-analysis is a fresh `go/packages.Load` for the one package. Wiring the existing Tier 0/2 cache into the server is a later perf slice.
- **Incremental document sync.** Slice 1 advertises full-document sync (`textDocumentSync: 1`). Range-based incremental sync is a later optimization.
- **New (never-saved) `.gsx` files.** Analysis discovers files via on-disk glob, so a buffer with no disk file yet is not analyzed. Handling unsaved-new files (synthesize the glob entry from open docs) is deferred.
- **All `go/types`-backed read features** (hover, goto-def, references, symbols, formatting) and the reverse `.gsx`→skeleton position map — these are slice 2 (see the design doc §5/§5.1).
- **Completion** — slice 3 (design doc §6).
- **`window/logMessage` / client telemetry, `$/cancelRequest`, work-done progress** — not needed for the walking skeleton.

## Self-Review

**Spec coverage** (against `2026-06-23-gsx-lsp-design.md`):
- §3 component boundaries — transport (T1), protocol (T2), documents (T3), analysis bridge (T7+T8), server/session (T5+T6). ✓
- §4.1 hand-rolled transport + own minimal types — T1, T2. ✓
- §4.2 lifecycle + position-encoding negotiation + capabilities — T5. ✓
- §4.3 document lifecycle + open-buffer overlay + per-package re-check — T6 (sync) + T7 (override seam) + T8 (analyzer). Debounce explicitly deferred above. ✓ (with recorded deferral)
- §4.4 publish + 1-based→0-based/encoding conversion + clear-when-clean — T4 (conversion) + T6 (publish/clear). ✓
- §4.5 subcommand wiring — T8. ✓
- §4.6 testing (scripted JSON-RPC, txtar-style; encoding unit tests on non-ASCII) — T4 (non-ASCII/emoji units), T5/T6 (scripted loop), T8 (e2e). ✓
- §5/§5.1/§6 — out of scope, recorded above. ✓
- Non-goal "no `.x.go` to disk" — analyzer reads only `.Diags`, never `.Files`; never calls `gen.Generate`. ✓

**Placeholder scan:** no "TBD"/"handle errors appropriately"/"similar to Task N". Two NOTE callouts flag (a) a deliberate test-typo fix and (b) verifying gsx grammar in the fixture snippets — both are concrete instructions, not deferrals of plan content.

**Type consistency:** `Analyzer.Diagnose(dir string, override map[string][]byte) ([]diag.Diagnostic, error)` is identical in T5 (definition), T6 (call), T8 (impl). `GeneratePackagesWithFilters(..., srcOverride map[string][]byte)` is identical in T7 (def + callers) and T8 (call). `convertDiag(d, lineAt, enc)`, `lineAtFunc(src)`, `pathToURI`/`uriToPath`, `newDocStore`/`openInDir` names match across T3/T4/T6. `Server` fields (`conn`,`docs`,`analyzer`,`enc`,`shutdown`,`exited`) defined in T5 and used in T6. ✓
