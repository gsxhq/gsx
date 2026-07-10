package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/codegen"
)

// TestWithFieldMatcherGoodMapper is the first end-to-end test of the custom
// FieldMatcher path (gen.WithFieldMatcher). It installs a matcher that maps the
// attribute "x-title" directly to "Title" (bypassing the default "XTitle"
// candidate) and confirms the generated output reflects that mapping.
func TestWithFieldMatcherGoodMapper(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxfmgood")
	pkgDir := filepath.Join(mod, "views")

	// External struct with a Title field — the custom matcher maps "x-title" → "Title".
	writeFile(t, pkgDir, "card.go", `package views

type CardProps struct {
	Title string
}
`)
	writeFile(t, pkgDir, "page.gsx", `package views

component Card(p CardProps) {
	<h1>{ p.Title }</h1>
}

component Page() {
	<Card x-title="Hello"/>
}
`)

	// Custom matcher: "x-title" → "Title"; everything else falls through.
	customMatcher := codegen.FieldMatcher(func(attr string, fields []string) (string, bool) {
		if attr == "x-title" {
			return "Title", true
		}
		return "", false
	})

	res, err := generateCached([]string{pkgDir}, nil, nil, attrclass.Builtin(), customMatcher, false, nil, nil, true, true, nil)
	if err != nil {
		t.Fatalf("generateCached: %v (errs=%v diags=%v)", err, res.Errs, res.Diags)
	}
	if len(res.Diags) != 0 {
		t.Fatalf("expected no diagnostics, got: %v", res.Diags)
	}
	if len(res.Written) != 1 {
		t.Fatalf("expected 1 written, got %d: %v", len(res.Written), res.Written)
	}
	// Verify the generated file uses Title: "Hello" (the matcher's mapping),
	// not XTitle (what the default kebab→Camel rule would produce).
	xgo := filepath.Join(pkgDir, "page.x.go")
	data, rerr := os.ReadFile(xgo)
	if rerr != nil {
		t.Fatalf("reading generated file: %v", rerr)
	}
	if !strings.Contains(string(data), `Title: "Hello"`) {
		t.Errorf("expected generated code to contain `Title: \"Hello\"` (custom matcher mapping); got:\n%s", data)
	}
	if strings.Contains(string(data), "XTitle") {
		t.Errorf("generated code must NOT contain XTitle (default kebab rule was not bypassed); got:\n%s", data)
	}
}

// TestWithFieldMatcherBadMapper proves that a custom FieldMatcher that returns a
// field name not present in the struct produces a "bad-field-match" diagnostic
// instead of a raw Go compile error. This is the safety net for buggy custom
// matchers; the default matcher is unaffected (it only ever returns known fields).
func TestWithFieldMatcherBadMapper(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	mod := newModule(t, "gsxfmbad")
	pkgDir := filepath.Join(mod, "views")

	writeFile(t, pkgDir, "card.go", `package views

type CardProps struct {
	Title string
}
`)
	writeFile(t, pkgDir, "page.gsx", `package views

component Card(p CardProps) {
	<h1>{ p.Title }</h1>
}

component Page() {
	<Card title="Hello"/>
}
`)

	// Bad matcher: returns a field name that does not exist on the struct.
	badMatcher := codegen.FieldMatcher(func(attr string, fields []string) (string, bool) {
		if attr == "title" {
			return "GhostField", true // GhostField is NOT on CardProps
		}
		return "", false
	})

	res, err := generateCached([]string{pkgDir}, nil, nil, attrclass.Builtin(), badMatcher, false, nil, nil, true, true, nil)
	if err == nil {
		t.Fatal("expected a non-nil error for a bad custom matcher, got nil")
	}
	var foundBadFieldMatch bool
	for _, d := range res.Diags {
		if d.Code == "bad-field-match" {
			foundBadFieldMatch = true
			if !strings.Contains(d.Message, "GhostField") || !strings.Contains(d.Message, "title") {
				t.Errorf("bad-field-match diagnostic message %q should mention both the returned field name and the attribute", d.Message)
			}
		}
	}
	if !foundBadFieldMatch {
		t.Fatalf("expected a bad-field-match diagnostic; got diags: %v", res.Diags)
	}
	// A codegen error poisons the dir's .x.go rather than leaving nothing
	// written — the package must never silently build stale output.
	if len(res.Written) != 1 {
		t.Fatalf("expected 1 written (poison) for a bad matcher, got %v", res.Written)
	}
	xgo, rerr := os.ReadFile(filepath.Join(pkgDir, "page.x.go"))
	if rerr != nil || !strings.Contains(string(xgo), "GSX GENERATION FAILED") {
		t.Fatalf("expected page.x.go to be poisoned (err=%v):\n%s", rerr, xgo)
	}
}
