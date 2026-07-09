package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// handleCodeAction answers textDocument/codeAction for a .gsx document. The only
// action offered is source.organizeImports.
//
// The action ALWAYS applies the goimports transform — remove unused imports and
// reorder (merge/dedup/group/sort) — regardless of the configured
// [formatter] imports mode. That is the point of the action, and it mirrors
// gopls: textDocument/formatting may be plain gofmt while source.organizeImports
// still organizes. This is what lets a project set imports = "gofmt" for
// format-on-save and wire editor.codeActionsOnSave to organize separately.
//
// The edit is a single whole-document TextEdit: gsx has no partial formatter, so
// its canonical form is produced by a whole-document parse → print. Applying the
// action therefore also canonicalizes the rest of the document — a deliberate
// deviation from gopls's import-region-only edits.
//
// An empty result (no action) is returned when the document is not .gsx, when
// context.only excludes the kind, when the buffer does not parse (mid-edit), or
// when the organized document already equals the buffer — so an on-save action
// is a true no-op rather than a redundant edit.
func (s *Server) handleCodeAction(f frame) error {
	var p codeActionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []CodeAction{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	if !strings.HasSuffix(path, ".gsx") {
		return s.reply(f.ID, []CodeAction{}) // gopls owns .go
	}
	if !wantsKind(p.Context.Only, organizeImportsKind) {
		return s.reply(f.ID, []CodeAction{})
	}
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, []CodeAction{})
	}
	dir := filepath.Dir(path)
	var unused []gsxfmt.ImportRef
	if pkg := s.pkgs[dir]; pkg != nil {
		unused = pkg.UnusedImports[path] // nil when analysis is unavailable
	}
	fs := s.analyzer.FormatSettings(path)
	organized, err := gsxfmt.FormatWith(path, []byte(text), gsxfmt.FormatOptions{
		Unused:   unused,
		Width:    fs.Width,
		TabWidth: fs.TabWidth,
		Reorder:  true, // always: this action IS goimports
	})
	if err != nil || string(organized) == text {
		return s.reply(f.ID, []CodeAction{}) // unparseable mid-edit, or already organized
	}
	edit := TextEdit{
		Range:   Range{Start: Position{Line: 0, Character: 0}, End: endPosition(text, s.enc)},
		NewText: string(organized),
	}
	return s.reply(f.ID, []CodeAction{{
		Title: "Organize Imports",
		Kind:  organizeImportsKind,
		Edit:  &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {edit}}},
	}})
}

// wantsKind reports whether a client asking for the kinds in `only` wants `kind`.
// An empty `only` means "any kind". LSP kinds are dot-separated hierarchies, so a
// requested "source" matches "source.organizeImports".
func wantsKind(only []string, kind string) bool {
	if len(only) == 0 {
		return true
	}
	for _, k := range only {
		if k == kind || strings.HasPrefix(kind, k+".") {
			return true
		}
	}
	return false
}
