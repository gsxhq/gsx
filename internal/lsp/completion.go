package lsp

import (
	"encoding/json"
	"strings"
)

// handleCompletion answers textDocument/completion for a .gsx file. .go files
// are gopls's to complete (null). Source-state problems (mid-edit breakage,
// package-clause mismatch) yield an empty list, never an error: completion is
// advisory and must fail soft.
func (s *Server) handleCompletion(f frame) error {
	var p completionParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, nil)
	}
	if !s.diskViewValid {
		return s.reply(f.ID, emptyCompletion())
	}
	path := uriToPath(p.TextDocument.URI)
	if strings.HasSuffix(path, ".go") {
		return s.reply(f.ID, nil) // gopls owns .go completion
	}
	sources := s.sourceSnapshot()
	text, ok := sources.sourceString(path)
	if !ok {
		return s.reply(f.ID, emptyCompletion())
	}
	_ = text // consumed from Task 6 on
	return s.reply(f.ID, emptyCompletion())
}

func emptyCompletion() CompletionList {
	return CompletionList{IsIncomplete: false, Items: []CompletionItem{}}
}
