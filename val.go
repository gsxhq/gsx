package gsx

import (
	"context"
	"fmt"
	"io"
	"strconv"
)

// Val wraps any renderable value as a Node (so a value can fill a gsx.Node prop).
// A Node renders itself; string/[]byte/fmt.Stringer render as escaped text; the
// numeric and bool kinds render their plain Go form (use the |> pipeline for
// formatted numbers, e.g. { f | money("$") }); nil renders nothing.
//
// Named scalar types (e.g. type Slug string, type Money float64) are NOT matched
// by the type switch — they hit the default error case even though { x } inline
// classifies them by underlying type. Workaround: convert to the base type
// (string(slug)) or pass through a |> pipeline before promotion.
//
// Why a runtime box rather than classify-and-specialize at codegen (the type IS
// known at emit time): see docs/superpowers/specs/2026-06-23-gsx-node-prop-promotion-design.md §8.
func Val(v any) Node { return valNode{v} }

type valNode struct{ v any }

func (n valNode) Render(ctx context.Context, w io.Writer) error {
	if n.v == nil {
		return nil
	}
	gw := W(w)
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
	case string:
		gw.Text(t)
	case []byte:
		gw.Text(string(t))
	case fmt.Stringer:
		gw.Text(t.String())
	case bool:
		// gw.Text mirrors emitRender: catBool uses _gsxgw.Text(strconv.FormatBool(...)).
		gw.Text(strconv.FormatBool(t))
	case int:
		gw.Text(strconv.FormatInt(int64(t), 10))
	case int8:
		gw.Text(strconv.FormatInt(int64(t), 10))
	case int16:
		gw.Text(strconv.FormatInt(int64(t), 10))
	case int32:
		gw.Text(strconv.FormatInt(int64(t), 10))
	case int64:
		gw.Text(strconv.FormatInt(t, 10))
	case uint:
		gw.Text(strconv.FormatUint(uint64(t), 10))
	case uint8:
		gw.Text(strconv.FormatUint(uint64(t), 10))
	case uint16:
		gw.Text(strconv.FormatUint(uint64(t), 10))
	case uint32:
		gw.Text(strconv.FormatUint(uint64(t), 10))
	case uint64:
		gw.Text(strconv.FormatUint(t, 10))
	case float32:
		// emitRender always uses bitsize 64: FormatFloat(float64(x), 'g', -1, 64).
		gw.Text(strconv.FormatFloat(float64(t), 'g', -1, 64))
	case float64:
		gw.Text(strconv.FormatFloat(t, 'g', -1, 64))
	default:
		return fmt.Errorf("gsx.Val: value of type %T is not renderable in a gsx.Node prop", n.v)
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
