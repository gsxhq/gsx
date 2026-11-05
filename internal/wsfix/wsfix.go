// Package wsfix applies the whitespace lint fixes of internal/wsnorm to .gsx
// source: `gsx fix whitespace`, and the editor quick fixes.
package wsfix

import (
	"fmt"
	"go/token"
	"sort"

	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// Findings parses src and returns its whitespace findings. The returned
// token.File resolves their positions to byte offsets in src.
func Findings(path string, src []byte) ([]wsnorm.Finding, *token.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return nil, nil, err
	}
	return wsnorm.Lint(f), fset.File(f.Pos()), nil
}

// maxPasses bounds Fix. One pass fixes every finding it reports; a later pass
// only sees a finding an earlier fix exposed (a joined line facing another
// line break), which converges in a pass or two.
const maxPasses = 8

// Fix rewrites src until it has no migration findings (Finding.Migrate) and
// returns the result with the number fixed. Each fix deletes a line break the
// pre-v0.5 rule rendered as nothing, so the result renders exactly what that
// rule rendered at those boundaries and is otherwise unchanged.
func Fix(path string, src []byte) ([]byte, int, error) {
	fixed := 0
	for range maxPasses {
		all, file, err := Findings(path, src)
		if err != nil {
			return nil, 0, err
		}
		var findings []wsnorm.Finding
		for _, f := range all {
			if f.Migrate {
				findings = append(findings, f)
			}
		}
		if len(findings) == 0 {
			return src, fixed, nil
		}
		var n int
		src, n = apply(src, file, findings)
		fixed += n
	}
	return nil, 0, fmt.Errorf("%s: whitespace fixes did not converge after %d passes", path, maxPasses)
}

// apply applies every finding whose edits do not overlap an earlier finding's;
// a skipped finding is reported again by the next pass.
func apply(src []byte, file *token.File, findings []wsnorm.Finding) ([]byte, int) {
	type span struct {
		start, end int
		repl       string
	}
	var spans []span
	taken := func(s, e int) bool {
		for _, x := range spans {
			if s < x.end && x.start < e || s == x.start && e == x.end {
				return true
			}
		}
		return false
	}
	n := 0
	for _, f := range findings {
		edits := make([]span, 0, len(f.Edits))
		ok := true
		for _, e := range f.Edits {
			s, end := file.Offset(e.Pos), file.Offset(e.End)
			if taken(s, end) {
				ok = false
				break
			}
			edits = append(edits, span{s, end, e.New})
		}
		if !ok {
			continue
		}
		spans = append(spans, edits...)
		n++
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	out := make([]byte, 0, len(src))
	last := 0
	for _, s := range spans {
		out = append(out, src[last:s.start]...)
		out = append(out, s.repl...)
		last = s.end
	}
	return append(out, src[last:]...), n
}
