package lsp

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func mkPos(line, col int) token.Position {
	return token.Position{Filename: "f.gsx", Line: line, Column: col}
}

func TestConvertDiagASCII(t *testing.T) {
	src := "line one\n  bad here\n"
	d := diag.Diagnostic{Start: mkPos(2, 3), End: mkPos(2, 6), Severity: diag.Error, Code: "x", Source: "types", Message: "boom"}
	got := convertDiag(d, lineAtFunc(src), encUTF16)
	want := Range{Start: Position{Line: 1, Character: 2}, End: Position{Line: 1, Character: 5}}
	if got.Range != want {
		t.Fatalf("range = %+v, want %+v", got.Range, want)
	}
	if got.Severity != 1 {
		t.Fatalf("severity = %d, want 1 (Error)", got.Severity)
	}
}

func TestConvertDiagUTF16MultiByte(t *testing.T) {
	// "é" is 2 bytes in UTF-8, 1 UTF-16 code unit. Cursor after "héllo " (byte col 8, 1-based).
	src := "héllo world\n"
	d := diag.Diagnostic{Start: mkPos(1, 8), End: mkPos(1, 8), Severity: diag.Warning}
	got := convertDiag(d, lineAtFunc(src), encUTF16)
	// bytes before col 8: "héllo " = h(1) é(2) l(1) l(1) o(1) space(1) = 7 bytes -> 6 UTF-16 units.
	if got.Range.Start.Character != 6 {
		t.Fatalf("utf16 char = %d, want 6", got.Range.Start.Character)
	}
	if got.Severity != 2 {
		t.Fatalf("severity = %d, want 2 (Warning)", got.Severity)
	}
}

func TestConvertDiagUTF8MultiByte(t *testing.T) {
	src := "héllo world\n"
	d := diag.Diagnostic{Start: mkPos(1, 8), End: mkPos(1, 8)}
	got := convertDiag(d, lineAtFunc(src), encUTF8)
	if got.Range.Start.Character != 7 { // byte offset
		t.Fatalf("utf8 char = %d, want 7", got.Range.Start.Character)
	}
}

func TestConvertDiagEmojiUTF16(t *testing.T) {
	// "😀" is 4 bytes UTF-8, 2 UTF-16 code units (surrogate pair). col after it = byte 5.
	src := "😀x\n"
	d := diag.Diagnostic{Start: mkPos(1, 5), End: mkPos(1, 5)}
	got := convertDiag(d, lineAtFunc(src), encUTF16)
	if got.Range.Start.Character != 2 {
		t.Fatalf("emoji utf16 char = %d, want 2", got.Range.Start.Character)
	}
}

func TestConvertDiagPositionless(t *testing.T) {
	d := diag.Diagnostic{Start: token.Position{}, End: token.Position{}, Severity: diag.Error, Message: "no pos"}
	got := convertDiag(d, lineAtFunc(""), encUTF16)
	zero := Range{Start: Position{0, 0}, End: Position{0, 0}}
	if got.Range != zero {
		t.Fatalf("positionless range = %+v, want zero", got.Range)
	}
}
