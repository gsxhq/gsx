# Generic Tag Inference: Caller-Side Probe Rework + Review Fixes — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make generic-tag type inference behave like Go's own inference (partial props, sibling files, cross-package, method components) and never emit non-compiling `.x.go`, by reworking the probe layer to caller-side per-site helpers and fixing the 9 verified review findings.

**Architecture:** Replace the exported declaring-side `GsxInfer<Name>` helper + positional harvest with **unexported per-call-site helpers** (`_gsxinfer<N>`) emitted into the *calling* file's skeleton, whose parameter list is exactly the props supplied at that tag, and whose result type is the component's props type. Go's checker infers the helper's type args; harvest maps `_gsxinfer<N>` back to its tag element by exact name via a per-file **probe registry**. Constraints and param types for imported components are re-qualified into the caller's context by a small AST-level requalification engine over the dep's parsed sources. Emission (`childTypeArgUse`) gains unspeakable-type detection and collision-free import aliasing. The `cannot infer` diagnostic rewrite keys off registered probe spans instead of guessing by tag position.

**Tech Stack:** Go 1.26.1 (pinned), go/ast + go/types, txtar corpus.

**Worktree:** all work happens in `/Users/jackieli/personal/gsxhq/gsx/.worktrees/generic-inference` (branch `generic-inference`, based on main@8a42468). All paths below are relative to it.

## Global Constraints

- The bar (user-stated): **inference succeeds wherever Go's own inference would succeed given the supplied props as call arguments** — including when optional props are omitted.
- `gsx generate` must NEVER exit 0 having emitted non-compiling `.x.go`. Anything unspeakable/unresolvable at emission becomes a positioned gsx diagnostic.
- Probe layer identifiers live in the reserved `_gsx` namespace (`_gsxinfer<N>`); nothing exported, no `GsxInfer` prefix anywhere after this plan (grep must come back empty at the finale).
- The `cannot infer` rewrite may ONLY fire for type errors positioned inside a registered probe's span; all other type errors pass through untouched.
- Every syntax/codegen change ships corpus cases per context; goldens regenerated via `go test ./internal/corpus -run TestCorpus -update`, never hand-written; commit `coverage.golden` when it changes.
- Runtime (root package) untouched. `make check` for the inner loop; `make ci` authoritative before merge.
- Every commit message ends with:
  `Claude-Session: https://claude.ai/code/session_01R6cMqzYs4Wo28Q68FsQgM5`

**Verification baseline (before Task 1):** `make check` passes at 8a42468. If not, stop and report.

## File Structure

- **Create `internal/codegen/infer.go`** — the whole probe layer in one focused file: `inferRegistry`, per-site helper emission, the requalification engine, harvest matching, and the diagnostic-rewrite gate. (analyze.go is ~2700 lines; the current inference code scattered through it moves here.)
- **Create `internal/codegen/infer_test.go`** — unit tests for the registry, requalifier, and harvest matcher.
- **Modify** `internal/codegen/analyze.go` (buildSkeleton wiring, delete old helper/harvest code), `internal/codegen/module_importer.go` (package-wide genericProps + arity facts, rewrite gating), `internal/codegen/emit.go` (childTypeArgUse safety).
- **Corpus:** new cases under `internal/corpus/testdata/cases/components/` and `internal/corpus/testdata/cases/xpkg/`.

---

### Task 1: Probe registry + per-site helpers for same-file components (findings 3, 5 groundwork)

Replace the positional probe/harvest pipeline with a name-keyed registry, and emit per-site helpers with **exactly the supplied props**. This task covers components declared in the same file (the only thing the old path handled reliably); later tasks extend to siblings and imports. The old `GsxInfer` emission and positional harvest are deleted in THIS task — the registry path replaces them outright, so there is no dual-path window.

**Files:**
- Create: `internal/codegen/infer.go`, `internal/codegen/infer_test.go`
- Modify: `internal/codegen/analyze.go` (buildSkeleton: emit probes via registry; delete `emitInferHelper`, `inferHelperTarget`, `isInferHelperCall`, `inferHelperArgs`, and harvest's positional `inferred[i]` block), `internal/codegen/module_importer.go` (thread the registry from buildSkeleton to harvest)
- Test: `internal/codegen/infer_test.go`, plus existing corpus (generic_inferred_tag.txtar must keep passing byte-identically EXCEPT the golden is regenerated if probe changes alter nothing emitted — they must not: probes never appear in .x.go)

**Interfaces (later tasks consume these — keep signatures exact):**

```go
// inferRegistry records every inference probe emitted into one file's skeleton.
type inferRegistry struct {
	n     int
	sites map[string]*inferSite // helper name "_gsxinfer3" -> site
}

type inferSite struct {
	el        *gsxast.Element // the tag this probe infers for
	propsType string          // e.g. "ButtonProps" or "components.ButtonProps"
	span      inferSpan       // skeleton byte span of the emitted probe stmt (Task 8 uses it)
}

type inferSpan struct{ start, end int } // offsets into the skeleton string

// newInferRegistry() *inferRegistry
// (r *inferRegistry) nextName() string                        // "_gsxinfer1", "_gsxinfer2", ...
// (r *inferRegistry) record(name string, s *inferSite)
// (r *inferRegistry) lookup(name string) (*inferSite, bool)   // exact-name match ONLY
// isInferProbeName(name string) bool                          // regexp ^_gsxinfer[0-9]+$
```

- [ ] **Step 1: Write failing unit tests for the registry + name matcher**

```go
// infer_test.go
func TestInferProbeNameExactMatch(t *testing.T) {
	for name, want := range map[string]bool{
		"_gsxinfer1":    true,
		"_gsxinfer42":   true,
		"_gsxinfer":     false,
		"_gsxinfer1x":   false,
		"GsxInferStuff": false, // the finding-3 attack: must NOT match
		"gsxinfer1":     false,
	} {
		if got := isInferProbeName(name); got != want {
			t.Errorf("isInferProbeName(%q) = %v, want %v", name, got, want)
		}
	}
}
```

Run: `go test ./internal/codegen -run TestInferProbeName -v` → FAIL (undefined).

- [ ] **Step 2: Write the failing behavior test — partial props (the finding-5 headline)**

```go
// infer_test.go — GenerateDirs/DirResult idiom as in generic_typeparam_err_test.go
func TestInferPartialProps(t *testing.T) {
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module ipp\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	writeFile(t, tmp, "views.gsx", `package ipp

component Button[T string | int](label T, size string) {
	<button class={size}>{label}</button>
}

component Page() {
	<Button label={7} />
}
`)
	out, err := GenerateDirs(tmp, []string{tmp}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out[tmp].Diags) != 0 {
		t.Fatalf("diags: %+v", out[tmp].Diags)
	}
	var src string
	for _, b := range out[tmp].Files {
		src = string(b)
	}
	if !strings.Contains(src, "Button[int](ButtonProps[int]{Label: 7})") {
		t.Fatalf("partial-props inference failed; generated:\n%s", src)
	}
}
```

Run: `go test ./internal/codegen -run TestInferPartialProps -v` → FAIL today (raw `cannot use generic type ButtonProps[...]` diag).

- [ ] **Step 3: Write the failing user-namespace test (finding 3)**

```go
func TestUserGsxInferFuncDoesNotCorruptHarvest(t *testing.T) {
	// same module scaffold; views.gsx:
	// func GsxInferStuff() float64 { return 1.5 }   (in a go chunk)
	// component Button[T string | int](label T) { <b>{label}</b> }
	// component Page() { <p>{ GsxInferStuff() }</p> <Button label={7} /> }
	// Assert: no diags, generated contains "Button[int](ButtonProps[int]{Label: 7})".
}
```

(Write it out fully following the Step-2 scaffold shape.) Run → FAIL today (silent miscompile: uninstantiated Button emitted).

- [ ] **Step 4: Implement**

In `infer.go`: the registry types above, plus the helper emitter for a component declared in the current package (constraint text verbatim from the component AST):

```go
// emitInferProbe writes one per-site inference helper + probe statement into
// the skeleton and records it in the registry. params is the component's full
// declared param list; supplied maps FIELD name -> the attr's probe arg
// expression for the props supplied at this tag (subset — omitted props are
// simply absent, mirroring a Go call that Go infers from the args it has).
// typeParamsDecl is the component's bracketed constraint list rendered for the
// CALLING file (Task 4 requalifies imported ones; same-package = verbatim).
// Returns false when no supplied prop mentions any inference-relevant field
// (zero args would be a nullary helper Go cannot infer from — the tag then
// falls through to the type-checker's own error, rewritten by Task 8).
func (r *inferRegistry) emitInferProbe(sb *strings.Builder, el *gsxast.Element,
	propsType, typeParamsDecl, typeParamsUse string,
	params []param, supplied map[string]string) bool {

	ordered := suppliedInDeclOrder(params, supplied) // params order, filtered to supplied
	if len(ordered) == 0 {
		return false
	}
	name := r.nextName()
	start := sb.Len()
	fmt.Fprintf(sb, "func %s%s(", name, typeParamsDecl)
	for i, p := range ordered {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(sb, "_gsxv%d %s", i, p.typeSrc)
	}
	fmt.Fprintf(sb, ") %s%s { return %s%s{} }\n", propsType, typeParamsUse, propsType, typeParamsUse)
	fmt.Fprintf(sb, "var _ = %s(", name)
	for i, p := range ordered {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(supplied[fieldName(p.name)])
	}
	sb.WriteString(")\n")
	r.record(name, &inferSite{el: el, propsType: propsType, span: inferSpan{start, sb.Len()}})
	return true
}
```

(Adapt to the real emission context: the current code emits probes inside a component's probe func body — inspect where the old `GsxInfer` call sites were emitted in `emitComponentSkeleton`/`emitProbes` and emit `_gsxinfer` helpers at PACKAGE level in the skeleton prefix with the probe call as `var _ = ...` at package level too, OR helper at package level + call inside the probe body — choose whichever the current skeleton structure accepts, and document the choice. The critical properties: helper name from the registry, params = supplied subset in declaration order, `//line`-free so its errors carry skeleton positions Task 8 can match.)

Harvest replacement in `analyze.go` (`harvest`): delete the `inferred[i]` positional block and the `isInferHelperCall` scan; instead walk `info` for calls whose fun is an `*ast.Ident` with `isInferProbeName(name)`, look up the registry, and set `out[site.el] = info.Types[call].Type` (the instantiated `*types.Named` props type). Thread the per-file registry from `buildSkeleton`'s return values through `typesPackageWith` to `harvest` (add it to the existing per-file maps like `compsByXGo` → `inferByXGo map[string]*inferRegistry`).

Wire `buildSkeleton`: where the old inference probes were emitted for a tag with `el.TypeArgs == ""` and `genericProps[propsType]`, gather `supplied` from the tag's attrs (reuse the existing attr→field mapping used by `inferHelperArgs`'s `propFieldEntry` plumbing — same source, but now a subset is fine) and call `emitInferProbe`. The tag's child-invocation probe (`Button(...)` with the props literal) stays exactly as-is — when inference succeeds, harvest supplies `resolved[el]` and emission instantiates; the old in-skeleton `Button(GsxInferButton(...))` composite call is gone.

Delete: `emitInferHelper`, `inferHelperTarget`, `isInferHelperCall`, `inferHelperArgs`. `grep -rn "GsxInfer" internal/ --include="*.go"` must return ONLY test fixtures written by this plan (Step 3's user-collision fixture).

- [ ] **Step 5: Run the new tests + full suites**

Run: `go test ./internal/codegen -run 'TestInferProbeName|TestInferPartialProps|TestUserGsxInfer' -v -count=1`
Expected: PASS.
Run: `go test ./internal/codegen ./internal/corpus -count=1`
Expected: PASS — existing corpus goldens unchanged (probes never reach `.x.go`); if `generic_inferred_tag` or `generic_inference_failed_diag` fail, the failure diagnostic text may have changed shape — that rewrite is Task 8's scope; if THAT case fails here, regenerate it with `-update`, inspect the new diagnostic is still positioned and sensible, and note it in the commit message.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/
git commit -m "refactor(codegen): caller-side per-site inference probes with name-keyed harvest"
```

---

### Task 2: Package-wide generic component visibility (finding 2)

`genericProps` (and the supporting param/type-param info the probe emitter needs) must cover ALL components of the package, not just the current file's.

**Files:**
- Modify: `internal/codegen/module_importer.go` (build package-wide generic facts once per package, alongside `componentPropFieldsFor`), `internal/codegen/analyze.go` (buildSkeleton consumes them)
- Test: corpus case `internal/corpus/testdata/cases/xpkg/generic_sibling_file.txtar` (the corpus supports multi-file packages — copy the section layout from an existing `xpkg/` case, e.g. `xpkg/multi_gsx_package`)

**Interfaces:**
- Produces: package-wide `genericComps map[string]*gsxast.Component` (props-type name → declaring component AST) available to buildSkeleton for every file of the package; Task 4 extends the same map shape to imported components (via a parallel dep view), so keep the key = props-type name exactly as `genericPropsFor` computes it today.

- [ ] **Step 1: Write the failing corpus case** — `box.gsx` declares `component Box[T string | int](value T) { <span>{value}</span> }`, `page.gsx` (same package) uses `<Box value={7} />`; expected render `<span>7</span>`, empty diagnostics. Leave goldens empty for `-update`.

- [ ] **Step 2: Run to verify the current failure**

Run: `go test ./internal/corpus -run TestCorpus -count=1 2>&1 | head -20`
Expected: the new case FAILS with `cannot use generic type BoxProps[T string | int] without instantiation` (the review's live-probe result).

- [ ] **Step 3: Implement** — in the per-package analysis path (where `componentPropFieldsFor(dir, gsxFiles)` is computed once), also compute `genericComps` from ALL files' components (use `componentsInFiles(gsxFiles)` + the `genericPropsFor` filter conditions, but keep the `*gsxast.Component` value, not just a bool — the probe emitter needs `TypeParams`/`Params`). Pass it into every `buildSkeleton` call (replacing the per-file `genericPropsFor(comps, byo)` + `importedGenericProps` bool-map merge; derive the bool view where only a bool is needed). Mirror at the second `buildSkeleton`/harvest call path (`module_importer.go` ~592 and ~785): both must consume the same package-wide map.

- [ ] **Step 4: Regenerate + verify**

```bash
go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -count=1 && go test ./internal/codegen -count=1
```
Expected: PASS; new case golden shows `Box[int](BoxProps[int]{Value: 7})` in page.x.go.

- [ ] **Step 5: Commit**

```bash
git add internal/codegen/ internal/corpus/testdata
git commit -m "fix(codegen): package-wide generic component visibility for tag inference"
```

---

### Task 3: The requalification engine (groundwork for Task 4)

Render a type expression that was written in a DEP package's context (constraint lists, param types) into text valid in the CALLING file's skeleton: qualify dep-local named types with the caller's alias for the dep, requalify the dep's own imports, and refuse unexported names.

**Files:**
- Modify: `internal/codegen/infer.go`
- Test: `internal/codegen/infer_test.go`

**Interfaces:**
- Produces:

```go
// requalifyTypeExpr rewrites a type expression src (written in the dep
// package's file context) for use in the calling file's skeleton.
//   depAlias:   the caller's import alias/name for the dep package ("components")
//   depImports: the dep FILE's import specs (alias -> path), so `pq.T` in dep
//               context can be re-imported by the caller under a fresh alias
//   addImport:  callback registering an extra skeleton import (path, alias);
//               alias "" = plain import
// Returns an error for any unexported ident that would need qualification
// (unspeakable outside the dep) and for exprs it cannot parse.
func requalifyTypeExpr(src, depAlias string, depImports []importSpec, addImport func(path, alias string)) (string, error)
```

Implementation: parse `src` with `go/parser.ParseExpr` wrapped as a type (`type _t = <src>` file trick, matching `parseTypeParamNames`'s synth style), walk the AST:
- `*ast.Ident` that is a predeclared universe name (`types.Universe.Lookup(name) != nil`) or a declared type param name → leave.
- Other bare `*ast.Ident`: if `!ast.IsExported(name)` → error (`unexported type %s`); else rewrite to `<depAlias>.<name>`.
- `*ast.SelectorExpr` `pq.T`: resolve `pq` in depImports → path; register `addImport(path, "_gsxti"+n)` and rewrite qualifier to the fresh alias (fresh alias avoids caller-side name collisions entirely); `!ast.IsExported(T)` → error.
- Everything else (slices, maps, funcs, channels, unions `|`, tilde `~`, parens) recurses structurally. Note: constraint LISTS (`T string | int, U any`) are a field list, not a single expr — expose a second function `requalifyTypeParams(decl string, ...) (string, error)` that synthesizes `func _[<decl>]() {}` (exactly `parseTypeParamNames`'s trick), rewrites each field's constraint expr with the same walker, and prints back via `go/printer`. Type-param names declared in the list are recorded first so they are never qualified.

- [ ] **Step 1: Write the failing table test** — cases: `string | int` (unchanged), `~string` (unchanged), `MyInt | string` → `components.MyInt | string`, `fmt.Stringer` (dep imports `fmt`) → `_gsxti1.Stringer` + addImport("fmt","_gsxti1"), `secret | int` → error, `[]Row` → `[]components.Row`, `map[string]Cfg` → `map[string]components.Cfg`, two-param decl `K comparable, V Renderer` → `K comparable, V components.Renderer`, and a decl whose constraint references the OTHER type param `K any, V interface{ ~[]K }` (K stays bare).
- [ ] **Step 2: Run → FAIL (undefined).**
- [ ] **Step 3: Implement per the spec above.**
- [ ] **Step 4: Run → PASS. Also `go test ./internal/codegen -count=1`.**
- [ ] **Step 5: Commit** — `feat(codegen): type-expression requalifier for cross-package inference probes`

---

### Task 4: Cross-package caller-side probes (replaces dep-side helpers; findings 2-cross, 3-cross)

Imported generic components get the same per-site probes: props type `components.ButtonProps`, constraints and param types requalified via Task 3. Dep facts must carry what the emitter needs.

**Files:**
- Modify: `internal/codegen/module_importer.go` (`depPropFacts`/`fileFacts`: replace the bool `genericProps` with `genericSigs map[string]*genericSig`), `internal/codegen/infer.go`, `internal/codegen/analyze.go`
- Test: extend `internal/codegen/generic_crosspkg_test.go`; corpus `xpkg` case for an inferred imported tag if an `xpkg` case can express imports (check how `xpkg/multi_gsx_package` structures multi-package fixtures; if imports across packages are expressible, add `xpkg/generic_inferred_import.txtar`)

**Interfaces:**
- Produces:

```go
type genericSig struct {
	typeParams string      // raw decl text as written in the dep ("T string | int")
	params     []param     // dep param list (typeSrc in dep context)
	arity      int         // number of type params (Task 8's hint consumes this)
	imports    []importSpec // the DECLARING FILE's imports (requalifier input)
}
```

  Same-package components populate the same struct (typeParams/params verbatim, imports = own file — requalifier not invoked). One emitter path for both: `typeParamsDecl` and each `param.typeSrc` pass through `requalifyTypeParams`/`requalifyTypeExpr` when the component is imported (depAlias = the tag's qualifier resolved through the file's imports); on requalification error, SKIP the probe and record a positioned `inference-unavailable` diagnostic naming the offending type (`type inference for <components.Button> needs unexported dep type secret; instantiate explicitly with <components.Button[type] ...>`).

- [ ] **Step 1: Failing test** — extend `generic_crosspkg_test.go` with `TestGenericCrossPackageInference`: `components/button.gsx` declares `component Button[T string | int](label T, size string)`; root `post.gsx` uses `<components.Button label={7} />` (partial + imported). Assert no diags and generated root contains `components.Button[int](components.ButtonProps[int]{Label: 7})`.
- [ ] **Step 2: Run → FAIL** (today the imported path requires all props via the dep-side helper; partial-props kills it).
- [ ] **Step 3: Implement** — replace `depPropFacts.genericProps`/`fileFacts.genericProps` with `genericSigs`; delete the dep-side skeleton emission of exported helpers (the dep skeleton no longer contains any inference helper — verify by grepping the built skeleton in a debug run or unit test); wire the caller-side emitter for dotted tags. A constraint referencing a SECOND dep (`fmt.Stringer`) exercises addImport → the skeleton import block gains `_gsxti1 "fmt"` — make sure buildSkeleton's import assembly accepts registry-added imports.
- [ ] **Step 4: Also add** `TestGenericCrossPackageInferenceUnexportedConstraint`: dep constraint `T secret | string` (unexported dep type) → expect ONE positioned `inference-unavailable` diagnostic, generation of other files unaffected.
- [ ] **Step 5: Run all: `go test ./internal/codegen ./internal/corpus -count=1` → PASS. Commit** — `feat(codegen): caller-side cross-package inference probes with requalified constraints`

---

### Task 5: Method-component inference (finding 8)

With caller-side probes there is no receiver selector: `<p.Row v={1}/>` emits `func _gsxinferN[T any](v T) PageRowProps[T]` — the dotted-tag handling must distinguish "receiver-var qualifier" (method component: props type is `<RecvType><Name>Props`, same package) from "package qualifier" (imported). `childInvocation` already computes this (it produced `propsType` for the registry in Task 1) — this task is the explicit gate + tests.

**Files:**
- Modify: `internal/codegen/infer.go` (only if Step 2 fails — the Task 1/2 plumbing may already be correct), `internal/codegen/generic_method_go127_test.go`
- Test: extend the go1.27-gated test.

- [ ] **Step 1: Extend `TestGenericMethodComponentGo127`** — add to its views.gsx a second call site `<p.Box value={7} />` (NO explicit type args) and assert the generated source contains `p.Box[int](PageBoxProps[int]{Value: 7})`.
- [ ] **Step 2: Run under the gate** — on go1.26.1 it skips; verify compilation of the test file and run `GSX_REQUIRE_GENERIC_METHODS=1 go test ./internal/codegen -run Go127 -count=1` expecting the documented FAIL-because-toolchain (proves the assertions compile). If gotip is available (`command -v gotip`), run `make test-gotip` for the real signal; otherwise note in the commit body that the lane covers it in CI.
- [ ] **Step 3: Trace the 1.26-reachable half** — a method component tag on an UNSUPPORTED toolchain never reaches inference (the Task-8 guard from the previous plan skips the component). Add a plain test asserting that `<p.Row v={1}/>` without type args on go1.26 still produces the `unsupported-toolchain` diagnostic and NOT a `p.GsxInferRow`-style undefined-selector error (regression pin for the old bug shape).
- [ ] **Step 4: Commit** — `fix(codegen): method-component tag inference probes without receiver selectors`

---

### Task 6: Emission safety — unspeakable inferred type args (finding 4)

`childTypeArgUse` must refuse to print a type argument that names an unexported type (from any package other than the current one) — positioned diagnostic instead of non-compiling output.

**Files:**
- Modify: `internal/codegen/emit.go` (`childTypeArgUse` and its 4 call sites at emit.go:2409/2426/2464/2597 — it needs the `bag` and the element for positioning; change signature to return `(string, bool)` with `false` = diagnostic recorded)
- Test: `internal/codegen/infer_test.go`

- [ ] **Step 1: Failing test** — `TestInferredUnexportedTypeArgRejected`: module with `models/models.gsx` exporting `func NewSecret() secret` (unexported type via a Go chunk), `components/box.gsx` generic `Box[T any](value T)` — root page uses `<components.Box value={m.NewSecret()} />`. Expect ONE positioned diagnostic (code `unrenderable-type-arg`, message naming `models.secret` and suggesting explicit instantiation or an exported type), NO generated file for that .gsx, sibling files unaffected, and `err == nil`.
- [ ] **Step 2: Run → FAIL** (today: generate exits 0, output does not compile).
- [ ] **Step 3: Implement** — in `childTypeArgUse`, before printing, walk each inferred type arg with a checker: for every named type reachable at the TOP LEVEL of the printed representation (the qualifier function already visits every named type — set a flag inside `qf` when `!token.IsExported(obj name)` and `pkg != currentPkg`), collect offenders; if any, `bag.Errorf(el.Pos(), el.End(), "unrenderable-type-arg", ...)` and return `("", false)`. Callers propagate `false` exactly like other emission failures (component fails, siblings continue — the established recovery boundary).
- [ ] **Step 4: Run** `go test ./internal/codegen ./internal/corpus -count=1` → PASS. **Commit** — `fix(codegen): reject unspeakable inferred type args with a positioned diagnostic`

---

### Task 7: Emission safety — import-name collisions (finding 1)

When `childTypeArgUse` adds an import for an inferred type arg's package, a same-NAME different-PATH import already in the file must not collide. Give generator-added type-arg imports fresh reserved aliases when their package name is taken.

**Files:**
- Modify: `internal/codegen/emit.go` (`childTypeArgUse` qualifier + `writeImports` plumbing: a new `typeArgAliases map[string]string` path→alias, emitted like filter aliases)
- Test: `internal/codegen/infer_test.go`

- [ ] **Step 1: Failing test** — `TestInferredTypeArgImportCollision`: reproduce the review probe — main.gsx imports `example.com/x/other/ids` (package `ids`) and uses it in markup; `<components.Button label={components.NewID()} />` infers `example.com/x/ids.ID` (different package also named `ids`). Assert generate succeeds AND `go build ./...` in the tmp module succeeds (write the generated files to disk as in `TestBuildTagExcludesGeneratedFile`), and the generated source references the aliased qualifier (grep `_gsxti`).
- [ ] **Step 2: Run → FAIL** (build error `ids redeclared`).
- [ ] **Step 3: Implement** — in `qf`: when the path is not already imported (plain or aliased) and its package NAME is already bound in this file (track a set of taken names: user plain imports' base names, user aliases, filter aliases, previously added type-arg imports), allocate `_gsxti<N>`, record in `typeArgAliases`, return it. `writeImports` emits `typeArgAliases` entries alongside filter aliases (sorted, after filters, before user-aliased — extend the existing comment describing the import-region order). Same package name NOT taken → keep today's plain-import behavior (byte-identical existing goldens).
- [ ] **Step 4: Run** `go test ./internal/codegen ./internal/corpus -count=1` → PASS (no existing golden churn). **Commit** — `fix(codegen): alias inferred type-arg imports that collide with existing import names`

---

### Task 8: Precise inference diagnostics (findings 6, 7)

The `cannot infer` rewrite fires ONLY for errors inside a registered probe's span, and the hint uses the real component's arity — local or imported.

**Files:**
- Modify: `internal/codegen/module_importer.go` (the rewrite at ~730 and its twin at ~883; delete `componentTagAtTypeError` + `explicitTypeArgHint` in favor of registry-driven versions in infer.go), `internal/codegen/infer.go`
- Test: `internal/codegen/infer_test.go`; corpus `generic_inference_failed_diag.txtar` regenerated

**Interfaces:**
- Consumes: `inferRegistry.sites[*].span` (Task 1) — skeleton offsets. IMPORTANT (Task-1 review finding): probe statements are deliberately emitted UNDER the enclosing tag's `//line` mapping — the diagnostic-survival filter (module_importer.go ~729) silently drops any type error whose position still names the `.x.go` skeleton, so a //line-free probe's errors would vanish entirely. To match an error against a probe span, use the UNADJUSTED position: `fset.PositionFor(e.Pos, false)` (ignores //line remapping, yields the raw skeleton offset) and compare against `inferSite.span`. `TestInferProbeRawSpanRecovery` (infer_test.go, Task 1 fix commit) proves this recovery works — build on it.
- Produces: `(r *inferRegistry) siteAt(rawOffset int) (*inferSite, bool)` (offset into the skeleton, from PositionFor(false)); hint arity from `genericSig.arity` (Task 4).

- [ ] **Step 1: Failing test A (hijack)** — `TestUserCannotInferErrorNotRewritten`: non-generic `component Card(title string)` used as `<Card title="x">{First(nil)}</Card>` with `func First[T any](v []T) T` in a Go chunk. Expect the diagnostic to CONTAIN `cannot infer T` / mention `First`, and NOT mention `<Card`.
- [ ] **Step 2: Failing test B (arity)** — `TestCrossPackageInferenceHintArity`: imported `components.Grid[K comparable, V any](rows map[K]V)` invoked with a value Go cannot infer from (`<components.Grid rows={nil} />`); expect the diagnostic `please instantiate with <components.Grid[type, type] ...>` (two placeholders).
- [ ] **Step 3: Run → both FAIL today** (A: rewritten to blame Card; B: single `[type]`).
- [ ] **Step 4: Implement** — rewrite gate: `if strings.Contains(msg, "cannot infer")` → find the registry for the error's skeleton file, `siteAt(pos)`; only rewrite when a site matches, naming `site.el.Tag` and building the hint from the site's known arity (`strings.Repeat` join of "type"). No registry match → message passes through untouched. Delete the old line/column tag-span guessing.
- [ ] **Step 5: Regenerate** `generic_inference_failed_diag.txtar` with `-update`, inspect the diagnostic still names the right tag; run `go test ./internal/codegen ./internal/corpus -count=1` → PASS. **Commit** — `fix(codegen): key inference diagnostics off probe spans with real arity hints`

---

### Task 9: Per-context corpus sweep (finding 9) + docs

**Files:**
- Create: `internal/corpus/testdata/cases/components/generic_inferred_controlflow.txtar` (inferred tag inside `for` and `if` bodies), `internal/corpus/testdata/cases/components/generic_inferred_childprop.txtar` (inferred tag as a child-prop slot value), `internal/corpus/testdata/cases/components/generic_inferred_partial_props.txtar` (the Task-1 partial-props behavior as a rendered corpus pin)
- Modify: `docs/guide/syntax/composition.md` — the inference paragraphs: state the partial-props rule ("inference sees exactly the props you supply, like a Go call") and remove/adjust any text implying all props are required; document that user identifiers may not start with `_gsx` (reserved).
- Test: corpus regen.

- [ ] **Step 1: Write the three cases** (bodies modeled on `generic_inferred_tag.txtar`; control-flow case wraps `<Box value={x} />` in `for x := range 2 {}` and an `if`; child-prop case passes an inferred generic tag as a named-slot/child-prop value — copy the shape from an existing child-prop corpus case).
- [ ] **Step 2: `-update`, verify, run full corpus + codegen suites.**
- [ ] **Step 3: Docs edit; check no literal `{{ }}` outside v-pre.**
- [ ] **Step 4: Commit** — `test(corpus): per-context pins for inferred generic tags; docs: partial-props inference rule`

---

### Task 10: Finale — full verification + adversarial re-probe

- [ ] **Step 1:** `make ci` in the worktree → PASS end-to-end.
- [ ] **Step 2:** `grep -rn "GsxInfer" internal/ --include="*.go" | grep -v _test` → empty; `gopls check -severity=hint internal/codegen/infer.go internal/codegen/analyze.go internal/codegen/emit.go internal/codegen/module_importer.go` → no new hints.
- [ ] **Step 3:** Independent adversarial reviewer (repo convention: throwaway probe programs, not diff-reading) re-runs ALL NINE original review findings' live probes against this branch, plus: partial props cross-package, a probe with `attrs`/`children` supplied alongside inferred props, inference inside a named slot, and the examples fixtures (`make examples` + `TestExamples` must stay green since `124-generic-explicit-args` exercises inference). Findings loop back through the tasks; merge only when clean.
- [ ] **Step 4:** Update `docs/ROADMAP.md` inference status; commit.
