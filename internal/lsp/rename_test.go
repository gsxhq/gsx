package lsp

import (
	"encoding/json"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type renameAnalyzer struct {
	nilAnalyzer
	facts []ComponentParamRenameFact
	err   error
	calls int
}

func (a *renameAnalyzer) AnalyzeModuleParams(string, map[string][]byte) ([]ComponentParamRenameFact, error) {
	a.calls++
	return a.facts, a.err
}

func TestInitializeAdvertisesPrepareRename(t *testing.T) {
	out := drive(t, &renameAnalyzer{}, initFrame()+jsonFrame(map[string]any{
		"jsonrpc": "2.0", "method": "exit",
	}))
	msgs := readFrames(t, out)
	var result initializeResult
	if err := json.Unmarshal(msgs[0]["result"], &result); err != nil {
		t.Fatal(err)
	}
	if result.Capabilities.RenameProvider == nil || !result.Capabilities.RenameProvider.PrepareProvider {
		t.Fatalf("rename capability = %+v, want prepareProvider", result.Capabilities.RenameProvider)
	}
}

func TestPrepareAndRenameComponentParameterFromDeclarationAndInvocation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	source := "package views\ncomponent Card(title string) { <h1>{title}</h1> }\ncomponent Page() { <Card title=\"hello\"/> }\n"
	decl := strings.Index(source, "title string")
	bodyRef := strings.Index(source, "{title}") + 1
	ref := strings.LastIndex(source, "title=")
	fact := renameFact(path, source, ".Card", 0, "title", ComponentParamOrdinary, []int{decl}, []int{bodyRef, ref})
	a := &renameAnalyzer{facts: []ComponentParamRenameFact{fact}}
	uri := pathToURI(path)

	for _, target := range []int{decl, ref} {
		position := positionForByteOffset(source, target+2, encUTF16)
		out := drive(t, a, initFrame()+didOpenFrame(uri, source)+renameRequestFrame(2, "textDocument/prepareRename", uri, position, "")+
			jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
		msg := responseByID(t, out, 2)
		var got prepareRenameResult
		if err := json.Unmarshal(msg["result"], &got); err != nil {
			t.Fatal(err)
		}
		if got.Placeholder != "title" || got.Range != rangeForSpan(source, target, target+len("title"), encUTF16) {
			t.Fatalf("prepare at %d = %+v", target, got)
		}
	}

	position := positionForByteOffset(source, ref+2, encUTF16)
	out := drive(t, a, initFrame()+didOpenFrame(uri, source)+renameRequestFrame(2, "textDocument/rename", uri, position, "heading")+
		jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
	msg := responseByID(t, out, 2)
	var edit WorkspaceEdit
	if err := json.Unmarshal(msg["result"], &edit); err != nil {
		t.Fatal(err)
	}
	edits := edit.Changes[uri]
	if len(edits) != 3 {
		t.Fatalf("edits = %+v, want declaration, body use, and invocation", edit.Changes)
	}
	assertEdit(t, edits[0], source, decl, "title", "heading")
	assertEdit(t, edits[1], source, bodyRef, "title", "heading")
	assertEdit(t, edits[2], source, ref, "title", "heading")
}

func TestRenameComponentParameterAcrossPackagesAndEquivalentVariants(t *testing.T) {
	dir := t.TempDir()
	iconAPath := filepath.Join(dir, "ui", "icon_a.gsx")
	iconBPath := filepath.Join(dir, "ui", "icon_b.gsx")
	pagePath := filepath.Join(dir, "page", "page.gsx")
	iconA := "package ui\ncomponent Icon(value string) { <i>{value}</i> }\n"
	iconB := "package ui\ncomponent Icon(value string) { <b>{value}</b> }\n"
	page := "package page\ncomponent Page() { <ui.Icon value=\"ok\"/> }\n"
	for path, source := range map[string]string{iconAPath: iconA, iconBPath: iconB, pagePath: page} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fact := ComponentParamRenameFact{
		Key:  ComponentParamKey{PackagePath: "example.test/ui", ComponentKey: ".Icon", Ordinal: 0},
		Name: "value", Role: ComponentParamOrdinary, Origin: types.NewVar(token.NoPos, nil, "value", types.Typ[types.String]),
		Decls: []token.Position{
			tokenPosition(iconAPath, iconA, strings.Index(iconA, "value string")),
			tokenPosition(iconBPath, iconB, strings.Index(iconB, "value string")),
		},
		Refs: []token.Position{tokenPosition(pagePath, page, strings.Index(page, "value="))},
	}
	a := &renameAnalyzer{facts: []ComponentParamRenameFact{fact}}
	uri := pathToURI(pagePath)
	position := positionForByteOffset(page, strings.Index(page, "value=")+1, encUTF16)
	out := drive(t, a, initFrame()+didOpenFrame(uri, page)+renameRequestFrame(2, "textDocument/rename", uri, position, "label")+
		jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
	msg := responseByID(t, out, 2)
	var edit WorkspaceEdit
	if err := json.Unmarshal(msg["result"], &edit); err != nil {
		t.Fatal(err)
	}
	if len(edit.Changes) != 3 {
		t.Fatalf("files edited = %+v, want both variants and cross-package invocation", edit.Changes)
	}
	for _, tc := range []struct {
		path   string
		source string
		off    int
	}{{iconAPath, iconA, strings.Index(iconA, "value string")}, {iconBPath, iconB, strings.Index(iconB, "value string")}, {pagePath, page, strings.Index(page, "value=")}} {
		edits := edit.Changes[pathToURI(tc.path)]
		if len(edits) != 1 {
			t.Fatalf("edits for %s = %+v", tc.path, edits)
		}
		assertEdit(t, edits[0], tc.source, tc.off, "value", "label")
	}
}

func TestComponentParameterRenameRejectsReservedTargetsAndNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	source := "package views\ncomponent Card(title string, other string, children gsx.Node, attrs gsx.Attrs) {}\n"
	titleOff := strings.Index(source, "title string")
	otherOff := strings.Index(source, "other string")
	childrenOff := strings.Index(source, "children gsx")
	attrsOff := strings.Index(source, "attrs gsx")
	facts := []ComponentParamRenameFact{
		renameFact(path, source, ".Card", 0, "title", ComponentParamOrdinary, []int{titleOff}, nil),
		renameFact(path, source, ".Card", 1, "other", ComponentParamOrdinary, []int{otherOff}, nil),
		renameFact(path, source, ".Card", 2, "children", ComponentParamChildren, []int{childrenOff}, nil),
		renameFact(path, source, ".Card", 3, "attrs", ComponentParamAttrs, []int{attrsOff}, nil),
	}
	uri := pathToURI(path)

	for _, tc := range []struct {
		name string
		off  int
	}{{"children", childrenOff}, {"attrs", attrsOff}} {
		t.Run("existing_"+tc.name, func(t *testing.T) {
			position := positionForByteOffset(source, tc.off+1, encUTF16)
			out := drive(t, &renameAnalyzer{facts: facts}, initFrame()+didOpenFrame(uri, source)+renameRequestFrame(2, "textDocument/prepareRename", uri, position, "")+
				renameRequestFrame(3, "textDocument/rename", uri, position, "other")+jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
			if got := responseByID(t, out, 2)["result"]; string(got) != "null" {
				t.Fatalf("prepare result = %s, want null", got)
			}
			assertInvalidParams(t, responseByID(t, out, 3))
		})
	}

	for _, newName := range []string{"_", "children", "attrs", "ctx", "_gsxName", "bad-name", "func", ""} {
		t.Run("new_"+newName, func(t *testing.T) {
			position := positionForByteOffset(source, titleOff+1, encUTF16)
			out := drive(t, &renameAnalyzer{facts: facts}, initFrame()+didOpenFrame(uri, source)+renameRequestFrame(2, "textDocument/rename", uri, position, newName)+
				jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
			assertInvalidParams(t, responseByID(t, out, 2))
		})
	}

	t.Run("collision", func(t *testing.T) {
		position := positionForByteOffset(source, titleOff+1, encUTF16)
		out := drive(t, &renameAnalyzer{facts: facts}, initFrame()+didOpenFrame(uri, source)+renameRequestFrame(2, "textDocument/rename", uri, position, "other")+
			jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
		assertInvalidParams(t, responseByID(t, out, 2))
	})
}

func TestComponentParameterRenameRejectsUnavailableAndStaleFamilies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.gsx")
	source := "package views\ncomponent Page() { <GoCard title=\"ok\"/> }\n"
	uri := pathToURI(path)
	off := strings.Index(source, "title=")
	position := positionForByteOffset(source, off+1, encUTF16)

	t.Run("plain Go or non-equivalent variants publish no fact", func(t *testing.T) {
		out := drive(t, &renameAnalyzer{}, initFrame()+didOpenFrame(uri, source)+renameRequestFrame(2, "textDocument/prepareRename", uri, position, "")+
			renameRequestFrame(3, "textDocument/rename", uri, position, "label")+jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
		if got := responseByID(t, out, 2)["result"]; string(got) != "null" {
			t.Fatalf("prepare result = %s, want null", got)
		}
		assertInvalidParams(t, responseByID(t, out, 3))
	})

	t.Run("one stale span rejects the whole edit", func(t *testing.T) {
		fact := renameFact(path, source, ".GoCard", 0, "title", ComponentParamOrdinary, nil, []int{off})
		fact.Decls = []token.Position{tokenPosition(filepath.Join(dir, "missing.gsx"), "title", 0)}
		out := drive(t, &renameAnalyzer{facts: []ComponentParamRenameFact{fact}}, initFrame()+didOpenFrame(uri, source)+renameRequestFrame(2, "textDocument/prepareRename", uri, position, "")+
			renameRequestFrame(3, "textDocument/rename", uri, position, "label")+
			jsonFrame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
		if got := responseByID(t, out, 2)["result"]; string(got) != "null" {
			t.Fatalf("prepare result = %s, want null", got)
		}
		assertInvalidParams(t, responseByID(t, out, 3))
	})
}

func renameFact(path, source, componentKey string, ordinal int, name string, role ComponentParamRole, decls, refs []int) ComponentParamRenameFact {
	fact := ComponentParamRenameFact{
		Key:  ComponentParamKey{PackagePath: "example.test/views", ComponentKey: componentKey, Ordinal: ordinal},
		Name: name, Role: role, Origin: types.NewVar(token.NoPos, nil, name, types.Typ[types.String]),
	}
	for _, off := range decls {
		fact.Decls = append(fact.Decls, tokenPosition(path, source, off))
	}
	for _, off := range refs {
		fact.Refs = append(fact.Refs, tokenPosition(path, source, off))
	}
	return fact
}

func tokenPosition(path, source string, off int) token.Position {
	lineStart := strings.LastIndex(source[:off], "\n") + 1
	return token.Position{
		Filename: path,
		Offset:   off,
		Line:     strings.Count(source[:off], "\n") + 1,
		Column:   off - lineStart + 1,
	}
}

func renameRequestFrame(id int, method, uri string, position Position, newName string) string {
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
	}
	if method == "textDocument/rename" {
		params["newName"] = newName
	}
	return jsonFrame(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func responseByID(t *testing.T, out string, id int) map[string]json.RawMessage {
	t.Helper()
	for _, msg := range readFrames(t, out) {
		var got int
		if err := json.Unmarshal(msg["id"], &got); err == nil && got == id {
			return msg
		}
	}
	t.Fatalf("response %d not found in %s", id, out)
	return nil
}

func assertEdit(t *testing.T, edit TextEdit, source string, off int, oldName, newName string) {
	t.Helper()
	if edit.NewText != newName || edit.Range != rangeForSpan(source, off, off+len(oldName), encUTF16) {
		t.Fatalf("edit = %+v, want %q at %d", edit, newName, off)
	}
}

func assertInvalidParams(t *testing.T, msg map[string]json.RawMessage) {
	t.Helper()
	var got struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(msg["error"], &got); err != nil {
		t.Fatalf("error response = %s: %v", msg["error"], err)
	}
	if got.Code != -32602 {
		t.Fatalf("error = %+v, want invalid params", got)
	}
}
