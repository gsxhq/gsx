package gsx

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestTupleErrorPropagation asserts the KEY BEHAVIORAL INVARIANT of (T,error)
// auto-unwrap: when the hoisted function returns a non-nil error, the enclosing
// Render returns that EXACT error and output is TRUNCATED at the failing point.
//
// This exercises the `if _gsxerr != nil { return _gsxerr }` generated pattern
// at runtime — corpus render cases use nil-error functions, so this is the only
// direct behavioral test of the error path.
func TestTupleErrorPropagation(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("tuple-test: render aborted")

	// failingFn simulates a (string, error) function that fails.
	// Mirrors the codegen pattern: v, _gsxerr := failingFn(); if _gsxerr != nil { return _gsxerr }
	failingFn := func() (string, error) { return "", sentinel }

	// Component that writes prefix, then calls failingFn (hoisted), then suffix.
	// Matches the codegen structure: partial output then error → truncate.
	component := Func(func(ctx context.Context, w io.Writer) error {
		gw := W(w)
		gw.S("<div>before")
		v, err := failingFn()
		if err != nil {
			return err
		}
		gw.Text(v)
		gw.S("after</div>")
		return gw.Err()
	})

	var b strings.Builder
	err := component.Render(context.Background(), &b)

	// (a) The exact sentinel error must propagate out of Render.
	if !errors.Is(err, sentinel) {
		t.Fatalf("Render returned %v; want sentinel %v", err, sentinel)
	}

	// (b) Output must be TRUNCATED — only the bytes written before the failure.
	got := b.String()
	if strings.Contains(got, "after") {
		t.Fatalf("output %q contains post-failure content; want truncation at error point", got)
	}
	// Confirm pre-failure output IS present (function aborted mid-render, not at start).
	if !strings.Contains(got, "before") {
		t.Fatalf("output %q missing pre-failure content %q", got, "before")
	}
}

// TestTupleErrorPropagationChildComponent mirrors what codegen emits for a
// (T,error) child-prop:
//
//	_gsxv0, _gsxerr := propFn()
//	if _gsxerr != nil { return _gsxerr }
//	_gsxgw.Node(ctx, Child(ChildProps{X: _gsxv0}))
//
// Asserts that the error short-circuits BEFORE the child component renders.
func TestTupleErrorPropagationChildComponent(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("tuple-test: prop error")
	childRendered := false

	propFn := func() (string, error) { return "", sentinel }

	child := func(x string) Node {
		return Func(func(ctx context.Context, w io.Writer) error {
			childRendered = true
			W(w).S("<span>" + x + "</span>")
			return nil
		})
	}

	// Mirrors generated Page.Render for <Child x={propFn()}/> :
	//   _gsxv0, _gsxerr := propFn()
	//   if _gsxerr != nil { return _gsxerr }
	//   _gsxgw.Node(ctx, Child(ChildProps{X: _gsxv0}))
	page := Func(func(ctx context.Context, w io.Writer) error {
		gw := W(w)
		gw.S("<main>")
		v, err := propFn()
		if err != nil {
			return err
		}
		gw.Node(ctx, child(v))
		gw.S("</main>")
		return gw.Err()
	})

	var b strings.Builder
	err := page.Render(context.Background(), &b)

	if !errors.Is(err, sentinel) {
		t.Fatalf("Render returned %v; want sentinel", err)
	}
	if childRendered {
		t.Fatal("child component rendered despite prop error; want short-circuit before child")
	}
	got := b.String()
	if strings.Contains(got, "span") {
		t.Fatalf("output %q contains child output; want truncation", got)
	}
}
