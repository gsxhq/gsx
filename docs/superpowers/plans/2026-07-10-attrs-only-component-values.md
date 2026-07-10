# Attrs-only Component Values Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a package-level `var`/`func` whose static type is exactly `func(gsx.Attrs) gsx.Node` or `func(...gsx.Attr) gsx.Node` callable as a component tag (`<HomeIcon class="w-5 h-5"/>`), per `docs/superpowers/specs/2026-07-07-attrs-only-component-values-design.md`.

**Architecture:** Recognition rides the existing emit≡probe loop: tags with no discoverable `<Name>Props` type are gated onto a `_gsxcompsig` probe in the type-checked skeleton; the harvest returns the real `types.Type`; the emitter branches on the harvested signature and emits `Ident(bag)` / `Ident(bag...)`, reusing `childPropsLiteral`'s fallthrough bag assembly with a synthetic `{"Attrs"}` field set. Probed-but-no-match gets a new positioned diagnostic (replacing the old skeleton `undefined: <Name>Props` for the gated region, which is guaranteed-failing today).

**Tech Stack:** Go, `go/types`, existing gsx codegen internals (`internal/codegen`), txtar corpus (`internal/corpus`).

## Global Constraints

- Work happens in the worktree `.claude/worktrees/attrs-only-component-values` on branch `worktree-attrs-only-component-values`. Every task: verify `pwd` and `git branch --show-current` before committing (subagent cwd guard).
- Runtime (root `gsx` package) is standard-library only and this feature must not touch it at all.
- No new `packages.Load` calls anywhere (it is expensive; the whole design avoids it).
- Never hand-edit `.x.go` or golden files. Regenerate corpus goldens with `go test ./internal/corpus -run TestCorpus -update`, then ALWAYS re-run without `-update` to verify. Adding/removing a case changes `internal/corpus/testdata/coverage.golden` — the `-update` run rewrites it; a forgotten regen fails the suite.
- Inner loop: `go test ./internal/codegen ./internal/corpus`. Before finishing a task: `make check`. Before merge: `make ci` and `make lint`.
- Diagnostic message wording in this plan is normative — corpus goldens pin it; if you must deviate, update the spec's wording too.
- Literal `{{ }}` in `docs/guide/**` prose must be inside a `::: v-pre` block (VitePress).
- Commit after every task with a conventional-commit message.

## Shared code anchors (read these before any task that edits them)

| What | Where |
|---|---|
| Probe emission for component tags | `internal/codegen/analyze.go:1261-1315` (`case *gsxast.Element` in `emitProbes`; `isBareCallCandidate` branch emits `_gsxcompsig`) |
| `isBareCallCandidate` | `internal/codegen/analyze.go:294` |
| `_gsxcompsig` harvest (`sigByName` → `resolved[el]`) | `internal/codegen/analyze.go:2048-2077` |
| `_gsxuse` positional (k-th) harvest — do NOT perturb | `internal/codegen/analyze.go` just above the sigByName block; probe helpers declared in `module_importer.go:932` (`_gsxuseq` exists and is alignment-neutral) |
| Emitter branch on harvested signature | `internal/codegen/emit.go:3741-3759` (`genChildComponent`) |
| Convention fallback (`Name(NameProps{…})`) | `childInvocation` `emit.go:3689` (returns `el.Tag+"Props"`), call emitted `emit.go:3954` |
| Fallthrough bag assembly (`bag`/`mergeChain`/`attrsLitIdx`) | `childPropsLiteral` `emit.go:4533-4866`; the Attrs field entry is built at `emit.go:4844-4864` |
| `byoData`, `packageNullaryFuncs` | `internal/codegen/byo.go` (`packageNullaryFuncs` at `byo.go:156` is the template for the new type-name scan) |
| Per-package fact derivation | `componentPropFieldsFor` `internal/codegen/analyze.go:70` |
| Cross-package facts | `importedPropFacts` / `depPropFacts` `internal/codegen/module_importer.go:324-391` |
| Corpus case anatomy (multi-pkg, sibling .go, diagnostics-only) | `internal/corpus/loader.go:63-140`; examples: `cases/xpkg/cross_package.txtar`, `cases/byo/external_struct_node_attrs.txtar`, `cases/diagnostics/noprops_component_with_attrs.txtar` |
| LSP tag go-to-def dispatch | `internal/lsp/definition.go` (`componentTagDeclAt`, dispatch order D2/B/A-C/E/F), `definition_crosspkg.go:109` |

---

### Task 1: `attrsOnlySig` — the signature matcher

**Files:**
- Create: `internal/codegen/attrsonly.go`
- Test: `internal/codegen/attrsonly_test.go`

**Interfaces:**
- Produces: `attrsOnlySig(t types.Type) (variadic, ok bool)` and `isGsxNamed(t types.Type, name string) bool` — consumed by Tasks 3, 6, 8.

- [ ] **Step 1: Check for an existing gsx module-path constant**

Run: `grep -rn '"github.com/gsxhq/gsx"' internal/codegen/*.go | grep -v _test | grep -i 'const\|= "'`
If a constant exists (e.g. in imports/writeImports handling), reuse it; otherwise declare `const gsxModulePath = "github.com/gsxhq/gsx"` in `attrsonly.go`.

- [ ] **Step 2: Write the failing test**

```go
package codegen

import (
	"go/token"
	"go/types"
	"testing"
)

// fabricated gsx package: named types with the right package path + underlying
// shapes, sufficient for identity checks (attrsOnlySig matches by package path
// and type name, never structurally).
func fakeGsx(t *testing.T) (pkg *types.Package, attr, attrs, node types.Type) {
	t.Helper()
	pkg = types.NewPackage("github.com/gsxhq/gsx", "gsx")
	attrN := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Attr", nil), types.NewStruct(nil, nil), nil)
	attrsN := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Attrs", nil), types.NewSlice(attrN), nil)
	nodeN := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Node", nil), types.NewInterfaceType(nil, nil), nil)
	return pkg, attrN, attrsN, nodeN
}

func sig(variadic bool, param, result types.Type, extraResults ...types.Type) *types.Signature {
	params := types.NewTuple(types.NewVar(token.NoPos, nil, "attrs", param))
	rs := []*types.Var{types.NewVar(token.NoPos, nil, "", result)}
	for _, r := range extraResults {
		rs = append(rs, types.NewVar(token.NoPos, nil, "", r))
	}
	return types.NewSignatureType(nil, nil, nil, params, types.NewTuple(rs...), variadic)
}

func TestAttrsOnlySig(t *testing.T) {
	_, attr, attrs, node := fakeGsx(t)
	otherPkg := types.NewPackage("example.com/other", "other")
	otherAttrs := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "Attrs", nil), types.NewSlice(attr), nil)

	cases := []struct {
		name     string
		typ      types.Type
		variadic bool
		ok       bool
	}{
		{"named-attrs", sig(false, attrs, node), false, true},
		{"variadic-attr", sig(true, types.NewSlice(attr), node), true, true},
		{"unnamed-slice", sig(false, types.NewSlice(attr), node), false, false},
		{"extra-error", sig(false, attrs, node, types.Universe.Lookup("error").Type()), false, false},
		{"wrong-result", sig(false, attrs, types.Typ[types.String]), false, false},
		{"wrong-pkg-attrs", sig(false, otherAttrs, node), false, false},
		{"non-signature", types.Typ[types.Int], false, false},
		{"alias-of-match", types.NewAlias(types.NewTypeName(token.NoPos, otherPkg, "Component", nil), sig(true, types.NewSlice(attr), node)), true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			variadic, ok := attrsOnlySig(c.typ)
			if ok != c.ok || variadic != c.variadic {
				t.Fatalf("attrsOnlySig(%s) = (variadic=%v, ok=%v), want (%v, %v)", c.typ, variadic, ok, c.variadic, c.ok)
			}
		})
	}
}
```

Also add a zero-param and a two-param case to the table (both `ok=false`): build them with hand-rolled `types.NewSignatureType` tuples.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/codegen -run TestAttrsOnlySig -count=1`
Expected: FAIL — `undefined: attrsOnlySig`.

- [ ] **Step 4: Implement**

```go
// Package-level docs: attrs-only component values (spec:
// docs/superpowers/specs/2026-07-07-attrs-only-component-values-design.md).

// attrsOnlySig reports whether t is exactly one of the two attrs-only
// component-value shapes:
//
//	func(gsx.Attrs) gsx.Node
//	func(...gsx.Attr) gsx.Node
//
// Matching is by NAMED-type identity against the gsx package (path + name),
// never structural: the assignability-accident spelling
// func([]gsx.Attr) gsx.Node is deliberately excluded (spec §"The one shape
// deliberately excluded"). Aliases are unwrapped first, so a userland
// `type Component = func(...gsx.Attr) gsx.Node` matches. Generic signatures
// never match.
func attrsOnlySig(t types.Type) (variadic, ok bool) {
	sig, isSig := types.Unalias(t).(*types.Signature)
	if !isSig {
		if n, isNamed := types.Unalias(t).(*types.Named); isNamed {
			sig, isSig = n.Underlying().(*types.Signature)
		}
		if !isSig {
			return false, false
		}
	}
	if sig.TypeParams().Len() != 0 || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false, false
	}
	if !isGsxNamed(sig.Results().At(0).Type(), "Node") {
		return false, false
	}
	p := sig.Params().At(0).Type()
	if sig.Variadic() {
		sl, isSlice := types.Unalias(p).(*types.Slice)
		if !isSlice || !isGsxNamed(sl.Elem(), "Attr") {
			return false, false
		}
		return true, true
	}
	if !isGsxNamed(p, "Attrs") {
		return false, false
	}
	return false, true
}

// isGsxNamed reports whether t is the named type gsx.<name> (matched by the
// gsx module path so vendored/replaced copies still match, forks don't).
func isGsxNamed(t types.Type, name string) bool {
	n, isNamed := types.Unalias(t).(*types.Named)
	if !isNamed {
		return false
	}
	obj := n.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == gsxModulePath && obj.Name() == name
}
```

Note: a named type whose underlying is a matching signature (`type C func(...gsx.Attr) gsx.Node`, defined not alias) also matches via the `n.Underlying()` unwrap — that is intentional (assignment `var X C = …` still calls fine at `X(bag...)`). Add one table case for it.

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/codegen -run TestAttrsOnlySig -count=1` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/codegen/attrsonly.go internal/codegen/attrsonly_test.go
git commit -m "feat(codegen): attrsOnlySig matcher for attrs-only component values"
```

---

### Task 2: package type-name facts (`byoData.typeNames`)

**Files:**
- Modify: `internal/codegen/byo.go` (add field + scan func, mirror `packageNullaryFuncs` at `byo.go:156`)
- Modify: `internal/codegen/analyze.go:70+` (`componentPropFieldsFor`: populate)
- Test: `internal/codegen/byo_test.go` (or the file where `packageNullaryFuncs` is tested — check `grep -rn packageNullaryFuncs internal/codegen/*_test.go`)

**Interfaces:**
- Produces: `byoData.hasTypeName(name string) bool` — true when `name` is declared as ANY package-level type in the package's sibling `.go` files or `.gsx` GoChunks. Consumed by Task 3's gate and Task 8.

- [ ] **Step 1: Write the failing test** — temp dir with a `.go` file declaring `type FooProps struct{}`, `type Alias = int`, a func, plus a `.gsx`-derived files map containing a GoChunk struct (reuse how existing `componentPropFieldsFor` tests build `files`; check `grep -rn componentPropFieldsFor internal/codegen/*_test.go` for the harness). Assert `hasTypeName` true for both type decls, false for the func name and for `_test.go`/`.x.go` decls.

- [ ] **Step 2: Run to verify failure** — `go test ./internal/codegen -run TypeNames -count=1` → FAIL.

- [ ] **Step 3: Implement**

In `byoData`: add `typeNames map[string]bool`. New scan (same file-walk skeleton as `packageNullaryFuncs`, identical skip rules for `_test.go`/`.x.go`):

```go
// packageTypeNames parses the package's hand-written .go files (parse-only)
// and returns the set of package-level declared type names (any TypeSpec —
// struct, alias, defined type). Consumed by the attrs-only gate: a tag whose
// <Name>Props type name exists anywhere in the package keeps the XxxProps
// convention probe (and its generate-time attr diagnostics); only a tag with
// NO such type is gated onto the _gsxcompsig probe.
func packageTypeNames(dir string) map[string]bool {
	out := map[string]bool{}
	// … same ReadDir/ParseFile loop as packageNullaryFuncs …
	for _, decl := range f.Decls {
		gd, ok := decl.(*goast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*goast.TypeSpec); ok {
				out[ts.Name.Name] = true
			}
		}
	}
	return out
}
```

In `componentPropFieldsFor`, next to `byo.nullaryFuncs = packageNullaryFuncs(dir)`: `byo.typeNames = packageTypeNames(dir)`, then merge `.gsx`-declared type names. `gsxStructDecls(files)` covers structs declared in GoChunks; check whether it returns non-struct type decls too (read its implementation) — if not, add the GoChunk `type` names by the same scanner used there. Add method:

```go
// hasTypeName reports whether name is declared as a package-level type in
// this package (sibling .go files or .gsx GoChunks). nil-safe.
func (b *byoData) hasTypeName(name string) bool {
	return b != nil && b.typeNames[name]
}
```

- [ ] **Step 4: Verify** — `go test ./internal/codegen -count=1` → PASS (full package: the byoData change must not disturb anything).

- [ ] **Step 5: Check the cross-package bundle.** `importedPropFacts` (`module_importer.go:361`) calls `componentPropFieldsFor`, so `depPropFacts.byo.typeNames` is populated for free. Confirm with a quick read; nothing to change yet (Task 8 consumes it).

- [ ] **Step 6: Commit** — `git commit -m "feat(codegen): package-level type-name facts for the attrs-only gate"`.

---

### Task 3: core same-package path — gate, probe, harvest, emit

This is the load-bearing task. Emit ≡ probe: the gate predicate must be called identically from `emitProbes` and `genChildComponent`.

**Files:**
- Modify: `internal/codegen/attrsonly.go` (gate predicate)
- Modify: `internal/codegen/analyze.go` (probe emission ~1276; no harvest change needed yet — `_gsxcompsig` harvest already handles `*ast.Ident` args)
- Modify: `internal/codegen/emit.go` (`genChildComponent` branch after `emit.go:3759`; bag-expression helper)
- Create: `internal/corpus/testdata/cases/attrsonly/func_variadic.txtar`, `internal/corpus/testdata/cases/attrsonly/func_named_attrs.txtar`

**Interfaces:**
- Consumes: `attrsOnlySig` (Task 1), `byoData.hasTypeName` (Task 2).
- Produces: `isAttrsOnlyCandidate(el *gsxast.Element, propFields map[string]map[string]bool, byo *byoData, recvVar, recvTypeName string) bool` and `attrsOnlyBagExpr(...)` — consumed by Tasks 4-8.

- [ ] **Step 1: Write the two failing corpus cases**

`internal/corpus/testdata/cases/attrsonly/func_variadic.txtar`:

```
# attrs-only component value: package-level func with the variadic signature.
# Zero-attr call must emit Chip() (variadic needs no nil argument).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type chipProps struct {
	Attrs gsx.Attrs
}

component renderChip(p chipProps) {
	<span { p.Attrs... }>chip</span>
}

func Chip(attrs ...gsx.Attr) gsx.Node {
	return renderChip(chipProps{Attrs: gsx.Attrs(attrs)})
}

component Page() {
	<div>
		<Chip class="c1" data-k="v"/>
		<Chip/>
	</div>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div><span class="c1" data-k="v">chip</span><span>chip</span></div>
```

`func_named_attrs.txtar`: same shape but `func Pill(attrs gsx.Attrs) gsx.Node` and a `pillProps`; call sites `<Pill class="p"/>` and `<Pill/>`; render.golden `<div><span class="p">pill</span><span>pill</span></div>`. Also pin `-- generated.x.go.golden --` on THIS case only (leave the section present but empty; the `-update` run fills it — it pins the `Pill(nil)` zero-attr emission and the `Pill(gsx.Attrs{…})` form).

Exact render.golden whitespace: if the harness renders differently (attribute spacing, self-closing handling), let the FIRST `-update` run write it, then eyeball it against the expectation above; the point that must hold is caller attrs present, one class attribute, zero-attr call renders.

- [ ] **Step 2: Run to verify current failure**

Run: `go test ./internal/corpus -run 'TestCorpus/attrsonly' -count=1`
Expected: FAIL with `undefined: ChipProps` / `undefined: PillProps` diagnostics — today's convention-path behavior, proving the cases exercise the gap.

- [ ] **Step 3: Implement the gate predicate** (in `attrsonly.go`)

```go
// isAttrsOnlyCandidate reports whether a component tag should be resolved as
// a potential attrs-only component value: same-package (non-dotted,
// non-generic) tag that is not component-declared, not byo, not a method,
// not a bare-call nullary candidate, and whose <Name>Props type does not
// exist anywhere in the package. That region is guaranteed to fail today
// (undefined: <Name>Props), so gating it onto the _gsxcompsig probe is a
// pure capability addition. emitProbes and genChildComponent both branch on
// this so emit ≡ probe.
func isAttrsOnlyCandidate(el *gsxast.Element, propFields map[string]map[string]bool, byo *byoData, recvVar, recvTypeName string) bool {
	if !isComponentTag(el.Tag) || strings.Contains(el.Tag, ".") || el.TypeArgs != "" {
		return false
	}
	_, propsType, isMethod := childInvocation(el, byo, recvVar, recvTypeName)
	if isMethod {
		return false
	}
	if _, isByo := byo.isByoStruct(propsType); isByo {
		return false
	}
	if _, known := propFields[propsType]; known {
		return false // component-declared or enumerated props type
	}
	if byo.isNullaryFunc(el.Tag) {
		return false // bare-call candidate keeps its existing probe
	}
	return !byo.hasTypeName(propsType)
}
```

Check the exact field name for type args on `*gsxast.Element` (`grep -n 'TypeArgs' internal/gsxast/*.go`) and the exact name of `byo.isNullaryFunc` (`grep -n 'isNullaryFunc' internal/codegen/byo.go`); adjust spelling to what exists.

- [ ] **Step 4: Probe emission** (in `emitProbes`, `analyze.go` — add an `else if` between the `isBareCallCandidate` branch and the nullary `_ = F()` branch):

```go
} else if isAttrsOnlyCandidate(t, propFields, byo, recvVar, recvTypeName) {
	// Attrs-only component value candidate (no <Name>Props type exists —
	// the convention literal probe would be a guaranteed `undefined:` error).
	// _gsxcompsig(F) carries F's real type to the harvest; the bag expression
	// rides _gsxuseq — the alignment-NEUTRAL keep-alive — because the plain
	// _gsxuse harvest maps calls to interp nodes POSITIONALLY (k-th) and an
	// extra _gsxuse here would corrupt every later interp's harvested type.
	emitSkeletonLine(sb, fset, t.Pos())
	fmt.Fprintf(sb, "_gsxcompsig(%s)\n", t.Tag)
	if expr, _, err := attrsOnlyBagExpr(t, /* probeWrap= */ true, …); err == nil && expr != "" {
		emitSkeletonLine(sb, fset, t.Pos())
		fmt.Fprintf(sb, "_gsxuseq(%s)\n", expr)
	}
	// a bag-build error here is NOT reported from the probe pass: the emit
	// pass builds the same expression and owns the positioned diagnostic.
}
```

The `…` parameters mirror what the existing probe-path `childPropsLiteral` call in this function passes (rtPkg for the skeleton, `table`, `byo`, `fm`, probeWrap=true, nil resolved, the probe scratch buffer). Read the surrounding probe code for the exact values — `attrsOnlyBagExpr`'s signature (Step 5) is designed to take exactly those.

- [ ] **Step 5: Bag-expression helper** (in `emit.go`, next to `childPropsLiteral`):

```go
// attrsOnlyPropsKey is a synthetic props-type key for attrsOnlyBagExpr's
// childPropsLiteral call. It contains a '.' so it can never collide with a
// real same-package <Name>Props key, and never escapes into emitted code.
const attrsOnlyPropsKey = "attrsonly.bag"

// attrsOnlyBagExpr builds the single gsx.Attrs expression for an attrs-only
// component-value call site by reusing childPropsLiteral's fallthrough
// assembly with a synthetic declared-field set of {"Attrs"}: every call-site
// attr is fallthrough; spreads become .Merge(...) links; an attrs={{ }}
// ordered literal targets the bag and merges last — all existing behavior,
// no new merge code. Returns "" when the tag has no attrs at all.
func attrsOnlyBagExpr(el *ast.Element, rtPkg, mergeExpr string, table filterTable, byo *byoData, fm FieldMatcher, probeWrap bool, resolved map[ast.Node]types.Type, b *bytes.Buffer, interpTemp *int) (expr string, usedPkgs map[string]string, err error) {
	synthetic := map[string]map[string]bool{attrsOnlyPropsKey: {"Attrs": true}}
	fields, splat, used, err := childPropsLiteral(el, attrsOnlyPropsKey, rtPkg, mergeExpr, table, synthetic, nil, byo, fm,
		func([]ast.Markup) (string, error) { return "", fmt.Errorf("attrs-only components take no slots") },
		probeWrap, resolved, b, interpTemp)
	if err != nil {
		return "", nil, err
	}
	if splat != "" {
		// cannot happen: the synthetic set has an Attrs bag, so the
		// whole-struct-splat branch is skipped; guard anyway.
		return "", nil, fmt.Errorf("codegen: unexpected splat on attrs-only component <%s>", el.Tag)
	}
	if len(fields) == 0 {
		return "", used, nil
	}
	if len(fields) != 1 || !strings.HasPrefix(fields[0].str, "Attrs: ") {
		return "", nil, fmt.Errorf("codegen: attrs-only bag for <%s> produced unexpected fields %v", el.Tag, fields)
	}
	return strings.TrimPrefix(fields[0].str, "Attrs: "), used, nil
}
```

Caveat to verify while implementing: `childPropsLiteral` may route an attr literally named like a field through `matchField(declared, …)` — with the synthetic set, only the `attrs={{ }}` ordered literal matches "Attrs" (by design, that's the merge-last rule). A bare attr named `attrs` (e.g. `attrs="x"`) — check what `matchField` does with it and pin whichever behavior falls out in the Task 7 corpus case rather than special-casing.

Also verify the `propFieldEntry` post-processing `genChildComponent` normally applies to `fieldEntries` (the `(T, error)` tuple rejection loop at `emit.go:3960+`, hoisting via `oaMergePrefix`): the returned single entry must go through the same tuple check — mirror the loop for this one entry (the bag's values are `any`-typed pairs, but a pipeline stage inside a value can still return a tuple).

- [ ] **Step 6: Emitter branch** (in `genChildComponent`, immediately after the existing `resolved[el].(*types.Signature)` nullary block ending `emit.go:3759`):

```go
if isAttrsOnlyCandidate(el, structFields, byo, recvVar, recvTypeName) {
	if t, probed := resolved[el]; probed {
		if variadic, match := attrsOnlySig(t); match {
			if len(el.Children) > 0 {
				bag.Errorf(el.Pos(), el.End(), "attrsonly-children",
					"component values do not support children — declare a Children slot on a named-struct component instead")
				return false
			}
			expr, usedPkgs, err := attrsOnlyBagExpr(el, rt.rt(), classMergeExpr(mergeExpr, rt), table, byo, fm, false, resolved, b, interpTemp)
			if err != nil {
				if ae, ok := errors.AsType[*attrError](err); ok {
					bag.Errorf(ae.pos, ae.end, ae.code, "%s", ae.msg)
				} else {
					bag.Errorf(el.Pos(), el.End(), "attrsonly-bag", "%s", strings.TrimPrefix(err.Error(), "codegen: "))
				}
				return false
			}
			for _, path := range usedPkgs {
				imports[path] = true
			}
			switch {
			case expr == "" && variadic:
				fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s())\n", el.Tag)
			case expr == "":
				fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(nil))\n", el.Tag)
			case variadic:
				fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(%s...))\n", el.Tag, expr)
			default:
				fmt.Fprintf(b, "\t\t_gsxgw.Node(ctx, %s(%s))\n", el.Tag, expr)
			}
			return true
		}
		bag.Errorf(el.Pos(), el.End(), "attrsonly-bad-type",
			"<%s> is not tag-callable: its type is %s, not func(gsx.Attrs) gsx.Node or func(...gsx.Attr) gsx.Node, and no %sProps struct was found",
			el.Tag, types.TypeString(t, nil), el.Tag)
		return false
	}
	// not harvested: the skeleton already reported the underlying error
	// (e.g. undefined: <Tag>); fall through to the convention emission,
	// which is never reached in a successful generation.
}
```

Match the surrounding code's real parameter names (`structFields` vs `propFields`, `rt.rt()`, `classMergeExpr(mergeExpr, rt)`, `imports` map value type — the convention path at `emit.go:3946+` shows the exact idioms; copy them, including how `usedPkgs` is folded).

- [ ] **Step 7: Regenerate + verify**

```bash
go test ./internal/corpus -run TestCorpus -update
go test ./internal/corpus -run TestCorpus -count=1
go test ./internal/codegen -count=1
```
All green. Inspect `func_named_attrs.txtar`'s now-filled `generated.x.go.golden`: it must show `Pill(nil)` for the zero-attr call and a single `gsx.Attrs{…}` bag argument (under the `_gsxrt` alias) for the other — no `PillProps` anywhere.

- [ ] **Step 8: Full-suite regression gate**

Run: `make check` → green. The gated region was previously guaranteed-failing, so NO existing golden may change; `git status` must show only the two new txtar files and `coverage.golden`. If any other golden changed, the gate is wrong — stop and re-examine `isAttrsOnlyCandidate`.

- [ ] **Step 9: Commit** — `git commit -m "feat(codegen): attrs-only component values — same-package gate, probe, emit"`.

---

### Task 4: generality corpus — factory var and alias spellings

No production code expected; these cases prove the go/types route delivers what the syntactic closed list could not. If either fails, the fix belongs in Task 3's code, not in new special cases.

**Files:**
- Create: `internal/corpus/testdata/cases/attrsonly/factory_var.txtar`, `internal/corpus/testdata/cases/attrsonly/alias_type.txtar`

- [ ] **Step 1: `factory_var.txtar`** — the icons pattern, adapter in a sibling `.go` file (sibling-`.go` support per `cases/byo/external_struct_node_attrs.txtar`):

```
# attrs-only value recognized through a factory-initialized var: go/types
# infers the var's type; no syntactic initializer-chasing exists or is needed.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type iconProps struct {
	Name  string
	Attrs gsx.Attrs
}

component renderIcon(p iconProps) {
	<svg { gsx.Attrs{{Key: "class", Value: "w-5 h-5"}}.Merge(p.Attrs)... }>{p.Name}</svg>
}

component Page() {
	<div>
		<HomeIcon class="h-3 w-3"/>
		<HomeIcon/>
	</div>
}
-- icons.go --
package views

import "github.com/gsxhq/gsx"

func namedIcon(name string) func(gsx.Attrs) gsx.Node {
	return func(attrs gsx.Attrs) gsx.Node {
		return renderIcon(iconProps{Name: name, Attrs: attrs})
	}
}

var HomeIcon = namedIcon("house")
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<div><svg class="w-5 h-5 h-3 w-3">house</svg><svg class="w-5 h-5">house</svg></div>
```

This also pins the spec's worked-example fidelity claim: ONE class attribute, default tokens first (probe-verified 2026-07-10 on main via the byo path).

- [ ] **Step 2: `alias_type.txtar`** — the deferred note's spelling, now valid:

```
# The userland alias spelling from the element-literals deferred note:
# aliases are transparent to go/types, so this is recognized with no
# alias-specific code.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type Component = func(...gsx.Attr) gsx.Node

type thingProps struct {
	Label string
	Attrs gsx.Attrs
}

component renderThing(p thingProps) {
	<span { p.Attrs... }>{p.Label}</span>
}

func namedThing(label string) Component {
	return func(attrs ...gsx.Attr) gsx.Node {
		return renderThing(thingProps{Label: label, Attrs: gsx.Attrs(attrs)})
	}
}

var Hello = namedThing("hello")

component Page() {
	<Hello class="w"/>
}
-- invoke --
Page()
-- diagnostics.golden --
-- render.golden --
<span class="w">hello</span>
```

- [ ] **Step 3: Regen + verify + commit**

```bash
go test ./internal/corpus -run TestCorpus -update && go test ./internal/corpus -run TestCorpus -count=1
git add internal/corpus/testdata && git commit -m "test(corpus): attrs-only factory-var and alias acceptance cases"
```
If either case FAILS before regen, debug Task 3's gate/harvest first (likely suspects: `hasTypeName` false-positives, `types.Unalias` missing on some path).

---

### Task 5: rejection diagnostics

**Files:**
- Create: `internal/corpus/testdata/cases/attrsonly/reject_unnamed_slice.txtar`, `reject_error_result.txtar`, `reject_children.txtar`, `reject_undefined.txtar`

All four are diagnostics-only cases (no `-- invoke --`, no render.golden; format per `cases/diagnostics/noprops_component_with_attrs.txtar`).

- [ ] **Step 1: Write all four failing cases**

`reject_unnamed_slice.txtar` (the deliberately excluded shape):

```
# func([]gsx.Attr) gsx.Node — the unnamed-slice assignability accident — is
# NOT tag-callable: the harvest requires the NAMED gsx.Attrs. New clean
# diagnostic (this region used to be a raw `undefined: BadProps`).
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

var Bad = func(attrs []gsx.Attr) gsx.Node { return nil }

component Page() {
	<Bad class="x"/>
}
-- diagnostics.golden --
8:2: <Bad> is not tag-callable: its type is func(attrs []github.com/gsxhq/gsx.Attr) github.com/gsxhq/gsx.Node, not func(gsx.Attrs) gsx.Node or func(...gsx.Attr) gsx.Node, and no BadProps struct was found
```

The exact `types.TypeString` rendering (package-path qualification) and position will differ — let `-update` write the golden, then verify it names the type and the missing struct. If the fully-qualified type string is unreadable, switch the diagnostic to `types.TypeString(t, types.RelativeTo(pkg))` in Task 3's code and regen.

`reject_error_result.txtar`: same shape with `func Bad2(attrs gsx.Attrs) (gsx.Node, error)` declared in a sibling `-- bad.go --`, tag `<Bad2 class="x"/>`, same diagnostic family.

`reject_children.txtar`: reuse Task 3's `Chip` (variadic func) with `<Chip>text</Chip>` → diagnostic `component values do not support children — declare a Children slot on a named-struct component instead`.

`reject_undefined.txtar`: `<NopeIcon class="x"/>` with no declaration anywhere → the probe's own positioned `undefined: NopeIcon` (pins that a missing identifier did not get WORSE under the gate).

- [ ] **Step 2: Verify each fails informatively before regen** — `go test ./internal/corpus -run 'TestCorpus/attrsonly' -count=1`, then `-update`, then verify without `-update`. Read every diagnostics.golden the update wrote; each must match the intent above (a wrong-but-passing golden is the failure mode here).

- [ ] **Step 3: Commit** — `git commit -m "test(corpus): attrs-only rejection diagnostics (unnamed slice, error result, children, undefined)"`.

---

### Task 6: merge-order corpus case

**Files:**
- Create: `internal/corpus/testdata/cases/attrsonly/merge_order.txtar`

- [ ] **Step 1: Write the case** — every attr form on one attrs-only tag, pinning one bag in source order with the ordered literal last (spec §"ordered-attrs literal and merge order"):

```
# All call-site attr forms merge into ONE bag in source order; the
# attrs={{ }} ordered literal merges last regardless of position.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type boxProps struct {
	Attrs gsx.Attrs
}

component renderBox(p boxProps) {
	<div { p.Attrs... }>box</div>
}

func Box(attrs gsx.Attrs) gsx.Node {
	return renderBox(boxProps{Attrs: attrs})
}

component Page(on bool, extra gsx.Attrs) {
	<Box data-a="1" attrs={{ "data-z": "9" }} { extra... } { if on { data-c="3" } } data-b="2"/>
}
-- invoke --
Page(PageProps{On: true, Extra: gsx.Attrs{{Key: "data-x", Value: "8"}}})
-- diagnostics.golden --
-- render.golden --
<div data-a="1" data-x="8" data-c="3" data-b="2" data-z="9">box</div>
```

Check the corpus invoke convention for components with params (`grep -rn 'Props{' internal/corpus/testdata/cases/fallthrough/*.txtar` shows the pattern, e.g. `Card(CardProps{Attrs: …})`). If cond-attr placement inside a component tag needs different syntax, mirror an existing `cond_attr_*` case's spelling.

- [ ] **Step 2: Regen, verify the rendered ORDER matches the merge contract** (base entries + spreads in source order via `.Merge`, ordered literal last). If the actual order differs but is consistent with the documented existing rule (`attributes.md` §"Targeting the synthesized attrs bag"), accept the golden — the point is pinning, not inventing new order. If it differs from the DOCUMENTED rule, stop: that's a bug to raise, not pin.

- [ ] **Step 3: Commit** — `git commit -m "test(corpus): attrs-only call-site merge-order pin"`.

---

### Task 7: cross-package (dotted-tag) support

**Files:**
- Modify: `internal/codegen/attrsonly.go` (gate: dotted variant)
- Modify: `internal/codegen/analyze.go:2048-2077` (harvest: accept `*ast.SelectorExpr` probe args, key `sigByName` by full `pkg.Name` tag string)
- Modify: wherever the per-file qualified facts merge happens (`fileScopedFacts` — find via `grep -n fileScopedFacts internal/codegen/*.go`) so the gate can consult the DEP package's `typeNames`/`nullaryFuncs` for a dotted tag
- Create: `internal/corpus/testdata/cases/attrsonly/imported.txtar`, `internal/corpus/testdata/cases/attrsonly/reject_field_callee.txtar`

**Interfaces:**
- Consumes: `depPropFacts.byo.typeNames` (populated since Task 2).

- [ ] **Step 1: Write the failing imported case** (multi-package txtar per `cases/xpkg/cross_package.txtar`: needs `-- go.mod --` with `module example.com/app`, subdir files, imports rewritten by the harness):

```
-- go.mod --
module example.com/app

go 1.26
-- ui/icons.gsx --
package ui

import "github.com/gsxhq/gsx"

type iconProps struct {
	Name  string
	Attrs gsx.Attrs
}

component renderIcon(p iconProps) {
	<svg { p.Attrs... }>{p.Name}</svg>
}

func namedIcon(name string) func(gsx.Attrs) gsx.Node {
	return func(attrs gsx.Attrs) gsx.Node {
		return renderIcon(iconProps{Name: name, Attrs: attrs})
	}
}

var HomeIcon = namedIcon("house")
-- pages/home.gsx --
package pages

import "example.com/app/ui"

component Home() {
	<ui.HomeIcon class="h-3 w-3"/>
}
-- invoke --
pages.Home()
-- diagnostics.golden --
-- render.golden --
<svg class="h-3 w-3">house</svg>
```

Run `go test ./internal/corpus -run 'TestCorpus/attrsonly' -count=1` → must FAIL (dotted tags are not gated yet); note the exact current failure text.

- [ ] **Step 2: Extend the gate for dotted tags.** In `isAttrsOnlyCandidate`, replace the `strings.Contains(el.Tag, ".")` early-false with: split `pkgAlias.Name`; resolve the alias through the same per-file import mapping the dotted convention path uses (find it: `grep -n 'depPropFacts\|qualified' internal/codegen/analyze.go | head` and read how `fileScopedFacts` qualifies keys like `"ui.XxxProps"`); the gate then requires (a) the alias resolves to a project-internal dep dir with facts available, (b) `propFields["ui.XxxProps"]`-style qualified lookup misses, (c) dep facts' `byo.hasTypeName("XxxProps")` false, (d) dep `byo.isNullaryFunc("Name")` false. A qualifier that is NOT a known package import (a local/receiver/field like `item.Icon`) fails (a) and is never gated — preserving today's behavior exactly.

The gate's signature will need the per-file facts context that carries dep facts — thread whatever `emitProbes`/`genChildComponent` already have in scope for dotted-tag handling (both passes must see identical data; if only one has it, that's the emit≡probe risk to fix, not work around).

- [ ] **Step 3: Extend the harvest.** In the `sigByName` block (`analyze.go:2055-2069`), accept selector args:

```go
var key string
switch a := call.Args[0].(type) {
case *goast.Ident:
	key = a.Name
case *goast.SelectorExpr:
	if x, ok := a.X.(*goast.Ident); ok {
		key = x.Name + "." + a.Sel.Name
	}
}
if key == "" { return true }
if tv, ok := info.Types[call.Args[0]]; ok && tv.Type != nil {
	sigByName[key] = tv.Type
}
```

`forEachComponentTagElement` matching by `el.Tag` string already works for dotted tags — verify by reading it.

- [ ] **Step 4: Probe emission for dotted tags** — the Task 3 probe branch already prints `t.Tag`, which for a dotted tag is `ui.HomeIcon`; confirm the skeleton file imports the dep under that alias in this context (the convention literal probe for dotted tags already relies on the same import — read how it's ensured, mirror it).

- [ ] **Step 5: `reject_field_callee.txtar`** — pin that a non-import qualifier is untouched:

```
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

type holder struct {
	Icon func(gsx.Attrs) gsx.Node
}

component Page(item holder) {
	<item.Icon class="x"/>
}
-- diagnostics.golden --
(let -update pin today's failure text — the case exists to prove this
region did NOT silently become the attrs-only path)
```

Before regen, check what today's failure is (run the case on the pre-change code if unsure — `git stash` the codegen edits, run, unstash). The golden must show the SAME failure class after the change.

- [ ] **Step 6: Regen all, verify, full `make check`.** Again `git status`: only `attrsonly/` txtar + coverage.golden may change.

- [ ] **Step 7: Commit** — `git commit -m "feat(codegen): attrs-only component values across packages (dotted tags)"`.

---

### Task 8: LSP go-to-definition for attrs-only tags

**Files:**
- Modify: `internal/lsp/definition.go` (`componentTagDeclAt` / dispatch) and/or `internal/codegen/crossnav.go` (CrossIndex build)
- Test: alongside existing definition tests (`grep -rln componentTagDeclAt internal/lsp/*_test.go`)

Two acceptable outcomes; decide after Step 1's reading, do not force outcome A if it balloons.

- [ ] **Step 1: Read the wiring.** `componentTagDeclAt` keys off `pkg.CrossIndex["."+tag]`; read how CrossIndex entries are built (`internal/codegen/crossnav.go`) and whether package-level `var`/`func` decls from GoChunks/sibling files are reachable (the FileSymbols extractor from the LSP-symbols work already maps GoChunk decls by byte offset).

- [ ] **Step 2 (outcome A, preferred): implement.** Add attrs-only callees to the index (or a parallel lookup): same-package tag → the `var`/`func` decl position (GoChunk offset or sibling-`.go` position); dotted tag → dep package decl via the existing cross-package nav machinery. Write the failing LSP test first (cursor on `<HomeIcon` in a fixture → expect the `var HomeIcon` decl location), then implement, then `go test ./internal/lsp -count=1`.

- [ ] **Step 2 (outcome B, fallback): document the gap.** If outcome A requires reshaping CrossIndex, add to `docs/ROADMAP.md`'s known-gaps list: "LSP go-to-definition on attrs-only component-value tags (`<HomeIcon/>` → var/func decl) — not wired; recognition shipped in the attrs-only component-values PR." and note it in the PR description. Hover follows whichever outcome gd got.

- [ ] **Step 3: Commit** — `git commit -m "feat(lsp): go-to-definition for attrs-only component tags"` (or `docs(roadmap): …` for outcome B).

---

### Task 9: documentation

**Files:**
- Modify: `docs/guide/syntax/props.md` (three-row table → four)
- Modify: `docs/guide/syntax/composition.md` (tag-callable values paragraph)
- Modify: `docs/guide/syntax/attributes.md` (cross-reference)
- Modify: `docs/ROADMAP.md` (mark the deferred component-values item shipped; add the forwarding-through-component-calls follow-up from the spec's Alternatives section)

- [ ] **Step 1: props.md** — add the fourth row to the model table:

```markdown
| **Attrs-only func value** — a package-level `var`/`func` of type `func(gsx.Attrs) gsx.Node` or `func(...gsx.Attr) gsx.Node` | **Component value** — no props struct; every call-site attribute merges into one `gsx.Attrs` bag | `HomeIcon(gsx.Attrs{{Key: "class", Value: "w-5 h-5"}})` |
```

Follow with a short section (place after "Whole-struct splat") covering: recognition is by static type (any initializer, aliases fine); both accepted signatures and why (zero-arg direct calls vs method access — condensed from the spec); every attribute is fallthrough, `attrs={{ }}` merges last; no children (link the diagnostic); the factory/adapter example (the spec's corrected `renderNamedIcon` example, shortened). Any literal `{{ }}` in prose goes inside `::: v-pre`.

- [ ] **Step 2: composition.md** — after the cross-package paragraph, add: a tag may also resolve to a package-level *value* of the attrs-only shape; link to props.md's new section.

- [ ] **Step 3: attributes.md** — one sentence in the spread section: on an attrs-only component value every attribute (named, spread, conditional, ordered literal) lands in the single bag argument; link props.md.

- [ ] **Step 4: Verify docs build hazards** — `grep -n '{{' docs/guide/syntax/props.md docs/guide/syntax/composition.md docs/guide/syntax/attributes.md` — every hit inside `::: v-pre` or a code fence.

- [ ] **Step 5: Commit** — `git commit -m "docs: attrs-only component values (props model 4, composition, attributes)"`.

---

### Task 10: final validation + real-world check

- [ ] **Step 1:** `make ci` in the worktree → green. `make lint` → green.
- [ ] **Step 2:** Confirm zero drift outside intended files: `git diff main --stat` shows only `internal/codegen`, `internal/corpus/testdata` (attrsonly cases + coverage.golden), `internal/lsp` (if outcome A), `docs/`, and the spec/plan.
- [ ] **Step 3 (real-world, requires `~/work/one-learning-gsx`):** In that tree (branch `gsx-migration`), collapse `ui/icons.gsx` per the spec's worked example — one `iconProps`/`renderNamedIcon` (with the explicit `twcfg.Merge([]string{"w-5 h-5", p.Attrs.Class()})` class + `{ p.Attrs.Without("class")... }` spread) + 62 one-line adapter `var`s in `ui/icons.go` — using the worktree's gsx binary (`go run <worktree>/cmd/gsx generate`). Then `go build ./...` and `go test ./ui/...`. Do NOT commit that repo's changes as part of this PR; report results. If this exposes a gap, fix it here with a new corpus case first.
- [ ] **Step 4:** Independent adversarial review (project convention): dispatch a reviewer that builds throwaway probe programs against the worktree binary — minimum probes: shadowed import alias for gsx (`import g "github.com/gsxhq/gsx"` in the icon package), an attrs-only value colliding with a same-name type in ANOTHER file, a generic factory, an attrs-only tag inside a `{ for }` loop and inside an element-literal Go expression, interp-heavy file (the `_gsxuseq` alignment claim), `gsx fmt` idempotence over the new corpus inputs.
- [ ] **Step 5:** PR via `superpowers:finishing-a-development-branch`: title "feat: attrs-only component values (`gsx.Component` un-deferred)", body summarizing spec §Proposal + the sibling-projects note ("no surface syntax changes — tree-sitter-gsx / vscode-gsx / CodeMirror unaffected").
