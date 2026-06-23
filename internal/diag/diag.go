// Package diag is gsx's structured-diagnostic foundation: a fileset-agnostic
// Diagnostic model (resolved token.Position ranges, severity, code, help,
// source), a Bag collector for error recovery, and renderers (see render.go).
package diag

import (
	"fmt"
	"go/token"
	"sort"
)

type Severity int

const (
	Error Severity = iota
	Warning
	Info
	Hint
)

func (s Severity) String() string {
	switch s {
	case Error:
		return "error"
	case Warning:
		return "warning"
	case Info:
		return "info"
	case Hint:
		return "hint"
	default:
		return "error"
	}
}

// Diagnostic is one structured problem with already-resolved positions. Start..End
// is a range; End may equal Start for a point. Positions are resolved because
// diagnostics originate from two token.FileSets (gsx parser; go/packages).
type Diagnostic struct {
	Start, End token.Position
	Severity   Severity
	Code       string
	Message    string
	Help       string
	Source     string
}

// Bag accumulates diagnostics for one package's resolve+codegen pass. It holds
// the gsx parser fset only to resolve AST token.Pos in Errorf.
type Bag struct {
	fset  *token.FileSet
	diags []Diagnostic
}

func NewBag(fset *token.FileSet) *Bag { return &Bag{fset: fset} }

// Add appends an already-resolved diagnostic (e.g. a go/types error).
func (b *Bag) Add(d Diagnostic) { b.diags = append(b.diags, d) }

// Report records a diagnostic for an AST node range with an explicit severity
// and source, resolving pos/end through the Bag's fset. end may be token.NoPos
// (then End == Start). Used by non-codegen layers (e.g. jsx) that must set their
// own Source.
func (b *Bag) Report(pos, end token.Pos, sev Severity, code, source, format string, args ...any) {
	d := Diagnostic{
		Severity: sev,
		Code:     code,
		Message:  fmt.Sprintf(format, args...),
		Source:   source,
	}
	if b.fset != nil {
		d.Start = b.fset.Position(pos)
		if end.IsValid() {
			d.End = b.fset.Position(end)
		} else {
			d.End = d.Start
		}
	}
	b.diags = append(b.diags, d)
}

// Errorf is the codegen convenience: an Error-severity diagnostic for an AST
// node range.
func (b *Bag) Errorf(pos, end token.Pos, code, format string, args ...any) {
	b.Report(pos, end, Error, code, "", format, args...)
}

func (b *Bag) HasErrors() bool {
	for _, d := range b.diags {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

// Sorted returns the diagnostics in deterministic filename→line→column order.
func (b *Bag) Sorted() []Diagnostic {
	out := append([]Diagnostic(nil), b.diags...)
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i].Start, out[j].Start
		if a.Filename != c.Filename {
			return a.Filename < c.Filename
		}
		if a.Line != c.Line {
			return a.Line < c.Line
		}
		return a.Column < c.Column
	})
	return out
}
