package lsp

import (
	"fmt"
	"strings"
	"testing"
)

// applyTextEdits applies edits (assumed non-overlapping) to text, rightmost
// first so earlier offsets stay valid. Used to prove an auto-import item's
// TextEdit + AdditionalTextEdits compose into a well-formed document.
func applyTextEdits(text string, edits []TextEdit, enc encoding) string {
	type off struct {
		start, end int
		newText    string
	}
	os := make([]off, len(edits))
	for i, e := range edits {
		os[i] = off{
			start:   byteOffsetForPosition(text, e.Range.Start.Line, e.Range.Start.Character, enc),
			end:     byteOffsetForPosition(text, e.Range.End.Line, e.Range.End.Character, enc),
			newText: e.NewText,
		}
	}
	// Insertion sort by start descending.
	for i := 1; i < len(os); i++ {
		for j := i; j > 0 && os[j].start > os[j-1].start; j-- {
			os[j], os[j-1] = os[j-1], os[j]
		}
	}
	out := text
	for _, o := range os {
		out = out[:o.start] + o.newText + out[o.end:]
	}
	return out
}

func TestImportEditForCases(t *testing.T) {
	tests := []struct {
		name, text, path, want string
	}{
		{
			name: "existing import block",
			text: "package page\n\nimport \"strings\"\n\ncomponent Home() {\n\t<div>{ x }</div>\n}\n",
			path: "fmt",
			want: "package page\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\ncomponent Home() {\n\t<div>{ x }</div>\n}\n",
		},
		{
			name: "no import block (insert after package clause)",
			text: "package page\n\ncomponent Home() {\n\t<div>{ x }</div>\n}\n",
			path: "strings",
			want: "package page\n\nimport \"strings\"\n\ncomponent Home() {\n\t<div>{ x }</div>\n}\n",
		},
		{
			name: "leading non-import chunk",
			text: "package page\n\nvar y = 1\n\ncomponent Home() {\n\t<div>{ x }</div>\n}\n",
			path: "fmt",
			want: "package page\n\nimport \"fmt\"\n\nvar y = 1\n\ncomponent Home() {\n\t<div>{ x }</div>\n}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// cursorStart deliberately large: the import region is always above it.
			ed, ok := importEditFor(tt.text, tt.path, len(tt.text), encUTF8)
			if !ok {
				t.Fatal("importEditFor = !ok, want an edit")
			}
			got := applyTextEdits(tt.text, []TextEdit{ed}, encUTF8)
			if got != tt.want {
				t.Errorf("applied edit =\n%q\nwant\n%q", got, tt.want)
			}
			// The edit must lie strictly above the cursor.
			endOff := byteOffsetForPosition(tt.text, ed.Range.End.Line, ed.Range.End.Character, encUTF8)
			if endOff > len(tt.text) {
				t.Errorf("edit end %d past buffer", endOff)
			}
		})
	}
}

func TestImportEditForAlreadyImported(t *testing.T) {
	text := "package page\n\nimport \"fmt\"\n\ncomponent Home() {\n\t<div>{ fmt.Sprint(1) }</div>\n}\n"
	if _, ok := importEditFor(text, "fmt", len(text), encUTF8); ok {
		t.Error("importEditFor for an already-imported path = ok, want no edit")
	}
}

func TestImportEditForRefusesOverlap(t *testing.T) {
	// A cursor at the very top (offset 0) forces the computed import edit end to
	// exceed cursorStart; importEditFor must refuse rather than emit an
	// overlapping edit.
	text := "package page\n\ncomponent Home() {\n\t<div>{ x }</div>\n}\n"
	if _, ok := importEditFor(text, "strings", 0, encUTF8); ok {
		t.Error("importEditFor with cursorStart=0 = ok, want refusal (would overlap)")
	}
}

func TestQualifierBeforeDot(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		start   int
		wantOK  bool
		wantQ   string
		wantEnd int
	}{
		{"trailing dot", "{ fmt. }", 6, true, "fmt", 5},
		{"typed prefix", "{ fmt.Spr }", 6, true, "fmt", 5},
		{"space before dot", "{ fmt . }", 7, true, "fmt", 5},
		{"chained receiver", "{ a.b. }", 6, false, "", 0},
		{"complex receiver", "{ foo(). }", 8, false, "", 0},
		{"not a member", "{ fm }", 4, false, "", 0},
		{"numeric receiver", "{ 3.14 }", 4, false, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Locate the dot's following position as the completion token start.
			q, end, ok := qualifierBeforeDot(tt.text, tt.start)
			if ok != tt.wantOK || q != tt.wantQ {
				t.Fatalf("qualifierBeforeDot(%q, %d) = (%q, %d, %v), want (%q, _, %v)",
					tt.text, tt.start, q, end, ok, tt.wantQ, tt.wantOK)
			}
			if ok && end != tt.wantEnd {
				t.Errorf("nameEnd = %d, want %d", end, tt.wantEnd)
			}
		})
	}
}

func TestSymbolKindCiKind(t *testing.T) {
	for k, want := range map[SymbolKind]int{
		SymbolFunc:          ciKindFunction,
		SymbolVar:           ciKindVariable,
		SymbolConst:         ciKindConstant,
		SymbolTypeStruct:    ciKindStruct,
		SymbolTypeInterface: ciKindInterface,
		SymbolTypeOther:     ciKindClass,
	} {
		if got := k.ciKind(); got != want {
			t.Errorf("SymbolKind(%d).ciKind() = %d, want %d", k, got, want)
		}
	}
}

// autoImportAnalyzer is a fake Analyzer supplying auto-import data (paths,
// symbols, package names) without a real module analysis. Other methods embed
// nilAnalyzer.
type autoImportAnalyzer struct {
	nilAnalyzer
	resolve  map[string][]string       // qualifier -> paths
	symbols  map[string][]ImportSymbol // path -> exported symbols
	packages []ImportablePackage
}

func (a autoImportAnalyzer) ResolveImport(_, name, _ string) []string { return a.resolve[name] }
func (a autoImportAnalyzer) ExportedSymbols(_, path string) []ImportSymbol {
	return a.symbols[path]
}
func (a autoImportAnalyzer) ImportablePackages(string) []ImportablePackage { return a.packages }

func itemsByLabel(items []CompletionItem) map[string]CompletionItem {
	m := map[string]CompletionItem{}
	for _, it := range items {
		m[it.Label] = it
	}
	return m
}

func TestUnimportedQualifierItems(t *testing.T) {
	text := "package page\n\ncomponent Home() {\n\t<div>{ fmt. }</div>\n}\n"
	// completion token span for the empty prefix after "fmt." : start==end at the
	// byte right after the dot.
	start := strings.Index(text, "fmt.") + len("fmt.")
	end := start

	a := autoImportAnalyzer{
		resolve: map[string][]string{"fmt": {"fmt"}},
		symbols: map[string][]ImportSymbol{
			"fmt": {
				{Name: "Sprintf", Kind: SymbolFunc, Detail: "func fmt.Sprintf(format string, a ...any) string"},
				{Name: "Stringer", Kind: SymbolTypeInterface, Detail: "type fmt.Stringer interface{ String() string }"},
			},
		},
	}

	t.Run("no labelDetails support", func(t *testing.T) {
		s := &Server{analyzer: a, enc: encUTF8}
		items := s.unimportedQualifierItems("dir", "page.gsx", text, "fmt", start, end)
		by := itemsByLabel(items)
		sp, ok := by["Sprintf"]
		if !ok {
			t.Fatalf("missing Sprintf; labels=%v", by)
		}
		if sp.Kind != ciKindFunction {
			t.Errorf("Sprintf kind = %d, want function", sp.Kind)
		}
		if len(sp.AdditionalTextEdits) != 1 {
			t.Fatalf("Sprintf AdditionalTextEdits = %d, want 1", len(sp.AdditionalTextEdits))
		}
		// Applying both edits inserts the import and the symbol, and reparses clean.
		all := append([]TextEdit{{Range: sp.TextEdit.Range, NewText: sp.TextEdit.NewText}}, sp.AdditionalTextEdits...)
		got := applyTextEdits(text, all, encUTF8)
		if !strings.Contains(got, "import \"fmt\"") || !strings.Contains(got, "fmt.Sprintf") {
			t.Errorf("applied doc missing import or symbol:\n%s", got)
		}
		// Fallback: the import path lives in the detail string, no labelDetails.
		if sp.LabelDetails != nil {
			t.Errorf("labelDetails set without capability: %+v", sp.LabelDetails)
		}
		if sp.Detail != "fmt" {
			t.Errorf("fallback detail = %q, want import path \"fmt\"", sp.Detail)
		}
	})

	t.Run("labelDetails support", func(t *testing.T) {
		s := &Server{analyzer: a, enc: encUTF8, labelDetailsSupport: true}
		items := s.unimportedQualifierItems("dir", "page.gsx", text, "fmt", start, end)
		sp := itemsByLabel(items)["Sprintf"]
		if sp.LabelDetails == nil || sp.LabelDetails.Description != "fmt" {
			t.Errorf("labelDetails = %+v, want description \"fmt\"", sp.LabelDetails)
		}
		if !strings.HasPrefix(sp.Detail, "func fmt.Sprintf(") {
			t.Errorf("detail = %q, want the type signature kept", sp.Detail)
		}
	})
}

func TestUnimportedQualifierAmbiguousPerPath(t *testing.T) {
	text := "package page\n\ncomponent Home() {\n\t<div>{ rand. }</div>\n}\n"
	start := strings.Index(text, "rand.") + len("rand.")
	a := autoImportAnalyzer{
		resolve: map[string][]string{"rand": {"crypto/rand", "math/rand/v2"}},
		symbols: map[string][]ImportSymbol{
			"crypto/rand":  {{Name: "Read", Kind: SymbolFunc, Detail: "func crypto/rand.Read(b []byte) (n int, err error)"}},
			"math/rand/v2": {{Name: "IntN", Kind: SymbolFunc, Detail: "func math/rand/v2.IntN(n int) int"}},
		},
	}
	s := &Server{analyzer: a, enc: encUTF8, labelDetailsSupport: true}
	items := s.unimportedQualifierItems("dir", "page.gsx", text, "rand", start, start)
	if len(items) != 2 {
		t.Fatalf("ambiguous rand = %d items, want 2 (one per path)", len(items))
	}
	// Each item's import edit adds its OWN path, and its labelDetails names it.
	for _, it := range items {
		var wantPath string
		switch it.Label {
		case "Read":
			wantPath = "crypto/rand"
		case "IntN":
			wantPath = "math/rand/v2"
		default:
			t.Fatalf("unexpected item %q", it.Label)
		}
		if it.LabelDetails == nil || it.LabelDetails.Description != wantPath {
			t.Errorf("%s labelDetails = %+v, want %q", it.Label, it.LabelDetails, wantPath)
		}
		got := applyTextEdits(text, append([]TextEdit{*it.TextEdit}, it.AdditionalTextEdits...), encUTF8)
		if !strings.Contains(got, "import \""+wantPath+"\"") {
			t.Errorf("%s applied doc missing import %q:\n%s", it.Label, wantPath, got)
		}
	}
}

func TestPackageNameItemsTierAndPrefix(t *testing.T) {
	text := "package page\n\ncomponent Home() {\n\t<div>{ fm }</div>\n}\n"
	start := strings.Index(text, "{ fm }") + len("{ ")
	end := start + len("fm")
	a := autoImportAnalyzer{
		packages: []ImportablePackage{
			{Name: "fmt", Path: "fmt"},
			{Name: "flag", Path: "flag"},
			{Name: "strings", Path: "strings"},
		},
	}
	s := &Server{analyzer: a, enc: encUTF8}
	items := s.packageNameItems("dir", text, "fm", start, end)
	by := itemsByLabel(items)
	if _, ok := by["fmt"]; !ok {
		t.Fatalf("prefix `fm` did not offer fmt; labels=%v", by)
	}
	if _, ok := by["strings"]; ok {
		t.Errorf("prefix `fm` wrongly offered strings")
	}
	fmtItem := by["fmt"]
	if fmtItem.Kind != ciKindModule {
		t.Errorf("fmt kind = %d, want Module", fmtItem.Kind)
	}
	if !strings.HasPrefix(fmtItem.SortText, "70") {
		t.Errorf("fmt SortText = %q, want tierUnimported prefix \"70\"", fmtItem.SortText)
	}
	if len(fmtItem.AdditionalTextEdits) != 1 {
		t.Fatalf("fmt AdditionalTextEdits = %d, want 1", len(fmtItem.AdditionalTextEdits))
	}
	got := applyTextEdits(text, append([]TextEdit{*fmtItem.TextEdit}, fmtItem.AdditionalTextEdits...), encUTF8)
	if !strings.Contains(got, "import \"fmt\"") || !strings.Contains(got, "{ fmt }") {
		t.Errorf("applied package-name item doc wrong:\n%s", got)
	}
}

func TestPackageNameItemsEmptyPrefixSuppressed(t *testing.T) {
	text := "package page\n\ncomponent Home() {\n\t<div>{  }</div>\n}\n"
	a := autoImportAnalyzer{packages: []ImportablePackage{{Name: "fmt", Path: "fmt"}}}
	s := &Server{analyzer: a, enc: encUTF8}
	if items := s.packageNameItems("dir", text, "", 40, 40); len(items) != 0 {
		t.Errorf("empty prefix offered %d package names, want 0 (noise cap)", len(items))
	}
}

// TestMergePackageNameItemsSuppressesShadow pins completion.go's shadow-
// suppression path (goContextCompletion, ~131-142): a package whose declared
// name collides with an identifier already in scope must NOT be offered as an
// import candidate, since accepting it would produce the exact identifier the
// file already has, silently shadowing the existing binding rather than
// introducing a new one. os/user is the paradigm case — its package name is
// `user`, a common local/parameter identifier.
func TestMergePackageNameItemsSuppressesShadow(t *testing.T) {
	text := "package page\n\ncomponent Home(user User) {\n\t<div>{ us }</div>\n}\n"
	start := strings.Index(text, "{ us }") + len("{ ")
	end := start + len("us")

	a := autoImportAnalyzer{
		packages: []ImportablePackage{
			{Name: "user", Path: "os/user"},    // shadows the in-scope `user` param
			{Name: "usage", Path: "pkg/usage"}, // distinct name: not a shadow
		},
	}
	s := &Server{analyzer: a, enc: encUTF8}

	// Sanity: os/user surfaces as a raw candidate from packageNameItems alone —
	// the suppression is the CALLER's job (mergePackageNameItems), not
	// packageNameItems'.
	pkgItems := s.packageNameItems("dir", text, "us", start, end)
	pkgByLabel := itemsByLabel(pkgItems)
	if _, ok := pkgByLabel["user"]; !ok {
		t.Fatalf("packageNameItems did not offer raw os/user candidate; got %v", pkgByLabel)
	}
	if _, ok := pkgByLabel["usage"]; !ok {
		t.Fatalf("packageNameItems did not offer pkg/usage candidate; got %v", pkgByLabel)
	}

	// The in-scope scope-chain already offered `user` as a plain identifier (no
	// import edit) — this simulates goCompletionItems' output for the component
	// parameter before the Option 2 merge runs.
	scopeItems := []CompletionItem{{Label: "user", Kind: ciKindVariable}}

	got := mergePackageNameItems(scopeItems, pkgItems)

	var userCount int
	var sawUsage bool
	for _, it := range got {
		if it.Label == "user" {
			userCount++
			if len(it.AdditionalTextEdits) != 0 {
				t.Errorf("merged `user` item carries an import edit %v, want the scope item preserved untouched (os/user suppressed)", it.AdditionalTextEdits)
			}
		}
		if it.Label == "usage" {
			sawUsage = true
		}
	}
	if userCount != 1 {
		t.Errorf("merged items contain %d `user` labels, want 1 (os/user suppressed as a shadow)", userCount)
	}
	if !sawUsage {
		t.Errorf("merged items missing `usage` (a non-colliding package must still be offered); got %v", got)
	}
}

// BenchmarkPackageNameItems measures packageNameItems' cost with a large
// candidate set (100 unimported packages all matching the typed prefix), the
// hot path importEditFor's per-candidate re-parse made expensive: before the
// prepareImportEdit hoist, each candidate re-parsed the whole buffer
// (gsxparser.ParseFile) independently of the import path; after, the buffer is
// parsed once per completion request and each candidate only runs
// AddChunkImports + the prefix/suffix diff.
func BenchmarkPackageNameItems(b *testing.B) {
	text := "package page\n\nimport \"strings\"\n\ncomponent Home() {\n\t<div>{ fm }</div>\n}\n"
	start := strings.Index(text, "{ fm }") + len("{ ")
	end := start + len("fm")

	pkgs := make([]ImportablePackage, 100)
	for i := range pkgs {
		pkgs[i] = ImportablePackage{Name: fmt.Sprintf("fmt%d", i), Path: fmt.Sprintf("pkg/fmt%d", i)}
	}
	a := autoImportAnalyzer{packages: pkgs}
	s := &Server{analyzer: a, enc: encUTF8}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items := s.packageNameItems("dir", text, "fmt", start, end)
		if len(items) != 100 {
			b.Fatalf("got %d items, want 100", len(items))
		}
	}
}
