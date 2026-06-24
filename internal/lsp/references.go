package lsp

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// handleReferences returns every reference to the gsx component under the cursor
// — .go call sites and .gsx <Card/> tags — from the cross-index. The RESULT
// always spans both .go and .gsx sites; the cursor that INVOKES it may be on the
// component's .gsx declaration or on a .go call site. Identifying the component
// from a .gsx <Card/> tag cursor is deferred (it needs component-tag resolution
// like definition's D2; the tag's //line column is approximate) — a tag cursor
// returns empty rather than a flaky off-by-column match.
func (s *Server) handleReferences(f frame) error {
	var p referenceParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.reply(f.ID, []Location{})
	}
	uri := p.TextDocument.URI
	path := uriToPath(uri)
	text, ok := s.docs.text(uri)
	if !ok {
		return s.reply(f.ID, []Location{})
	}
	pkg := s.pkgs[filepath.Dir(path)]
	if pkg == nil || len(pkg.CrossIndex) == 0 {
		return s.reply(f.ID, []Location{})
	}
	curLine := p.Position.Line + 1
	curCol := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc) -
		lineStartOffset(text, p.Position.Line) + 1

	// Identify the component by an EXACT cursor match: its .gsx declaration
	// (NamePos, exact) or a .go reference (real positions). Skip .gsx-file refs
	// for identification — their //line-derived columns are approximate, so a
	// tag cursor resolves predictably to "no match" instead of an off-column hit.
	var found *CrossRef
	for k := range pkg.CrossIndex {
		cr := pkg.CrossIndex[k]
		if posCoversCursor(cr.Decl, path, curLine, curCol, len(cr.Name)) {
			found = &cr
			break
		}
		for _, r := range cr.Refs {
			if strings.HasSuffix(r.Filename, ".go") &&
				posCoversCursor(r, path, curLine, curCol, len(cr.Name)) {
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
