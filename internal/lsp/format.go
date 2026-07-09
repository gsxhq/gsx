package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// handleFormatting answers textDocument/formatting for a .gsx document: it
// returns the canonical form (parse → whitespace-normalize → print) as a single
// whole-document TextEdit. It operates on the live buffer text, so unsaved edits
// are formatted. Only .gsx files are handled; gopls owns .go formatting.
//
// It honors the resolved gsx.toml [formatter] imports mode: goimports mode
// removes unused imports and reorders/merges import declarations, gofmt mode
// does neither.
//
// On a parse failure (the buffer is mid-edit and not valid gsx) it returns no
// edits rather than a destructive whole-file replacement; the same for a buffer
// that is already canonical.
func (s *Server) handleFormatting(f frame) error {
	var p documentFormattingParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []TextEdit{})
	}
	uri := p.TextDocument.URI
	if !strings.HasSuffix(uriToPath(uri), ".gsx") {
		return s.reply(f.ID, []TextEdit{}) // not a .gsx file
	}
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, []TextEdit{})
	}
	path := uriToPath(uri)
	dir := filepath.Dir(path)
	mode := s.analyzer.ImportsMode(dir)

	// Only goimports mode removes unused imports; gofmt mode leaves them.
	var unused []gsxfmt.ImportRef
	if mode.RemoveUnused() {
		if pkg := s.pkgs[dir]; pkg != nil {
			unused = pkg.UnusedImports[path] // nil when analysis is unavailable/unreliable
		}
	}
	width := s.analyzer.PrintWidth(dir)
	// CSSFmt/JSFmt stay nil: LSP formatting has never run the <style>/<script>
	// formatters that the CLI runs. Preserving that here keeps output identical;
	// closing the gap is a separate change.
	formatted, err := gsxfmt.FormatWith(path, []byte(text), gsxfmt.FormatOptions{
		Unused:  unused,
		Width:   width,
		Reorder: mode.Reorder(),
	})
	if err != nil || string(formatted) == text {
		return s.reply(f.ID, []TextEdit{}) // invalid mid-edit, or already canonical
	}
	edit := TextEdit{
		Range:   Range{Start: Position{Line: 0, Character: 0}, End: endPosition(text, s.enc)},
		NewText: string(formatted),
	}
	return s.reply(f.ID, []TextEdit{edit})
}

// endPosition returns the LSP position one past the last character of text — the
// end anchor of a whole-document range. The end character is counted in the
// negotiated encoding.
func endPosition(text string, enc encoding) Position {
	line := strings.Count(text, "\n")
	lastLineStart := strings.LastIndexByte(text, '\n') + 1 // 0 when there is no newline
	last := text[lastLineStart:]
	return Position{Line: line, Character: charForByteCol(last, len(last)+1, enc)}
}
