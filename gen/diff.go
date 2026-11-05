package gen

import (
	"fmt"
	"strings"
)

// diffContext is the number of unchanged lines shown around each change, as
// in gofmt -d and diff -u.
const diffContext = 3

// maxDiffCells bounds the LCS table over the changed middle of a file (after
// the common prefix and suffix are trimmed). A larger middle is shown as one
// replacement: still a correct unified diff, only coarser.
const maxDiffCells = 4 << 20

// unifiedDiff returns a unified diff from a to b with diffContext lines of
// context, or "" when they are equal. Headers name path.orig and path.
func unifiedDiff(path string, a, b []byte) string {
	aLines, bLines := splitLinesKeepEnds(a), splitLinesKeepEnds(b)
	ops := diffOps(aLines, bLines)
	var sb strings.Builder
	for start := 0; start < len(ops); {
		for start < len(ops) && ops[start].kind == ' ' {
			start++
		}
		if start == len(ops) {
			break
		}
		// Extend the hunk while the next change is within 2*diffContext
		// unchanged lines, so adjacent hunks merge as in diff -u.
		end := start
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*diffContext {
				break
			}
			end = run
		}
		lo, hi := max(start-diffContext, 0), min(end+diffContext, len(ops))
		if sb.Len() == 0 {
			fmt.Fprintf(&sb, "--- %s.orig\n+++ %s\n", path, path)
		}
		aStart, bStart, aN, bN := ops[lo].a, ops[lo].b, 0, 0
		for _, op := range ops[lo:hi] {
			if op.kind != '+' {
				aN++
			}
			if op.kind != '-' {
				bN++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", hunkRange(aStart, aN), hunkRange(bStart, bN))
		for _, op := range ops[lo:hi] {
			writeDiffLine(&sb, op.kind, op.line)
		}
		start = hi
	}
	return sb.String()
}

// hunkRange formats a 0-based start and length as a unified-diff range.
func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

// diffOp is one line of an edit script: kind ' ' (both), '-' (a) or '+' (b),
// with the 0-based a and b line numbers at that point.
type diffOp struct {
	kind byte
	line string
	a, b int
}

// diffOps returns a minimal line edit script from a to b: the common prefix
// and suffix, and a longest-common-subsequence alignment of the middle.
func diffOps(a, b []string) []diffOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := make([]diffOp, 0, len(a)+len(b))
	for i := range pre {
		ops = append(ops, diffOp{' ', a[i], i, i})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	i, j := pre, pre
	emit := func(kind byte, line string) {
		ops = append(ops, diffOp{kind, line, i, j})
		if kind != '+' {
			i++
		}
		if kind != '-' {
			j++
		}
	}
	if len(ma)*len(mb) > maxDiffCells {
		for _, l := range ma {
			emit('-', l)
		}
		for _, l := range mb {
			emit('+', l)
		}
	} else {
		// lcs[x][y] is the LCS length of ma[x:] and mb[y:].
		w := len(mb) + 1
		lcs := make([]int32, (len(ma)+1)*w)
		for x := len(ma) - 1; x >= 0; x-- {
			for y := len(mb) - 1; y >= 0; y-- {
				if ma[x] == mb[y] {
					lcs[x*w+y] = lcs[(x+1)*w+y+1] + 1
				} else {
					lcs[x*w+y] = max(lcs[(x+1)*w+y], lcs[x*w+y+1])
				}
			}
		}
		x, y := 0, 0
		for x < len(ma) || y < len(mb) {
			switch {
			case x < len(ma) && y < len(mb) && ma[x] == mb[y]:
				emit(' ', ma[x])
				x, y = x+1, y+1
			case x < len(ma) && (y == len(mb) || lcs[(x+1)*w+y] >= lcs[x*w+y+1]):
				emit('-', ma[x]) // deletions before insertions, as in diff -u
				x++
			default:
				emit('+', mb[y])
				y++
			}
		}
	}
	for k := len(a) - suf; k < len(a); k++ {
		emit(' ', a[k])
	}
	return ops
}
