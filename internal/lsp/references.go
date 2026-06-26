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
//
// The whole-module index (AnalyzeModule) is queried first: it is built lazily,
// cached across requests, and invalidated whenever any document mutates. On
// AnalyzeModule error the single-package CrossIndex (built by Analyze on didOpen)
// is used as a fallback so existing in-package references behaviour is preserved.
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
	curLine := p.Position.Line + 1
	curCol := byteOffsetForPosition(text, p.Position.Line, p.Position.Character, s.enc) -
		lineStartOffset(text, p.Position.Line) + 1

	// Whole-module index (lazy, cached, invalidated on edits). A successful
	// AnalyzeModule — even an empty result — is cached; an error leaves the
	// cache invalid so the single-package fallback below still answers.
	if !s.moduleRefsValid {
		if refs, err := s.analyzer.AnalyzeModule(filepath.Dir(path), s.docs.allOpenGSX()); err == nil {
			s.moduleRefs = refs
			s.moduleRefsValid = true
		}
	}

	found := identifyCrossRef(s.moduleRefs, path, curLine, curCol)
	if found == nil {
		// Fall back to the single-package index (covers AnalyzeModule errors and
		// any cursor the module index did not resolve).
		if pkg := s.pkgs[filepath.Dir(path)]; pkg != nil && len(pkg.CrossIndex) > 0 {
			vals := make([]CrossRef, 0, len(pkg.CrossIndex))
			for k := range pkg.CrossIndex {
				vals = append(vals, pkg.CrossIndex[k])
			}
			found = identifyCrossRef(vals, path, curLine, curCol)
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

// identifyCrossRef finds the component whose declaration (exact NamePos) or a
// .go reference covers the cursor. .gsx-file refs are skipped for identification
// — their //line-derived columns are approximate (see the original references
// comment), so a tag cursor resolves to "no match" rather than an off-column hit.
func identifyCrossRef(refs []CrossRef, path string, curLine, curCol int) *CrossRef {
	for i := range refs {
		cr := refs[i]
		if posCoversCursor(cr.Decl, path, curLine, curCol, len(cr.Name)) {
			return &refs[i]
		}
		for _, r := range cr.Refs {
			if strings.HasSuffix(r.Filename, ".go") &&
				posCoversCursor(r, path, curLine, curCol, len(cr.Name)) {
				return &refs[i]
			}
		}
	}
	return nil
}
