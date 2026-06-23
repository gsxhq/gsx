package diag

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// SourceProvider yields a file's bytes for snippet rendering (CLI reads disk;
// the LSP supplies the in-memory buffer). A nil provider disables snippets.
type SourceProvider func(filename string) ([]byte, bool)

// header formats "severity[code]: message" (code omitted when empty).
func header(d Diagnostic) string {
	if d.Code != "" {
		return fmt.Sprintf("%s[%s]: %s", d.Severity, d.Code, d.Message)
	}
	return fmt.Sprintf("%s: %s", d.Severity, d.Message)
}

// RenderCompact writes one deterministic line per diagnostic:
// file:line:col: severity[code]: message
func RenderCompact(w io.Writer, diags []Diagnostic) {
	for _, d := range diags {
		fmt.Fprintf(w, "%s:%d:%d: %s\n", d.Start.Filename, d.Start.Line, d.Start.Column, header(d))
	}
}

type jsonPos struct {
	Line int `json:"line"`
	Col  int `json:"col"`
}
type jsonRange struct {
	Start jsonPos `json:"start"`
	End   jsonPos `json:"end"`
}
type jsonDiag struct {
	File     string    `json:"file"`
	Range    jsonRange `json:"range"`
	Severity string    `json:"severity"`
	Code     string    `json:"code,omitempty"`
	Message  string    `json:"message"`
	Help     string    `json:"help,omitempty"`
	Source   string    `json:"source,omitempty"`
}

// RenderJSON writes the diagnostics as a JSON array (1-based positions).
func RenderJSON(w io.Writer, diags []Diagnostic) error {
	out := make([]jsonDiag, 0, len(diags))
	for _, d := range diags {
		out = append(out, jsonDiag{
			File:     d.Start.Filename,
			Range:    jsonRange{jsonPos{d.Start.Line, d.Start.Column}, jsonPos{d.End.Line, d.End.Column}},
			Severity: d.Severity.String(),
			Code:     d.Code,
			Message:  d.Message,
			Help:     d.Help,
			Source:   d.Source,
		})
	}
	enc := json.NewEncoder(w)
	return enc.Encode(out)
}

// RenderRich writes rustc/Go-style diagnostics with a source snippet + caret.
// It degrades to the compact line when src yields no source for the file.
func RenderRich(w io.Writer, diags []Diagnostic, src SourceProvider) {
	for _, d := range diags {
		line, ok := sourceLine(src, d.Start.Filename, d.Start.Line)
		if !ok {
			fmt.Fprintf(w, "%s:%d:%d: %s\n", d.Start.Filename, d.Start.Line, d.Start.Column, header(d))
			continue
		}
		fmt.Fprintf(w, "%s\n", header(d))
		fmt.Fprintf(w, "  --> %s:%d:%d\n", d.Start.Filename, d.Start.Line, d.Start.Column)
		gutter := fmt.Sprintf("%d", d.Start.Line)
		pad := strings.Repeat(" ", len(gutter))
		fmt.Fprintf(w, " %s |\n", pad)
		fmt.Fprintf(w, " %s | %s\n", gutter, line)
		fmt.Fprintf(w, " %s | %s%s\n", pad, strings.Repeat(" ", caretIndent(d.Start.Column)), carets(d))
		if d.Help != "" {
			fmt.Fprintf(w, " %s = help: %s\n", pad, d.Help)
		}
		fmt.Fprintln(w)
	}
}

// caretIndent converts a 1-based byte column to the leading-space count.
func caretIndent(col int) int {
	if col <= 1 {
		return 0
	}
	return col - 1
}

// carets returns the underline: one '^' per byte column in the range on the
// start line (at least one), capped so a multi-line range underlines to EOL of
// the start line via the caller's source length is left simple here: single-line.
func carets(d Diagnostic) string {
	n := 1
	if d.End.Line == d.Start.Line && d.End.Column > d.Start.Column {
		n = d.End.Column - d.Start.Column
	}
	return strings.Repeat("^", n)
}

// sourceLine returns the 1-based line lineNo of filename's source, without the
// trailing newline.
func sourceLine(src SourceProvider, filename string, lineNo int) (string, bool) {
	if src == nil || lineNo < 1 {
		return "", false
	}
	b, ok := src(filename)
	if !ok {
		return "", false
	}
	lines := strings.Split(string(b), "\n")
	if lineNo > len(lines) {
		return "", false
	}
	return strings.TrimRight(lines[lineNo-1], "\r"), true
}
