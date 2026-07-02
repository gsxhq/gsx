# URL Hardening Refresh Base Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sanitize dynamic URLs embedded in `<meta http-equiv="refresh" content={...}>`, prove `<base href={...}>` already uses URL sanitization, and leave `data:image` blocked until URL contexts are split.

**Architecture:** Add one focused runtime parser/sanitizer for meta refresh `content`, expose it through `Writer.RefreshContent`, and route only statically-known `<meta http-equiv="refresh" content={expr}>` through that writer method. Keep the existing URL scheme policy centralized in `urlSanitize`; do not add a general tag/attribute state machine.

**Tech Stack:** Go runtime package, `internal/codegen`, txtar corpus tests, root package unit tests, `gofmt`, `go test`, `make check`.

---

## File Structure

- Modify `escape.go`: add ASCII whitespace helpers and `refreshContentSanitize`.
- Modify `writer.go`: add `(*Writer).RefreshContent`.
- Modify `escape_test.go`: add table tests for refresh-content sanitization and escaping through the writer.
- Modify `internal/codegen/emit.go`: thread element tag/attrs context into expression attribute emission and detect static `meta http-equiv="refresh"`.
- Add `internal/corpus/testdata/cases/security/meta_refresh_url.txtar`: generated/render coverage for unsafe and safe dynamic refresh destinations.
- Add `internal/corpus/testdata/cases/security/base_href_url.txtar`: generated/render coverage proving `<base href={...}>` routes through `_gsxgw.URL`.
- Modify `docs/ROADMAP.md`: update URL hardening and `data:image` status after implementation.

---

### Task 1: Runtime Refresh Sanitizer

**Files:**
- Modify: `escape.go`
- Modify: `writer.go`
- Modify: `escape_test.go`

- [ ] **Step 1: Add failing runtime tests**

Append these tests to `escape_test.go` after `TestWriteURLEscapesAfterSanitize`:

```go
func TestRefreshContentSanitize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"reload integer", "0", "0"},
		{"reload decimal", "5.25", "5.25"},
		{"safe relative", "0;url=/next", "0;url=/next"},
		{"safe absolute", "0; URL=https://example.com/a?b=c", "0; URL=https://example.com/a?b=c"},
		{"safe quoted query", "0, url='?q=a:b'", "0, url='?q=a:b'"},
		{"unsafe unquoted", "0;url=javascript:alert(1)", "0;url=" + blockedURL},
		{"unsafe mixed case", "0;URL=JavaScript:alert(1)", "0;URL=" + blockedURL},
		{"unsafe after url whitespace", "0; url= \tjavascript:alert(1)", "0; url= \t" + blockedURL},
		{"unsafe embedded tab", "0;url=java\tscript:alert(1)", "0;url=" + blockedURL},
		{"unsafe double quoted", `0;url="javascript:alert(1)"`, `0;url="` + blockedURL + `"`},
		{"unsafe single quoted trailing", "0; url='javascript:alert(1)'; ignored", "0; url='" + blockedURL + "'; ignored"},
		{"no url assignment", "0; refresh", "0; refresh"},
		{"non refresh grammar", "url=javascript:alert(1)", "url=javascript:alert(1)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := refreshContentSanitize(tt.in); got != tt.want {
				t.Fatalf("refreshContentSanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWriterRefreshContentEscapesAfterSanitize(t *testing.T) {
	var b strings.Builder
	gw := W(&b)
	gw.RefreshContent(`0;url=/x?a="b"&c`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if b.String() != `0;url=/x?a=&#34;b&#34;&amp;c` {
		t.Fatalf("safe refresh content = %q", b.String())
	}

	b.Reset()
	gw = W(&b)
	gw.RefreshContent(`0;url="javascript:alert(1)"`)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if b.String() != `0;url=&#34;`+blockedURL+`&#34;` {
		t.Fatalf("blocked refresh content = %q, want quoted blocked URL", b.String())
	}
}
```

- [ ] **Step 2: Run runtime tests and verify they fail**

Run:

```bash
go test . -run 'TestRefreshContentSanitize|TestWriterRefreshContentEscapesAfterSanitize'
```

Expected: FAIL because `refreshContentSanitize` and `RefreshContent` are undefined.

- [ ] **Step 3: Implement `refreshContentSanitize`**

Add this code in `escape.go` after `writeURL`:

```go
func isASCIIWhitespaceByte(c byte) bool {
	switch c {
	case '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

func skipASCIIWhitespace(s string, i int) int {
	for i < len(s) && isASCIIWhitespaceByte(s[i]) {
		i++
	}
	return i
}

func refreshContentSanitize(s string) string {
	i := skipASCIIWhitespace(s, 0)
	startDigits := i
	for i < len(s) && '0' <= s[i] && s[i] <= '9' {
		i++
	}
	if i == startDigits {
		if i >= len(s) || s[i] != '.' {
			return s
		}
	} else if i < len(s) && s[i] == '.' {
		i++
	}
	for i < len(s) && (('0' <= s[i] && s[i] <= '9') || s[i] == '.') {
		i++
	}
	if i >= len(s) {
		return s
	}
	if s[i] != ';' && s[i] != ',' && !isASCIIWhitespaceByte(s[i]) {
		return s
	}
	i = skipASCIIWhitespace(s, i)
	if i < len(s) && (s[i] == ';' || s[i] == ',') {
		i++
	}
	i = skipASCIIWhitespace(s, i)
	if i >= len(s) {
		return s
	}

	if i+3 <= len(s) && strings.EqualFold(s[i:i+3], "url") {
		j := skipASCIIWhitespace(s, i+3)
		if j < len(s) && s[j] == '=' {
			i = skipASCIIWhitespace(s, j+1)
		}
	}

	if i >= len(s) {
		return s
	}
	if s[i] == '\'' || s[i] == '"' {
		quote := s[i]
		urlStart := i + 1
		urlEnd := urlStart
		for urlEnd < len(s) && s[urlEnd] != quote {
			urlEnd++
		}
		return s[:urlStart] + urlSanitize(s[urlStart:urlEnd]) + s[urlEnd:]
	}
	return s[:i] + urlSanitize(s[i:])
}
```

- [ ] **Step 4: Add writer method**

Add this method in `writer.go` immediately after `URL`:

```go
// RefreshContent writes a meta refresh content value with any embedded redirect
// URL sanitized, then HTML-escapes the complete attribute value.
func (gw *Writer) RefreshContent(s string) {
	if gw.err != nil {
		return
	}
	gw.err = writeHTML(gw.w, refreshContentSanitize(s))
}
```

- [ ] **Step 5: Format and rerun runtime tests**

Run:

```bash
gofmt -w escape.go writer.go escape_test.go
go test . -run 'TestRefreshContentSanitize|TestWriterRefreshContentEscapesAfterSanitize'
```

Expected: PASS.

- [ ] **Step 6: Commit runtime sanitizer**

Run:

```bash
git add escape.go writer.go escape_test.go
git commit -m "feat: sanitize meta refresh content URLs"
```

---

### Task 2: Codegen Routing for Static Meta Refresh

**Files:**
- Modify: `internal/codegen/emit.go`
- Add: `internal/corpus/testdata/cases/security/meta_refresh_url.txtar`

- [ ] **Step 1: Add failing corpus case**

Create `internal/corpus/testdata/cases/security/meta_refresh_url.txtar`:

```txtar
-- input.gsx --
package views

component Refresh(to string) {
	<meta http-equiv="refresh" content={"0;url=" + to}/>
}

-- invoke --
Refresh("javascript:alert(1)")

-- render.golden --
<meta http-equiv="refresh" content="0;url=about:invalid#gsx">
```

- [ ] **Step 2: Run corpus update and verify generated output is wrong**

Run:

```bash
go test ./internal/corpus -run TestCorpus/security/meta_refresh_url -update
```

Expected: FAIL or produces a generated golden using `_gsxgw.AttrValue(...)` instead of `_gsxgw.RefreshContent(...)`. Do not keep an incorrect generated golden as the final state.

- [ ] **Step 3: Thread parent element context through attr emission**

In `internal/codegen/emit.go`, change `emitFallthroughAttrs`, `emitAttr`, and `emitExprAttr` signatures to include the parent tag and full parent attr list:

```go
func emitFallthroughAttrs(b *bytes.Buffer, tag string, attrs []ast.Attr, splitIdx int, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr, bagExpr string) bool {
```

```go
func emitAttr(b *bytes.Buffer, tag string, attrs []ast.Attr, a ast.Attr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag, mergeExpr string) bool {
```

```go
func emitExprAttr(b *bytes.Buffer, tag string, attrs []ast.Attr, a *ast.ExprAttr, resolved map[ast.Node]types.Type, table filterTable, imports map[string]bool, interpTemp *int, cls *attrclass.Classifier, bag *diag.Bag) bool {
```

Update these direct callers:

```go
if !emitFallthroughAttrs(b, el.Tag, el.Attrs, len(el.Attrs), resolved, table, imports, interpTemp, cls, bag, mergeExpr, "_gsxp.Attrs") {
	return false
}
```

```go
if !emitFallthroughAttrs(b, el.Tag, el.Attrs, splitIdx, resolved, table, imports, interpTemp, cls, bag, mergeExpr, "attrs") {
	return false
}
```

```go
if !emitAttr(b, t.Tag, t.Attrs, a, resolved, table, imports, interpTemp, cls, bag, mergeExpr) {
	return false
}
```

Update every call from `emitFallthroughAttrs` and every recursive call inside `emitAttr` to pass the same `tag` and `attrs` values:

```go
return emitAttr(b, tag, attrs, a, resolved, table, imports, interpTemp, cls, bag, mergeExpr)
```

```go
if !emitAttr(b, tag, attrs, inner, resolved, table, imports, interpTemp, cls, bag, mergeExpr) {
	return false
}
```

Update the `*ast.ExprAttr` branch:

```go
case *ast.ExprAttr:
	return emitExprAttr(b, tag, attrs, t, resolved, table, imports, interpTemp, cls, bag)
```

Do not change component-prop lowering or spread-bag behavior.

- [ ] **Step 4: Add static refresh detection helper**

Add this helper near `emitExprAttr` in `internal/codegen/emit.go`:

```go
func isStaticMetaRefreshAttr(tag string, attrs []ast.Attr) bool {
	if !strings.EqualFold(tag, "meta") {
		return false
	}
	for _, a := range attrs {
		s, ok := a.(*ast.StaticAttr)
		if !ok {
			continue
		}
		if strings.EqualFold(s.Name, "http-equiv") && strings.EqualFold(strings.TrimSpace(s.Value), "refresh") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Route `content` through `RefreshContent`**

Inside `emitExprAttr`, after the bool-attr branch and before URL-context handling, add:

```go
	isMetaRefreshContent := strings.EqualFold(a.Name, "content") && isStaticMetaRefreshAttr(tag, attrs)

	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(" "+a.Name+`="`))
	if isMetaRefreshContent {
		fmt.Fprintf(b, "\t\t_gsxgw.RefreshContent(%s)\n", urlStringExpr(expr, t))
	} else if cls.Context(a.Name) == attrclass.CtxURL && !isRawURL(t) {
		// URL context: value must be string-like; sanitize + escape. A gsx.RawURL
		// value (isRawURL) is the author's vouch — fall through to gw.AttrValue,
		// which entity-escapes but skips the scheme allow-list.
		fmt.Fprintf(b, "\t\t_gsxgw.URL(%s)\n", urlStringExpr(expr, t))
	} else {
		if !emitAttrValue(b, expr, t, imports, a, bag) {
			return false
		}
	}
	fmt.Fprintf(b, "\t\t_gsxgw.S(%s)\n", strconv.Quote(`"`))
	return true
```

Keep the existing tuple unwrap, pipeline lowering, type resolution, and bool handling unchanged.

- [ ] **Step 6: Format and update corpus goldens**

Run:

```bash
gofmt -w internal/codegen/emit.go
go test ./internal/corpus -run TestCorpus/security/meta_refresh_url -update
```

Expected: PASS and the generated golden includes:

```go
_gsxgw.RefreshContent("0;url=" + to)
```

- [ ] **Step 7: Verify the new corpus case without update**

Run:

```bash
go test ./internal/corpus -run TestCorpus/security/meta_refresh_url
```

Expected: PASS.

- [ ] **Step 8: Commit codegen routing**

Run:

```bash
git add internal/codegen/emit.go internal/corpus/testdata/cases/security/meta_refresh_url.txtar
git commit -m "feat: route meta refresh content through URL sanitizer"
```

---

### Task 3: Base Href Proof and URL Fuzz Seeds

**Files:**
- Add: `internal/corpus/testdata/cases/security/base_href_url.txtar`
- Modify: `escape_diff_test.go`

- [ ] **Step 1: Add base href corpus case**

Create `internal/corpus/testdata/cases/security/base_href_url.txtar`:

```txtar
-- input.gsx --
package views

component Base(u string) {
	<base href={u}/>
}

-- invoke --
Base("javascript:alert(1)")

-- render.golden --
<base href="about:invalid#gsx">
```

- [ ] **Step 2: Update corpus goldens for base href**

Run:

```bash
go test ./internal/corpus -run TestCorpus/security/base_href_url -update
```

Expected: PASS and the generated golden contains `_gsxgw.URL(u)`.

- [ ] **Step 3: Extend differential URL corpus seeds**

In `escape_diff_test.go`, add these strings to the URL-scheme section of `diffCorpus()`:

```go
		"java\tscript:alert(1)", "java\nscript:alert(1)", "java\rscript:alert(1)",
		"\tjavascript:alert(1)", "\njavascript:alert(1)", " javascript:alert(1)",
		"0;url=javascript:alert(1)", "0; url= java\tscript:alert(1)",
		"0;url='javascript:alert(1)'", "0, URL=\"JavaScript:alert(1)\"",
```

These refresh strings are not direct URL inputs, so the URL differential check will treat some as relative strings. That is acceptable; direct refresh sanitization is covered by `TestRefreshContentSanitize`.

- [ ] **Step 4: Verify corpus and runtime tests**

Run:

```bash
go test . -run 'TestURLSanitize|TestRefreshContentSanitize|TestWriterRefreshContentEscapesAfterSanitize|TestEscaperMatchesStdlib'
go test ./internal/corpus -run 'TestCorpus/security/(base_href_url|meta_refresh_url)'
```

Expected: PASS.

- [ ] **Step 5: Commit base href proof**

Run:

```bash
git add escape_diff_test.go internal/corpus/testdata/cases/security/base_href_url.txtar
git commit -m "test: cover base href URL sanitization"
```

---

### Task 4: Roadmap and Full Verification

**Files:**
- Modify: `docs/ROADMAP.md`

- [ ] **Step 1: Update roadmap status**

In `docs/ROADMAP.md`, update the security item:

```markdown
4. [x] **Harden `urlSanitize` + complete URL-attr table** — control-char /
   whitespace scheme evasion maps to the sentinel (adversarial-probed); the
   `urlAttrs` table covers `href`/`src`/`action`/`formaction`/`poster`/`cite`/`ping`/
   `data`/`background`/`manifest`/`xlink:href`/`hx-*`; dynamic
   `<meta http-equiv="refresh" content={...}>` sanitizes its embedded redirect
   URL; `<base href={...}>` is explicitly covered by the normal `href` URL path.
```

In the tracked debts `Codegen niceties` line, change the `data:image` note to make the dependency explicit:

```markdown
- [ ] **Codegen niceties** — [x] coalesce adjacent `gw.S` static writes;
  [ ] `//line` trailing-state reset; [ ] `data:image` resource-URL allowance
  after navigational/resource URL contexts are split.
```

- [ ] **Step 2: Run targeted tests**

Run:

```bash
go test .
go test ./internal/codegen
go test ./internal/corpus -run 'TestCorpus/security/(base_href_url|meta_refresh_url)'
```

Expected: PASS.

- [ ] **Step 3: Run authoritative inner-loop check**

Run:

```bash
make check
```

Expected: PASS.

- [ ] **Step 4: Commit roadmap and verification-ready state**

Run:

```bash
git add docs/ROADMAP.md
git commit -m "docs: update URL hardening roadmap"
```

---

## Self-Review

- Spec coverage: Task 1 implements runtime refresh sanitization; Task 2 routes static meta refresh content; Task 3 proves base href and adds adversarial seeds; Task 4 updates roadmap and keeps `data:image` deferred behind URL-kind splitting.
- Scope control: dynamic `http-equiv={kind}` remains plain attribute escaping, matching the design tradeoff.
- Type consistency: `RefreshContent(string)` matches `Writer.URL(string)` style and codegen emits `urlStringExpr(expr, t)` for string-like conversion.
- Verification: targeted runtime, codegen/corpus, and `make check` gates are included.
