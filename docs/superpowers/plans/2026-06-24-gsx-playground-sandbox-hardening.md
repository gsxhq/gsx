# gsx Playground — Sandbox Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the playground safe to expose publicly on free-tier Cloud Run by removing the network/exec capability from visitor code at the source layer (deny-by-default import allowlist), building/running offline with cgo disabled, and neutralizing the metadata-token target with a zero-permission service account.

**Architecture:** Borrows the Go Playground's *principle* (run untrusted code with no useful capabilities) adapted to our narrower use case. Because the playground only renders templates, we reject any import outside a curated allowlist — enforced on the *generated* `.x.go` (where all visitor Go-chunk imports surface) by parsing imports with `go/parser` between `gsx generate` (which only type-checks, never executes) and `go build`/run. Defense-in-depth atop Cloud Run's gVisor.

**Tech Stack:** Go stdlib (`go/parser`, `go/token`), the existing `playground/server` service, Cloud Run deploy config.

## Global Constraints

- `playground/server` non-test code is **standard-library only**.
- Deny-by-default: the allowlist enumerates ALLOWED imports; anything else is rejected. This is a real allowlist, not a denylist or string heuristic — enforce by parsing the generated `.x.go` with `go/parser` (`parser.ImportsOnly`) and checking each import path against the set.
- Enforcement happens AFTER `gsx generate` succeeds and BEFORE `go build`/`go run` (generate type-checks only; it never runs visitor code).
- Rejecting `unsafe` also defangs `//go:linkname` (the compiler requires `import "unsafe"` for linkname); rejecting `"C"` blocks cgo; visitor supplies a single `.gsx` so no `.s`/asm files are reachable.
- Build/run offline with cgo off: `GOPROXY=off`, `GOFLAGS=-mod=mod`, `CGO_ENABLED=0` (deps are stdlib + gsx-via-local-replace; no fetch needed).
- The allowlist (exact set — gsx-emitted ∪ curated safe stdlib):
  `context, io, strconv, fmt, strings, time, sort, errors, math, math/rand, unicode, unicode/utf8, html, github.com/gsxhq/gsx, github.com/gsxhq/gsx/std`

---

## File Structure

- `playground/server/render.go` — **modify**: add `allowedImports` set + `checkImports(viewDir string) (*diagnostic, error)`; call it in `renderIn` after generate, before build; add the offline/cgo env to the pool's `run` env.
- `playground/server/render_test.go` — **modify**: tests for rejected + allowed imports.
- `playground/server/deploy.md` — **modify**: document the zero-permission service account and the source-level allowlist as the egress mitigation.

---

## Task 1: Import allowlist + offline/cgo-disabled build

**Files:**
- Modify: `playground/server/render.go`
- Modify: `playground/server/render_test.go`

**Interfaces:**
- Consumes: `renderIn(gsxBin, gocache string, ws *workspace, in renderReq) renderResp`, `diagnostic{Severity, Message string; Line, Column int}`, the pool's shared `env` (`[]string{"GOCACHE=..."}`).
- Produces: `var allowedImports map[string]bool`; `checkImports(viewDir string) *diagnostic` (returns a non-nil diagnostic naming the first disallowed import, or nil if all allowed).

- [ ] **Step 1: Write the failing tests**

```go
// add to render_test.go
func TestImportRejected(t *testing.T) {
	resp := testPool.render(renderReq{
		GSX: "package views\n\nimport \"net/http\"\n\ncomponent C() {\n\t<p>{http.MethodGet}</p>\n}\n",
		Invoke: "C(CProps{})",
	})
	if resp.HTML != "" {
		t.Fatalf("expected rejection, got html %q", resp.HTML)
	}
	found := false
	for _, d := range resp.Diagnostics {
		if d.Severity == "error" && strings.Contains(d.Message, "not allowed") && strings.Contains(d.Message, "net/http") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an 'import not allowed: net/http' diagnostic, got %+v (err=%q)", resp.Diagnostics, resp.Error)
	}
}

func TestAllowedImportRenders(t *testing.T) {
	resp := testPool.render(renderReq{
		GSX: "package views\n\nimport \"strings\"\n\ncomponent C(s string) {\n\t<p>{strings.ToUpper(s)}</p>\n}\n",
		Invoke: `C(CProps{S: "hi"})`,
	})
	if resp.Error != "" || len(resp.Diagnostics) != 0 {
		t.Fatalf("unexpected error/diags: %q %+v", resp.Error, resp.Diagnostics)
	}
	if strings.TrimSpace(resp.HTML) != "<p>HI</p>" {
		t.Fatalf("html = %q", resp.HTML)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd playground/server && go test ./... -run 'TestImportRejected|TestAllowedImportRenders' -v`
Expected: FAIL — `TestImportRejected` fails because `net/http` currently compiles and runs (no allowlist yet).

- [ ] **Step 3: Implement the allowlist + enforcement**

Add to `render.go` (imports: add `go/parser`, `go/token`):

```go
// allowedImports is the deny-by-default allowlist for the generated views
// package. It is the union of imports gsx codegen emits and a curated set of
// capability-free stdlib packages safe for a public template playground.
// Anything not listed (net*, os*, os/exec, syscall, unsafe, runtime, "C", ...)
// is rejected, removing the network/exec/filesystem vectors before the program
// is ever built or run.
var allowedImports = map[string]bool{
	"context": true, "io": true, "strconv": true, "fmt": true,
	"strings": true, "time": true, "sort": true, "errors": true,
	"math": true, "math/rand": true, "unicode": true, "unicode/utf8": true,
	"html": true,
	"github.com/gsxhq/gsx":     true,
	"github.com/gsxhq/gsx/std": true,
}

// checkImports parses every generated *.x.go in viewDir for its imports and
// returns a diagnostic naming the first import not on the allowlist, or nil if
// all are allowed. Parsing the GENERATED code is comprehensive: all visitor
// Go-chunk imports flow into the .x.go. Rejecting unsafe also blocks
// //go:linkname; rejecting "C" blocks cgo.
func checkImports(viewDir string) *diagnostic {
	entries, err := os.ReadDir(viewDir)
	if err != nil {
		return &diagnostic{Severity: "error", Message: "playground: cannot inspect generated code: " + err.Error()}
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".x.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(viewDir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			return &diagnostic{Severity: "error", Message: "playground: cannot parse generated code: " + err.Error()}
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if !allowedImports[path] {
				p := fset.Position(imp.Pos())
				return &diagnostic{
					Severity: "error",
					Message:  "import " + strconv.Quote(path) + " is not allowed in the playground",
					Line:     p.Line, Column: p.Column,
				}
			}
		}
	}
	return nil
}
```

In `renderIn`, after the `gsx generate` step succeeds and `generatedGo` is read, before the `go build`/`go run`, add:

```go
	if d := checkImports(ws.viewDir); d != nil {
		return renderResp{GeneratedGo: generatedGo, Diagnostics: []diagnostic{*d}, Ms: ms()}
	}
```

Add the offline/cgo env to the pool's shared env in `newPool` (where `env := []string{"GOCACHE=" + gocache}` is built):

```go
	env := []string{
		"GOCACHE=" + gocache,
		"GOPROXY=off",
		"GOFLAGS=-mod=mod",
		"CGO_ENABLED=0",
	}
```

(Confirm the one-time `go mod tidy` in pool setup still succeeds with these — deps are stdlib + local-replace gsx, so no fetch is needed. If `tidy` specifically needs network, run only `tidy` without `GOPROXY=off` and keep the offline env for generate/build/run.)

- [ ] **Step 4: Run the tests**

Run: `cd playground/server && go test ./... -v`
Expected: PASS — `TestImportRejected` (net/http rejected with diagnostic), `TestAllowedImportRenders` (`<p>HI</p>`), plus all prior tests still green.

- [ ] **Step 5: Verify a representative dangerous import is blocked end to end**

Run: `cd playground/server && go test ./... -run TestImportRejected -v`
Expected: PASS — confirms the net path is rejected before build/run.

- [ ] **Step 6: Commit**

```bash
git add playground/server/render.go playground/server/render_test.go
git commit -m "feat(playground): deny-by-default import allowlist + offline/cgo-off build"
```

---

## Task 2: Deploy hardening — zero-permission service account

**Files:**
- Modify: `playground/server/deploy.md`

- [ ] **Step 1: Add the service-account + allowlist mitigation to the Security section**

In `playground/server/deploy.md`, update the `## Security` section so the free-tier mitigation is the documented, recommended path. Add:

- **Source-level import allowlist** (implemented in the service): visitor components may only import a curated capability-free set; `net`/`os`/`os/exec`/`syscall`/`unsafe`/cgo are rejected before the program is built or run. This removes the network/exec/file vectors without a VPC connector.
- **Dedicated zero-permission service account** — the cheap kill-shot for the metadata-token target. Create an SA with NO IAM roles and deploy under it, so a metadata-endpoint read yields a useless token:

```bash
SA=gsx-playground-sandbox
gcloud iam service-accounts create "$SA" --project "$PROJECT" \
  --display-name "gsx playground (no permissions)"
# Grant it NOTHING. Then deploy with:
gcloud run deploy "$SERVICE" --project "$PROJECT" --region "$REGION" \
  --image "gcr.io/$PROJECT/$SERVICE" \
  --service-account "${SA}@${PROJECT}.iam.gserviceaccount.com" \
  --allow-unauthenticated \
  --memory 1Gi --cpu 1 --concurrency 4 --timeout 30 \
  --min-instances 0 --max-instances 3 \
  --set-env-vars ALLOWED_ORIGIN=https://gsxhq.github.io
```

- Note that with (a) the import allowlist, (b) the zero-permission SA, and (c) Cloud Run's gVisor + the request limits, a public free-tier deploy is acceptable for a docs playground; the residual (no hard namespace-level network-off, unlike Go's Playground which runs on gVisor hosts it controls) is documented as the known trade-off. For a hard no-network guarantee, use a VPC connector with `--vpc-egress=all-traffic` and no internet route (adds cost) or GKE Sandbox.

- [ ] **Step 2: Verify the doc is coherent**

Run: `grep -n "service-account\|allowlist\|zero-permission" playground/server/deploy.md`
Expected: the new lines are present.

- [ ] **Step 3: Commit**

```bash
git add playground/server/deploy.md
git commit -m "docs(playground): zero-permission SA + import-allowlist as the free-tier egress mitigation"
```

---

## Self-Review

- Allowlist is deny-by-default, enforced via `go/parser` on the generated `.x.go` (comprehensive for visitor imports), between generate (type-check only) and build/run — matches the Global Constraints. ✓
- `unsafe`/`C` rejection covers `//go:linkname`/cgo; single-`.gsx` input precludes asm. ✓
- Offline + cgo-off build closes the build-time fetch + C vectors. ✓
- Zero-permission SA neutralizes the metadata-token SSRF target (Task 2). ✓
- Allowlist contents include the exact gsx-emitted imports (context/io/strconv/fmt/gsx/gsx/std) so legitimate renders + all 5 presets still pass — Task 1 Step 4 runs the full suite (which includes the preset-equivalent render tests). ✓
- No placeholders; allowlist set and enforcement code are complete; `$PROJECT`/`$SA` in deploy.md are clearly maintainer inputs.
