package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var errComponentParamNotRenamable = errors.New("component parameter is not available for semantic rename")

func (s *Server) handlePrepareRename(f frame) error {
	var params textDocumentPositionParams
	if err := json.Unmarshal(f.Params, &params); err != nil {
		return s.reply(f.ID, nil)
	}
	fact, span, err := s.componentParamAt(params.TextDocument.URI, params.Position)
	if err != nil || !componentParamCanRename(fact) {
		return s.reply(f.ID, nil)
	}
	if _, err := s.componentParamWorkspaceEdit(fact, fact.Name); err != nil {
		return s.reply(f.ID, nil)
	}
	return s.reply(f.ID, prepareRenameResult{
		Range:       span,
		Placeholder: fact.Name,
	})
}

func (s *Server) handleRename(f frame) error {
	var params renameParams
	if err := json.Unmarshal(f.Params, &params); err != nil {
		return s.replyError(f.ID, -32602, "invalid rename parameters")
	}
	fact, _, err := s.componentParamAt(params.TextDocument.URI, params.Position)
	if err != nil {
		return s.replyError(f.ID, -32602, err.Error())
	}
	if !componentParamCanRename(fact) {
		return s.replyError(f.ID, -32602, "reserved component parameters cannot be renamed")
	}
	if err := validateComponentParamName(params.NewName); err != nil {
		return s.replyError(f.ID, -32602, err.Error())
	}
	for _, other := range s.moduleParams {
		if other.Key.PackagePath == fact.Key.PackagePath &&
			other.Key.ComponentKey == fact.Key.ComponentKey &&
			other.Key.Ordinal != fact.Key.Ordinal && other.Name == params.NewName {
			return s.replyError(f.ID, -32602, fmt.Sprintf("component parameter %q already exists", params.NewName))
		}
	}

	edit, err := s.componentParamWorkspaceEdit(fact, params.NewName)
	if err != nil {
		return s.replyError(f.ID, -32602, err.Error())
	}
	return s.reply(f.ID, edit)
}

// componentParamAt resolves only retained semantic parameter facts. It does not
// inspect declaration or invocation text to infer identity; source is consulted
// solely to translate and verify the exact span published by analysis.
func (s *Server) componentParamAt(uri string, position Position) (*ComponentParamRenameFact, Range, error) {
	path := filepath.Clean(uriToPath(uri))
	text, ok := s.docs.text(uri)
	if !ok {
		return nil, Range{}, errComponentParamNotRenamable
	}
	if err := s.loadModuleParams(filepath.Dir(path)); err != nil {
		return nil, Range{}, fmt.Errorf("component parameter analysis failed: %w", err)
	}
	offset := byteOffsetForPosition(text, position.Line, position.Character, s.enc)
	var found *ComponentParamRenameFact
	var foundPos token.Position
	for index := range s.moduleParams {
		fact := &s.moduleParams[index]
		for _, pos := range componentParamPositions(fact) {
			if filepath.Clean(pos.Filename) != path || offset < pos.Offset || offset >= pos.Offset+len(fact.Name) {
				continue
			}
			if found != nil && found != fact {
				return nil, Range{}, errors.New("ambiguous component parameter facts")
			}
			found = fact
			foundPos = pos
		}
	}
	if found == nil {
		return nil, Range{}, errComponentParamNotRenamable
	}
	if err := verifyComponentParamSpan(text, foundPos, found.Name); err != nil {
		return nil, Range{}, err
	}
	return found, rangeForSpan(text, foundPos.Offset, foundPos.Offset+len(found.Name), s.enc), nil
}

func (s *Server) loadModuleParams(dir string) error {
	dir = filepath.Clean(dir)
	if s.moduleParamsValid && s.moduleParamsDir == dir {
		return nil
	}
	facts, err := s.analyzer.AnalyzeModuleParams(dir, s.docs.allOpenGSX())
	if err != nil {
		return err
	}
	seen := make(map[ComponentParamKey]bool, len(facts))
	for index := range facts {
		fact := &facts[index]
		if fact.Key.PackagePath == "" || fact.Key.ComponentKey == "" || fact.Key.Ordinal < 0 ||
			fact.Name == "" || fact.Origin == nil || len(fact.Decls) == 0 {
			return errors.New("component parameter analysis returned an incomplete family")
		}
		if seen[fact.Key] {
			return errors.New("component parameter analysis returned duplicate families")
		}
		seen[fact.Key] = true
	}
	s.moduleParams = facts
	s.moduleParamsValid = true
	s.moduleParamsDir = dir
	return nil
}

func componentParamCanRename(fact *ComponentParamRenameFact) bool {
	if fact == nil {
		return false
	}
	if fact.Role == ComponentParamAttrs || fact.Role == ComponentParamChildren {
		return false
	}
	return fact.Name != "attrs" && fact.Name != "children"
}

func validateComponentParamName(name string) error {
	if !token.IsIdentifier(name) {
		return fmt.Errorf("%q is not a valid Go identifier", name)
	}
	if name == "_" || name == "children" || name == "attrs" || name == "ctx" || strings.HasPrefix(name, "_gsx") {
		return fmt.Errorf("%q is reserved in component signatures", name)
	}
	return nil
}

func (s *Server) componentParamWorkspaceEdit(fact *ComponentParamRenameFact, newName string) (WorkspaceEdit, error) {
	positions := componentParamPositions(fact)
	sort.Slice(positions, func(i, j int) bool {
		left, right := filepath.Clean(positions[i].Filename), filepath.Clean(positions[j].Filename)
		if left != right {
			return left < right
		}
		return positions[i].Offset < positions[j].Offset
	})

	changes := make(map[string][]TextEdit)
	seen := make(map[string]bool, len(positions))
	for _, pos := range positions {
		path := filepath.Clean(pos.Filename)
		key := path + ":" + strconv.Itoa(pos.Offset)
		if seen[key] {
			continue
		}
		seen[key] = true
		text, err := s.componentParamSource(path)
		if err != nil {
			return WorkspaceEdit{}, fmt.Errorf("cannot verify complete rename family: %w", err)
		}
		if err := verifyComponentParamSpan(text, pos, fact.Name); err != nil {
			return WorkspaceEdit{}, err
		}
		uri := pathToURI(path)
		changes[uri] = append(changes[uri], TextEdit{
			Range:   rangeForSpan(text, pos.Offset, pos.Offset+len(fact.Name), s.enc),
			NewText: newName,
		})
	}
	return WorkspaceEdit{Changes: changes}, nil
}

func (s *Server) componentParamSource(path string) (string, error) {
	if text, ok := s.docs.text(pathToURI(path)); ok {
		return text, nil
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(source), nil
}

func componentParamPositions(fact *ComponentParamRenameFact) []token.Position {
	positions := make([]token.Position, 0, len(fact.Decls)+len(fact.Refs))
	positions = append(positions, fact.Decls...)
	positions = append(positions, fact.Refs...)
	return positions
}

func verifyComponentParamSpan(text string, pos token.Position, name string) error {
	if !pos.IsValid() || pos.Filename == "" || pos.Offset < 0 || pos.Offset+len(name) > len(text) ||
		text[pos.Offset:pos.Offset+len(name)] != name {
		return errors.New("cannot verify complete rename family against current source")
	}
	return nil
}
