package lsp

import (
	"encoding/json"
	"path/filepath"
)

// handleReferences returns every reference to the gsx component under the cursor
// — .go call sites and .gsx <Card/> tags — from the cross-index. Cursor may be
// in the component's .gsx declaration, a .gsx tag, or a .go call.
func (s *Server) handleReferences(f frame) error {
	var p referenceParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []Location{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || len(pkg.CrossIndex) == 0 {
		return s.reply(f.ID, []Location{})
	}
	text, _ := s.docs.text(uri)
	curLine := p.Position.Line + 1
	curCol := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc) -
		lineStartOffset(text, p.Position.Line) + 1

	// Identify the component: the cursor is on its Decl, or on one of its Refs.
	var found *CrossRef
	for k := range pkg.CrossIndex {
		cr := pkg.CrossIndex[k]
		if posCoversCursor(cr.Decl, path, curLine, curCol, len(cr.Name)) {
			found = &cr
			break
		}
		for _, r := range cr.Refs {
			if posCoversCursor(r, path, curLine, curCol, len(cr.Name)) {
				found = &cr
				break
			}
		}
		if found != nil {
			break
		}
	}
	if found == nil {
		return s.reply(f.ID, []Location{})
	}

	locs := make([]Location, 0, len(found.Refs)+1)
	for _, r := range found.Refs {
		locs = append(locs, s.locationForPos(r))
	}
	if p.Context.IncludeDeclaration {
		locs = append(locs, s.locationForPos(found.Decl))
	}
	return s.reply(f.ID, locs)
}
