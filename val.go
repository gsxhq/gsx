package gsx

import (
	"context"
	"fmt"
	"io"
)

// Val wraps any renderable value as a Node (so a value can fill a gsx.Node prop).
// A Node renders itself; string/[]byte/[]string/fmt.Stringer render as escaped
// text ([]string joined with spaces); the numeric and bool kinds render their
// plain Go form (use the |> pipeline for formatted numbers, e.g.
// { f | money("$") }); nil renders nothing.
//
// Values are classified by their UNDERLYING type, so a named scalar (type Slug
// string, type Money float64) renders exactly as { x } does inline. See
// anyRenderVal, the single classifier this shares with every other runtime
// consumer.
//
// Why a runtime box rather than classify-and-specialize at codegen (the type IS
// known at emit time): see docs/superpowers/specs/2026-06-23-gsx-node-prop-promotion-design.md §8.
func Val(v any) Node { return valNode{v} }

type valNode struct{ v any }

func (n valNode) Render(ctx context.Context, w io.Writer) error {
	if n.v == nil {
		return nil
	}
	// Node and []Node first: reflect cannot see interface satisfaction the way
	// classify's implementsNode can, and these render themselves rather than
	// stringifying. Mirrors classify's Node → NodeSlice → Stringer → kind order.
	switch t := n.v.(type) {
	case Node:
		if t == nil {
			return nil
		}
		return t.Render(ctx, w)
	case []Node:
		// Parity with emitRender's catNodeSlice: a []gsx.Node renders each
		// element in order (so a value that renders inline as { rows } also
		// renders when promoted into a gsx.Node prop). nil elements skipped.
		return renderNodes(ctx, w, t)
	}
	s, k, ok := anyRenderVal(n.v)
	if !ok {
		return fmt.Errorf("gsx.Val: value of type %T is not renderable in a gsx.Node prop", n.v)
	}
	gw := W(w)
	if k == kindString {
		gw.Text(s) // arbitrary author content — must escape
	} else {
		// kindBool/kindNumber promise an escape-free charset (strconv's digits,
		// '-', '+', '.', 'e', and the Inf/NaN and true/false letters), so the
		// replacer scan is a no-op on every possible value — see PR #122.
		gw.S(s)
	}
	return gw.Err()
}

// Text is the escaped-text Node — codegen's static-string fast-path and a Go-side
// text constructor (one alloc, no any-box).
func Text(s string) Node { return textNode(s) }

type textNode string

func (t textNode) Render(_ context.Context, w io.Writer) error {
	gw := W(w)
	gw.Text(string(t))
	return gw.Err()
}

// Fragment groups children into one Node with no wrapper element — the
// type-safe, variadic way to fill a single gsx.Node prop with multiple nodes
// (and the lowering target for a future <>…</> syntax). Renders each child in
// order; nil children are skipped; Fragment() renders nothing.
func Fragment(nodes ...Node) Node { return fragmentNode(nodes) }

type fragmentNode []Node

func (f fragmentNode) Render(ctx context.Context, w io.Writer) error {
	return renderNodes(ctx, w, f)
}

// renderNodes renders each node in order, skipping nils — the shared body of
// Val's []Node case and fragmentNode (one place for the slice-render rule).
func renderNodes(ctx context.Context, w io.Writer, nodes []Node) error {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if err := n.Render(ctx, w); err != nil {
			return err
		}
	}
	return nil
}
