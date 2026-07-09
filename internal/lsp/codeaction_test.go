package lsp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// codeActions drives one textDocument/codeAction request against a server backed
// by nilAnalyzer, and returns the decoded result.
func codeActions(t *testing.T, uri, src string, only []string) []CodeAction {
	t.Helper()
	return codeActionsWith(t, uri, src, only, nilAnalyzer{})
}

// codeActionsWith is codeActions with an explicit Analyzer, so a test can vary
// the reported [formatter] imports mode.
func codeActionsWith(t *testing.T, uri, src string, only []string, a Analyzer) []CodeAction {
	t.Helper()
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": "gsx", "version": 1, "text": src}},
	})
	ctx := map[string]any{"diagnostics": []any{}}
	if only != nil {
		ctx["only"] = only
	}
	in += framed(t, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "textDocument/codeAction",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"range":        map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 0}},
			"context":      ctx,
		},
	})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, a)
	if err := srv.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range readFrames(t, out.String()) {
		raw, ok := m["result"]
		if !ok {
			continue
		}
		var actions []CodeAction
		if err := json.Unmarshal(raw, &actions); err == nil && len(actions) > 0 {
			return actions
		}
		// An empty result array decodes to len 0; distinguish it from the
		// initialize result (an object, which fails to unmarshal into a slice).
		if string(raw) == "[]" {
			return nil
		}
	}
	return nil
}

const dupImportSrc = "package x\n\nimport \"strings\"\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
	"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"

// TestCodeActionOrganizeImports: the action is offered and its edit merges and
// dedups the imports.
func TestCodeActionOrganizeImports(t *testing.T) {
	actions := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source.organizeImports"})
	if len(actions) != 1 {
		t.Fatalf("want 1 action, got %d", len(actions))
	}
	a := actions[0]
	if a.Kind != "source.organizeImports" {
		t.Fatalf("kind = %q", a.Kind)
	}
	if a.Edit == nil || len(a.Edit.Changes["file:///tmp/c.gsx"]) != 1 {
		t.Fatalf("want one whole-document edit, got %+v", a.Edit)
	}
	got := a.Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("duplicate not deduped (%d):\n%s", n, got)
	}
	if n := strings.Count(got, "import"); n != 1 {
		t.Fatalf("declarations not merged (%d):\n%s", n, got)
	}
}

// TestCodeActionOrganizeImportsIgnoresGofmtMode: the action organizes even when
// the configured formatter mode is gofmt — organizing is its entire purpose.
func TestCodeActionOrganizeImportsIgnoresGofmtMode(t *testing.T) {
	// gofmtAnalyzer reports ImportsMode = gofmt; the action must ignore it.
	actions := codeActionsWith(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source.organizeImports"}, gofmtAnalyzer{})
	if len(actions) != 1 {
		t.Fatalf("action must be offered under gofmt mode, got %d", len(actions))
	}
	got := actions[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if n := strings.Count(got, "\"strings\""); n != 1 {
		t.Fatalf("action must organize under gofmt mode (%d):\n%s", n, got)
	}
}

// TestCodeActionNoOpWhenAlreadyOrganized: an already-canonical document yields
// no action, so codeActionsOnSave is a no-op.
func TestCodeActionNoOpWhenAlreadyOrganized(t *testing.T) {
	src := "package x\n\nimport (\n\t\"fmt\"\n\n\t\"strings\"\n)\n\n" +
		"component C() {\n\t<p>{ fmt.Sprint(strings.ToUpper(\"x\")) }</p>\n}\n"
	if got := codeActions(t, "file:///tmp/c.gsx", src, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for canonical doc, got %+v", got)
	}
}

// TestCodeActionSkipsNonGsx: gopls owns .go files.
func TestCodeActionSkipsNonGsx(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.go", dupImportSrc, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for .go, got %+v", got)
	}
}

// TestCodeActionHonorsOnlyFilter: a request restricted to quickfix gets nothing.
func TestCodeActionHonorsOnlyFilter(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"quickfix"}); len(got) != 0 {
		t.Fatalf("want no action when only=[quickfix], got %+v", got)
	}
}

// TestCodeActionOnlySourcePrefixMatches: "source" is a prefix of
// "source.organizeImports" in LSP's kind hierarchy, so it must match.
func TestCodeActionOnlySourcePrefixMatches(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"source"}); len(got) != 1 {
		t.Fatalf("only=[source] must match, got %d", len(got))
	}
}

// TestCodeActionEmptyOnlyOffersAction: an unrestricted request offers it.
func TestCodeActionEmptyOnlyOffersAction(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, nil); len(got) != 1 {
		t.Fatalf("unrestricted request must offer the action, got %d", len(got))
	}
}

// TestCodeActionParseErrorYieldsNothing: a mid-edit buffer never gets a
// destructive whole-file edit.
func TestCodeActionParseErrorYieldsNothing(t *testing.T) {
	bad := "package x\n\ncomponent C() {\n\t<p>unclosed\n"
	if got := codeActions(t, "file:///tmp/c.gsx", bad, []string{"source.organizeImports"}); len(got) != 0 {
		t.Fatalf("want no action for unparseable buffer, got %+v", got)
	}
}

// TestInitializeAdvertisesOrganizeImports: the capability names the kind so the
// client can wire editor.codeActionsOnSave.
func TestInitializeAdvertisesOrganizeImports(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	srv := NewServer(strings.NewReader(in), &out, nilAnalyzer{})
	if err := srv.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"source.organizeImports"`) {
		t.Fatalf("initialize did not advertise source.organizeImports:\n%s", out.String())
	}
}

// gofmtAnalyzer reports gofmt mode, to prove the code action ignores it and
// organizes anyway. It embeds nilAnalyzer for every other Analyzer method.
type gofmtAnalyzer struct{ nilAnalyzer }

func (gofmtAnalyzer) ImportsMode(string) gsxfmt.ImportsMode { return gsxfmt.ImportsGofmt }

// addAnalyzer reports one missing qualifier and resolves names from a fixed map.
type addAnalyzer struct {
	nilAnalyzer
	missing map[string][]MissingImport
	resolve map[string][]string
}

func (a addAnalyzer) Analyze(dir string, _ map[string][]byte) (*Package, error) {
	return &Package{MissingImports: a.missing}, nil
}
func (a addAnalyzer) ResolveImport(_, name, _ string) []string { return a.resolve[name] }

const missingFmtSrc = "package x\n\nvar hello = \"hi\"\n\ncomponent C() {\n\t<p>{ fmt.Sprint(hello) }</p>\n}\n"

// TestOrganizeImportsAddsUnambiguous.
func TestOrganizeImportsAddsUnambiguous(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "fmt", Symbol: "Sprint"}}},
		resolve: map[string][]string{"fmt": {"fmt"}},
	}
	got := codeActionsWith(t, "file:///tmp/c.gsx", missingFmtSrc, []string{organizeImportsKind}, a)
	if len(got) != 1 {
		t.Fatalf("want 1 action, got %d", len(got))
	}
	txt := got[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if !strings.Contains(txt, "import \"fmt\"") {
		t.Fatalf("organizeImports did not add fmt:\n%s", txt)
	}
}

// TestOrganizeImportsSkipsAmbiguous: two candidates ⇒ never guess on save.
func TestOrganizeImportsSkipsAmbiguous(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "rand", Symbol: "Foo"}}},
		resolve: map[string][]string{"rand": {"crypto/rand", "math/rand"}},
	}
	src := "package x\n\ncomponent C() {\n\t<p>{ rand.Foo() }</p>\n}\n"
	if got := codeActionsWith(t, "file:///tmp/c.gsx", src, []string{organizeImportsKind}, a); len(got) != 0 {
		t.Fatalf("organizeImports must not guess an ambiguous import, got %+v", got)
	}
}

// TestQuickfixOffersOneActionPerCandidate.
func TestQuickfixOffersOneActionPerCandidate(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "rand", Symbol: "Foo"}}},
		resolve: map[string][]string{"rand": {"crypto/rand", "math/rand"}},
	}
	src := "package x\n\ncomponent C() {\n\t<p>{ rand.Foo() }</p>\n}\n"
	got := codeActionsWith(t, "file:///tmp/c.gsx", src, []string{quickFixKind}, a)
	if len(got) != 2 {
		t.Fatalf("want 2 quickfixes, got %d", len(got))
	}
	titles := got[0].Title + "|" + got[1].Title
	for _, want := range []string{"Add import: crypto/rand", "Add import: math/rand"} {
		if !strings.Contains(titles, want) {
			t.Fatalf("missing quickfix %q in %q", want, titles)
		}
	}
	txt := got[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if !strings.Contains(txt, "rand") {
		t.Fatalf("quickfix edit does not add the import:\n%s", txt)
	}
}

// TestQuickfixNoneWhenUnresolvable.
func TestQuickfixNoneWhenUnresolvable(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "zzz", Symbol: "Foo"}}},
		resolve: map[string][]string{},
	}
	src := "package x\n\ncomponent C() {\n\t<p>{ zzz.Foo() }</p>\n}\n"
	if got := codeActionsWith(t, "file:///tmp/c.gsx", src, []string{quickFixKind}, a); len(got) != 0 {
		t.Fatalf("want no quickfix for an unresolvable name, got %+v", got)
	}
}

// TestInitializeAdvertisesQuickfix.
func TestInitializeAdvertisesQuickfix(t *testing.T) {
	in := framed(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += framed(t, map[string]any{"jsonrpc": "2.0", "method": "exit"})
	var out bytes.Buffer
	if err := NewServer(strings.NewReader(in), &out, nilAnalyzer{}).Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"quickfix"`) {
		t.Fatalf("initialize did not advertise quickfix:\n%s", out.String())
	}
}

// TestAddsForOrganizeNeverAliases is a unit test on the HARD CONTRACT: an add
// must never carry a Name (alias). astutil.AddNamedImport binds Name as the
// local identifier; passing one that happens to already be bound to a different
// path emits invalid Go (two imports aliased to the same short name).
func TestAddsForOrganizeNeverAliases(t *testing.T) {
	a := addAnalyzer{resolve: map[string][]string{"fmt": {"fmt"}}}
	srv := &Server{analyzer: a}
	adds := srv.addsForOrganize("/tmp", []MissingImport{{Name: "fmt", Symbol: "Sprint"}})
	if len(adds) != 1 {
		t.Fatalf("want 1 add, got %d", len(adds))
	}
	if adds[0].Name != "" {
		t.Fatalf("addsForOrganize must never set an alias Name, got %+v", adds[0])
	}
	if adds[0].Path != "fmt" {
		t.Fatalf("adds[0].Path = %q, want fmt", adds[0].Path)
	}
}

// TestCodeActionBothKindsForEmptyOnly: an unrestricted request surfaces both the
// organizeImports edit and the quickfix(es) for the same missing qualifier.
func TestCodeActionBothKindsForEmptyOnly(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "fmt", Symbol: "Sprint"}}},
		resolve: map[string][]string{"fmt": {"fmt"}},
	}
	got := codeActionsWith(t, "file:///tmp/c.gsx", missingFmtSrc, nil, a)
	kinds := map[string]bool{}
	for _, ca := range got {
		kinds[ca.Kind] = true
	}
	if !kinds[organizeImportsKind] || !kinds[quickFixKind] {
		t.Fatalf("want both %q and %q present, got %+v", organizeImportsKind, quickFixKind, got)
	}
}

// TestOrganizeImportsIdempotentAfterAdd: applying the organizeImports edit and
// re-requesting against the already-imported text yields no action —
// gsxfmt.FormatOptions.Add is a no-op for an already-present path.
func TestOrganizeImportsIdempotentAfterAdd(t *testing.T) {
	a := addAnalyzer{
		missing: map[string][]MissingImport{"/tmp/c.gsx": {{Name: "fmt", Symbol: "Sprint"}}},
		resolve: map[string][]string{"fmt": {"fmt"}},
	}
	got := codeActionsWith(t, "file:///tmp/c.gsx", missingFmtSrc, []string{organizeImportsKind}, a)
	if len(got) != 1 {
		t.Fatalf("want 1 action, got %d", len(got))
	}
	applied := got[0].Edit.Changes["file:///tmp/c.gsx"][0].NewText
	if got2 := codeActionsWith(t, "file:///tmp/c.gsx", applied, []string{organizeImportsKind}, a); len(got2) != 0 {
		t.Fatalf("re-request on the organized text must be a no-op, got %+v", got2)
	}
}

// TestCodeActionQuickfixSkipsNonGsx: gopls owns .go files for quickfix too.
func TestCodeActionQuickfixSkipsNonGsx(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.go", dupImportSrc, []string{"quickfix"}); len(got) != 0 {
		t.Fatalf("want no quickfix for .go, got %+v", got)
	}
}

// TestCodeActionQuickfixParseErrorYieldsNothing: a mid-edit buffer never gets a
// quickfix edit either.
func TestCodeActionQuickfixParseErrorYieldsNothing(t *testing.T) {
	bad := "package x\n\ncomponent C() {\n\t<p>unclosed\n"
	if got := codeActions(t, "file:///tmp/c.gsx", bad, []string{"quickfix"}); len(got) != 0 {
		t.Fatalf("want no quickfix for unparseable buffer, got %+v", got)
	}
}

// TestCodeActionQuickfixNoMissingImports: a file with no missing imports offers
// no quickfix.
func TestCodeActionQuickfixNoMissingImports(t *testing.T) {
	if got := codeActions(t, "file:///tmp/c.gsx", dupImportSrc, []string{"quickfix"}); len(got) != 0 {
		t.Fatalf("want no quickfix when there are no missing imports, got %+v", got)
	}
}

// TestMissingImportsReachTheLSP: the adapter must carry MissingImports through,
// and the Analyzer must expose ResolveImport.
func TestMissingImportsReachTheLSP(t *testing.T) {
	var a Analyzer = nilAnalyzer{}
	if got := a.ResolveImport("/tmp", "fmt", "Sprintf"); got != nil {
		t.Fatalf("nilAnalyzer.ResolveImport = %v, want nil", got)
	}
	p := &Package{MissingImports: map[string][]MissingImport{
		"/tmp/a.gsx": {{Name: "fmt", Symbol: "Sprintf"}},
	}}
	if len(p.MissingImports["/tmp/a.gsx"]) != 1 {
		t.Fatal("Package.MissingImports not carried")
	}
}
