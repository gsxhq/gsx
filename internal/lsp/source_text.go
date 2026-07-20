package lsp

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

type capturedSource struct {
	stringValue string
	bytesValue  []byte
	open        bool
	hasString   bool
	hasBytes    bool
	ok          bool
}

// requestSourceSnapshot is one immutable view of editor and saved source for a
// request. Open documents are captured together; saved files are read lazily at
// most once per path and then retained for the request's lifetime.
type requestSourceSnapshot struct {
	enc     encoding
	sources map[string]*capturedSource
}

func (s *Server) sourceSnapshot() *requestSourceSnapshot {
	snapshot := &requestSourceSnapshot{enc: s.enc, sources: make(map[string]*capturedSource)}
	if s.docs == nil {
		return snapshot
	}
	s.docs.mu.Lock()
	for uri, document := range s.docs.docs {
		path := sourcePath(uri)
		if path == "" || strings.HasSuffix(path, ".x.go") {
			continue
		}
		snapshot.sources[path] = &capturedSource{stringValue: document.text, open: true, hasString: true, ok: true}
	}
	s.docs.mu.Unlock()
	return snapshot
}

// openGSXOverrides materializes the open GSX buffers retained by this request.
// Whole-module analysis consumes every override, so these are all used paths;
// unopened files remain lazy and are still read at most once by sourceText.
func (snapshot *requestSourceSnapshot) openGSXOverrides() map[string][]byte {
	overrides := make(map[string][]byte)
	for path, source := range snapshot.sources {
		if !source.open || !strings.HasSuffix(path, ".gsx") {
			continue
		}
		text, ok := snapshot.sourceText(path)
		if ok {
			overrides[path] = text
		}
	}
	return overrides
}

func (snapshot *requestSourceSnapshot) anyOpenDir() string {
	dir := ""
	for path, source := range snapshot.sources {
		if !source.open {
			continue
		}
		candidate := filepath.Dir(path)
		if dir == "" || candidate < dir {
			dir = candidate
		}
	}
	if dir == "" {
		return "."
	}
	return dir
}

func (snapshot *requestSourceSnapshot) sourceText(path string) ([]byte, bool) {
	source, ok := snapshot.source(path)
	if source == nil || !ok {
		return nil, false
	}
	if !source.hasBytes {
		source.bytesValue = []byte(source.stringValue)
		source.hasBytes = true
	}
	return source.bytesValue, true
}

func (snapshot *requestSourceSnapshot) sourceString(path string) (string, bool) {
	source, ok := snapshot.source(path)
	if source == nil || !ok {
		return "", false
	}
	if !source.hasString {
		source.stringValue = string(source.bytesValue)
		source.hasString = true
	}
	return source.stringValue, true
}

func (snapshot *requestSourceSnapshot) source(path string) (*capturedSource, bool) {
	path = sourcePath(path)
	if path == "" || strings.HasSuffix(path, ".x.go") {
		return nil, false
	}
	if source, found := snapshot.sources[path]; found {
		return source, source.ok
	}
	text, err := os.ReadFile(path)
	source := &capturedSource{bytesValue: text, hasBytes: true, ok: err == nil}
	snapshot.sources[path] = source
	return source, source.ok
}

// sourceText is the one-shot compatibility wrapper for callers outside a
// request handler. Handlers create one requestSourceSnapshot and reuse it.
func (s *Server) sourceText(path string) ([]byte, bool) {
	return s.sourceSnapshot().sourceText(path)
}

func (snapshot *requestSourceSnapshot) position(path string, offset int) (Position, bool) {
	text, ok := snapshot.sourceString(path)
	if !ok || offset < 0 || offset > len(text) {
		return Position{}, false
	}
	return positionForByteOffset(text, offset, snapshot.enc), true
}

func (s *Server) position(path string, offset int) (Position, bool) {
	return s.sourceSnapshot().position(path, offset)
}

func (snapshot *requestSourceSnapshot) rangeForSpan(span sourceintel.Span) (Range, bool) {
	text, ok := snapshot.sourceString(span.Path)
	if !ok || span.Start < 0 || span.End < span.Start || span.End > len(text) {
		return Range{}, false
	}
	return rangeForSpan(text, span.Start, span.End, snapshot.enc), true
}

func (s *Server) rangeForSpan(span sourceintel.Span) (Range, bool) {
	return s.sourceSnapshot().rangeForSpan(span)
}

func (snapshot *requestSourceSnapshot) locationForSpan(span sourceintel.Span) (Location, bool) {
	rng, ok := snapshot.rangeForSpan(span)
	if !ok {
		return Location{}, false
	}
	return Location{URI: pathToURI(sourcePath(span.Path)), Range: rng}, true
}

func (s *Server) locationForSpan(span sourceintel.Span) (Location, bool) {
	return s.sourceSnapshot().locationForSpan(span)
}

func (snapshot *requestSourceSnapshot) locationForVersionedSpan(versioned sourceintel.VersionedSpan) (Location, bool) {
	text, ok := snapshot.sourceText(versioned.Span.Path)
	if !ok || !versioned.SourceVersion.Matches(text) {
		return Location{}, false
	}
	return snapshot.locationForSpan(versioned.Span)
}

func sourcePath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(uriToPath(path))
}

func (snapshot *requestSourceSnapshot) locationForAuthoredPosition(pos token.Position, length int) (Location, bool) {
	span, ok := authoredSpanForPosition(pos, length)
	if !ok {
		return Location{}, false
	}
	return snapshot.locationForSpan(span)
}

func (s *Server) locationForAuthoredPosition(pos token.Position, length int) (Location, bool) {
	return s.sourceSnapshot().locationForAuthoredPosition(pos, length)
}

// authoredSpanForPosition uses filename and Offset only. A zero Line/Column is
// valid because adjusted token positions are not the authority for GSX facts.
func authoredSpanForPosition(pos token.Position, length int) (sourceintel.Span, bool) {
	if pos.Filename == "" || !strings.HasSuffix(pos.Filename, ".gsx") || length < 0 || pos.Offset < 0 {
		return sourceintel.Span{}, false
	}
	return sourceintel.Span{Path: sourcePath(pos.Filename), Start: pos.Offset, End: pos.Offset + length}, true
}

func (snapshot *requestSourceSnapshot) locationForGoPosition(pos token.Position, length int) (Location, bool) {
	if pos.Filename == "" || length < 0 || !strings.HasSuffix(pos.Filename, ".go") || strings.HasSuffix(pos.Filename, ".x.go") {
		return Location{}, false
	}
	text, available := snapshot.sourceText(pos.Filename)
	if available {
		start, ok := offsetForTokenPosition(text, pos)
		if !ok || start+length > len(text) {
			return Location{}, false
		}
		return snapshot.locationForSpan(sourceintel.Span{
			Path: sourcePath(pos.Filename), Start: start, End: start + length,
		})
	}
	line := pos.Line - 1
	start := pos.Column - 1
	if line < 0 || start < 0 {
		return Location{}, false
	}
	return Location{
		URI: pathToURI(sourcePath(pos.Filename)),
		Range: Range{
			Start: Position{Line: line, Character: start},
			End:   Position{Line: line, Character: start + length},
		},
	}, true
}

func (s *Server) locationForGoPosition(pos token.Position, length int) (Location, bool) {
	return s.sourceSnapshot().locationForGoPosition(pos, length)
}

func (snapshot *requestSourceSnapshot) rangeForAuthoredPositions(start, end token.Position) (Range, bool) {
	startSpan, ok := authoredSpanForPosition(start, 0)
	if !ok || end.Filename == "" || !strings.HasSuffix(end.Filename, ".gsx") || end.Offset < 0 || sourcePath(start.Filename) != sourcePath(end.Filename) {
		return Range{}, false
	}
	startSpan.End = end.Offset
	return snapshot.rangeForSpan(startSpan)
}

func (snapshot *requestSourceSnapshot) locationForResolvedPosition(pos token.Position, length int) (Location, bool) {
	switch {
	case strings.HasSuffix(pos.Filename, ".gsx"):
		return snapshot.locationForAuthoredPosition(pos, length)
	case strings.HasSuffix(pos.Filename, ".go") && !strings.HasSuffix(pos.Filename, ".x.go"):
		return snapshot.locationForGoPosition(pos, length)
	default:
		return Location{}, false
	}
}

func offsetForTokenPosition(text []byte, pos token.Position) (int, bool) {
	if pos.Line < 1 || pos.Column < 1 {
		return 0, false
	}
	lineStart := 0
	for line := 1; line < pos.Line; line++ {
		relative := bytes.IndexByte(text[lineStart:], '\n')
		if relative < 0 {
			return 0, false
		}
		lineStart += relative + 1
	}
	lineEnd := len(text)
	if relative := bytes.IndexByte(text[lineStart:], '\n'); relative >= 0 {
		lineEnd = lineStart + relative
	}
	offset := lineStart + pos.Column - 1
	if offset < lineStart || offset > lineEnd {
		return 0, false
	}
	return offset, true
}
