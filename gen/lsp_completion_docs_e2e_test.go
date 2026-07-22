package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCompletionEagerDocE2E drives textDocument/completion through the full
// JSON-RPC server (T9): a documented package-scope func offered at a bare
// scope cursor carries its doc comment inline, as `documentation.value` on
// the SAME completion reply — no completionItem/resolve round trip needed.
func TestCompletionEagerDocE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	source := "package page\n\n" +
		"// Greeting renders a friendly hello to name.\n" +
		"func Greeting(name string) string {\n\treturn \"Hello, \" + name\n}\n\n" +
		"component Home() {\n\t<div>{ Greeting(\"world\") }</div>\n}\n"
	pagePath := write("page/page.gsx", source)
	uri := "file://" + pagePath

	cursor := strings.Index(source, "{ Greeting(") + len("{ Gree") // mid-identifier, inside the interp

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	var input strings.Builder
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	}))
	pos := lspUTF16PositionAt(source, cursor)
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		},
	}))
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}

	items := completionItems(t, output.String(), 2)
	var docValue string
	found := false
	for _, it := range items {
		if it.Label != "Greeting" {
			continue
		}
		found = true
		if it.Documentation != nil {
			docValue = it.Documentation.Value
		}
	}
	if !found {
		t.Fatalf("completion missing package-scope func `Greeting`; items=%+v", items)
	}
	if docValue == "" {
		t.Fatal("`Greeting` completion item carries no Documentation, want the eager doc comment")
	}
	if !strings.Contains(docValue, "Greeting renders a friendly hello") {
		t.Errorf("`Greeting` Documentation = %q, want it to contain the doc comment text", docValue)
	}

	// Confirm via the RAW reply that an eager item carries NO "data" (eager
	// and lazy are mutually exclusive per the design's uniform rule).
	rawItem := rawCompletionItemByLabel(t, output.String(), 2, "Greeting")
	if _, hasData := rawItem["data"]; hasData {
		t.Errorf("eager `Greeting` item carries a \"data\" field, want none: %s", rawItem["data"])
	}
}

// TestCompletionResolveStdlibE2E drives completionItem/resolve through the
// full JSON-RPC server (T10): a member-completion item for a real stdlib
// symbol (`strings.HasPrefix`) carries no Documentation in the initial
// completion reply (lazy) but a non-empty one after the client round-trips
// the item's untouched Data back via completionItem/resolve — the exact
// client contract (send the item back verbatim, Data included).
func TestCompletionResolveStdlibE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	source := "package page\n\nimport \"strings\"\n\ncomponent Home() {\n\t<div>{ strings. }</div>\n\t<span>{ strings.ToUpper(\"x\") }</span>\n}\n"
	pagePath := write("page/page.gsx", source)
	uri := "file://" + pagePath

	cursor := strings.Index(source, "{ strings. }") + len("{ strings.")

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	var input strings.Builder
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	}))
	pos := lspUTF16PositionAt(source, cursor)
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		},
	}))

	var output1, stderr bytes.Buffer
	// Run just far enough to capture the completion response; the resolve
	// request needs the RAW item echoed back, so it is built from this
	// output before continuing the session.
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))
	if code := runLSP(strings.NewReader(input.String()), &output1, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}

	rawItem := rawCompletionItemByLabel(t, output1.String(), 2, "HasPrefix")
	if _, hasDoc := rawItem["documentation"]; hasDoc {
		t.Errorf("lazy `HasPrefix` item already carries \"documentation\" before resolve: %s", rawItem["documentation"])
	}
	dataField, hasData := rawItem["data"]
	if !hasData || len(dataField) == 0 || string(dataField) == "null" {
		t.Fatalf("lazy `HasPrefix` item carries no \"data\" to resolve: %+v", rawItem)
	}

	// Build the full item JSON to echo back verbatim, exactly as a real
	// client does (it round-trips the WHOLE item, not just Data).
	itemJSON, err := json.Marshal(rawItem)
	if err != nil {
		t.Fatal(err)
	}

	var resolveInput strings.Builder
	resolveInput.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	resolveInput.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "completionItem/resolve",
		"params": json.RawMessage(itemJSON),
	}))
	resolveInput.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output2 bytes.Buffer
	if code := runLSP(strings.NewReader(resolveInput.String()), &output2, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}

	var resolved struct {
		Documentation *struct {
			Kind  string `json:"kind"`
			Value string `json:"value"`
		} `json:"documentation"`
	}
	found := false
	for part := range strings.SplitSeq(output2.String(), "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != 2 {
			continue
		}
		if err := json.Unmarshal(response.Result, &resolved); err != nil {
			t.Fatalf("decode resolve result: %v (%s)", err, response.Result)
		}
		found = true
	}
	if !found {
		t.Fatalf("no completionItem/resolve response (id 2) in:\n%s", output2.String())
	}
	if resolved.Documentation == nil || resolved.Documentation.Value == "" {
		t.Fatal("completionItem/resolve for stdlib `strings.HasPrefix` returned empty Documentation")
	}
}

// TestCompletionResolveFilterE2E drives completionItem/resolve for a pipe
// filter candidate (T10's Filters class): `upper` resolves to std's
// `Upper`, a documented func in a real .go file (github.com/gsxhq/gsx/std) —
// the SAME lazy Data{file,line}+resolve round trip as an imported symbol,
// this time sourced from codegen's filter harvest (FilterCandidate.Pos)
// rather than an object's own go/types position. Confirms the filter-table
// plumbing (filterEntry.pos -> FilterCandidate.Pos -> lsp.FilterCandidate.Pos
// -> filterItems' Data) resolves to a REAL, readable position end to end.
func TestCompletionResolveFilterE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	root := t.TempDir()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) string {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	write("page/types.go", "package page\n\ntype User struct{ Name string }\n")
	source := "package page\n\ncomponent Home(user User) {\n\t<div>{ user.Name |> up }</div>\n}\n"
	pagePath := write("page/page.gsx", source)
	uri := "file://" + pagePath

	cursor := strings.Index(source, "|> up") + len("|> up")

	frame := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return "Content-Length: " + strconv.Itoa(len(data)) + "\r\n\r\n" + string(data)
	}
	var input strings.Builder
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": source}},
	}))
	pos := lspUTF16PositionAt(source, cursor)
	input.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/completion",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     map[string]any{"line": pos.Line, "character": pos.Character},
		},
	}))
	input.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output1, stderr bytes.Buffer
	if code := runLSP(strings.NewReader(input.String()), &output1, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}

	rawItem := rawCompletionItemByLabel(t, output1.String(), 2, "upper")
	dataField, hasData := rawItem["data"]
	if !hasData || len(dataField) == 0 || string(dataField) == "null" {
		t.Fatalf("filter `upper` item carries no \"data\" to resolve: %+v", rawItem)
	}

	itemJSON, err := json.Marshal(rawItem)
	if err != nil {
		t.Fatal(err)
	}
	var resolveInput strings.Builder
	resolveInput.WriteString(frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}}))
	resolveInput.WriteString(frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "completionItem/resolve",
		"params": json.RawMessage(itemJSON),
	}))
	resolveInput.WriteString(frame(map[string]any{"jsonrpc": "2.0", "method": "exit"}))

	var output2 bytes.Buffer
	if code := runLSP(strings.NewReader(resolveInput.String()), &output2, &stderr, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, stderr.String())
	}

	var resolved struct {
		Documentation *struct {
			Value string `json:"value"`
		} `json:"documentation"`
	}
	found := false
	for part := range strings.SplitSeq(output2.String(), "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != 2 {
			continue
		}
		if err := json.Unmarshal(response.Result, &resolved); err != nil {
			t.Fatalf("decode resolve result: %v (%s)", err, response.Result)
		}
		found = true
	}
	if !found {
		t.Fatalf("no completionItem/resolve response (id 2) in:\n%s", output2.String())
	}
	if resolved.Documentation == nil || resolved.Documentation.Value == "" {
		t.Fatal("completionItem/resolve for filter `upper` (std.Upper) returned empty Documentation")
	}
	if !strings.Contains(resolved.Documentation.Value, "Upper returns s with all Unicode letters mapped to their upper case") {
		t.Errorf("resolved `upper` Documentation = %q, want std.Upper's doc comment", resolved.Documentation.Value)
	}
}

// rawCompletionItemByLabel returns the raw JSON object (as a
// map[string]json.RawMessage, preserving exactly what the server sent — no
// typed re-encoding) of the completion item with the given label from the
// completion response with id, or fails the test if not found.
func rawCompletionItemByLabel(t *testing.T, output string, id int, label string) map[string]json.RawMessage {
	t.Helper()
	for part := range strings.SplitSeq(output, "Content-Length:") {
		_, body, ok := strings.Cut(part, "\r\n\r\n")
		if !ok {
			continue
		}
		var response struct {
			ID     int `json:"id"`
			Result struct {
				Items []map[string]json.RawMessage `json:"items"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id {
			continue
		}
		for _, item := range response.Result.Items {
			var l string
			if err := json.Unmarshal(item["label"], &l); err == nil && l == label {
				return item
			}
		}
	}
	t.Fatalf("no completion item labeled %q in response id %d:\n%s", label, id, output)
	return nil
}
