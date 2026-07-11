package codegen

import (
	"sort"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

// cycleEdge is a component→component edge in the unconditional lowercase-tag
// graph: the source component's body contains element el, a lowercase
// component-resolved tag naming component "to".
type cycleEdge struct {
	to string
	el *gsxast.Element
}

// collectUnconditionalEdges gathers, per receiver-less component, the
// component-resolved lowercase simple tags in its body NOT nested under
// if/for/switch markup (those legitimately break recursion — a for can run
// zero times, an if may be false). CondAttr (in-tag `{ if COND { attr } }`)
// does not gate CHILDREN, so it is deliberately not treated as conditional
// here: walkMarkupAttrs recurses through it the same as any other attr.
//
// Elements nested inside a component-resolved element's own Children are
// still walked and attributed to the SAME enclosing component (not to the
// nested element's target) — those children are slot content, rendered only
// if the target component actually renders {children}. Treating them as
// unconditional edges from the enclosing component is a conservative
// over-approximation (it can flag a cycle that in practice never renders,
// e.g. if the target never uses {children}), chosen because gsx cannot in
// general tell whether a component renders {children} without a much deeper
// analysis; false positives here are a warning, not a build error.
func collectUnconditionalEdges(c *gsxast.Component, nodes map[string]bool) []cycleEdge {
	var out []cycleEdge
	var walk func(ms []gsxast.Markup)
	walk = func(ms []gsxast.Markup) {
		for _, n := range ms {
			switch t := n.(type) {
			case *gsxast.Element:
				if t.IsComponent && !strings.Contains(t.Tag, ".") &&
					!gsxast.IsComponentTag(t.Tag) && nodes[t.Tag] {
					out = append(out, cycleEdge{to: t.Tag, el: t})
				}
				walkMarkupAttrs(t.Attrs, func(v []gsxast.Markup) { walk(v) })
				walk(t.Children)
			case *gsxast.Fragment:
				walk(t.Children)
				// IfMarkup / ForMarkup / SwitchMarkup: conditional — do not descend.
			}
		}
	}
	walk(c.Body)
	return out
}

// canonicalizeCycle rotates a closed cycle path (path[0] == path[len-1], as
// produced by slicing the DFS stack at a back edge) so it starts at the
// lexicographically smallest node name, preserving cyclic order. This makes
// the reported cycle identical (same key, same message, same edge) no matter
// which node in the cycle the DFS happened to enter from — which entry point
// fires first is otherwise a function of map iteration order over
// files/decls and is NOT deterministic across runs.
func canonicalizeCycle(path []string) []string {
	open := path[:len(path)-1] // drop the repeated closing node
	minIdx := 0
	for i, n := range open {
		if n < open[minIdx] {
			minIdx = i
		}
	}
	rotated := make([]string, 0, len(path))
	for i := range open {
		rotated = append(rotated, open[(minIdx+i)%len(open)])
	}
	rotated = append(rotated, rotated[0])
	return rotated
}

// reportWrapperCycles warns on every cycle in the unconditional
// component→component tag graph. Self-loops are impossible (self-exclusion
// stamps a self-named tag as a leaf, never IsComponent), so DFS never needs
// to special-case n == n; mutual wrapper cycles (A renders <B>, B renders
// <A>, unconditionally) compile clean and recurse forever at render, hence
// the warning. See docs/superpowers/specs/2026-07-10-lowercase-tag-symbol-resolution-design.md.
//
// DETERMINISM: files/nodes/edges are built from maps, whose iteration order
// Go deliberately randomizes. Every subsequent step is made order-independent
// on purpose: DFS roots are visited in sorted order, each node's outgoing
// edges are sorted (by target name, then source position), and the reported
// cycle is canonicalized (canonicalizeCycle) before it is deduped/reported —
// so the same input always yields byte-identical diagnostics.
//
// GUARANTEE (single witness, not full enumeration): every package with at
// least one unconditional wrapper cycle gets at least one deterministic
// warning per affected traversal region — but the DFS reports only the
// cycles its back edges happen to witness, NOT every elementary cycle. Two
// overlapping cycles sharing nodes ("detour" topologies, e.g. edges a→b,
// a→c, b→c, c→e, e→a: elementary cycles a→b→c→e→a and a→c→e→a) can yield a
// single warning, because an edge of the second cycle (a→c) is examined only
// once its target is already black, so no back edge ever witnesses that
// cycle — the same edge every run, the miss is itself deterministic, never
// flaky. This is deliberate: full
// elementary-cycle enumeration (Johnson's algorithm) is exponential in
// output and adds noise for a warning-grade diagnostic. Convergence is
// iterative: fixing the reported cycle and regenerating surfaces any
// remaining ones. TestWrapperCycleDetourSingleWitness pins this behavior so
// any future algorithm change is a conscious one.
func reportWrapperCycles(files map[string]*gsxast.File, bag *diag.Bag) {
	nodes := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" {
				nodes[c.Name] = true
			}
		}
	}
	edges := map[string][]cycleEdge{}
	for _, f := range files {
		for _, d := range f.Decls {
			if c, ok := d.(*gsxast.Component); ok && c.Recv == "" {
				// c.Name is unique among nodes, so this append is not itself
				// order-dependent on files-map iteration; the slice content
				// only ever comes from this one component's own body walk.
				edges[c.Name] = append(edges[c.Name], collectUnconditionalEdges(c, nodes)...)
			}
		}
	}
	for n, es := range edges {
		sort.Slice(es, func(i, j int) bool {
			if es[i].to != es[j].to {
				return es[i].to < es[j].to
			}
			return es[i].el.Pos() < es[j].el.Pos()
		})
		edges[n] = es
	}

	roots := make([]string, 0, len(nodes))
	for n := range nodes {
		roots = append(roots, n)
	}
	sort.Strings(roots)

	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	reported := map[string]bool{}

	var visit func(n string)
	visit = func(n string) {
		color[n] = grey
		stack = append(stack, n)
		for _, e := range edges[n] {
			switch color[e.to] {
			case white:
				visit(e.to)
			case grey:
				// Found a cycle: slice stack from e.to's position, close it
				// back to e.to.
				i := len(stack) - 1
				for i >= 0 && stack[i] != e.to {
					i--
				}
				if i < 0 {
					continue // unreachable: e.to is grey, so it IS on stack
				}
				path := append(append([]string{}, stack[i:]...), e.to)
				canon := canonicalizeCycle(path)
				key := strings.Join(canon, "→")
				if reported[key] {
					continue
				}
				reported[key] = true
				reportCycle(bag, edges, canon)
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
	}
	for _, n := range roots {
		if color[n] == white {
			visit(n)
		}
	}
}

// reportCycle emits the wrapper-cycle diagnostic for an already-canonicalized
// cycle path (canonicalizeCycle: starts at the lexicographically smallest
// node, path[0] == path[len(path)-1]). The reported position is the edge
// element FROM the smallest node TO the next node in the path — the
// simplest deterministic choice of "the edge that closes the cycle" once the
// path itself no longer depends on DFS entry point. edges[from] is sorted
// (by target, then position), so the match is the same element every time
// even if two elements in "from"'s body both name "to".
func reportCycle(bag *diag.Bag, edges map[string][]cycleEdge, canon []string) {
	from, to := canon[0], canon[1]
	var el *gsxast.Element
	for _, e := range edges[from] {
		if e.to == to {
			el = e.el
			break
		}
	}
	if el == nil {
		return // unreachable: canon was derived from a real edge in "edges"
	}
	bag.Report(el.Pos(), el.End(), diag.Warning, "wrapper-cycle", "codegen",
		"wrapper cycle %s will recurse infinitely at render", strings.Join(canon, " → "))
}
