package lsp

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// sourceText returns the authoritative bytes for path. An open editor snapshot
// takes precedence over the saved file.
func (s *Server) sourceText(path string) ([]byte, bool) {
	path = sourcePath(path)
	if path == "" || strings.HasSuffix(path, ".x.go") {
		return nil, false
	}
	if s.docs != nil {
		s.docs.mu.Lock()
		if document, ok := s.docs.docs[pathToURI(path)]; ok {
			text := []byte(document.text)
			s.docs.mu.Unlock()
			return text, true
		}
		for uri, document := range s.docs.docs {
			if sourcePath(uri) == path {
				text := []byte(document.text)
				s.docs.mu.Unlock()
				return text, true
			}
		}
		s.docs.mu.Unlock()
	}
	text, err := os.ReadFile(path)
	return text, err == nil
}

// position converts an authored byte offset using the negotiated LSP encoding.
// Invalid offsets fail closed rather than being clamped into the snapshot.
func (s *Server) position(path string, offset int) (Position, bool) {
	text, ok := s.sourceText(path)
	if !ok || offset < 0 || offset > len(text) {
		return Position{}, false
	}
	return positionForByteOffset(string(text), offset, s.enc), true
}

// rangeForSpan converts an exact authored byte span using one authoritative
// source snapshot. Invalid, reversed, and out-of-snapshot spans fail closed.
func (s *Server) rangeForSpan(span sourceintel.Span) (Range, bool) {
	text, ok := s.sourceText(span.Path)
	if !ok || span.Start < 0 || span.End < span.Start || span.End > len(text) {
		return Range{}, false
	}
	return rangeForSpan(string(text), span.Start, span.End, s.enc), true
}

func (s *Server) locationForSpan(span sourceintel.Span) (Location, bool) {
	rng, ok := s.rangeForSpan(span)
	if !ok {
		return Location{}, false
	}
	return Location{URI: pathToURI(sourcePath(span.Path)), Range: rng}, true
}

func sourcePath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(uriToPath(path))
}

// locationForAuthoredPosition converts an exact .gsx token offset to an
// authored span. Line and column are deliberately not an alternative source of
// truth for authored facts.
func (s *Server) locationForAuthoredPosition(pos token.Position, length int) (Location, bool) {
	span, ok := authoredSpanForPosition(pos, length)
	if !ok {
		return Location{}, false
	}
	return s.locationForSpan(span)
}

func authoredSpanForPosition(pos token.Position, length int) (sourceintel.Span, bool) {
	if !pos.IsValid() || !strings.HasSuffix(pos.Filename, ".gsx") || length < 0 || pos.Offset < 0 {
		return sourceintel.Span{}, false
	}
	return sourceintel.Span{Path: sourcePath(pos.Filename), Start: pos.Offset, End: pos.Offset + length}, true
}

// locationForGoPosition adapts a genuine Go dependency token position. It
// consults authoritative source text before interpreting line/column. The raw
// byte-column fallback exists only when that real .go source is unavailable.
func (s *Server) locationForGoPosition(pos token.Position, length int) (Location, bool) {
	if pos.Filename == "" || length < 0 || !strings.HasSuffix(pos.Filename, ".go") || strings.HasSuffix(pos.Filename, ".x.go") {
		return Location{}, false
	}
	span, ok := s.spanForGoPosition(pos, length)
	if ok {
		return s.locationForSpan(span)
	}
	if _, available := s.sourceText(pos.Filename); available || !strings.HasSuffix(pos.Filename, ".go") {
		return Location{}, false
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

func (s *Server) spanForGoPosition(pos token.Position, length int) (sourceintel.Span, bool) {
	text, ok := s.sourceText(pos.Filename)
	if !ok || length < 0 {
		return sourceintel.Span{}, false
	}
	start, ok := offsetForTokenPosition(text, pos)
	if !ok || start+length > len(text) {
		return sourceintel.Span{}, false
	}
	return sourceintel.Span{Path: sourcePath(pos.Filename), Start: start, End: start + length}, true
}

func (s *Server) rangeForAuthoredPositions(start, end token.Position) (Range, bool) {
	startSpan, ok := authoredSpanForPosition(start, 0)
	if !ok || !end.IsValid() || !strings.HasSuffix(end.Filename, ".gsx") || end.Offset < 0 || sourcePath(start.Filename) != sourcePath(end.Filename) {
		return Range{}, false
	}
	startSpan.End = end.Offset
	return s.rangeForSpan(startSpan)
}

func (s *Server) locationForResolvedPosition(pos token.Position, length int) (Location, bool) {
	switch {
	case strings.HasSuffix(pos.Filename, ".gsx"):
		return s.locationForAuthoredPosition(pos, length)
	case strings.HasSuffix(pos.Filename, ".go") && !strings.HasSuffix(pos.Filename, ".x.go"):
		return s.locationForGoPosition(pos, length)
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
