# `data:` image resource URLs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow `data:image/*` URLs in image-rendering resource sinks (`<img src>`, `<video poster>`, etc.) while keeping navigational and `<script src>` sinks blocked, via two authoring forms (a static-prefix literal with type-driven base64, and a `dataURL` std filter), plus make `std` the lowest-precedence filter base so built-ins stay overridable.

**Architecture:** Split URL classification into two tiers — `SinkImage` (allows `data:` narrowed to an image-MIME allow-list) and `SinkStrict` (today's behavior, no `data:`) — keyed on the element **tag + attribute** pair (gsx already knows the tag at codegen time). A new runtime `Writer.URLImage` sanitizer enforces the image tier; codegen picks `URLImage` vs `URL` per sink. Form A (`` src=`data:image/png;base64,@{bytes}` ``) assembles the whole value and routes it through `URLImage`, with `[]byte` holes after a `;base64,` marker base64-encoded; Form B (`{ bytes |> dataURL("image/png") }`) is an ordinary std filter whose output `URLImage` re-validates. No trusted runtime type is introduced; `gsx.RawURL` stays the escape hatch.

**Tech Stack:** Go (stdlib-only runtime + `std` package), `go/types` + `go/ast` codegen (`internal/codegen`), attribute classifier (`internal/attrclass`), txtar corpus (`internal/corpus`).

## Global Constraints

- **Runtime (root `gsx` package) is stdlib-only.** `escape.go`, `writer.go`, `node.go`, `std/` must import only the Go standard library. No `golang.org/x/...`.
- **Security escaping is a faithful, fail-closed port** — never an approximation. The image allow-list is an explicit MIME set; anything unrecognized returns the `blockedURL` sentinel (`about:invalid#gsx`).
- **"Assemble first, sanitize once."** URL values are sanitized as one whole assembled string, never per-hole (per-hole classification caused 5 confirmed XSS bypasses). Form A preserves this: it routes the whole assembled value through `URLImage`.
- **No trusted runtime type.** Do not add `gsx.SafeURL`/`gsx.DataImageURL`. `gsx.RawURL` (`node.go:39`) is the only opt-out.
- **Any knob/behavior that changes generated output folds into `computeKey`** (`gen/cachekey.go`) or the incremental cache serves stale output.
- **Every syntax/codegen change ships a txtar corpus case** pinning `input.gsx` + `generated.x.go.golden` + `render.golden`, per context. Regenerate with `go test ./internal/corpus -run TestCorpus -update`, then verify without `-update`. Never hand-edit `.x.go`/golden files.
- **Image-MIME allow-list (canonical, used verbatim in code):** raster = `image/png`, `image/jpeg`, `image/gif`, `image/webp`, `image/avif`; plus `image/svg+xml`. All are allowed on `SinkImage` (see Task 2 note on why svg is safe there). Match case-insensitively.
- **Do the work in a git worktree** (project convention; see `superpowers:using-git-worktrees`).

---

## File Structure

- `escape.go` (modify) — add `isImageDataURL`, `urlSanitizeImage`, `writeURLImage`.
- `writer.go` (modify) — add `Writer.URLImage`.
- `escape_test.go` (modify) — resource-sanitizer allow/deny matrix.
- `internal/attrclass/attrclass.go` (modify) — `SinkClass` type + `URLSink(tag, name)`; sink pairs.
- `internal/attrclass/attrclass_test.go` (modify) — sink classification.
- `std/std.go` (modify) — `DataURL([]byte, string) string`.
- `std/std_test.go` (modify) — `DataURL` unit test.
- `internal/codegen/codegen.go` (modify) — `dedupFilterPkgs` always folds std in first.
- `internal/codegen/emit.go` (modify) — thread `tag` into attr emitters; pick `URLImage` vs `URL`; Form A `[]byte` base64 encoding.
- `gen/*_test.go` (modify/add) — filter-override + cache-key invalidation test.
- `internal/corpus/testdata/cases/url/*.txtar` (create) — Form A/B corpus cases.
- Docs: `docs/guide/escaping.md`, `docs/guide/attributes.md`/`syntax.md`, `docs/guide/pipelines.md`, `docs/guide/config.md`, `docs/ROADMAP.md`.

---

### Task 1: Runtime image-resource sanitizer

**Files:**
- Modify: `escape.go` (add after `writeURL`, ~line 56)
- Modify: `writer.go` (add after `Writer.URL`, ~line 87)
- Test: `escape_test.go`

**Interfaces:**
- Produces: `func isImageDataURL(s string) bool`, `func urlSanitizeImage(s string) string`, `func writeURLImage(w io.Writer, s string) error`, `func (gw *Writer) URLImage(s string)`.
- Consumes: existing `blockedURL` const, `writeHTML`, `urlSanitize` logic shape.

- [ ] **Step 1: Write the failing test**

Add to `escape_test.go`:

```go
func TestURLSanitizeImage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// allowed: standard schemes and relative still pass through
		{"http", "http://example.com/a.png", "http://example.com/a.png"},
		{"relative", "/img/a.png", "/img/a.png"},
		// allowed: image data URLs (raster + svg)
		{"png", "data:image/png;base64,iVBORw0KGgo=", "data:image/png;base64,iVBORw0KGgo="},
		{"jpeg", "data:image/jpeg;base64,/9j/4AAQ==", "data:image/jpeg;base64,/9j/4AAQ=="},
		{"webp upper mime", "data:IMAGE/WEBP;base64,UklGRg==", "data:IMAGE/WEBP;base64,UklGRg=="},
		{"svg", "data:image/svg+xml;base64,PHN2Zz4=", "data:image/svg+xml;base64,PHN2Zz4="},
		// blocked: non-image data URLs
		{"html", "data:text/html;base64,PHNjcmlwdD4=", blockedURL},
		{"js", "data:application/javascript;base64,YWxlcnQ=", blockedURL},
		{"no mime", "data:;base64,AAAA", blockedURL},
		{"image no base64 marker", "data:image/png,rawbytes", blockedURL},
		// blocked: other dangerous schemes
		{"javascript", "javascript:alert(1)", blockedURL},
		{"vbscript", "vbscript:msgbox(1)", blockedURL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := urlSanitizeImage(c.in); got != c.want {
				t.Fatalf("urlSanitizeImage(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test . -run TestURLSanitizeImage -v`
Expected: FAIL — `undefined: urlSanitizeImage`.

- [ ] **Step 3: Write minimal implementation**

Add to `escape.go` after `writeURL` (line 56):

```go
// imageDataMIMEs is the allow-list of data: MIME types permitted in an image
// resource sink (SinkImage). Raster types are inert pixels; image/svg+xml is
// safe HERE because SinkImage is only <img>/<source>/poster/background, where
// browsers load SVG in restricted mode (no script, no external fetch). It is
// NOT permitted on iframe/object/embed/script sinks, which never reach this
// sanitizer (codegen routes them through urlSanitize). Keys are lowercase.
var imageDataMIMEs = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true,
	"image/webp": true, "image/avif": true, "image/svg+xml": true,
}

// isImageDataURL reports whether s is a data: URL whose MIME is in the image
// allow-list and which is base64-encoded (the ";base64," marker is required so
// the payload charset is constrained to [A-Za-z0-9+/=], which cannot carry a
// scheme break). It parses conservatively: data:<mime>[;param]*;base64,<payload>.
func isImageDataURL(s string) bool {
	const prefix = "data:"
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return false
	}
	rest := s[len(prefix):]
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return false
	}
	meta := rest[:comma] // e.g. "image/png;base64" or "image/svg+xml;base64"
	semi := strings.IndexByte(meta, ';')
	if semi < 0 {
		return false // no ";base64" marker
	}
	mime := strings.ToLower(meta[:semi])
	if !imageDataMIMEs[mime] {
		return false
	}
	// The remaining meta parameters must include base64 as the final token.
	params := meta[semi+1:]
	return strings.EqualFold(params, "base64") ||
		strings.HasSuffix(strings.ToLower(params), ";base64")
}

// urlSanitizeImage is urlSanitize for an image RESOURCE sink: it accepts the
// same relative/fragment/query and http/https/mailto/tel values, and ALSO
// accepts a data: URL whose MIME is in the image allow-list. Every other scheme
// (including non-image data:) yields blockedURL.
func urlSanitizeImage(s string) string {
	if before, _, ok := strings.Cut(s, ":"); ok {
		if !strings.ContainsAny(before, "/?#") {
			switch strings.ToLower(before) {
			case "http", "https", "mailto", "tel":
				// allowed
			case "data":
				if isImageDataURL(s) {
					return s
				}
				return blockedURL
			default:
				return blockedURL
			}
		}
	}
	return s
}

// writeURLImage streams an image-resource-sanitized, attribute-escaped URL to w.
func writeURLImage(w io.Writer, s string) error {
	return writeHTML(w, urlSanitizeImage(s))
}
```

Add to `writer.go` after `Writer.URL` (line 87):

```go
// URLImage writes s as an image-resource-sanitized, escaped URL attribute value.
// It permits data:image/* (raster + svg) in addition to the standard URL()
// allow-list; codegen emits it only for image-rendering sinks (<img src>,
// <source src>, <video poster>, background), never for navigational or script
// sinks.
func (gw *Writer) URLImage(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeURLImage(gw.w, s)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test . -run TestURLSanitizeImage -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add escape.go writer.go escape_test.go
git commit -m "feat(runtime): image-resource URL sanitizer (data:image allow-list)

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 2: Attribute sink classification (tag-aware)

**Files:**
- Modify: `internal/attrclass/attrclass.go` (add after `builtinURL`, ~line 164)
- Test: `internal/attrclass/attrclass_test.go`

**Interfaces:**
- Consumes: `Context` / `CtxURL` (existing).
- Produces:
  - `type SinkClass int` with `SinkStrict SinkClass = iota` then `SinkImage`.
  - `func URLSink(tag, name string) SinkClass` — returns `SinkImage` only for the image-render tag+attr pairs; `SinkStrict` otherwise. Caller must have already established `Context(name) == CtxURL`; `URLSink` does not re-check the context.

Note: This is a package-level function, not a `*Classifier` method — the image sink set is a fixed HTML fact independent of user rules. User `WithURLAttrs` rules add *navigational* (strict) URL attrs only; they never widen the image allow-list (that would be a footgun). Document this.

- [ ] **Step 1: Write the failing test**

Add to `internal/attrclass/attrclass_test.go`:

```go
func TestURLSink(t *testing.T) {
	image := []struct{ tag, name string }{
		{"img", "src"}, {"IMG", "SRC"},
		{"source", "src"},
		{"input", "src"},
		{"video", "poster"},
		{"body", "background"},
		{"table", "background"},
	}
	for _, c := range image {
		if got := URLSink(c.tag, c.name); got != SinkImage {
			t.Errorf("URLSink(%q,%q) = %v, want SinkImage", c.tag, c.name, got)
		}
	}
	strict := []struct{ tag, name string }{
		{"a", "href"},
		{"form", "action"},
		{"script", "src"},   // script src must stay strict
		{"iframe", "src"},   // iframe src must stay strict
		{"object", "data"},
		{"embed", "src"},
		{"video", "src"},    // media src, not an image sink
		{"img", "href"},     // href on img is not a resource sink
	}
	for _, c := range strict {
		if got := URLSink(c.tag, c.name); got != SinkStrict {
			t.Errorf("URLSink(%q,%q) = %v, want SinkStrict", c.tag, c.name, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/attrclass -run TestURLSink -v`
Expected: FAIL — `undefined: URLSink` / `undefined: SinkImage`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/attrclass/attrclass.go` after `builtinURL` (line 164):

```go
// SinkClass distinguishes URL attribute sinks that differ in what schemes are
// safe. It is only meaningful for attributes already classified CtxURL.
type SinkClass int

const (
	// SinkStrict is the default, navigational-strict sink: only the standard
	// http/https/mailto/tel allow-list; no data:. Covers href, action, script
	// src, iframe src, object data, media src, etc.
	SinkStrict SinkClass = iota
	// SinkImage is an image-rendering resource sink where data:image/* (raster +
	// svg) is safe: <img src>, <source src>, <input src>, <video poster>, and the
	// legacy background attribute. Browsers render these as inert images (SVG in
	// restricted mode), so no script executes.
	SinkImage
)

// URLSink classifies a tag+attribute pair (both matched case-insensitively) as
// an image-rendering resource sink or the strict default. The caller must have
// already established Context(name) == CtxURL; URLSink assumes it.
//
// The image set is intentionally narrow and tag-specific: `src` is an image
// sink on <img>/<source>/<input> but strict on <script>/<iframe>/<embed>/<video>
// (where a data: URL is a live document or executable). `poster` is image-only
// on <video>. `background` (legacy) is an image sink on any tag.
func URLSink(tag, name string) SinkClass {
	lt := strings.ToLower(tag)
	ln := strings.ToLower(name)
	switch ln {
	case "src":
		switch lt {
		case "img", "source", "input":
			return SinkImage
		}
	case "poster":
		if lt == "video" {
			return SinkImage
		}
	case "background":
		return SinkImage
	}
	return SinkStrict
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/attrclass -run TestURLSink -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/attrclass/attrclass.go internal/attrclass/attrclass_test.go
git commit -m "feat(attrclass): tag-aware image vs strict URL sink classification

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 3: `std.DataURL` filter

**Files:**
- Modify: `std/std.go` (add a new exported func; add `encoding/base64` import)
- Test: `std/std_test.go`

**Interfaces:**
- Produces: `func DataURL(subject []byte, mime string) string` — seed-first (`subject` first), stdlib-only. Registered automatically as the `dataURL` filter by whole-package harvest.

- [ ] **Step 1: Write the failing test**

Add to `std/std_test.go`:

```go
func TestDataURL(t *testing.T) {
	got := DataURL([]byte("PNGDATA"), "image/png")
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	if got != want {
		t.Fatalf("DataURL = %q, want %q", got, want)
	}
	if empty := DataURL(nil, "image/gif"); empty != "data:image/gif;base64," {
		t.Fatalf("DataURL(nil) = %q, want %q", empty, "data:image/gif;base64,")
	}
}
```

Ensure `std_test.go` imports `"encoding/base64"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./std -run TestDataURL -v`
Expected: FAIL — `undefined: DataURL`.

- [ ] **Step 3: Write minimal implementation**

Add `"encoding/base64"` to the `std/std.go` import block, then add:

```go
// DataURL assembles a base64 data: URL from raw bytes and a MIME type:
//
//	{ imageBytes |> dataURL("image/png") }  →  data:image/png;base64,<base64(imageBytes)>
//
// It is a plain assembly helper and grants NO privilege by itself: the value it
// returns is re-validated by the sink's URL sanitizer, so a non-image MIME (or an
// image data URL in a navigational sink) is still blocked. Use it in an image
// resource sink (<img src>, <video poster>, …).
func DataURL(subject []byte, mime string) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(subject)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./std -run TestDataURL -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add std/std.go std/std_test.go
git commit -m "feat(std): dataURL filter (base64 data: URL assembly)

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 4: `std` as lowest-precedence filter base

**Files:**
- Modify: `internal/codegen/codegen.go:20-34` (`dedupFilterPkgs`)
- Test: `internal/codegen/filters_test.go` (or the nearest existing filter-precedence test file — search `grep -rln "loadFilterTable\|last-wins\|WithFilter" internal/codegen/*_test.go`), plus a cache-key test in `gen/cachekey_test.go`.

**Interfaces:**
- Consumes: `stdImportPath` (`internal/codegen/filters.go:163`).
- Produces: `dedupFilterPkgs` now always returns a list whose FIRST element is `stdImportPath`, deduped, regardless of input — so `std` is the lowest-precedence (first-harvested, most-shadowed) package and user packages/aliases win on name collisions.

- [ ] **Step 1: Write the failing test**

Add to the filter-precedence test file:

```go
func TestDedupFilterPkgsAlwaysIncludesStd(t *testing.T) {
	// Empty -> just std.
	if got := dedupFilterPkgs(nil); len(got) != 1 || got[0] != stdImportPath {
		t.Fatalf("dedupFilterPkgs(nil) = %v, want [%s]", got, stdImportPath)
	}
	// Non-empty without std -> std is prepended as lowest precedence.
	got := dedupFilterPkgs([]string{"example.com/userfilters"})
	if len(got) != 2 || got[0] != stdImportPath || got[1] != "example.com/userfilters" {
		t.Fatalf("dedupFilterPkgs(user) = %v, want [std, user]", got)
	}
	// std listed explicitly (anywhere) -> not duplicated, still first.
	got = dedupFilterPkgs([]string{"example.com/userfilters", stdImportPath})
	if len(got) != 2 || got[0] != stdImportPath || got[1] != "example.com/userfilters" {
		t.Fatalf("dedupFilterPkgs(user,std) = %v, want [std, user]", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/codegen -run TestDedupFilterPkgsAlwaysIncludesStd -v`
Expected: FAIL — current code drops std for a non-empty list and does not reorder.

- [ ] **Step 3: Write minimal implementation**

Replace `dedupFilterPkgs` in `internal/codegen/codegen.go`:

```go
// dedupFilterPkgs returns filterPkgs with duplicate import paths removed and the
// built-in std package guaranteed present as the FIRST (lowest-precedence)
// entry, so callers always have std available and a user package or alias can
// shadow an individual std filter by name (last-wins) without dropping the rest
// of std. First-seen order is preserved among the remaining packages.
func dedupFilterPkgs(filterPkgs []string) []string {
	seen := map[string]bool{stdImportPath: true}
	out := []string{stdImportPath}
	for _, p := range filterPkgs {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/codegen -run TestDedupFilterPkgsAlwaysIncludesStd -v`
Expected: PASS.

- [ ] **Step 5: Write the cache-key invalidation test**

`computeKey` (`gen/cachekey.go:189`) already sorts+dedups `filterPkgs` via `dedupSorted` (line 214) into the key, but its input is the *config* list, which historically excluded std for non-std configs. Confirm the key now reflects std's presence. Add to `gen/cachekey_test.go`:

```go
func TestComputeKeyFoldsStdBase(t *testing.T) {
	// A config that lists only a user filter package must key identically whether
	// or not std is explicitly present, because std is always folded in.
	// (Full computeKey needs a populated graph; assert via dedupFilterPkgs, the
	// single source of the effective set, instead.)
	a := codegen.DedupFilterPkgs([]string{"example.com/userfilters"})            // exported shim if needed
	b := codegen.DedupFilterPkgs([]string{"github.com/gsxhq/gsx/std", "example.com/userfilters"})
	if !slices.Equal(a, b) {
		t.Fatalf("effective filter set differs: %v vs %v", a, b)
	}
}
```

If `dedupFilterPkgs` is unexported and not reachable from `gen`, drop this step's cross-package assertion and instead rely on the Task-4 Step-1 test in `internal/codegen`; note in the commit that the effective set is centralized in `dedupFilterPkgs`. Do NOT add an exported shim solely for the test unless one already exists.

- [ ] **Step 6: Run the full codegen + gen filter tests**

Run: `go test ./internal/codegen ./gen -run 'Filter|DedupFilterPkgs|ComputeKey' -count=1`
Expected: PASS. Watch for goldens that change because a project fixture used a non-std filter list — regenerate corpus in a later task if needed, but pure filter-table tests should not shift std-only output (std keeps its reserved `_gsxstd` alias and is first, so std-only generation is byte-identical).

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/codegen.go internal/codegen/*_test.go gen/cachekey_test.go
git commit -m "feat(codegen): std is the lowest-precedence filter base (overridable built-ins)

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 5: Thread element tag into attribute emitters; pick `URLImage` for image sinks (Form B)

This makes `{ bytes |> dataURL("image/png") }` and a plain `src={someDataImageString}` pass on an image sink, while `href=`/`<script src>` stay strict.

**Files:**
- Modify: `internal/codegen/emit.go`
- Test: corpus (Task 7) is the integration test; this task's unit check is a focused codegen test if one exists, else rely on corpus.

**Interfaces:**
- Consumes: `attrclass.URLSink` (Task 2), `Writer.URLImage` (Task 1).
- Produces: a threaded `tag string` parameter on `emitAttr`, `emitExprAttr`, `emitEmbeddedTextAttr`; a helper `urlWriterMethod(tag, name string, cls *attrclass.Classifier) string` returning `"URLImage"` or `"URL"`.

- [ ] **Step 1: Add the writer-method helper**

Add near `emitEmbeddedTextAttr` in `emit.go`:

```go
// urlWriterMethod returns the generated Writer method for a URL-context
// attribute: "URLImage" for an image resource sink (data:image/* allowed),
// "URL" otherwise. Callers must have established CtxURL for name.
func urlWriterMethod(tag, name string) string {
	if attrclass.URLSink(tag, name) == attrclass.SinkImage {
		return "URLImage"
	}
	return "URL"
}
```

- [ ] **Step 2: Thread `tag` through the attr emitters**

Add a `tag string` parameter (immediately after `cls *attrclass.Classifier`) to each of:
- `emitAttr` (`emit.go:2168`)
- `emitExprAttr` (`emit.go:2958`)
- `emitEmbeddedTextAttr` (`emit.go:2355`)

Update every call site to pass the enclosing element's tag:
- `genNode` element case (`emit.go:1410`): pass `t.Tag`.
- `emitManualSpreadElement` leaf loop (`emit.go:944`): pass `el.Tag`.
- The composable-style branch calls (`emit.go:793` `emitAttr`, `emit.go:817` `emitEmbeddedTextAttr`): pass the enclosing element tag in scope (`el.Tag`/`t.Tag`).
- `emitFallthroughAttrs` internal `emitAttr` calls (`emit.go:629,632`): pass the element tag `emitFallthroughAttrs` is emitting for — add a `tag string` parameter to `emitFallthroughAttrs` too and pass `el.Tag`/`t.Tag` from its callers (`emit.go:1077`, `emit.go:1420` region).
- The recursive `emitAttr` calls inside `emitAttr` itself (the conditional-attr unwrap at `emit.go:2241,2248`): forward the same `tag`.

Where no element tag applies (an attr being forwarded into a component's `Attrs` bag), pass `""` — `URLSink("", name)` returns `SinkStrict`, the safe default.

- [ ] **Step 3: Use the helper at the two `_gsxgw.URL(...)` emit sites in `emitEmbeddedTextAttr`**

Replace the two `_gsxgw.URL(%s)` fprintf calls (`emit.go:2393` piped branch, `emit.go:2402` static-URL branch) with the method chosen by `urlWriterMethod(tag, a.Name)`:

```go
// piped branch (was line ~2393):
fmt.Fprintf(b, "\t\t_gsxgw.%s(%s)\n", urlWriterMethod(tag, a.Name), strExpr)
// static-URL branch (was line ~2402):
fmt.Fprintf(b, "\t\t_gsxgw.%s(%s)\n", urlWriterMethod(tag, a.Name), concat)
```

- [ ] **Step 4: Use the helper in `emitExprAttr`**

Replace the `_gsxgw.URL(%s)` fprintf (`emit.go:3010`) with:

```go
fmt.Fprintf(b, "\t\t_gsxgw.%s(%s)\n", urlWriterMethod(tag, a.Name), urlStringExpr(expr, t))
```

(The `isRawURL(t)` guard at `emit.go:3006` is unchanged — `gsx.RawURL` still bypasses to `_gsxgw.RawURL`.)

- [ ] **Step 5: Build**

Run: `go build ./... && go vet ./internal/codegen`
Expected: compiles; no unused-parameter or signature-mismatch errors.

- [ ] **Step 6: Regenerate + verify existing corpus is byte-identical**

Run: `go test ./internal/corpus -run TestCorpus -update -count=1 && go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS. Existing goldens must NOT change — every current URL attr is `SinkStrict` (href, plain src on non-image tags in the fixtures), so `urlWriterMethod` returns `"URL"` and output is unchanged. If any existing golden flips to `URLImage`, verify the fixture element is genuinely an image sink (e.g. `<img src>`); a flip there is correct, otherwise investigate.

- [ ] **Step 7: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "feat(codegen): route image-sink URL attrs through Writer.URLImage

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 6: Form A — `[]byte` holes after a `;base64,` marker base64-encode

Form A's whole-value routing through `URLImage` already works after Task 5 (a `data:image/png;base64,...` literal on `<img src>` assembles and passes `URLImage`). This task adds the type-driven encoding: a `[]byte` hole immediately following a `;base64,` marker is base64-encoded instead of `string(x)`; a `string` hole passes through unchanged.

**Files:**
- Modify: `internal/codegen/emit.go` — `holeStringExpr` (`emit.go:2486`) or, more precisely, the URL-context assembly path in `embeddedTextValueExpr`/`embeddedValueExpr`. Because the encoding decision needs the preceding static segment, implement it in the segment-assembly loop, not in the type-blind `holeStringExpr`.

**Interfaces:**
- Consumes: `resolved` types (to detect `[]byte`), the assembled segment list.
- Produces: no new exported symbol; a local predicate `precedingIsBase64Marker(segs []ast.Markup, i int) bool` and a base64 encode branch emitting `base64.StdEncoding.EncodeToString(<expr>)` with a `"encoding/base64"` import registered.

- [ ] **Step 1: Write the failing corpus case (drives the behavior)**

Create `internal/corpus/testdata/cases/url/data_image_bytes.txtar` with an `input.gsx` component:

```gsx
package pages

func Avatar(png []byte) {
	<img src=`data:image/png;base64,@{png}` alt="avatar" />
}
```

(Leave `generated.x.go.golden` and `render.golden` absent for now; `-update` writes them, but first assert the generated code base64-encodes.)

- [ ] **Step 2: Run to see current (wrong) output**

Run: `go test ./internal/corpus -run 'TestCorpus/url/data_image_bytes' -update -count=1`
Then inspect the generated golden:
Run: `grep -n "png\|base64\|URLImage\|string(" internal/corpus/testdata/cases/url/data_image_bytes.txtar`
Expected BEFORE the fix: the `png` hole lowers to `string(png)` (raw bytes), assembled into the `_gsxgw.URLImage(...)` arg — a data URL with raw (non-base64) bytes, which is wrong. This confirms the gap.

- [ ] **Step 3: Implement the encode branch**

In the URL-context segment assembly (the path `embeddedTextValueExpr` → `embeddedValueExpr` builds for a `CtxURL` `EmbeddedText` attr), when a hole segment's resolved type is `[]byte` (`isByteSlice(t)`) AND its immediately-preceding `*ast.Text` segment's value ends with `;base64,` (case-insensitive, trailing), emit `base64.StdEncoding.EncodeToString(<holeExpr>)` and register `imports["encoding/base64"] = true`. Otherwise keep the existing `holeStringExpr` routing.

Sketch (adapt to the actual assembly loop; the key predicate and emit):

```go
// within the CtxURL assembly, per hole segment at index i:
if isByteSlice(resolved[hole]) && precedingIsBase64Marker(segs, i) {
	imports["encoding/base64"] = true
	part = fmt.Sprintf("base64.StdEncoding.EncodeToString(%s)", holeExpr(hole))
} else {
	part, ok = holeStringExpr(...)  // existing routing
}
```

with:

```go
// precedingIsBase64Marker reports whether the segment before index i is static
// text ending in ";base64," (case-insensitive) — the marker that makes a []byte
// hole a base64 payload rather than raw bytes.
func precedingIsBase64Marker(segs []ast.Markup, i int) bool {
	if i == 0 {
		return false
	}
	txt, ok := segs[i-1].(*ast.Text)
	if !ok {
		return false
	}
	return strings.HasSuffix(strings.ToLower(txt.Value), ";base64,")
}
```

If a `isByteSlice(types.Type) bool` helper does not already exist in `internal/codegen`, add one (`grep -n "isByteSlice\|\[\]byte" internal/codegen/*.go` first; reuse if present).

Note on scope: this encode branch applies ONLY on the CtxURL image-sink assembly path. A `[]byte` hole in a URL attr WITHOUT the `;base64,` marker keeps existing behavior (`string(x)`); document that auto-encode requires the marker. A `string` hole always passes through unchanged (already-encoded text).

- [ ] **Step 4: Regenerate and verify the golden now base64-encodes**

Run: `go test ./internal/corpus -run 'TestCorpus/url/data_image_bytes' -update -count=1`
Run: `grep -n "base64.StdEncoding.EncodeToString(png)" internal/corpus/testdata/cases/url/data_image_bytes.txtar`
Expected: present. And the `render.golden` shows `src="data:image/png;base64,<valid base64>"`.

- [ ] **Step 5: Verify without `-update`**

Run: `go test ./internal/corpus -run 'TestCorpus/url/data_image_bytes' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "feat(codegen): base64-encode []byte holes after a ;base64, marker (Form A)

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 7: Corpus coverage (per-context) + Form-A-on-strict-sink error

**Files:**
- Create: `internal/corpus/testdata/cases/url/*.txtar` (cases below)
- Modify: `internal/codegen/emit.go` — add the codegen error when a data-image literal lands on a strict sink (only if not already covered by whole-value sanitize; see Step 4).

**Interfaces:** none new; exercises Tasks 1–6 end-to-end.

- [ ] **Step 1: Add the passing corpus cases**

Create these `.txtar` cases (each `input.gsx` + goldens via `-update`):

- `url/data_image_string.txtar` — `string` hole passthrough:

```gsx
package pages

func Avatar(b64 string) {
	<img src=`data:image/png;base64,@{b64}` alt="a" />
}
```

- `url/data_image_svg.txtar` — svg on `<img>` (allowed):

```gsx
package pages

func Icon(svg []byte) {
	<img src=`data:image/svg+xml;base64,@{svg}` alt="i" />
}
```

- `url/data_url_filter.txtar` — Form B on `<img src>`:

```gsx
package pages

func Avatar(png []byte) {
	<img src={png |> dataURL("image/png")} alt="a" />
}
```

- `url/data_url_filter_blocked_mime.txtar` — Form B with non-image MIME → sanitized to `about:invalid#gsx` at render:

```gsx
package pages

func Danger(bytes []byte) {
	<img src={bytes |> dataURL("text/html")} alt="x" />
}
```

- `url/data_image_href_strict.txtar` — a `data:image/...` literal on `<a href>` (strict sink) → rendered blocked (whole value goes through `URL`, not `URLImage`, so `data:` is rejected):

```gsx
package pages

func Bad(png []byte) {
	<a href=`data:image/png;base64,@{png}`>x</a>
}
```

For each: `go test ./internal/corpus -run 'TestCorpus/url/<name>' -update -count=1`, inspect the `render.golden` matches the expected allow/deny, then verify without `-update`.

- [ ] **Step 2: Verify the deny cases actually deny**

Run: `grep -rn "about:invalid#gsx" internal/corpus/testdata/cases/url/data_url_filter_blocked_mime.txtar internal/corpus/testdata/cases/url/data_image_href_strict.txtar`
Expected: both `render.golden` sections contain `about:invalid#gsx` for the offending attribute. If `data_image_href_strict` does NOT block (e.g. because `[]byte` after `;base64,` was still base64-encoded and then `URL` saw `data:image/png;base64,<b64>` and rejected it — correct), confirm the rendered `href` is `about:invalid#gsx`.

- [ ] **Step 3: Decide whether a hard codegen error is warranted for the strict-sink case**

The spec proposed a *codegen error* for a data-image literal on an excluded sink, pointing at `gsx.RawURL`. The runtime already fails safe (blocks to `about:invalid#gsx`). Choose the stricter, more helpful behavior: if the static prefix of a URL-attr embedded literal is a `data:` scheme (statically visible) AND `URLSink(tag, name) == SinkStrict`, emit a codegen diagnostic instead of silently rendering a blocked URL. Rationale: a `data:image` literal on `<a href>` is always author error; a compile-time message beats a mystery `about:invalid#gsx` at runtime.

Implement in `emitEmbeddedTextAttr`'s CtxURL branch: before assembling, if the first segment is `*ast.Text` whose value (lowercased, trimmed) starts with `data:` and `urlWriterMethod(tag, a.Name) == "URL"`, call:

```go
bag.Errorf(a.Pos(), a.End(), "data-url-strict-sink",
	"data: URL literal in attribute %q on <%s> is a navigational/script sink where data: is blocked; use an image sink (<img src>, <video poster>, background) or gsx.RawURL if you have validated it",
	a.Name, tag)
return false
```

- [ ] **Step 4: Convert the strict-sink case to an error case**

Change `url/data_image_href_strict.txtar` to expect a codegen diagnostic (corpus error cases pin the diagnostic output rather than goldens — follow the pattern in an existing error case; find one via `grep -rln "error\[" internal/corpus/testdata/cases | head`). Regenerate and verify the diagnostic text matches.

- [ ] **Step 5: Full corpus run**

Run: `go test ./internal/corpus -run TestCorpus -count=1`
Expected: PASS, including the coverage manifest (`coverage.golden`) which `-update` rewrote.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/emit.go internal/corpus/testdata
git commit -m "test(corpus): data:image resource-URL cases (Form A/B, svg, strict-sink error)

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 8: Filter-override end-to-end test (user shadows `dataURL`)

**Files:**
- Test: add to the existing multi-package filter test file (`grep -rln "WithFilter\|multi.*filter\|last-wins" gen/*_test.go internal/codegen/*_test.go`).

**Interfaces:** exercises Task 3 + Task 4 together.

- [ ] **Step 1: Write the test**

Add a test that registers a user `dataURL` alias (`gen.WithFilter("dataURL", userDataURL)` or a fixture package listed after std) and asserts the generated call resolves to the USER function, not `_gsxstd.DataURL`, while another std filter (e.g. `upper`) still resolves to std. Follow the shape of the nearest existing multi-package/last-wins test; assert on the harvested `filterTable` entry's package alias or on generated output containing the user alias.

- [ ] **Step 2: Run to verify it fails, then that it passes after wiring**

Run: `go test ./gen ./internal/codegen -run 'Filter.*Override|Override.*Filter' -v -count=1`
Expected: PASS (the mechanism already exists — this test pins that `dataURL` participates in it and std-as-base doesn't break override).

- [ ] **Step 3: Commit**

```bash
git add gen/*_test.go internal/codegen/*_test.go
git commit -m "test: user filter overrides built-in dataURL (std-as-base precedence)

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 9: Documentation + ROADMAP

**Files:**
- Modify: `docs/guide/escaping.md` — the navigational/resource (strict vs image) split, the image-MIME allow-list, why gsx diverges from `html/template` here, the two-tier collapse of the design matrix, `gsx.RawURL` as the escape hatch for anything refused (exotic MIME, svg in `<object>`, etc.).
- Modify: `docs/guide/attributes.md` (and/or `syntax.md`) — Form A `` src=`data:image/png;base64,@{bytes}` ``, the `[]byte`→base64 vs `string`→passthrough rule (auto-encode requires the `;base64,` marker), the strict-sink error.
- Modify: `docs/guide/pipelines.md` — the `dataURL(mime)` std filter.
- Modify: `docs/guide/config.md` — `std` is the lowest-precedence filter base; how to override a built-in filter (`[filters]` alias or `filterPackages` last-wins) without dropping the rest of std.
- Modify: `docs/ROADMAP.md` — check `data:image` resource-URL allowance; record that the navigational/resource split shipped (two-tier: image vs strict; `srcset` / CSS `background:url()` remain follow-ups).

**Interfaces:** none.

- [ ] **Step 1: Write the escaping.md section**

Add a "Resource vs navigational URL sinks" subsection with the sink table (image sinks: `<img src>`, `<source src>`, `<input src>`, `<video poster>`, `background`; everything else strict), the allowed MIME list, and the `RawURL` note. If literal `{{ }}` appears in prose, wrap in `::: v-pre` (VitePress).

- [ ] **Step 2: Write the attributes/pipelines/config sections** (content per file list above).

- [ ] **Step 3: Update ROADMAP** — flip `- [ ] data:image resource-URL allowance` to `[x]` with a one-line summary; note the two-tier split and the deferred `srcset`/CSS-background follow-ups.

- [ ] **Step 4: Commit**

```bash
git add docs
git commit -m "docs: data:image resource URLs, dataURL filter, std-as-base override

Claude-Session: https://claude.ai/code/session_01GCvAa6qCFw2pZRdgwnAyxH"
```

---

### Task 10: Full CI gate

**Files:** none (verification only).

- [ ] **Step 1: Run the full local CI**

Run: `make ci`
Expected: PASS — build/vet/test both modules, examples drift, `gofmt` + `gsx fmt`, corpus. If gofmt drift appears, confirm Go is pinned to `GO_VERSION` in `ci.yml`.

- [ ] **Step 2: Run the linter**

Run: `make lint`
Expected: PASS.

- [ ] **Step 3: Sibling-repo note**

No new grammar token is introduced (Form A reuses the existing backtick-with-`@{}` attribute literal; Form B is an ordinary pipeline). Confirm no `tree-sitter-gsx`/`vscode-gsx`/CodeMirror change is required for this feature; if the backtick-attr-literal highlight backlog is being worked, this rides on it. Record in the commit/PR description that siblings need no change.

- [ ] **Step 4: Final commit / open PR**

Only if the user asks to push/PR. Otherwise stop here with a green `make ci`.

---

## Self-Review

**Spec coverage:**
- Motivation / `<img src>` dynamic payload → Tasks 5, 6 (Form A) + 3, 5 (Form B). ✓
- Tag-aware navigational/resource split → Task 2 (`URLSink`) + Task 5 (threading). ✓
- Image-MIME allow-list incl. svg-on-image-sinks → Task 1 (`imageDataMIMEs`, `isImageDataURL`). ✓
- Raster-safe-everywhere-but-script / svg-image-sinks-only → collapsed to two tiers (Task 2 note); `<script src>` = strict (Task 2 test). ✓ (Divergence from spec's 3-tier matrix documented in plan header + Task 2 + Task 9.)
- Both re-validated by resource sanitizer, no trusted type → Form A routes whole value through `URLImage` (Task 5/6); Form B output re-sanitized (Task 3 doc + Task 7 blocked-mime case). ✓
- `[]byte`→base64 / `string`→passthrough → Task 6. ✓
- `dataURL` filter, Go-std only → Task 3. ✓
- std lowest-precedence base + overridable + computeKey fold → Task 4 + Task 8. ✓
- `gsx.RawURL` escape hatch unchanged → asserted in Task 5 Step 4 (isRawURL guard untouched); documented Task 9. ✓
- Corpus per context + fuzz → Task 6, 7. (NOTE: the spec lists fuzz-target extensions; add them as a follow-up step in Task 7 if `internal/codegen/url_fuzz_test.go` is straightforward to extend — see Placeholder note below.)
- Docs + siblings → Task 9, 10. ✓
- Open questions (srcset, CSS background) → explicitly deferred, recorded in Task 9 ROADMAP update. ✓

**Placeholder scan:** One deliberate under-specification — the fuzz-target extension (`FuzzURLWholeLiteralPipeSchemeSafety`) is mentioned in the spec but not given verbatim test code here because it depends on the existing fuzz harness shape. Resolve during Task 7 by extending the existing target to assert no `SinkImage` input yields an executable navigational scheme and no `image/svg+xml` reaches a strict sink; if the harness is non-trivial, file it as a fast-follow. This is the only intentionally open item.

**Type consistency:** `SinkClass`/`SinkImage`/`SinkStrict` (Task 2) used consistently in `URLSink` (Task 2) and `urlWriterMethod` (Task 5). `URLImage` method name consistent across Task 1 (definition) and Task 5 (emit). `isImageDataURL`/`urlSanitizeImage`/`writeURLImage` consistent Task 1. `dataURL`/`DataURL` — Go func `DataURL` (Task 3), template name `dataURL` (harvest lowercases first rune), consistent. `dedupFilterPkgs` signature unchanged (Task 4).
