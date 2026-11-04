package lsp

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

// diagAnalyzer reports a fixed diagnostic list for every package.
type diagAnalyzer struct {
	nilAnalyzer
	diags []diag.Diagnostic
}

func (a diagAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	return &Package{Diags: a.diags}, nil
}

// spacingSrc's line 4 ends "Report template:" and line 5 starts the <a>: the
// dropped run is the newline plus the tab, 4:21 to 5:2 (1-based, byte columns).
const spacingSrc = "package x\n\ncomponent C() {\n\t<p>Report template:\n\t<a href=\"/t\">x</a></p>\n}\n"

func spacingDiag(file string, startLine, startCol, endLine, endCol int) diag.Diagnostic {
	return diag.Diagnostic{
		Start:    token.Position{Filename: file, Line: startLine, Column: startCol},
		End:      token.Position{Filename: file, Line: endLine, Column: endCol},
		Severity: diag.Warning,
		Code:     "child-element-spacing",
		Message:  "line break after this text is dropped: no space renders before <a>",
		Source:   "codegen",
	}
}

func TestQuickfixChildElementSpacing(t *testing.T) {
	const uri, path = "file:///tmp/c.gsx", "/tmp/c.gsx"
	onLine := func(line, char int) Range {
		return Range{Start: Position{Line: line, Character: char}, End: Position{Line: line, Character: char}}
	}
	tests := []struct {
		name  string
		diags []diag.Diagnostic
		rng   Range
		want  []CodeAction
	}{
		{
			name:  "insert space or join lines",
			diags: []diag.Diagnostic{spacingDiag(path, 4, 21, 5, 2)},
			rng:   onLine(4, 0),
			want: []CodeAction{
				{Title: `Insert {" "}`, Edit: &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {{Range: Range{Start: Position{3, 20}, End: Position{3, 20}}, NewText: `{" "}`}}}}},
				{Title: "Join lines", Edit: &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {{Range: Range{Start: Position{3, 20}, End: Position{4, 1}}, NewText: " "}}}}},
			},
		},
		{
			name:  "range outside the diagnostic",
			diags: []diag.Diagnostic{spacingDiag(path, 4, 21, 5, 2)},
			rng:   onLine(1, 0),
		},
		{
			name:  "stale range no longer whitespace",
			diags: []diag.Diagnostic{spacingDiag(path, 4, 10, 4, 14)},
			rng:   onLine(3, 11),
		},
		{
			name:  "other file",
			diags: []diag.Diagnostic{spacingDiag("/tmp/other.gsx", 4, 21, 5, 2)},
			rng:   onLine(4, 0),
		},
		{
			name: "other code",
			diags: []diag.Diagnostic{func() diag.Diagnostic {
				d := spacingDiag(path, 4, 21, 5, 2)
				d.Code = "duplicate-attribute"
				return d
			}()},
			rng: onLine(4, 0),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := codeActionsInRange(t, uri, spacingSrc, []string{quickFixKind}, diagAnalyzer{diags: tc.diags}, tc.rng)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d actions %+v, want %d", len(got), got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Title != w.Title || g.Kind != quickFixKind || len(g.Diagnostics) != 1 || g.Diagnostics[0].Code != "child-element-spacing" {
					t.Fatalf("action %d = %+v, want title %q kind quickfix with its diagnostic", i, g, w.Title)
				}
				ge, we := g.Edit.Changes[uri], w.Edit.Changes[uri]
				if len(ge) != 1 || ge[0] != we[0] {
					t.Fatalf("action %q edit = %+v, want %+v", w.Title, ge, we)
				}
			}
		})
	}
}
