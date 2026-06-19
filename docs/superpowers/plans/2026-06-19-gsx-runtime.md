# gsx Runtime Package Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the dependency-free `gsx` runtime package (the module root) that generated code calls to stream context-escaped HTML to an `io.Writer`.

**Architecture:** A single `package gsx` at the module root, split into focused files: `node.go` (the `templ.Component`-shaped `Node` interface + `Func` adapter + `Raw`), `escape.go` (streaming text/attr/URL escapers), `writer.go` (the error-threading `Writer` and its scalar write helpers), `class.go` (class/style composition + the pluggable `ClassMerger`), `attrs.go` (the `Attrs` bag + deterministic spread). Built and tested standalone by hand-writing `Node`s — no parser, no codegen, no type resolution.

**Tech Stack:** Go 1.26.1, standard library only. Package `github.com/gsxhq/gsx` (module root).

## Global Constraints

- **Standard library only** — no third-party dependencies, ever. Imports limited to `context`, `io`, `strings`, `sort`, `fmt`.
- **`gsx.Node` is gsx's own interface** — `Render(ctx context.Context, w io.Writer) error`, method set **identical to `templ.Component`** (structural interop, no templ import).
- **Streaming + error threading** — helpers write straight to the underlying `io.Writer`; `Writer` retains the **first** error; after an error is set every helper is a no-op; the error is read once via `Err()`.
- **Auto-escaping is the default** — `Text`/`AttrValue` HTML-escape; `URL` sanitizes the scheme then escapes; `Raw` is the only opt-out (verbatim).
- **`Spread` is deterministic** — attribute keys are emitted in sorted order so generated HTML is stable and golden-testable.
- **`ClassMerger` is a package-level `var`** — default dedupe+space-join; apps replace it once at init.
- **gofmt + go vet clean**; every task ends green via `go test ./...`. Unexported by default (only the documented public surface is exported).

### Public surface (the exact API generated code will target)

```
type Node interface { Render(ctx context.Context, w io.Writer) error }
type Func func(ctx context.Context, w io.Writer) error ; (Func).Render
func Raw(html string) Node

type Writer struct{ /* unexported */ }
func W(w io.Writer) *Writer
func (*Writer) Err() error
func (*Writer) S(s string)
func (*Writer) Text(s string)
func (*Writer) AttrValue(s string)
func (*Writer) URL(s string)
func (*Writer) BoolAttr(name string, on bool)
func (*Writer) Class(parts ...ClassPart)
func (*Writer) Style(parts ...ClassPart)
func (*Writer) Spread(ctx context.Context, a Attrs)
func (*Writer) Node(ctx context.Context, n Node)

type ClassPart struct{ /* unexported */ }
func Class(s string) ClassPart
func ClassIf(s string, on bool) ClassPart
var ClassMerger func(tokens []string) string

type Attrs map[string]any
func (Attrs) Has(key string) bool
func (Attrs) Get(key string) (any, bool)
func (Attrs) Class() string
func (Attrs) Without(keys ...string) Attrs
func (Attrs) Take(key string) (any, Attrs)
func (Attrs) Merge(other Attrs) Attrs
```

---

## File Structure

- `node.go` — `Node`, `Func`, `Raw`.
- `escape.go` — `htmlReplacer`, `writeHTML`, `urlSanitize`, `writeURL`, `blockedURL` (all unexported).
- `writer.go` — `Writer`, `W`, `Err`, unexported `writeStr`, `S`, `Text`, `AttrValue`, `URL`, `BoolAttr`, `Node`.
- `class.go` — `ClassPart`, `Class`, `ClassIf`, `ClassMerger`, `defaultClassMerge`, `classTokens`, `(*Writer).Class`, `(*Writer).Style`.
- `attrs.go` — `Attrs` + methods, `toStr`, `joinAttrStrings`, `(*Writer).Spread`.
- `gsx_test.go` — the integration golden (hand-built `Node` trees → exact HTML).
- Per-file `*_test.go` for unit tests.

---

### Task 1: Node, Func, Raw (`node.go`)

**Files:**
- Create: `node.go`
- Test: `node_test.go`

**Interfaces:**
- Produces: `Node` interface; `Func` adapter (with `Render`); `Raw(html string) Node`.

- [ ] **Step 1: Write the failing test**

Create `node_test.go`:

```go
package gsx

import (
	"bytes"
	"context"
	"testing"
)

func TestFuncRenders(t *testing.T) {
	called := false
	var n Node = Func(func(ctx context.Context, w io.Writer) error {
		called = true
		_, err := w.Write([]byte("hi"))
		return err
	})
	var b bytes.Buffer
	if err := n.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !called || b.String() != "hi" {
		t.Fatalf("called=%v out=%q", called, b.String())
	}
}

func TestRawIsVerbatim(t *testing.T) {
	var b bytes.Buffer
	if err := Raw(`<b>bold</b>`).Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if b.String() != `<b>bold</b>` { // NOT escaped
		t.Fatalf("got %q", b.String())
	}
	b.Reset()
	if err := Raw("").Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if b.String() != "" {
		t.Fatalf("Raw(\"\") wrote %q", b.String())
	}
}
```

Add `"io"` to the imports of `node_test.go` (used in `TestFuncRenders`).

- [ ] **Step 2: Run to verify failure**

Run: `go test . -run 'TestFuncRenders|TestRawIsVerbatim' -v`
Expected: FAIL — `undefined: Node` / `undefined: Raw` (package doesn't compile yet).

- [ ] **Step 3: Implement `node.go`**

Create `node.go`:

```go
// Package gsx is the runtime that gsx-generated code calls to stream HTML to an
// io.Writer. It is dependency-free (standard library only).
package gsx

import (
	"context"
	"io"
)

// Node is gsx's own rendering interface. Its method set is identical to
// templ.Component, so a gsx.Node satisfies templ.Component structurally — no
// templ import is needed for ecosystem interop.
type Node interface {
	Render(ctx context.Context, w io.Writer) error
}

// Func adapts a plain render function to a Node (cf. templ.ComponentFunc).
type Func func(ctx context.Context, w io.Writer) error

// Render implements Node.
func (f Func) Render(ctx context.Context, w io.Writer) error { return f(ctx, w) }

// Raw wraps trusted, already-safe HTML — the opt-out from auto-escaping. The
// string is written verbatim.
func Raw(html string) Node {
	return Func(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, html)
		return err
	})
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test . -run 'TestFuncRenders|TestRawIsVerbatim' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add node.go node_test.go
git commit -m "feat(gsx): Node interface, Func adapter, Raw"
```

---

### Task 2: Streaming escapers (`escape.go`)

**Files:**
- Create: `escape.go`
- Test: `escape_test.go`

**Interfaces:**
- Produces (unexported, used by `writer.go`): `writeHTML(w io.Writer, s string) error` (text + attribute escaping), `urlSanitize(s string) string`, `writeURL(w io.Writer, s string) error`, `const blockedURL`.

- [ ] **Step 1: Write the failing test**

Create `escape_test.go`:

```go
package gsx

import (
	"strings"
	"testing"
)

func TestWriteHTML(t *testing.T) {
	cases := map[string]string{
		`a & b`:           `a &amp; b`,
		`<script>`:        `&lt;script&gt;`,
		`" onmouseover=`:  `&#34; onmouseover=`,
		`it's`:            `it&#39;s`,
		`plain`:           `plain`,
	}
	for in, want := range cases {
		var b strings.Builder
		if err := writeHTML(&b, in); err != nil {
			t.Fatal(err)
		}
		if b.String() != want {
			t.Fatalf("writeHTML(%q) = %q, want %q", in, b.String(), want)
		}
	}
}

func TestURLSanitize(t *testing.T) {
	safe := []string{
		"http://example.com/x",
		"https://example.com",
		"HTTPS://EXAMPLE.com", // scheme case-insensitive
		"mailto:a@b.com",
		"tel:+1234",
		"/relative/path",
		"../up",
		"#fragment",
		"?q=:colon",          // ':' after '?' is not a scheme
		"//cdn.example.com/x", // protocol-relative
	}
	for _, s := range safe {
		if got := urlSanitize(s); got != s {
			t.Fatalf("urlSanitize(%q) = %q, want unchanged", s, got)
		}
	}
	blocked := []string{
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"vbscript:msgbox",
		"data:text/html,<script>",
	}
	for _, s := range blocked {
		if got := urlSanitize(s); got != blockedURL {
			t.Fatalf("urlSanitize(%q) = %q, want %q", s, got, blockedURL)
		}
	}
}

func TestWriteURLEscapesAfterSanitize(t *testing.T) {
	var b strings.Builder
	if err := writeURL(&b, `/x?a="b"&c`); err != nil {
		t.Fatal(err)
	}
	if b.String() != `/x?a=&#34;b&#34;&amp;c` {
		t.Fatalf("got %q", b.String())
	}
	b.Reset()
	if err := writeURL(&b, "javascript:alert(1)"); err != nil {
		t.Fatal(err)
	}
	if b.String() != blockedURL {
		t.Fatalf("blocked URL = %q, want %q", b.String(), blockedURL)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test . -run 'TestWriteHTML|TestURLSanitize|TestWriteURL' -v`
Expected: FAIL — `undefined: writeHTML` etc.

- [ ] **Step 3: Implement `escape.go`**

Create `escape.go`:

```go
package gsx

import (
	"io"
	"strings"
)

// htmlReplacer escapes the bytes unsafe in HTML text and double-quoted attribute
// contexts. The entity set matches html.EscapeString.
var htmlReplacer = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&#34;",
	"'", "&#39;",
)

// writeHTML streams s to w with HTML text/attribute escaping. strings.Replacer
// writes safe runs directly, so this allocates only for the (rare) entity spans.
func writeHTML(w io.Writer, s string) error {
	_, err := htmlReplacer.WriteString(w, s)
	return err
}

// blockedURL replaces a URL whose scheme is not allow-listed (mirrors
// html/template's #ZgotmplZ sentinel for an unsafe URL).
const blockedURL = "about:invalid#gsx"

// urlSanitize returns s unchanged when it is relative/fragment/query or carries
// an allow-listed scheme (http, https, mailto, tel — case-insensitive); any
// other scheme (javascript:, vbscript:, data:, …) yields blockedURL.
func urlSanitize(s string) string {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		// A scheme exists only when no '/', '?', or '#' precedes the ':'.
		if !strings.ContainsAny(s[:i], "/?#") {
			switch strings.ToLower(s[:i]) {
			case "http", "https", "mailto", "tel":
				// allowed
			default:
				return blockedURL
			}
		}
	}
	return s
}

// writeURL streams a sanitized, attribute-escaped URL value to w.
func writeURL(w io.Writer, s string) error {
	return writeHTML(w, urlSanitize(s))
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test . -run 'TestWriteHTML|TestURLSanitize|TestWriteURL' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add escape.go escape_test.go
git commit -m "feat(gsx): streaming HTML/attr/URL escapers"
```

---

### Task 3: Writer with error threading (`writer.go`)

**Files:**
- Create: `writer.go`
- Test: `writer_test.go`

**Interfaces:**
- Consumes: `writeHTML`, `writeURL` (Task 2); `Node` (Task 1).
- Produces: `Writer`, `W(io.Writer) *Writer`, `(*Writer).Err`, unexported `(*Writer).writeStr`, and `S`/`Text`/`AttrValue`/`URL`/`BoolAttr`/`Node`.

- [ ] **Step 1: Write the failing test**

Create `writer_test.go`:

```go
package gsx

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWriterHelpers(t *testing.T) {
	var b strings.Builder
	gw := W(&b)
	gw.S(`<a href="`)
	gw.URL("/path?x=1")
	gw.S(`" data-t="`)
	gw.AttrValue(`a"&b`)
	gw.S(`">`)
	gw.Text(`hi <there>`)
	gw.BoolAttr("hidden", false) // omitted
	gw.BoolAttr("checked", true) // ` checked`
	gw.S(`</a>`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	want := `<a href="/path?x=1" data-t="a&#34;&amp;b">hi &lt;there&gt; checked</a>`
	if b.String() != want {
		t.Fatalf("got  %q\nwant %q", b.String(), want)
	}
}

func TestWriterNodeNilSafe(t *testing.T) {
	var b strings.Builder
	gw := W(&b)
	gw.Node(context.Background(), nil) // no-op, no panic
	gw.Node(context.Background(), Raw("X"))
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if b.String() != "X" {
		t.Fatalf("got %q", b.String())
	}
}

// failingWriter fails on the Nth write.
type failingWriter struct {
	n int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("boom")
	}
	f.n--
	return len(p), nil
}

func TestWriterErrorThreadingShortCircuits(t *testing.T) {
	fw := &failingWriter{n: 1} // allow one write, then fail
	gw := W(fw)
	gw.S("ok")    // succeeds
	gw.S("boom")  // fails, sets err
	gw.Text("xx") // no-op (err already set)
	if gw.Err() == nil {
		t.Fatal("expected threaded error, got nil")
	}
	var _ io.Writer = fw
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test . -run 'TestWriter' -v`
Expected: FAIL — `undefined: W` / `undefined: Writer`.

- [ ] **Step 3: Implement `writer.go`**

Create `writer.go`:

```go
package gsx

import (
	"context"
	"io"
)

// Writer streams HTML to an underlying io.Writer, retaining the first write error
// so generated code need not check every write. Once an error is set, every
// helper is a no-op; read it once via Err.
type Writer struct {
	w   io.Writer
	err error
}

// W wraps w. The returned *Writer is always usable.
func W(w io.Writer) *Writer { return &Writer{w: w} }

// Err returns the first write error encountered, or nil.
func (gw *Writer) Err() error { return gw.err }

// writeStr writes s verbatim, threading the first error.
func (gw *Writer) writeStr(s string) {
	if gw.err != nil {
		return
	}
	_, gw.err = io.WriteString(gw.w, s)
}

// S writes trusted static markup verbatim.
func (gw *Writer) S(s string) { gw.writeStr(s) }

// Text writes s as HTML-escaped text content.
func (gw *Writer) Text(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeHTML(gw.w, s)
}

// AttrValue writes s as an escaped double-quoted attribute value.
func (gw *Writer) AttrValue(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeHTML(gw.w, s)
}

// URL writes s as a sanitized, escaped URL attribute value.
func (gw *Writer) URL(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeURL(gw.w, s)
}

// BoolAttr writes ` name` when on, and nothing otherwise.
func (gw *Writer) BoolAttr(name string, on bool) {
	if !on {
		return
	}
	gw.writeStr(" ")
	gw.writeStr(name)
}

// Node renders a child node to the same writer; a nil node is a no-op. A render
// error is retained.
func (gw *Writer) Node(ctx context.Context, n Node) {
	if gw.err != nil || n == nil {
		return
	}
	gw.err = n.Render(ctx, gw.w)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test . -run 'TestWriter' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add writer.go writer_test.go
git commit -m "feat(gsx): error-threading Writer with scalar helpers"
```

---

### Task 4: Class/style composition + ClassMerger (`class.go`)

**Files:**
- Create: `class.go`
- Test: `class_test.go`

**Interfaces:**
- Consumes: `(*Writer).AttrValue` (Task 3).
- Produces: `ClassPart`, `Class(s) ClassPart`, `ClassIf(s, on) ClassPart`, `var ClassMerger func([]string) string`, unexported `defaultClassMerge`, unexported `classTokens(parts []ClassPart) []string`, `(*Writer).Class(parts ...ClassPart)`, `(*Writer).Style(parts ...ClassPart)`.

- [ ] **Step 1: Write the failing test**

Create `class_test.go`:

```go
package gsx

import (
	"strings"
	"testing"
)

func renderClass(parts ...ClassPart) string {
	var b strings.Builder
	W(&b).Class(parts...)
	return b.String()
}

func TestClassComposeDedupeOrder(t *testing.T) {
	got := renderClass(
		Class("btn px-4"),         // whitespace-split into two tokens
		ClassIf("active", true),
		ClassIf("hidden", false),  // excluded
		Class("btn"),              // dup, dropped
	)
	if got != "btn px-4 active" {
		t.Fatalf("got %q", got)
	}
}

func TestClassEscapesValue(t *testing.T) {
	if got := renderClass(Class(`a"b`)); got != `a&#34;b` {
		t.Fatalf("got %q", got)
	}
}

func TestStyleJoins(t *testing.T) {
	var b strings.Builder
	W(&b).Style(Class("color: red"), ClassIf("display: none", false), Class("margin: 0"))
	if b.String() != "color: red; margin: 0" {
		t.Fatalf("got %q", b.String())
	}
}

func TestClassMergerOverride(t *testing.T) {
	orig := ClassMerger
	t.Cleanup(func() { ClassMerger = orig })
	ClassMerger = func(tokens []string) string { return "MERGED:" + strings.Join(tokens, ",") }
	if got := renderClass(Class("a b")); got != "MERGED:a,b" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test . -run 'TestClass|TestStyle' -v`
Expected: FAIL — `undefined: ClassPart` / `undefined: Class`.

- [ ] **Step 3: Implement `class.go`**

Create `class.go`:

```go
package gsx

import "strings"

// ClassPart is one contribution to a class or style attribute: a string included
// only when on. Generated code builds these from the `"str": cond` source sugar.
type ClassPart struct {
	s  string
	on bool
}

// Class is an unconditional class/style contribution.
func Class(s string) ClassPart { return ClassPart{s: s, on: true} }

// ClassIf includes s only when on.
func ClassIf(s string, on bool) ClassPart { return ClassPart{s: s, on: on} }

// ClassMerger is the installable class-merge strategy. It receives the flattened,
// non-empty class tokens in source order and returns the final class string. The
// default dedupes (first occurrence wins) and joins with single spaces. Apps
// replace it once at init to install e.g. a Tailwind-aware merger.
var ClassMerger func(tokens []string) string = defaultClassMerge

func defaultClassMerge(tokens []string) string {
	seen := make(map[string]struct{}, len(tokens))
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return strings.Join(out, " ")
}

// classTokens flattens the on parts into whitespace-split, non-empty tokens.
func classTokens(parts []ClassPart) []string {
	var toks []string
	for _, p := range parts {
		if !p.on {
			continue
		}
		toks = append(toks, strings.Fields(p.s)...)
	}
	return toks
}

// Class composes parts, runs them through ClassMerger, and writes the escaped
// class attribute value.
func (gw *Writer) Class(parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	gw.AttrValue(ClassMerger(classTokens(parts)))
}

// Style composes the on parts as '; '-joined declarations (no merge) and writes
// the escaped style attribute value.
func (gw *Writer) Style(parts ...ClassPart) {
	if gw.err != nil {
		return
	}
	var decls []string
	for _, p := range parts {
		if !p.on {
			continue
		}
		if d := strings.TrimSpace(p.s); d != "" {
			decls = append(decls, d)
		}
	}
	gw.AttrValue(strings.Join(decls, "; "))
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test . -run 'TestClass|TestStyle' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add class.go class_test.go
git commit -m "feat(gsx): class/style composition + pluggable ClassMerger"
```

---

### Task 5: Attrs bag + deterministic Spread (`attrs.go`)

**Files:**
- Create: `attrs.go`
- Test: `attrs_test.go`

**Interfaces:**
- Consumes: `(*Writer).BoolAttr`, `(*Writer).AttrValue`, `(*Writer).writeStr` (Task 3); `classTokens`, `ClassMerger`, `Class` (Task 4).
- Produces: `Attrs`, its methods (`Has`/`Get`/`Class`/`Without`/`Take`/`Merge`), unexported `toStr`, `joinAttrStrings`, and `(*Writer).Spread(ctx, a)`.

- [ ] **Step 1: Write the failing test**

Create `attrs_test.go`:

```go
package gsx

import (
	"context"
	"strings"
	"testing"
)

func TestAttrsHasGet(t *testing.T) {
	a := Attrs{"id": "x", "disabled": true}
	if !a.Has("id") || a.Has("nope") {
		t.Fatal("Has wrong")
	}
	if v, ok := a.Get("disabled"); !ok || v != true {
		t.Fatalf("Get = %v,%v", v, ok)
	}
}

func TestAttrsWithoutAndTakeAreImmutable(t *testing.T) {
	a := Attrs{"a": 1, "b": 2, "c": 3}
	w := a.Without("b")
	if w.Has("b") || !w.Has("a") || !a.Has("b") { // original keeps b
		t.Fatalf("Without mutated or wrong: w=%v a=%v", w, a)
	}
	v, rest := a.Take("a")
	if v != 1 || rest.Has("a") || !a.Has("a") {
		t.Fatalf("Take wrong: v=%v rest=%v a=%v", v, rest, a)
	}
}

func TestAttrsMergeConcatenatesClass(t *testing.T) {
	a := Attrs{"class": "btn", "id": "x"}
	b := Attrs{"class": "active", "id": "y"}
	m := a.Merge(b)
	if m["class"] != "btn active" { // concatenated
		t.Fatalf("class = %v", m["class"])
	}
	if m["id"] != "y" { // other wins
		t.Fatalf("id = %v", m["id"])
	}
}

func TestAttrsClassExtract(t *testing.T) {
	if got := (Attrs{"class": "btn btn px-4"}).Class(); got != "btn px-4" {
		t.Fatalf("got %q", got)
	}
	if got := (Attrs{}).Class(); got != "" {
		t.Fatalf("empty class = %q", got)
	}
}

func TestSpreadDeterministicAndTyped(t *testing.T) {
	var b strings.Builder
	W(&b).Spread(context.Background(), Attrs{
		"data-z":   "9",
		"id":       `a"b`,
		"checked":  true,
		"hidden":   false, // omitted
		"count":    3,     // fmt-formatted
	})
	// keys sorted: checked, count, data-z, hidden(omitted), id
	want := ` checked count="3" data-z="9" id="a&#34;b"`
	if b.String() != want {
		t.Fatalf("got  %q\nwant %q", b.String(), want)
	}
}

func TestSpreadEmpty(t *testing.T) {
	var b strings.Builder
	W(&b).Spread(context.Background(), nil)
	if b.String() != "" {
		t.Fatalf("got %q", b.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test . -run 'TestAttrs|TestSpread' -v`
Expected: FAIL — `undefined: Attrs` methods / `Spread`.

- [ ] **Step 3: Implement `attrs.go`**

Create `attrs.go`:

```go
package gsx

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Attrs is an attribute bag (spread / implicit rest). Values are bool (boolean
// attribute), string, []string, or anything fmt can format.
type Attrs map[string]any

// Has reports whether key is present.
func (a Attrs) Has(key string) bool { _, ok := a[key]; return ok }

// Get returns the value for key and whether it was present.
func (a Attrs) Get(key string) (any, bool) { v, ok := a[key]; return v, ok }

// Without returns a copy of a without the given keys (a is not mutated).
func (a Attrs) Without(keys ...string) Attrs {
	drop := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		drop[k] = struct{}{}
	}
	out := make(Attrs, len(a))
	for k, v := range a {
		if _, skip := drop[k]; skip {
			continue
		}
		out[k] = v
	}
	return out
}

// Take returns the value for key and a copy of a without it (a is not mutated).
func (a Attrs) Take(key string) (any, Attrs) {
	return a[key], a.Without(key)
}

// Merge returns a new bag combining a and other. For most keys other wins; the
// "class" and "style" values are concatenated (a's then other's).
func (a Attrs) Merge(other Attrs) Attrs {
	out := make(Attrs, len(a)+len(other))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range other {
		if (k == "class" || k == "style") && out[k] != nil {
			out[k] = joinAttrStrings(k, toStr(out[k]), toStr(v))
			continue
		}
		out[k] = v
	}
	return out
}

// Class returns the merged class string from the bag's "class" entry, or "".
func (a Attrs) Class() string {
	v, ok := a["class"]
	if !ok {
		return ""
	}
	return ClassMerger(classTokens([]ClassPart{Class(toStr(v))}))
}

// Spread renders the bag deterministically (keys sorted). bool values use
// boolean-attribute semantics; everything else is written as key="value" with
// attribute escaping. ctx is reserved for forward-compatibility.
func (gw *Writer) Spread(ctx context.Context, a Attrs) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if b, ok := a[k].(bool); ok {
			gw.BoolAttr(k, b)
			continue
		}
		gw.writeStr(" ")
		gw.writeStr(k)
		gw.writeStr(`="`)
		gw.AttrValue(toStr(a[k]))
		gw.writeStr(`"`)
	}
}

// toStr renders an attribute/class value to a string.
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		return strings.Join(t, " ")
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// joinAttrStrings concatenates two non-empty class/style values with the right
// separator (space for class, "; " for style).
func joinAttrStrings(key, a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	if key == "style" {
		return a + "; " + b
	}
	return a + " " + b
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test . -run 'TestAttrs|TestSpread' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add attrs.go attrs_test.go
git commit -m "feat(gsx): Attrs bag with methods and deterministic Spread"
```

---

### Task 6: Integration golden (`gsx_test.go`)

**Files:**
- Create: `gsx_test.go`

**Interfaces:**
- Consumes: the whole public surface (Tasks 1–5).

This proves the helpers compose into correct HTML before any codegen exists — the seed of the eventual `render.golden` acceptance gate. The two trees mirror the codegen-walkthrough's `Card` and `Box`.

- [ ] **Step 1: Write the integration test**

Create `gsx_test.go`:

```go
package gsx

import (
	"context"
	"strings"
	"testing"
)

// card mirrors the walkthrough's generated Card.
func card(title string, featured bool, children Node) Node {
	return Func(func(ctx context.Context, w io.Writer) error {
		gw := W(w)
		gw.S(`<section class="`)
		gw.Class(Class("card"), ClassIf("card-featured", featured))
		gw.S(`"><h2>`)
		gw.Text(title)
		gw.S(`</h2>`)
		gw.Node(ctx, children)
		gw.S(`</section>`)
		return gw.Err()
	})
}

// box mirrors the walkthrough's generated Box (conditional attr + bool + spread).
func box(padded, disabled bool, attrs Attrs, children Node) Node {
	return Func(func(ctx context.Context, w io.Writer) error {
		gw := W(w)
		gw.S(`<div class="`)
		gw.Class(Class("box"), ClassIf("p-4", padded))
		gw.S(`"`)
		gw.BoolAttr("disabled", disabled)
		if !padded {
			gw.S(` data-tight`)
		}
		gw.Spread(ctx, attrs)
		gw.S(`>`)
		gw.Node(ctx, children)
		gw.S(`</div>`)
		return gw.Err()
	})
}

func render(t *testing.T, n Node) string {
	t.Helper()
	var b strings.Builder
	if err := n.Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestIntegrationCard(t *testing.T) {
	got := render(t, card("Hi & <Bye>", true, Raw(`<p>kid</p>`)))
	want := `<section class="card card-featured"><h2>Hi &amp; &lt;Bye&gt;</h2><p>kid</p></section>`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestIntegrationBox(t *testing.T) {
	got := render(t, box(false, true, Attrs{"id": "b1", "aria-hidden": true}, Raw("x")))
	// not padded -> data-tight + box class only; disabled bool; spread sorted (aria-hidden, id)
	want := `<div class="box" disabled data-tight aria-hidden id="b1">x</div>`
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
```

Add `"io"` to the imports of `gsx_test.go` (used in the `card`/`box` closures).

- [ ] **Step 2: Run to verify it passes**

Run: `go test . -run TestIntegration -v`
Expected: PASS. (If `TestIntegrationBox`'s `want` does not match, the bug is in the test's expectation, not the runtime — recompute the exact sorted-spread output and fix the literal; do NOT change runtime behavior to match a wrong expectation.)

- [ ] **Step 3: Full suite + vet + fmt**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all green; `gofmt -l .` prints nothing.

- [ ] **Step 4: Commit**

```bash
git add gsx_test.go
git commit -m "test(gsx): integration goldens (Card/Box compose to exact HTML)"
```

---

## Self-Review

**1. Spec coverage** (against `2026-06-19-gsx-runtime-design.md`):
- `Node`/`Func`/`Raw` → Task 1. ✓
- Streaming text/attr/URL escapers (XSS vectors) → Task 2. ✓
- `Writer` + error threading + `S`/`Text`/`AttrValue`/`URL`/`BoolAttr`/`Node` → Task 3. ✓
- Class/style compose + merge + `ClassMerger` hook → Task 4. ✓
- `Attrs` + methods (immutability, `Merge` class concat) + deterministic `Spread` → Task 5. ✓
- Integration golden (Card/Box → exact HTML) → Task 6. ✓
- Error threading short-circuit → Task 3 test. ✓
- Numeric helpers deferred (codegen formats → `Text`); real Tailwind merger out of scope; `Spread` ctx pass-through — all honored. ✓

**2. Placeholder scan:** No TBD/"handle edge cases"/"similar to Task N". Task 6 Step 2's note is a concrete recompute-the-literal instruction, not a placeholder.

**3. Type/signature consistency:**
- `writeHTML`/`writeURL`/`urlSanitize`/`blockedURL` defined Task 2, used Task 3. ✓
- `(*Writer).writeStr`/`AttrValue`/`BoolAttr` defined Task 3, used Tasks 4–5. ✓
- `classTokens`/`ClassMerger`/`Class` defined Task 4, used Task 5 (`Attrs.Class`). ✓
- `ClassPart` is `{s string; on bool}` consistently; `Class`/`ClassIf` constructors match. ✓
- `toStr`/`joinAttrStrings` defined and used within Task 5. ✓
- Public signatures match the Global-Constraints "Public surface" block exactly. ✓

**Note on the spec's `data:` carve-out:** the spec mentioned allowing safe `data:image` URLs; this plan blocks ALL non-allow-listed schemes including `data:` (simpler, safer, matches `html/template`). A `data:image` allowance can be added later if needed. This is the one deliberate simplification vs. the spec.

---

## Execution Notes for the Controller

- Tasks are sequential 1→6 by dependency: 1 (Node) and 2 (escapers) are independent; 3 (Writer) needs 1+2; 4 (class) needs 3; 5 (attrs) needs 3+4; 6 (integration) needs all.
- The package does not compile until Task 1 lands (it defines `package gsx`); that's why Task 1's failing test fails at compile, which is the expected red.
- Model guidance: every task is small and fully specified (complete code) → cheap model for implementation; the final whole-package review → most capable model. Reviewers scaled to the small diffs.
- Build on a branch `feat/gsx-runtime` off `main`; land via review when all six tasks are green.
