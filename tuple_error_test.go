package gsx

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestValueFormArmErrorPropagation mirrors what codegen emits for a value-form
// switch arm whose function returns a (string, error) tuple — specifically the
// error path. Generated structure (from value_switch_tuple corpus case):
//
//	var _gsxv0 string
//	switch variant {
//	case 1:
//	    _gsxv1, _gsxerr := cls(variant)
//	    if _gsxerr != nil { return _gsxerr }
//	    _gsxv0 = _gsxv1
//	}
//	_gsxgw.S(` class="`)
//	_gsxgw.Class(...)
//
// The error fires INSIDE the switch block (before the class attr is written), so
// the output must contain the opening tag but NOT the class attribute.
func TestValueFormArmErrorPropagation(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("tuple-test: value-form arm error")

	// clsFn simulates a (string, error) function used in a value-form arm.
	clsFn := func() (string, error) { return "", sentinel }

	// Mirrors generated code for:
	//   <span class={ "base", switch variant { case 1: { cls(variant) } default: { "gray" } } }>x</span>
	// when variant==1 and cls returns an error.
	component := Func(func(ctx context.Context, w io.Writer) error {
		gw := W(w)
		gw.S("<span")
		// Hoisted switch block (value-form CF):
		var gsxv0 string
		// case 1 arm: hoist (string, error) tuple
		gsxv1, err := clsFn()
		if err != nil {
			return err // error fires here, before class attr
		}
		gsxv0 = gsxv1
		// class attr written AFTER the switch block
		gw.S(` class="`)
		gw.Class(DefaultClassMerge, Class("base"), Class(gsxv0))
		gw.S(`">x</span>`)
		return gw.Err()
	})

	var b strings.Builder
	err := component.Render(context.Background(), &b)

	// (a) The exact sentinel error must propagate out of Render.
	if !errors.Is(err, sentinel) {
		t.Fatalf("Render returned %v; want sentinel", err)
	}

	got := b.String()
	// (b) Output contains the opening tag (written before the switch block).
	if !strings.Contains(got, "<span") {
		t.Fatalf("output %q missing opening tag; want partial output before class attr", got)
	}
	// (c) Class attr must NOT be present — error fired before it was written.
	if strings.Contains(got, `class=`) {
		t.Fatalf("output %q contains class attr; want truncation before class attr", got)
	}
	if strings.Contains(got, "base") {
		t.Fatalf("output %q contains class content; want truncation before class attr", got)
	}
}

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
