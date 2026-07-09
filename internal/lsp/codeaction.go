package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// handleCodeAction answers textDocument/codeAction for a .gsx document. Two
// kinds are offered:
//
//   - source.organizeImports: removes unused imports and reorders (merge/dedup/
//     group/sort), ALWAYS, regardless of the configured [formatter] imports mode
//     — that is the point of the action, mirroring gopls (textDocument/formatting
//     may be plain gofmt while source.organizeImports still organizes). It also
//     adds a missing qualifier, but ONLY when Analyzer.ResolveImport reports
//     EXACTLY ONE candidate: this action is meant for format-on-save, so an
//     ambiguous name is left alone rather than guessed — a wrong import written
//     unattended is unrecoverable.
//   - quickfix: one action per candidate for every unresolved qualifier in the
//     file (e.g. "Add import: crypto/rand", "Add import: math/rand"), so the
//     user picks when organizeImports could not.
//
// Both edits are a single whole-document TextEdit: gsx has no partial
// formatter, so its canonical form is produced by a whole-document parse →
// print. Applying either action therefore also canonicalizes the rest of the
// document — a deliberate deviation from gopls's import-region-only edits.
//
// An empty result (no actions of a kind) is returned when the document is not
// .gsx, when context.only excludes the kind, when the buffer does not parse
// (mid-edit), or when the resulting document already equals the buffer — so an
// on-save action is a true no-op rather than a redundant edit.
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
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, []CodeAction{})
	}
	dir := filepath.Dir(path)
	var unused []gsxfmt.ImportRef
	if pkg := s.pkgs[dir]; pkg != nil {
		unused = pkg.UnusedImports[path] // nil when analysis is unavailable
	}
	missing := s.missingImportsFor(dir, path)
	width := s.analyzer.PrintWidth(dir)

	var actions []CodeAction
	if wantsKind(p.Context.Only, organizeImportsKind) {
		organized, err := gsxfmt.FormatWith(path, []byte(text), gsxfmt.FormatOptions{
			Unused:  unused,
			Add:     s.addsForOrganize(dir, missing),
			Width:   width,
			Reorder: true, // always: this action IS goimports
		})
		if err == nil && string(organized) != text {
			actions = append(actions, CodeAction{
				Title: "Organize Imports",
				Kind:  organizeImportsKind,
				Edit:  &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {{Range: Range{Start: Position{Line: 0, Character: 0}, End: endPosition(text, s.enc)}, NewText: string(organized)}}}},
			})
		}
		// err != nil: unparseable mid-edit buffer, offer nothing.
		// organized == text: already organized, a true no-op.
	}
	if wantsKind(p.Context.Only, quickFixKind) {
		for _, mi := range missing {
			for _, cand := range s.analyzer.ResolveImport(dir, mi.Name, mi.Symbol) {
				edited, err := gsxfmt.FormatWith(path, []byte(text), gsxfmt.FormatOptions{
					Unused: unused,
					// HARD CONTRACT: never pass a Name (alias) here. astutil.AddNamedImport
					// binds Name as the local identifier; if it happened to already be bound
					// to a different path, it would emit invalid Go (two imports aliased to
					// the same short name). A bare ImportRef{Path: cand} always binds the
					// import's own package name.
					Add:     []gsxfmt.ImportRef{{Path: cand}},
					Width:   width,
					Reorder: true,
				})
				if err != nil || string(edited) == text {
					continue // unparseable mid-edit, or this candidate changed nothing
				}
				actions = append(actions, CodeAction{
					Title: "Add import: " + cand,
					Kind:  quickFixKind,
					Edit:  &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {{Range: Range{Start: Position{Line: 0, Character: 0}, End: endPosition(text, s.enc)}, NewText: string(edited)}}}},
				})
			}
		}
	}
	if actions == nil {
		actions = []CodeAction{}
	}
	return s.reply(f.ID, actions)
}

// missingImportsFor returns the file's unresolved qualifiers, or nil when
// analysis is unavailable for dir or reports none for path.
func (s *Server) missingImportsFor(dir, path string) []MissingImport {
	pkg := s.pkgs[dir]
	if pkg == nil {
		return nil
	}
	return pkg.MissingImports[path]
}

// addsForOrganize resolves each missing qualifier and keeps only those with
// EXACTLY ONE candidate.
//
// organizeImports runs non-interactively (format-on-save). An ambiguous name has
// no safe answer without asking the user, and a wrong import written on save is
// unrecoverable — so ambiguity is left to the quickfix, which offers one action
// per candidate. Never guess here.
func (s *Server) addsForOrganize(dir string, missing []MissingImport) []gsxfmt.ImportRef {
	seen := map[string]bool{}
	var adds []gsxfmt.ImportRef
	for _, mi := range missing {
		cands := s.analyzer.ResolveImport(dir, mi.Name, mi.Symbol)
		if len(cands) != 1 || seen[cands[0]] {
			continue
		}
		seen[cands[0]] = true
		// HARD CONTRACT: Name is deliberately left unset (never an alias). See the
		// quickfix branch above for why an aliased ImportRef is unsafe here.
		adds = append(adds, gsxfmt.ImportRef{Path: cands[0]})
	}
	return adds
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
