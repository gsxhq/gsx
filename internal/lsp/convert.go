package lsp

import (
	"go/token"
	"strings"

	"github.com/gsxhq/gsx/internal/diag"
)

type encoding int

const (
	encUTF16 encoding = iota
	encUTF8
)

// lineAtFunc returns a closure giving the text of the 1-based line number in
// src (without the trailing newline). Out-of-range lines yield "".
func lineAtFunc(src string) func(line1 int) string {
	lines := strings.Split(src, "\n")
	return func(line1 int) string {
		if line1 < 1 || line1 > len(lines) {
			return ""
		}
		return lines[line1-1]
	}
}

// convertDiag converts a gsx diagnostic (1-based, byte columns) to an LSP
// Diagnostic (0-based, characters in the negotiated encoding). A positionless
// diagnostic (Line == 0) maps to the zero range at the file start.
func convertDiag(d diag.Diagnostic, lineAt func(line1 int) string, enc encoding) Diagnostic {
	return Diagnostic{
		Range:    Range{Start: convertPos(d.Start, lineAt, enc), End: convertPos(d.End, lineAt, enc)},
		Severity: lspSeverity(d.Severity),
		Code:     d.Code,
		Source:   d.Source,
		Message:  d.Message,
	}
}

func convertPos(p token.Position, lineAt func(line1 int) string, enc encoding) Position {
	if p.Line == 0 {
		return Position{Line: 0, Character: 0}
	}
	return Position{Line: p.Line - 1, Character: charForByteCol(lineAt(p.Line), p.Column, enc)}
}

// charForByteCol converts a 1-based byte column within lineText to a 0-based LSP
// character offset in enc. A column past the line end clamps to the line length.
func charForByteCol(lineText string, col int, enc encoding) int {
	byteOff := min(max(col-1, 0), len(lineText))
	prefix := lineText[:byteOff]
	if enc == encUTF8 {
		return len(prefix)
	}
	return utf16Len(prefix)
}

// utf16Len counts UTF-16 code units in s (chars above U+FFFF take two).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// byteOffsetForPosition maps a 0-based LSP position (character counted in enc)
// to a byte offset in text. It is the inverse of charForByteCol. A line or
// character past the end clamps to the end of the line / text.
func byteOffsetForPosition(text string, line, character int, enc encoding) int {
	off := 0
	for range line {
		nl := strings.IndexByte(text[off:], '\n')
		if nl < 0 {
			return len(text)
		}
		off += nl + 1
	}
	lineText := text[off:]
	if nl := strings.IndexByte(lineText, '\n'); nl >= 0 {
		lineText = lineText[:nl]
	}
	return off + byteForChar(lineText, character, enc)
}

// byteForChar returns the byte offset within lineText for the 0-based character
// index in enc.
func byteForChar(lineText string, character int, enc encoding) int {
	if enc == encUTF8 {
		if character > len(lineText) {
			return len(lineText)
		}
		return character
	}
	units := 0
	for i, r := range lineText {
		if units >= character {
			return i
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return len(lineText)
}

// lspSeverity maps a gsx severity to an LSP DiagnosticSeverity (1=Error,
// 2=Warning, 3=Information, 4=Hint).
func lspSeverity(s diag.Severity) int {
	switch s {
	case diag.Error:
		return 1
	case diag.Warning:
		return 2
	case diag.Info:
		return 3
	case diag.Hint:
		return 4
	default:
		return 1
	}
}
