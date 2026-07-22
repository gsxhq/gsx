package codegen

import (
	"crypto/sha256"
	"errors"
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"go/types"
	"maps"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/exp/typeparams"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/goexprshape"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
)

var errSkipComponent = errors.New("skip component")

// materializeEmbeddedMarkup expands every operand-position element/fragment
// and prefixed literal carried inside an interpolation or Go block. It is the
// mutation primitive used only by preprocessComponentCallSites: classification
// and call-site allocation deliberately happen in later package-wide stages.
// Existing Embedded slices are never replaced, so all later phases retain the
// exact same node pointers. A seed with no embedded construct keeps Embedded
// nil and follows the existing verbatim path. A failed split records positioned
// parser diagnostics, leaves that Embedded field nil, and makes preprocessing
// fail closed before later analysis stages.
//
// Materialized markup can recursively contain another splittable expression,
// so the walk covers every []Markup-bearing field plus Interp.Embedded and
// GoBlock.Embedded. The following preprocessing stage runs JSX classification
// over this complete expanded tree.
func materializeEmbeddedMarkup(file *gsxast.File, cls *attrclass.Classifier, fset *token.FileSet, bag *diag.Bag) bool {
	var walk func([]gsxast.Markup)
	var walkParts func([]gsxast.GoPart)
	syntaxOK := true
	maySplit := func(src string) bool {
		if strings.ContainsRune(src, '<') {
			return true
		}
		if !strings.ContainsAny(src, "`\"") {
			return false
		}
		_, ok := gsxparser.ContainsEmbeddedLiteral(src)
		return ok
	}

	splitInterp := func(interp *gsxast.Interp) {
		if interp.Embedded != nil || !maySplit(interp.Expr) || !interp.ExprPos.IsValid() {
			return
		}
		parts, errs := gsxparser.SplitGoExprElements(fset, interp.Expr, interp.ExprPos, cls)
		if len(errs) > 0 {
			syntaxOK = false
			for _, err := range errs {
				bag.Report(err.Pos, err.End, diag.Error, "parse-error", "parser", "%s", err.Msg)
			}
			return
		}
		if len(parts) == 0 {
			return
		}
		interp.Embedded = parts
	}
	splitGoBlock := func(block *gsxast.GoBlock) {
		if block.Embedded != nil {
			if block.UnsupportedMarkup == nil {
				block.UnsupportedMarkup = firstDirectGoBlockMarkup(block.Embedded)
			}
			return
		}
		if !maySplit(block.Code) || !block.CodePos.IsValid() {
			return
		}
		parts, errs := gsxparser.SplitGoExprElements(fset, block.Code, block.CodePos, cls)
		if unsupported := firstDirectGoBlockMarkup(parts); unsupported != nil {
			block.Embedded = parts
			block.UnsupportedMarkup = unsupported
			return
		}
		if len(errs) > 0 {
			syntaxOK = false
			for _, err := range errs {
				bag.Report(err.Pos, err.End, diag.Error, "parse-error", "parser", "%s", err.Msg)
			}
			return
		}
		if len(parts) == 0 {
			return
		}
		block.Embedded = parts
	}
	walkParts = func(parts []gsxast.GoPart) {
		for _, part := range parts {
			if markup, ok := part.(gsxast.Markup); ok {
				walk([]gsxast.Markup{markup})
			}
		}
	}
	walk = func(nodes []gsxast.Markup) {
		for _, node := range nodes {
			switch node := node.(type) {
			case *gsxast.Interp:
				splitInterp(node)
				walkParts(node.Embedded)
			case *gsxast.EmbeddedInterp:
				walk(node.Segments)
			case *gsxast.Element:
				walkMarkupAttrs(node.Attrs, walk)
				walk(node.Children)
			case *gsxast.Fragment:
				walk(node.Children)
			case *gsxast.ForMarkup:
				walk(node.Body)
			case *gsxast.IfMarkup:
				walk(node.Then)
				walk(node.Else)
			case *gsxast.SwitchMarkup:
				for _, clause := range node.Cases {
					walk(clause.Body)
				}
			case *gsxast.GoBlock:
				splitGoBlock(node)
				if node.UnsupportedMarkup == nil {
					walkParts(node.Embedded)
				}
			}
		}
	}

	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *gsxast.Component:
			walk(decl.Body)
		case *gsxast.GoWithElements:
			walkParts(decl.Parts)
		}
	}
	return syntaxOK
}

// buildSkeleton synthesizes a Go file standing in for the gsx file during type
// resolution: the file's Go chunks, each component's exact authored signature,
// and probe bodies for expressions and component calls.
//
// buildSkeleton is read-only with respect to the gsx AST. Every production
// caller that requests skeletonFull must first run
// preprocessComponentCallSites for its parsed package; that single pass owns
// embedded-markup materialization and component classification.
type skeletonMode uint8

const (
	skeletonFull skeletonMode = iota
	skeletonDeclarations
	skeletonTargetDiscovery
	skeletonTargetDeclarations
)

// splitFileGoSource is the single hoisting boundary shared by shipping and
// target-only skeletons. It preserves every non-import GoChunk byte and records
// exact authored import/body positions; target discovery must not grow an
// independent interpretation of pass-through Go.
func splitFileGoSource(file *gsxast.File, fset *token.FileSet) ([]importSpec, []goBody, error) {
	var imports []importSpec
	var bodies []goBody
	for _, d := range file.Decls {
		gc, ok := d.(*gsxast.GoChunk)
		if !ok {
			continue
		}
		imps, body, bodyOff, err := splitChunk(gc.Src)
		if err != nil {
			return nil, nil, err
		}
		if fset != nil && gc.Pos().IsValid() {
			if tf := fset.File(gc.Pos()); tf != nil {
				base := fset.Position(gc.Pos()).Offset
				for i := range imps {
					imps[i].pos = tf.Pos(base + imps[i].srcOff)
					if imps[i].nameOff >= 0 {
						imps[i].namePos = tf.Pos(base + imps[i].nameOff)
					}
					if imps[i].pathOff >= 0 {
						imps[i].pathPos = tf.Pos(base + imps[i].pathOff)
					}
				}
			}
		}
		imports = append(imports, imps...)
		if body != "" {
			bodies = append(bodies, goBody{src: body, pos: gc.Pos() + token.Pos(bodyOff)})
		}
	}
	return imports, bodies, nil
}

// declarationOnlyGoWithElementsSource replaces every function body in one
// reconstructed GoWithElements region with a terminating built-in call. The
// declaration resolver needs exact signatures and package-level initializer
// types, but function implementation bodies are deliberately outside that
// non-circular universe. Replacing bodies structurally also keeps the resulting
// syntax valid under normal body checking: locals used only by removed markup
// cannot become synthetic "declared and not used" errors, while companion Go
// and ordinary GoChunk bodies remain fully checked.
func declarationOnlyGoWithElementsSource(source string) (string, error) {
	const header = "package _gsxdecl\n"
	fullSource := header + source
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", fullSource, parser.SkipObjectResolution)
	if err != nil {
		return "", fmt.Errorf("codegen: parse declaration-only Go-with-elements source: %w", err)
	}
	tokenFile := fset.File(file.Pos())
	if tokenFile == nil {
		return "", fmt.Errorf("codegen: declaration-only Go-with-elements source has no token file")
	}
	type bodySpan struct {
		start token.Pos
		end   token.Pos
	}
	var bodies []bodySpan
	goast.Inspect(file, func(node goast.Node) bool {
		var body *goast.BlockStmt
		switch node := node.(type) {
		case *goast.FuncDecl:
			body = node.Body
		case *goast.FuncLit:
			body = node.Body
		default:
			return true
		}
		if body != nil {
			bodies = append(bodies, bodySpan{start: body.Pos(), end: body.End()})
		}
		// The outer body is replaced wholesale, so nested functions must not
		// produce overlapping edits.
		return false
	})
	sort.Slice(bodies, func(i, j int) bool { return bodies[i].start > bodies[j].start })
	for _, body := range bodies {
		start := tokenFile.Offset(body.start)
		end := tokenFile.Offset(body.end)
		if start < len(header) || end < start || end > len(fullSource) {
			return "", fmt.Errorf("codegen: invalid declaration-only function-body span %d:%d", start, end)
		}
		var replacement strings.Builder
		replacement.WriteString(`{ panic("gsx declaration body") }`)
		// Re-synchronize following authored syntax after changing the physical
		// body width. The block form preserves same-line tokens without invoking
		// semicolon insertion.
		emitSkeletonBlockLine(&replacement, fset, body.end)
		fullSource = fullSource[:start] + replacement.String() + fullSource[end:]
	}
	return fullSource[len(header):], nil
}

func buildSkeleton(file *gsxast.File, table funcTables, fset *token.FileSet, bag *diag.Bag, plan *componentTargetPlan, mode skeletonMode) (string, []*gsxast.Component, []importSpec, map[gsxast.Node]int, [][]gsxast.Markup, error) {
	build, err := buildSkeletonResult(file, table, fset, bag, plan, mode, newUnmappedSkeletonSourceWriter())
	return build.source, build.components, build.imports, build.ctrlStarts, build.markupGroups, err
}

func buildMappedSkeleton(file *gsxast.File, table funcTables, fset *token.FileSet, bag *diag.Bag, plan *componentTargetPlan, mode skeletonMode, sourcePath string, source []byte) (skeletonBuild, error) {
	build, err := buildSkeletonResult(file, table, fset, bag, plan, mode, newSkeletonSourceWriter(sourcePath, source))
	if err != nil {
		return skeletonBuild{}, err
	}
	build.sourceHash = sha256.Sum256(source)
	return build, nil
}

func buildSkeletonResult(file *gsxast.File, table funcTables, fset *token.FileSet, bag *diag.Bag, plan *componentTargetPlan, mode skeletonMode, recorder *skeletonSourceWriter) (skeletonBuild, error) {
	source, components, imports, ctrlStarts, markupGroups, err := buildSkeletonWithRecorder(file, table, fset, bag, plan, mode, recorder)
	if err != nil {
		return skeletonBuild{}, err
	}
	finished, sourceMap, err := recorder.finish()
	if err != nil {
		return skeletonBuild{}, err
	}
	if source != finished {
		return skeletonBuild{}, fmt.Errorf("codegen: skeleton recorder changed assembled bytes")
	}
	return skeletonBuild{
		source:       source,
		components:   components,
		imports:      imports,
		ctrlStarts:   ctrlStarts,
		markupGroups: markupGroups,
		sourceMap:    sourceMap,
	}, nil
}

func buildSkeletonWithRecorder(file *gsxast.File, table funcTables, fset *token.FileSet, bag *diag.Bag, plan *componentTargetPlan, mode skeletonMode, recorder *skeletonSourceWriter) (string, []*gsxast.Component, []importSpec, map[gsxast.Node]int, [][]gsxast.Markup, error) {
	var comps []*gsxast.Component
	for _, d := range file.Decls {
		if c, ok := d.(*gsxast.Component); ok {
			comps = append(comps, c)
		}
	}

	// Split every GoChunk into its imports (hoisted ahead of all declarations)
	// and its non-import body. Each body carries the .gsx source position of its
	// first byte so it can be emitted under a //line directive — go-to-definition
	// on a user-declared top-level Go symbol (a helper func/type/var in the .gsx)
	// then resolves back to the .gsx instead of the synthetic overlay.
	imports, bodies, err := splitFileGoSource(file, fset)
	if err != nil {
		return "", nil, nil, nil, nil, err
	}

	// Emit each component's probe body into a temp buffer, accumulating the
	// filter packages the probes actually reference (alias→pkgPath). The probes
	// reference <alias>.<Func>, so the skeleton must import each USED filter
	// package under the SAME reserved alias the emitter uses — driven by the same
	// lowerPipe report so probe (skeleton) and emit agree on which packages and
	// aliases are in play. Only USED packages are imported (an unused import fails
	// the skeleton type-check).
	usedFilters := map[string]string{} // alias -> pkgPath
	compBuf := recorder.child()
	// ctrlOff maps each control-flow node (ForMarkup/IfMarkup/GoBlock, and each
	// value-form if condition's *ValueIf) to the
	// byte offset of its clause/cond/code text within the final skeleton string.
	// Offsets are recorded relative to compBuf during emitComponentSkeleton and
	// adjusted below (after the prefix is assembled into sb) to be file-relative.
	ctrlOff := map[gsxast.Node]int{}
	// gwMarkups collects, in source order, the markup list each embedded value's
	// IIFE probes — from BOTH top-level GoWithElements decls (the loop below) AND
	// <tag>/<> literals embedded inside a `{ }` interpolation (emitProbes' Interp
	// case, via the pre-pass that populates ast.Interp.Embedded). Declared here so
	// both producers append to the SAME flat slice; each IIFE carries an
	// `_gsxelem(N)` marker where N is its index, which harvestEmbeddedElements uses
	// to resolve the probe back onto the markup's nodes (order-independent).
	var gwMarkups [][]gsxast.Markup
	// Keep only the components whose skeletons succeed. A validation error
	// (errSkipComponent — reserved param/recv, parse failure) means the component
	// is invalid for codegen; skip its skeleton so the overall file stays valid Go.
	// genComponent will re-encounter the same error at emit time and record a
	// positioned diagnostic via the bag. Any OTHER error is a real infrastructure
	// failure and must abort the whole skeleton build.
	var validComps []*gsxast.Component
	for _, c := range comps {
		emission := componentTargetEmission{public: true}
		if plan != nil {
			var ok bool
			emission, ok = plan.emission(c)
			if !ok {
				return "", nil, nil, nil, nil, fmt.Errorf("codegen: component %s is absent from the package component plan", c.Name)
			}
		}
		declaration, err := componentDeclarationFor(c)
		if err != nil {
			continue
		}
		if err := emitComponentSkeleton(compBuf, c, declaration, table, usedFilters, fset, ctrlOff, &gwMarkups, bag, mode, emission); err != nil {
			if errors.Is(err, errSkipComponent) {
				// Validation failure: skip this component's skeleton; it will fail
				// again (with a positioned diagnostic) during generateFile.
				continue
			}
			return "", nil, nil, nil, nil, err
		}
		validComps = append(validComps, c)
		if plan != nil {
			emission.parsedDeclaration = declaration
			emission.declarationParsed = true
			plan.emissions[c] = emission
		}
	}
	comps = validComps

	// GoWithElements: a top-level Go region with one or more gsx elements
	// embedded directly in expression position (e.g. `var help = <a
	// href={u}>{ label }</a>`, or `func H(label string) gsx.Node { return
	// <div>{ label }</div> }`) — see ast.GoWithElements's doc and emit.go's
	// *ast.GoWithElements case (Task 4). Mirror emitElementValue's lowering on
	// the probe side, INLINE: the region's Go text is spliced verbatim and
	// each *Element Part is replaced, IN PLACE, by an immediately-invoked
	// func literal (IIFE) of type _gsxrt.Node:
	//
	//	func() _gsxrt.Node { _gsxelem(N); var ctx _gsxctx.Context; _ = ctx; <probes>; return nil }()
	//
	// so the surrounding Go construct (a var initializer, a return statement,
	// a call argument, …) still sees a real _gsxrt.Node-typed expression
	// there and type-checks. The IIFE's OWN body probes the element's
	// interpolations/props via emitProbes — the EXACT same machinery a
	// component body's child elements use — so a wrong-typed interpolation or
	// component-tag prop is caught exactly as inside a component.
	//
	// INLINE (not a hoisted top-level func) is load-bearing for emit≡probe:
	// an embedded interpolation can reference the SURROUNDING lexical scope —
	// a func parameter, a local variable, or a method receiver (`func H(label
	// string) … { return <div>{ label }</div> }`). A separate top-level probe
	// func would NOT see those names → a false `undefined: label` that blocks
	// generation. Because the IIFE is spliced inline inside the region's
	// (verbatim-spliced) surrounding Go, it captures params/locals/receiver by
	// ordinary Go closure capture, exactly as emit.go's real gsx.Func closure
	// does. The inner `var ctx _gsxctx.Context` mirrors the real closure's
	// `ctx context.Context` param (so an interp referencing `ctx` type-checks,
	// and — like the real closure — shadows any outer `ctx`); the OUTER scope
	// remains visible for every other name.
	//
	// Everything (verbatim Go + inline IIFEs) is written into compBuf (the
	// SAME temp builder emitComponentSkeleton just filled), so the single
	// compBufStart adjustment below (already needed for component skeletons)
	// also re-bases every ctrlOff/registry.spans entry emitProbes records
	// while probing an embedded element — no separate adjustment pass needed.
	// Each verbatim GoText Part is preceded by a `/*line*/` BLOCK-form
	// directive to its .gsx position (so a type error in the surrounding Go
	// maps back to source, and the synthetic IIFE lines injected before it
	// don't shift the mapping). The block form — not `//line` — is required:
	// an element can sit mid-expression (`Wrap(<Foo/>)`), where the trailing
	// GoText (`)`) must attach to the IIFE's `}()` on the SAME line; a
	// `//line` newline there would trigger Go's automatic semicolon insertion
	// after `}()` ("missing ',' before newline in argument list"). The block
	// comment carries no newline, so it re-syncs the position without breaking
	// the enclosing call/operand syntax.
	//
	// gwMarkups collects, in source order, the markup list each embedded
	// value's IIFE probes: one *gsxast.Element becomes a single-element
	// []Markup{p}; a *gsxast.Fragment contributes its whole Children list (a
	// fragment probes as one IIFE over all its children, so interps inside it
	// resolve against the enclosing scope exactly like an element's do).
	// Index N is the argument the corresponding IIFE's `_gsxelem(N)` marker
	// carries. The caller hands this slice to harvestEmbeddedElements, which
	// finds each marked IIFE in the type-checked skeleton and harvests its
	// _gsxuse/_gsxuseq/_gsxcompsig/inference-probe results back onto the
	// markup's nodes (via the shared harvestBody). The marker is what lets
	// harvest distinguish an embedded-value probe IIFE from any func literal
	// the user wrote verbatim in the surrounding Go.
	//
	// Unlike GoChunk, this does NOT run splitChunk to hoist an `import` spec out
	// of a GoText Part ahead of the skeleton's own import block — mirrors emit.go's
	// identical choice (see its *ast.GoWithElements case comment): splitChunk
	// cannot parse text containing markup. It does not need to: the parser peels a
	// leading run of import declarations off this region into its own GoChunk
	// before the region becomes a GoWithElements (parser/goexpr.go
	// leadingImportEnd), so a func returning an element whose return type needs a
	// user-imported package is hoisted through the normal GoChunk path. Only a
	// stray `import` placed AFTER a non-import declaration in the region can reach
	// here — that is invalid Go, and surfaces as a skeleton parse/type error,
	// never silently-broken output.
	for _, d := range file.Decls {
		we, ok := d.(*gsxast.GoWithElements)
		if !ok {
			continue
		}
		declarationGeneratedStart := compBuf.Len()
		// gsx fmt may have wrapped a bare-operand element/fragment in a
		// decorative "(" ")" purely for source readability (see internal/
		// printer's parenWrapDoc) — never a call argument or bare
		// composite-literal element, where the parens are real call/list
		// syntax. Those bytes must not reach the skeleton's spliced-in IIFE
		// any more than emit.go's real closure splice (see its identical
		// paren-strip and goWithElementsParenShapes' doc): a newline before
		// the IIFE's own trailing `}()` trips the exact ASI hazard
		// emitSkeletonBlockLine's block-form directive already works around
		// for the unrelated `Wrap(<Foo/>)` case below.
		var goWithElementsBuf skeletonWriter = compBuf
		var declarationBuf strings.Builder
		if mode == skeletonDeclarations {
			goWithElementsBuf = &declarationBuf
		}
		shapes := goWithElementsParenShapes(we)
		for i, part := range we.Parts {
			switch p := part.(type) {
			case gsxast.GoText:
				emitted := targetGoWithElementsText(we, shapes, i, p)
				// A decorative leading paren stripped off this GoText (the `)` of a
				// fmt `( <el/> )` wrap) advances `start` past one or more source lines.
				// The emitted bytes therefore begin at p.Pos()+start, not p.Pos() —
				// so the //line directive MUST anchor there too. Anchoring at p.Pos()
				// maps every following line one line too early, and because
				// consecutive top-level Go (a trailing `var (…)`/`func …` chunk) shares
				// this same GoWithElements region as trailing GoText, that drift lands
				// cross-package go-to-definition on the wrong declaration line.
				start := 0
				end := len(p.Src)
				if i > 0 && parenWrappable(we.Parts[i-1], shapes, i-1) {
					stripped := goexprshape.StripLeadingParen(p.Src[start:end])
					start += end - start - len(stripped)
				}
				if i < len(we.Parts)-1 && parenWrappable(we.Parts[i+1], shapes, i+1) {
					stripped := goexprshape.StripTrailingParen(p.Src[start:end])
					end = start + len(stripped)
				}
				// Block-form directive (no newline) so an element mid-expression
				// (`Wrap(<Foo/>)`) keeps its trailing `)` attached to the IIFE's
				// `}()` — a `//line` newline there would trip ASI.
				emitSkeletonBlockLine(goWithElementsBuf, fset, p.Pos()+token.Pos(start))
				if mode == skeletonDeclarations {
					writeSkeletonGenerated(goWithElementsBuf, emitted)
					continue
				}
				if p.Src[start:end] != emitted {
					return "", nil, nil, nil, nil, fmt.Errorf("codegen: Go-with-elements text transform did not preserve an exact authored subspan")
				}
				if emitted != "" {
					if err := writeSkeletonAuthoredAt(goWithElementsBuf, fset, p.Pos()+token.Pos(start), emitted, sourceintel.Definition|sourceintel.Hover|sourceintel.Symbol|sourceintel.Completion); err != nil {
						return "", nil, nil, nil, nil, err
					}
				}
			case *gsxast.Element:
				if mode == skeletonDeclarations {
					goWithElementsBuf.WriteString("func() _gsxrt.Node { return nil }()")
					continue
				}
				markup := []gsxast.Markup{p}
				// Reserve this element's index BEFORE probing its body: emitProbes
				// may itself append (a <tag> literal embedded in one of this
				// element's own interpolations), and those nested markups must take
				// LATER indices so this IIFE's _gsxelem(N) still names markup.
				idx := len(gwMarkups)
				gwMarkups = append(gwMarkups, markup)
				compBuf.WriteString("func() _gsxrt.Node {\n")
				fmt.Fprintf(compBuf, "_gsxelem(%d)\n", idx)
				compBuf.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				elemCFTemp := 0
				if err := emitProbes(compBuf, markup, table, "", "", usedFilters, fset, ctrlOff, nil, &gwMarkups, bag, &elemCFTemp, false); err != nil {
					return "", nil, nil, nil, nil, err
				}
				compBuf.WriteString("return nil\n}()")
			case *gsxast.Fragment:
				if mode == skeletonDeclarations {
					goWithElementsBuf.WriteString("func() _gsxrt.Node { return nil }()")
					continue
				}
				// A fragment probes its children list as one IIFE (empty <></> →
				// no probes, still a valid _gsxrt.Node-returning IIFE — the nop).
				idx := len(gwMarkups)
				gwMarkups = append(gwMarkups, p.Children)
				compBuf.WriteString("func() _gsxrt.Node {\n")
				fmt.Fprintf(compBuf, "_gsxelem(%d)\n", idx)
				compBuf.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
				fragCFTemp := 0
				if err := emitProbes(compBuf, p.Children, table, "", "", usedFilters, fset, ctrlOff, nil, &gwMarkups, bag, &fragCFTemp, false); err != nil {
					return "", nil, nil, nil, nil, err
				}
				compBuf.WriteString("return nil\n}()")
			case *gsxast.EmbeddedInterp:
				if mode == skeletonDeclarations {
					switch p.Lang {
					case gsxast.EmbeddedJS:
						goWithElementsBuf.WriteString("_gsxrt.RawJS(\"\")")
					case gsxast.EmbeddedCSS:
						goWithElementsBuf.WriteString("_gsxrt.RawCSS(\"\")")
					default:
						goWithElementsBuf.WriteString("\"\"")
					}
					continue
				}
				// A prefixed backtick literal (f`/js`/css`) in Go-expression
				// position → a Go VALUE. Probe it like an embedded element:
				// register its segments at index N, splice an IIFE marked
				// `_gsxelem(N)` whose body probes each @{…} hole (so it resolves
				// against THIS region's scope and its type is harvested) and returns
				// the assembled seed. The IIFE's return type MUST match exactly what
				// emit lowers the literal to (emit ≡ probe): f` → the built-in
				// `string` (embeddedValueExpr), js` → _gsxrt.RawJS, css` →
				// _gsxrt.RawCSS. harvestEmbeddedElements resolves the holes off
				// gwMarkups[N]; harvestBody SKIPs the marked IIFE, so the outer
				// region's k-alignment is undisturbed.
				if len(p.Stages) > 0 {
					return "", nil, nil, nil, nil, fmt.Errorf("codegen: whole-literal pipelines on a Go-expression backtick literal are not supported")
				}
				segCFTemp := 0
				if err := probeEmbeddedInterpIIFE(compBuf, p.Segments, p.Lang, table, "", "", usedFilters, fset, ctrlOff, nil, &gwMarkups, bag, &segCFTemp); err != nil {
					return "", nil, nil, nil, nil, err
				}
			default:
				return "", nil, nil, nil, nil, fmt.Errorf("codegen: unsupported Go-expression part %T", part)
			}
		}
		goWithElementsBuf.WriteString("\n")
		if mode == skeletonDeclarations {
			declarationSource, err := declarationOnlyGoWithElementsSource(declarationBuf.String())
			if err != nil {
				return "", nil, nil, nil, nil, err
			}
			compBuf.WriteString(declarationSource)
		} else if compBuf.enabled {
			if err := addGoWithElementsDeclarationRegions(compBuf, fset, we, declarationGeneratedStart, compBuf.Len()); err != nil {
				return "", nil, nil, nil, nil, err
			}
		}
	}

	sb := recorder
	fmt.Fprintf(sb, "package %s\n", file.Package)
	sb.WriteString("import _gsxrt \"github.com/gsxhq/gsx\"\n")
	// Import context under a RESERVED alias so each skeleton component func can
	// bind a real `ctx context.Context` (matching the emitted closure's ambient
	// param) — interp/attr exprs referencing `ctx` then type-check. The reserved
	// alias avoids any duplicate-import clash with a user GoChunk that also
	// imports "context" (Go rejects two plain imports of the same path); the type
	// _gsxctx.Context IS context.Context, so resolution is unaffected.
	sb.WriteString("import _gsxctx \"context\"\n")
	// Import each USED filter package under its reserved alias. std keeps its
	// _gsxstd alias and dedicated `import _gsxstd "<std>"` line (in alias order),
	// so std-only skeletons stay byte-identical to before.
	for _, alias := range sortedFilterAliases(usedFilters) {
		fmt.Fprintf(sb, "import %s %q\n", alias, usedFilters[alias])
	}
	for _, imp := range imports {
		// Map go/types import errors back to the .gsx source. The skeleton spec
		// starts at column 8 (after "import "), so compensate the //line column by
		// that prefix; when the source column is < 8 (the common indented-import
		// case) the compensated column would be < 1, so fall back to a line-only
		// directive (column 1) rather than emit a misleading offset.
		emitSkeletonLineImport(sb, fset, imp.pos)
		writeSkeletonGenerated(sb, "import ")
		if imp.name != "" {
			if err := writeSkeletonAuthoredAt(sb, fset, imp.namePos, imp.name, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
				return "", nil, nil, nil, nil, err
			}
			writeSkeletonGenerated(sb, " ")
		}
		writeSkeletonGenerated(sb, "\"")
		if imp.pathExact {
			if err := writeSkeletonAuthoredAt(sb, fset, imp.pathPos, imp.path, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
				return "", nil, nil, nil, nil, err
			}
		} else {
			quoted := strconv.Quote(imp.path)
			writeSkeletonGenerated(sb, quoted[1:len(quoted)-1])
		}
		writeSkeletonGenerated(sb, "\"\n")
	}
	// Always reference _gsxrt so the import stays used even when the file has no
	// non-method components (e.g. a method-only file whose components are skipped
	// below) — otherwise the import-unused error masks the real diagnostic.
	sb.WriteString("var _ _gsxrt.Node\n")
	// Keep the reserved context import used even when a file has no non-method
	// component func that binds `ctx` (e.g. a method-only file) — otherwise an
	// import-unused error would mask the real diagnostic.
	sb.WriteString("var _ _gsxctx.Context\n")
	// Component skeletons first, then the user's raw-Go bodies. Bodies follow the
	// components (Go permits forward references between top-level decls, so a probe
	// like `_gsxuse(helper())` still resolves) so each body's //line directive
	// stays scoped to that body — it cannot bleed into a component skeleton's
	// unmapped signature lines and shift their overlay positions.
	//
	// Adjust ctrlOff from compBuf-relative to skeleton-file-relative: each recorded
	// offset was relative to compBuf (the temp builder used by emitComponentSkeleton);
	// add the prefix length (everything written into sb before compBuf) so ctrlOff
	// values index into the final skeleton string returned by buildSkeleton.
	compBufStart := sb.Len()
	if err := sb.appendMapped(compBuf); err != nil {
		return "", nil, nil, nil, nil, err
	}
	for k, v := range ctrlOff {
		ctrlOff[k] = v + compBufStart
	}
	for _, b := range bodies {
		emitSkeletonLine(sb, fset, b.pos)
		if err := sb.writeAuthoredAt(fset, b.pos, b.src, sourceintel.Definition|sourceintel.Hover|sourceintel.Symbol|sourceintel.Completion); err != nil {
			return "", nil, nil, nil, nil, err
		}
		sb.WriteByte('\n')
	}
	return sb.String(), comps, imports, ctrlOff, gwMarkups, nil
}

func addGoWithElementsDeclarationRegions(writer *skeletonSourceWriter, sourceFset *token.FileSet, declaration *gsxast.GoWithElements, generatedStart, generatedEnd int) error {
	if writer == nil || !writer.enabled {
		return nil
	}
	if generatedStart < 0 || generatedEnd < generatedStart || generatedEnd > writer.Len() {
		return fmt.Errorf("codegen: invalid generated Go-with-elements declaration range %d:%d", generatedStart, generatedEnd)
	}

	reconstructed, err := reconstructGoWithElements(declaration)
	if err != nil {
		return err
	}
	authoredFset := token.NewFileSet()
	authoredFile, err := parser.ParseFile(authoredFset, "", reconstructed.source, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("codegen: parse reconstructed Go-with-elements declarations: %w", err)
	}
	const header = "package _gsxdecl\n"
	generatedFset := token.NewFileSet()
	generatedSource := header + writer.String()[generatedStart:generatedEnd]
	generatedFile, err := parser.ParseFile(generatedFset, "", generatedSource, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("codegen: parse generated Go-with-elements declarations: %w", err)
	}
	if len(authoredFile.Decls) != len(generatedFile.Decls) {
		return fmt.Errorf("codegen: Go-with-elements declaration count changed from %d to %d during skeleton lowering", len(authoredFile.Decls), len(generatedFile.Decls))
	}
	authoredTokenFile := authoredFset.File(authoredFile.Pos())
	generatedTokenFile := generatedFset.File(generatedFile.Pos())
	sourceTokenFile := sourceFset.File(declaration.Pos())
	if authoredTokenFile == nil || generatedTokenFile == nil || sourceTokenFile == nil {
		return fmt.Errorf("codegen: Go-with-elements declaration mapping has no token file")
	}
	for i := range authoredFile.Decls {
		authoredDeclaration := authoredFile.Decls[i]
		generatedDeclaration := generatedFile.Decls[i]
		if !sameTopLevelDeclarationShape(authoredDeclaration, generatedDeclaration) {
			return fmt.Errorf("codegen: Go-with-elements declaration %d changed shape during skeleton lowering", i)
		}
		sourceStart, _ := reconstructed.originalRange(authoredTokenFile.Offset(authoredDeclaration.Pos()))
		sourceEnd, _ := reconstructed.originalRange(authoredTokenFile.Offset(authoredDeclaration.End()))
		if !sourceStart.IsValid() || !sourceEnd.IsValid() || sourceEnd < sourceStart {
			return fmt.Errorf("codegen: Go-with-elements declaration %d has invalid authored endpoints", i)
		}
		regionGeneratedStart := generatedStart + generatedTokenFile.Offset(generatedDeclaration.Pos()) - len(header)
		regionGeneratedEnd := generatedStart + generatedTokenFile.Offset(generatedDeclaration.End()) - len(header)
		if err := writer.addDeclarationRegion(sourceintel.Span{
			Path:  writer.sourcePath,
			Start: sourceTokenFile.Offset(sourceStart),
			End:   sourceTokenFile.Offset(sourceEnd),
		}, regionGeneratedStart, regionGeneratedEnd); err != nil {
			return err
		}
	}
	return nil
}

func sameTopLevelDeclarationShape(authored, generated goast.Decl) bool {
	switch authored := authored.(type) {
	case *goast.FuncDecl:
		_, ok := generated.(*goast.FuncDecl)
		return ok
	case *goast.GenDecl:
		other, ok := generated.(*goast.GenDecl)
		return ok && authored.Tok == other.Tok && len(authored.Specs) == len(other.Specs)
	default:
		return false
	}
}

// goBody is a GoChunk's non-import remainder paired with the .gsx source
// position of its first byte (for the //line directive that maps it back).
type goBody struct {
	src string
	pos token.Pos
}

// ctrlRef maps a gsx control-flow node (ForMarkup/IfMarkup/GoBlock) to its
// clause's position in the skeleton (ClauseStart) and the smallest skeleton
// go/ast node that spans the clause region. The LSP uses this to place a
// cursor inside the clause and call innermostIdent for go-to-definition.
type ctrlRef struct {
	ClauseStart token.Pos
	Node        goast.Node
}

// SigTypeRef bridges one navigable identifier region in a component signature
// (a parameter type, method receiver type, type-parameter name, or
// type-parameter constraint) to its type-checked skeleton expression. GSXPos is
// the region's first byte in the .gsx and Len its byte length; SkelTyp is the
// corresponding skeleton expression, whose bytes are identical to the .gsx
// source span — so the LSP bridges a cursor into it by relative offset and
// resolves via go/types.
type SigTypeRef struct {
	GSXPos  token.Pos
	Len     int
	SkelTyp goast.Expr
}

// ctrlClauseText returns the clause/cond/code/tag text a control-flow node
// contributes verbatim to the skeleton at its recorded ctrlOff: ForMarkup →
// Clause, IfMarkup → Cond, GoBlock → Code, SwitchMarkup → Tag, CaseClause →
// List, CondAttr → Cond (in-tag `{ if … }` attribute group), ClassPart → Cond
// (a `: cond` guard in class/style), ValueIf → Cond, ValueSwitch → Tag,
// ValueSwitchCase → List (value-form if/switch inside class/style).
func ctrlClauseText(n gsxast.Node) string {
	switch t := n.(type) {
	case *gsxast.ForMarkup:
		return t.Clause
	case *gsxast.IfMarkup:
		return t.Cond
	case *gsxast.GoBlock:
		return t.Code
	case *gsxast.SwitchMarkup:
		return t.Tag
	case *gsxast.CaseClause:
		return t.List
	case *gsxast.CondAttr:
		return t.Cond
	case *gsxast.ClassPart:
		return t.Cond
	case *gsxast.ValueIf:
		return t.Cond
	case *gsxast.ValueSwitch:
		return t.Tag
	case *gsxast.ValueSwitchCase:
		return t.List
	}
	return ""
}

// buildCtrlMap converts each control-flow node's skeleton byte-offset to a
// token.Pos and finds the smallest skeleton node spanning its clause/cond/code
// region, so the LSP can bridge a cursor into the clause and innermostIdent it.
func buildCtrlMap(f *goast.File, fset *token.FileSet, ctrlOff map[gsxast.Node]int, clauseText map[gsxast.Node]string) map[gsxast.Node]ctrlRef {
	tf := fset.File(f.Pos())
	if tf == nil {
		return nil
	}
	out := map[gsxast.Node]ctrlRef{}
	for node, off := range ctrlOff {
		text := clauseText[node]
		start := tf.Pos(off)
		endOff := min(off+len(text), tf.Size())
		end := tf.Pos(endOff)
		var smallest goast.Node
		goast.Inspect(f, func(n goast.Node) bool {
			if n == nil {
				return false
			}
			if n.Pos() <= start && end <= n.End() {
				smallest = n // tighter container; keep descending
				return true
			}
			return false
		})
		out[node] = ctrlRef{ClauseStart: start, Node: smallest}
	}
	return out
}

// sortedFilterAliases returns the aliases of a usedFilters map (alias→pkgPath)
// in deterministic (sorted) order, so the skeleton's import block is stable.
func sortedFilterAliases(usedFilters map[string]string) []string {
	aliases := make([]string, 0, len(usedFilters))
	for a := range usedFilters {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	return aliases
}

// emitComponentSkeleton writes one component's probe skeleton (verbatim
// func/method signature plus probe body) into sb, accumulating into usedFilters
// (alias→pkgPath) every filter package the component's probes reference — so the
// caller imports exactly those packages under those aliases.
func emitComponentSkeleton(sb skeletonWriter, c *gsxast.Component, declaration componentDeclaration, table funcTables, usedFilters map[string]string, fset *token.FileSet, ctrlOff map[gsxast.Node]int, gw *[][]gsxast.Markup, bag *diag.Bag, mode skeletonMode, emission componentTargetEmission) error {
	if !emission.splitBody {
		if !emission.public {
			return fmt.Errorf("codegen: unsplit component %s has no public skeleton declaration", c.Name)
		}
		return emitNamedComponentSkeleton(sb, c, declaration, c.Name, true, table, usedFilters, fset, ctrlOff, gw, bag, mode)
	}
	if emission.bodyName == "" {
		return fmt.Errorf("codegen: split component %s has no analysis body name", c.Name)
	}
	if emission.public {
		if err := emitNamedComponentSkeleton(sb, c, declaration, c.Name, false, table, usedFilters, fset, ctrlOff, gw, bag, mode); err != nil {
			return err
		}
	}
	return emitNamedComponentSkeleton(sb, c, declaration, emission.bodyName, true, table, usedFilters, fset, ctrlOff, gw, bag, mode)
}

func emitNamedComponentSkeleton(sb skeletonWriter, c *gsxast.Component, declaration componentDeclaration, declarationName string, probeBody bool, table funcTables, usedFilters map[string]string, fset *token.FileSet, ctrlOff map[gsxast.Node]int, gw *[][]gsxast.Markup, bag *diag.Bag, mode skeletonMode) error {
	var err error
	var recvVar, recvTypeName string
	if c.Recv != "" {
		recvVar, _, recvTypeName, err = parseRecv(c.Recv)
		if err != nil || checkReservedRecvVar(recvVar) != nil {
			return errSkipComponent
		}
		if strings.TrimSpace(c.TypeParams) != "" && !toolchainHasGenericMethods() {
			return errSkipComponent
		}
	}
	for _, param := range declaration.params {
		if param.name != "ctx" && !strings.HasPrefix(param.name, reservedPrefix) {
			continue
		}
		// Keep the authored signature in the analysis package so every parameter
		// type and its imports remain live. The inert body avoids introducing the
		// ambient ctx binding that conflicts with the invalid parameter; emission
		// reports the positioned reserved-param diagnostic.
		emitSkeletonComponentNameLine(sb, fset, c)
		if err := writeSkeletonComponentSignature(sb, c, declarationName, fset, " _gsxrt.Node { return nil }\n"); err != nil {
			return err
		}
		return errSkipComponent
	}
	hasAttrs := false
	for _, parameter := range declaration.params {
		if parameter.role == declarationParamAttrs {
			hasAttrs = true
			break
		}
	}

	// The shipping probe and generated declaration share the exact authored
	// signature. Parameters remain in lexical scope for the probe body; there is
	// no Props-shaped projection and no synthetic binding layer.
	emitSkeletonComponentNameLine(sb, fset, c)
	if err := writeSkeletonComponentSignature(sb, c, declarationName, fset, " _gsxrt.Node {\n"); err != nil {
		return err
	}
	if !probeBody || mode == skeletonDeclarations {
		sb.WriteString("\treturn nil\n}\n")
		return nil
	}
	// Mirror the emitted render closure `_gsxrt.Func(func(ctx context.Context,
	// _gsxw io.Writer) error { … })`: run the probe body inside an
	// error-returning closure with `ctx` as its parameter. This binds the
	// ambient `ctx` probe exprs reference (`{ fromCtx(ctx) }`, `id={ g(ctx) }`)
	// AND lets a raw `{{ }}` block's `return err` type-check against `error`,
	// exactly as it does in the generated code — a component body returning
	// `_gsxrt.Node` would otherwise reject `return err`. A closure parameter is
	// never "declared and not used", so no `_ = ctx` is needed; `_ = _gsxbody`
	// keeps the closure itself used for components that never reference ctx.
	sb.WriteString("\t_gsxbody := func(ctx _gsxctx.Context) error {\n")
	cfTemp := 0
	if err := emitProbes(sb, c.Body, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, nil, gw, bag, &cfTemp, hasAttrs); err != nil {
		return err
	}
	sb.WriteString("\t\treturn nil\n\t}\n\t_ = _gsxbody\n\treturn nil\n}\n")
	return nil
}

func writeSkeletonComponentSignature(sb skeletonWriter, c *gsxast.Component, declarationName string, fset *token.FileSet, tail string) error {
	const capabilities = sourceintel.Definition | sourceintel.Hover | sourceintel.Completion
	canonical := declarationName == c.Name
	writeSkeletonGenerated(sb, "func ")
	if c.Recv != "" {
		if canonical {
			if err := writeSkeletonAuthoredAt(sb, fset, c.RecvPos, c.Recv, capabilities); err != nil {
				return err
			}
		} else {
			writeSkeletonGenerated(sb, c.Recv)
		}
		writeSkeletonGenerated(sb, " ")
	}
	if canonical {
		if err := writeSkeletonAuthoredAt(sb, fset, c.NamePos, c.Name, capabilities); err != nil {
			return err
		}
	} else {
		writeSkeletonGenerated(sb, declarationName)
	}
	if typeParams := strings.TrimSpace(c.TypeParams); typeParams != "" {
		writeSkeletonGenerated(sb, "[")
		if canonical {
			if err := writeSkeletonAuthoredAt(sb, fset, c.TypeParamsPos, typeParams, capabilities); err != nil {
				return err
			}
		} else {
			writeSkeletonGenerated(sb, typeParams)
		}
		writeSkeletonGenerated(sb, "]")
	}
	writeSkeletonGenerated(sb, "(")
	if params := strings.TrimSpace(c.Params); params != "" {
		if canonical {
			if err := writeSkeletonAuthoredAt(sb, fset, c.ParamsPos, params, capabilities); err != nil {
				return err
			}
		} else {
			writeSkeletonGenerated(sb, params)
		}
	}
	writeSkeletonGenerated(sb, ")"+tail)
	return nil
}

// emitProbes writes type-resolution probes for a component body. It MIRRORS the
// control structure (real for/if/switch + {{ }} code) so interpolations that
// reference loop vars / block-locals type-check in scope. Each interpolation is
// `_gsxuse(expr)`; component targets are checked separately by the authoritative
// target skeleton and positional planner.
//
// usedFilters (alias→pkgPath) accumulates every filter package the probes
// reference, so the skeleton imports exactly those packages under those aliases
// — driven by the SAME lowerPipe report the emitter uses.
// cfTemp is a monotonic counter shared across every value-form-CF class-part
// hoist WITHIN one skeleton function, so the `_gsxvN` temps stay globally
// unique and never collide across sibling component tags in the same block
// (fix #69). A fresh counter is started at each entry point that begins a new
// skeleton function/buffer; recursive calls thread the received counter through.
// targetRegistry is nil for the shipping skeleton. A non-nil registry emits
// target identity bindings in these same lexical scopes while retaining the
// ordinary operand, liveness, and slot probes below each component target.
func emitProbes(sb skeletonWriter, nodes []gsxast.Markup, table funcTables, recvVar, recvTypeName string, usedFilters map[string]string, fset *token.FileSet, ctrlOff map[gsxast.Node]int, targetRegistry *componentTargetMarkerRegistry, gw *[][]gsxast.Markup, bag *diag.Bag, cfTemp *int, enclosingAttrsBound bool) error {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			if t.Embedded != nil {
				// The seed carried operand-position <tag>/<> literals (e.g.
				// `wrap(<b/>)`): build the probe expression by splicing each
				// embedded element/fragment's inline IIFE (the SAME _gsxelem(N)
				// marker + probe form the top-level GoWithElements loop emits) in
				// between the verbatim GoText runs, then _gsxuse the whole
				// expression so harvest maps its type (e.g. wrap's return type) onto
				// resolved[t]. The element's own interps are probed INSIDE its IIFE
				// so they resolve against THIS enclosing component scope (recvVar /
				// recvTypeName threaded through unchanged), matching emit's closure
				// capture. Indices are reserved BEFORE probing each element so
				// nested embedded tags take later indices — harvestEmbeddedElements
				// resolves them off the shared gw slice for free.
				targetMarkerStart := 0
				if targetRegistry != nil {
					targetMarkerStart = len(targetRegistry.ordered)
				}
				eb := newSkeletonWriterChild(sb)
				for _, part := range t.Embedded {
					switch p := part.(type) {
					case gsxast.GoText:
						emitSkeletonBlockLine(eb, fset, p.Pos())
						if err := writeSkeletonAuthoredAt(eb, fset, p.Pos(), p.Src, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
							return err
						}
					case *gsxast.Element:
						markup := []gsxast.Markup{p}
						idx := len(*gw)
						*gw = append(*gw, markup)
						eb.WriteString("func() _gsxrt.Node {\n")
						fmt.Fprintf(eb, "_gsxelem(%d)\n", idx)
						eb.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
						if err := emitProbes(eb, markup, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
							return err
						}
						eb.WriteString("return nil\n}()")
					case *gsxast.Fragment:
						idx := len(*gw)
						*gw = append(*gw, p.Children)
						eb.WriteString("func() _gsxrt.Node {\n")
						fmt.Fprintf(eb, "_gsxelem(%d)\n", idx)
						eb.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
						if err := emitProbes(eb, p.Children, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
							return err
						}
						eb.WriteString("return nil\n}()")
					case *gsxast.EmbeddedInterp:
						// A prefixed backtick literal (f`/js`/css`) inside this interp's
						// seed expression → a Go VALUE. Same probe IIFE the top-level
						// GoWithElements loop splices: its holes resolve against this
						// enclosing component's scope (recvVar / recvTypeName threaded
						// through) and are harvested off gw[N], while the outer interp's
						// _gsxuse stays aligned (the marked IIFE is skipped by
						// harvestBody). The return type MUST match emit's lowering
						// (emit ≡ probe): f` → string, js` → _gsxrt.RawJS, css` →
						// _gsxrt.RawCSS — so wrap(...) etc. type-check against the exact
						// type the literal produces.
						if len(p.Stages) > 0 {
							return fmt.Errorf("codegen: whole-literal pipelines on a Go-expression backtick literal are not supported")
						}
						if err := probeEmbeddedInterpIIFE(eb, p.Segments, p.Lang, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp); err != nil {
							return err
						}
					default:
						return fmt.Errorf("codegen: unsupported embedded interpolation part %T", part)
					}
				}
				// Run the assembled seed through t.Stages via the SAME probeExpr /
				// lowerPipe path the non-embedded interp uses, so resolved[t] is the
				// POST-pipe type — matching genInterp, which lowers the pipeline over
				// the spliced seed too (emit ≡ probe). A Stages-less embedded interp
				// yields the seed unchanged, preserving prior behavior.
				seed := eb.String()
				var mappedBoundary componentTargetSeedBoundary
				hasMappedChild := false
				if mapped, ok := eb.(*skeletonSourceWriter); ok && mapped.enabled && (len(mapped.segments) != 0 || len(mapped.regions) != 0) {
					mappedBoundary = componentTargetSeedBoundary{open: "\x00gsx-mapped-seed-open\x00", close: "\x00gsx-mapped-seed-close\x00"}
					seed = mappedBoundary.open + seed + mappedBoundary.close
					hasMappedChild = true
				}
				var boundary componentTargetSeedBoundary
				hasTargetMarkers := targetRegistry != nil && len(targetRegistry.ordered) > targetMarkerStart
				if hasTargetMarkers {
					seed, boundary = markComponentTargetSeed(targetRegistry.ordered[targetMarkerStart].site, seed)
				}
				probe, err := probeExpr(seed, t.Stages, table, usedFilters, t, bag)
				if err != nil {
					return err
				}
				seedOffset := 0
				if hasTargetMarkers {
					probe, seedOffset, err = unmarkComponentTargetSeed(probe, boundary)
					if err != nil {
						return err
					}
				}
				if hasMappedChild {
					probe, seedOffset, err = unmarkComponentTargetSeed(probe, mappedBoundary)
					if err != nil {
						return err
					}
				}
				emitSkeletonLine(sb, fset, t.Pos())
				writeSkeletonGenerated(sb, "_gsxuse(")
				probeStart := sb.Len()
				if hasMappedChild {
					writeSkeletonGenerated(sb, probe[:seedOffset])
					if err := appendSkeletonWriter(sb, eb); err != nil {
						return err
					}
					writeSkeletonGenerated(sb, probe[seedOffset+len(eb.String()):])
				} else {
					writeSkeletonGenerated(sb, probe)
				}
				writeSkeletonGenerated(sb, ")\n")
				if hasTargetMarkers {
					targetRegistry.adjustFrom(targetMarkerStart, probeStart+seedOffset)
				}
				continue
			}
			const probePrefixLen = len("_gsxuse(") // 8
			if len(t.Stages) == 0 && t.ExprPos.IsValid() {
				ep := fset.Position(t.ExprPos)
				if col := ep.Column - probePrefixLen; col >= 1 {
					// Compensated //line: the probe's first token (at byte offset 8 into
					// "_gsxuse(expr)") will be reported at ep.Column, matching the source.
					fmt.Fprintf(sb, "//line %s:%d:%d\n", ep.Filename, ep.Line, col)
					writeSkeletonGenerated(sb, "_gsxuse(")
					if err := writeSkeletonProbeExpr(sb, fset, t.ExprPos, t.Expr, t.Stages, table, usedFilters, t, bag); err != nil {
						return err
					}
					writeSkeletonGenerated(sb, ")\n")
				} else {
					// Shallow interp (exprCol ≤ 8): a compensated column would be < 1,
					// which is an invalid //line column. Instead break the probe across
					// lines and put the expr as the FIRST token on its own line under an
					// exprCol-anchored //line, so go/types reports the expr at ep.Column
					// EXACTLY, however shallow. The newline right after "_gsxuse(" is
					// safe — Go inserts no semicolon after '(' — and the call still
					// type-checks as _gsxuse(expr). Harvest keys on the k-th _gsxuse call
					// node (AST order), not the probe's text layout, so the extra line
					// is transparent to it.
					fmt.Fprintf(sb, "_gsxuse(\n//line %s:%d:%d\n", ep.Filename, ep.Line, ep.Column)
					if err := writeSkeletonProbeExpr(sb, fset, t.ExprPos, t.Expr, t.Stages, table, usedFilters, t, bag); err != nil {
						return err
					}
					writeSkeletonGenerated(sb, ")\n")
				}
			} else {
				// Staged pipeline or no ExprPos: keep unchanged behavior ('{' pos).
				emitSkeletonLine(sb, fset, t.Pos())
				writeSkeletonGenerated(sb, "_gsxuse(")
				if err := writeSkeletonProbeExpr(sb, fset, t.ExprPos, t.Expr, t.Stages, table, usedFilters, t, bag); err != nil {
					return err
				}
				writeSkeletonGenerated(sb, ")\n")
			}
		case *gsxast.EmbeddedInterp:
			// Body backtick literal {`…@{expr}…`} [ |> f ]. Probe each hole
			// first (so every param it references stays live and its own
			// type is harvested — mirrors an EmbeddedAttr's holes), then, ONLY
			// when the whole literal itself carries a pipeline, probe the
			// assembled seed piped through node.Stages — the SAME lowerPipe
			// call codegen's emitEmbeddedInterp will build (via
			// embeddedTextValueExpr + lowerPipe), so resolved[t] ends up the
			// exact type codegen emits (emit ≡ probe).
			if err := emitProbes(sb, t.Segments, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
				return err
			}
			if len(t.Stages) > 0 {
				seed := embeddedProbeSeed(t.Segments, table, usedFilters, bag)
				emitSkeletonLine(sb, fset, t.Pos())
				writeSkeletonGenerated(sb, "_gsxuse(")
				if err := writeSkeletonProbeExpr(sb, fset, token.NoPos, seed, t.Stages, table, usedFilters, t, bag); err != nil {
					return err
				}
				writeSkeletonGenerated(sb, ")\n")
			}
		case *gsxast.Element:
			candidateProbe := targetRegistry != nil && targetRegistry.hasCandidate(t)
			if t.IsComponent || candidateProbe {
				if candidateProbe {
					if err := targetRegistry.emitBinding(sb, t, fset); err != nil {
						return err
					}
				} else if t.TypeArgs != "" {
					// A component tag's authored type arguments are consumed by the
					// target-discovery binding (emitBinding, above), but the shipping
					// skeleton lowers the call positionally and never re-emits them —
					// so an import referenced ONLY inside a markup type-argument (e.g.
					// <comp.Check[cons.Foo] .../>) would be a synthetic "imported and
					// not used". Reference the whole authored type-argument list as a
					// function-result type list (which is exactly a comma-separated
					// list of type expressions, so nested generics and multiple args
					// need no ad-hoc splitting), anchored at TypeArgsPos so any
					// undefined-type diagnostic maps back to the authored bytes. The
					// blank var is neither a _gsxuse nor _gsxuseq call, so the k-th
					// probe → k-th node harvest alignment is undisturbed.
					emitSkeletonLine(sb, fset, t.TypeArgsPos)
					writeSkeletonGenerated(sb, "var _ func() (")
					if err := writeSkeletonAuthoredAt(sb, fset, t.TypeArgsPos, t.TypeArgs, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
						return err
					}
					writeSkeletonGenerated(sb, ")\n")
				}
				// Probe simple ExprAttr values (child-prop values) with _gsxuse so harvest
				// records their RAW types into resolved[ea]. This is emitted for ALL child
				// component branches (bare-call, nullary, props-literal) so the k-th probe
				// always aligns with the k-th node in collectExprs (which also adds ExprAttr
				// nodes for all child components, before slot content).
				for _, a := range t.Attrs {
					ea, ok := a.(*gsxast.ExprAttr)
					if !ok {
						continue
					}
					emitSkeletonLine(sb, fset, ea.Pos())
					// The positional planner owns assignment checking, but this native
					// probe remains authoritative for errors inside the authored
					// expression (including missing imports). It must not be quiet: the
					// removed Props-literal probe no longer provides a duplicate error.
					if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, ea.ExprPos, ea.Expr, ea.Stages, table, usedFilters, ea, bag); err != nil {
						return err
					}
				}
				// A component spread is an attrs contributor, but its expression still
				// needs the same authoritative go/types fact as a leaf spread. Probe it
				// immediately after top-level ExprAttrs; collectExprs uses this exact
				// order, so harvest retains node identity without reparsing source.
				var spreadProbeErr error
				walkSpreadAttrs(t.Attrs, func(sa *gsxast.SpreadAttr) {
					if spreadProbeErr != nil {
						return
					}
					emitSkeletonLine(sb, fset, sa.Pos())
					// Component spreads accept the complete []gsx.Attr family, so a
					// canonical gsx.Attrs assignment would falsely reject a defined bag.
					// The non-quiet variadic probe both harvests the exact type and owns
					// expression diagnostics; semantic validation proves the bag family.
					if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, sa.ExprPos, sa.Expr, sa.Stages, table, usedFilters, sa, bag); err != nil {
						spreadProbeErr = err
					}
				})
				if spreadProbeErr != nil {
					return spreadProbeErr
				}
				// Probe ordered-attrs pair values AFTER ExprAttr probes, in attr source
				// order then pair order — matching collectExprs's ordering exactly.
				// _gsxuseq harvests the raw type (possibly a tuple) of each pair value;
				// the props-literal _gsxunwrap(...) probe already reports expression-internal
				// errors, so _gsxuseq's quiet suppression avoids duplicates.
				for _, a := range t.Attrs {
					oa, ok := a.(*gsxast.OrderedAttrsAttr)
					if !ok {
						continue
					}
					for i := range oa.Pairs {
						emitSkeletonLine(sb, fset, oa.Pairs[i].Pos())
						if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, oa.Pairs[i].Pos(), oa.Pairs[i].Value, nil, table, usedFilters, &oa.Pairs[i], bag); err != nil {
							return err
						}
					}
				}
				// Probe CF arm exprs and EVERY plain ClassPart expr (conditional or
				// not) AFTER pair probes — matching collectExprs's ClassPart ordering
				// exactly (the shared walkClassAttrs recurses CondAttr on both
				// sides). _gsxuse harvests the raw type so classEntryExpr can detect
				// and hoist (T, error) tuple call parts and CF arms, AND (#85) so its
				// applyClassRenderer call for a conditional part's value has a non-nil
				// resolved[part] to dispatch on — a conditional part's value expr is
				// stubbed in the props-literal probe exactly like an unconditional
				// one, so without this probe resolved[part] would stay nil and the
				// renderer would silently never apply. Unlike ordinary child-prop
				// expressions, call-shaped class parts are stubbed in the props-literal
				// probe to tolerate tuples, so this non-quiet probe is also responsible
				// for surfacing expression errors such as undefined identifiers.
				var classProbeErr error
				walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
					if classProbeErr != nil {
						return
					}
					for i := range ca.Parts {
						if ca.Parts[i].CF != nil {
							// Value-form CF part: probe each arm so harvest populates
							// resolved[arm] for classEntryExpr's (T, error) unwrap.
							for _, arm := range valueFormArms(ca.Parts[i].CF) {
								emitSkeletonLine(sb, fset, arm.Pos())
								if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, arm.ExprPos, arm.Expr, arm.Stages, table, usedFilters, arm, bag); err != nil {
									classProbeErr = err
									return
								}
							}
						} else if ca.Parts[i].CSSSegments == nil {
							emitSkeletonLine(sb, fset, ca.Parts[i].Pos())
							if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, ca.Parts[i].ExprPos, ca.Parts[i].Expr, ca.Parts[i].Stages, table, usedFilters, &ca.Parts[i], bag); err != nil {
								classProbeErr = err
								return
							}
						}
					}
				})
				if classProbeErr != nil {
					return classProbeErr
				}
				// The class-part probes above reference each part's VALUE expr, but a
				// conditional class part's `: cond` guard and a value-form CF part's
				// if/switch control are emitted verbatim by codegen with no harvest —
				// so a var used ONLY in a component-tag class cond (e.g. a loop index
				// in `<C class={ "first": i == 0 }/>`) would be a synthetic "declared
				// and not used". The leaf-element branch already emits this liveness
				// (see walkLivenessAttrExprs below); the component branch must too. An
				// element enters exactly one branch, so there is no double emission.
				// These yield empty-bodied `if cond {}` / `switch {}` blocks (not
				// _gsxuse calls), leaving the k-th probe → k-th node harvest alignment
				// undisturbed, and record ctrlOff entries for LSP go-to-definition.
				walkLivenessAttrExprs(t.Attrs, func(cf *gsxast.ValueCF) {
					emitValueCFControl(sb, fset, cf, ctrlOff)
				}, func(node gsxast.Node, cond string, condPos token.Pos) {
					emitCondLiveness(sb, fset, node, cond, condPos, ctrlOff)
				})
				// Probe ExprAttr values nested in a component cond-attr branch
				// (`{ if C { attr={expr} } }`) with _gsxuseq, AFTER the parts probes —
				// matching collectExprs's walkBranchAttrExprs pass exactly (Then→Else,
				// top-level ExprAttrs excluded). The positional call probe embeds the whole
				// AttrsCond(...) expression without a per-value
				// harvest probe, so these probes are what populate resolved for branch
				// ExprAttrs (Task 3 consumes them for (T, error) tuple detection). These
				// are TOP-LEVEL skeleton statements: the skeleton is compile-only and
				// never executed, so probing the branch value UNCONDITIONALLY (outside
				// any thunk/cond) is safe — laziness is irrelevant here. _gsxuseq (quiet)
				// harvests the raw type (possibly a tuple); an expression-internal error
				// is reported by the props-literal probe above, so the quiet suppression
				// avoids a duplicate. _gsxuseq(...any) also tolerates a multi-value
				// (T, error) argument that `_ = (expr)` liveness could not.
				var branchProbeErr error
				walkBranchAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
					if branchProbeErr != nil {
						return
					}
					emitSkeletonLine(sb, fset, ea.Pos())
					if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, ea.ExprPos, ea.Expr, ea.Stages, table, usedFilters, ea, bag); err != nil {
						branchProbeErr = err
					}
				})
				if branchProbeErr != nil {
					return branchProbeErr
				}
				// Probe slot content in the SAME canonical order collectExprs walks:
				// each markup-attr value (attr order) then the children.
				//
				// A named markup slot's value and the tag's children both lower into
				// a NESTED gsx.Func slot closure (emitSlotClosure). The skeleton is
				// flat, so a reserved-name shadow inside slot content (`{{ attrs :=
				// … }}`) probed at the closure's top scope would collide with the
				// enclosing component's authored attrs parameter (`no new variables on
				// left side of :=`) — a skeleton-only false rejection of
				// code the emitter compiles fine. Wrap each slot's probes in a plain
				// Go block so the shadow lands in a nested scope, restoring emit ≡
				// probe. Braces open no _gsxuse/_gsxuseq probe and carry no //line, so
				// the k-th-probe→k-th-node harvest alignment is undisturbed.
				var probeErr error
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					sb.WriteString("{\n")
					probeErr = emitProbes(sb, value, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound)
					sb.WriteString("}\n")
				})
				if probeErr != nil {
					return probeErr
				}
				// Probe each braced-attr whole-literal pipeline (`attr={`…` |> f}`)
				// AFTER the markup-attr/hole probes above — matching collectExprs's
				// walkEmbeddedAttrStages ordering exactly.
				walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
					if probeErr != nil {
						return
					}
					seed := embeddedProbeSeed(ea.Segments, table, usedFilters, bag)
					emitSkeletonLine(sb, fset, ea.Pos())
					writeSkeletonGenerated(sb, "_gsxuse(")
					if err := writeSkeletonProbeExpr(sb, fset, token.NoPos, seed, ea.Stages, table, usedFilters, ea, bag); err != nil {
						probeErr = err
						return
					}
					writeSkeletonGenerated(sb, ")\n")
				})
				if probeErr != nil {
					return probeErr
				}
				// Children lower into a nested slot closure (emitSlotClosure), same
				// as a named slot value above — wrap in a Go block so a reserved-name
				// shadow does not collide with the enclosing authored attrs parameter.
				sb.WriteString("{\n")
				childErr := emitProbes(sb, t.Children, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound)
				sb.WriteString("}\n")
				if childErr != nil {
					return childErr
				}
			} else {
				// Probe each attr-expr (top-level and CondAttr-nested) FLAT, in the
				// SAME canonical order collectExprs walks, so the k-th _gsxuse maps to
				// the k-th collected node. The nested exprs type-check regardless of
				// branch, so no real `if` wrapper is needed.
				var probeErr error
				walkAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
					if probeErr != nil {
						return
					}
					emitSkeletonLine(sb, fset, ea.Pos())
					if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, ea.ExprPos, ea.Expr, ea.Stages, table, usedFilters, ea, bag); err != nil {
						probeErr = err
					}
				})
				if probeErr != nil {
					return probeErr
				}
				// Probe each element-spread expr with _gsxuseq. The quiet probe checks
				// the expression and doubles as its liveness reference, so a var used
				// ONLY in a `{ x... }` spread stays "used". collectExprs appends each
				// SpreadAttr AFTER all the element's ExprAttrs, so the k-th probe stays
				// aligned with the k-th node.
				walkSpreadAttrs(t.Attrs, func(sa *gsxast.SpreadAttr) {
					if probeErr != nil {
						return
					}
					probe, err := probeExpr(sa.Expr, sa.Stages, table, usedFilters, sa, bag)
					if err != nil {
						probeErr = err
						return
					}
					emitSkeletonLine(sb, fset, sa.Pos())
					fmt.Fprintf(sb, "_gsxuseq(%s)\n", probe)
					// The _gsxuseq harvest above has its error span SUPPRESSED
					// (module_importer quietSpans). Re-check the expression in a native
					// gsx.Attrs assignment so invalid spread types and expression errors
					// surface exactly once without exposing a synthetic helper name.
					// This declaration is NOT a counted probe, so it is invisible to the
					// k-th-probe→k-th-node harvest alignment.
					emitSkeletonLine(sb, fset, sa.Pos())
					writeSkeletonGenerated(sb, "var _ _gsxrt.Attrs = (")
					if err := writeSkeletonProbeExpr(sb, fset, sa.ExprPos, sa.Expr, sa.Stages, table, usedFilters, sa, bag); err != nil {
						probeErr = err
						return
					}
					writeSkeletonGenerated(sb, ")\n")
				})
				if probeErr != nil {
					return probeErr
				}
				// Emit _gsxuse probes for value-form CF arm expressions in the SAME
				// source order collectExprs collects them (attr order → CF-part order
				// → arm order). harvest maps the k-th _gsxuse to the k-th node,
				// populating resolved[arm] so hoistValueCF can detect and unwrap
				// (T, error) return types. _gsxuse(...any) accepts multi-return calls
				// without a syntax error, and harvest reads the tuple type from the
				// argument's resolved type. A probe per CF arm, and also per plain
				// part (conditional or not, #88), replaces the former liveness-only
				// behavior for those parts; _gsxuse also keeps identifier references
				// live. walkClassAttrs recurses CondAttr Then/Else in lockstep with
				// collectExprs, so arms of a class attr nested in a conditional attr
				// group are probed (liveness + harvest) too.
				var leafClassProbeErr error
				walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
					if leafClassProbeErr != nil {
						return
					}
					for i := range ca.Parts {
						if ca.Parts[i].CF != nil {
							for _, arm := range valueFormArms(ca.Parts[i].CF) {
								emitSkeletonLine(sb, fset, arm.Pos())
								if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, arm.ExprPos, arm.Expr, arm.Stages, table, usedFilters, arm, bag); err != nil {
									leafClassProbeErr = err
									return
								}
							}
						} else if ca.Parts[i].CSSSegments == nil {
							// Plain part, conditional or not: harvest its type for
							// renderer application and (T, error) unwrap (#88). _gsxuse
							// also serves as a liveness reference (replaces `_ =
							// (expr)`); the cond guard itself (if any) still needs its
							// own liveness reference — see walkLivenessAttrExprs.
							emitSkeletonLine(sb, fset, ca.Parts[i].Pos())
							if err := writeSkeletonCanonicalProbe(sb, "_gsxuse", fset, ca.Parts[i].ExprPos, ca.Parts[i].Expr, ca.Parts[i].Stages, table, usedFilters, &ca.Parts[i], bag); err != nil {
								leafClassProbeErr = err
								return
							}
						}
					}
				})
				if leafClassProbeErr != nil {
					return leafClassProbeErr
				}
				// ClassAttr cond guards and value-form CF control expressions are
				// emitted verbatim by codegen (no type harvest), so a var used ONLY
				// in a `: cond` guard or in a value-form if/switch condition must
				// still be referenced here or it's "declared and not used". The walk
				// yields an empty-bodied `if cond {\n}` per cond guard
				// (emitCondLiveness) and per value-form if/switch condition
				// (emitValueCFControl; tags/case lists are only legal in statement
				// position) — NOT _gsxuse, so the harvest alignment is intact. Each
				// condition also records a ctrlOff entry so the LSP can
				// go-to-definition inside it. CF arms and ALL plain parts
				// (conditional or not, #88) are excluded here — they have _gsxuse
				// probes above, which harvest their type AND keep them live; a
				// conditional part's cond guard is still referenced via fnCond,
				// just not its value expr. Spreads are excluded too because their
				// _gsxuseq probes above also keep them live.
				walkLivenessAttrExprs(t.Attrs, func(cf *gsxast.ValueCF) {
					emitValueCFControl(sb, fset, cf, ctrlOff)
				}, func(node gsxast.Node, cond string, condPos token.Pos) {
					emitCondLiveness(sb, fset, node, cond, condPos, ctrlOff)
				})
				// Then probe each JS-attribute's @{ } interps, in attr source order —
				// collectExprs walks identically (same walkMarkupAttrs), so the k-th
				// _gsxuse maps to the k-th collected node.
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					if probeErr != nil {
						return
					}
					probeErr = emitProbes(sb, value, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound)
				})
				if probeErr != nil {
					return probeErr
				}
				// Probe each braced-attr whole-literal pipeline AFTER the
				// markup-attr/hole probes above — matching collectExprs's
				// walkEmbeddedAttrStages ordering exactly.
				walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
					if probeErr != nil {
						return
					}
					seed := embeddedProbeSeed(ea.Segments, table, usedFilters, bag)
					emitSkeletonLine(sb, fset, ea.Pos())
					writeSkeletonGenerated(sb, "_gsxuse(")
					if err := writeSkeletonProbeExpr(sb, fset, token.NoPos, seed, ea.Stages, table, usedFilters, ea, bag); err != nil {
						probeErr = err
						return
					}
					writeSkeletonGenerated(sb, ")\n")
				})
				if probeErr != nil {
					return probeErr
				}
				if err := emitProbes(sb, t.Children, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
					return err
				}
			}
		case *gsxast.Fragment:
			if err := emitProbes(sb, t.Children, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
				return err
			}
		case *gsxast.ForMarkup:
			emitSkeletonClauseLine(sb, fset, t.ClausePos, len("for ")) // 4
			ctrlOff[t] = sb.Len() + len("for ")
			writeSkeletonGenerated(sb, "for ")
			if err := writeSkeletonAuthoredAt(sb, fset, t.ClausePos, t.Clause, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
				return err
			}
			writeSkeletonGenerated(sb, " {\n")
			if err := emitProbes(sb, t.Body, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
				return err
			}
			sb.WriteString("}\n")
		case *gsxast.IfMarkup:
			emitSkeletonClauseLine(sb, fset, t.CondPos, len("if ")) // 3
			ctrlOff[t] = sb.Len() + len("if ")
			writeSkeletonGenerated(sb, "if ")
			if err := writeSkeletonAuthoredAt(sb, fset, t.CondPos, t.Cond, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
				return err
			}
			writeSkeletonGenerated(sb, " {\n")
			if err := emitProbes(sb, t.Then, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
				return err
			}
			sb.WriteString("}")
			if t.Else != nil {
				sb.WriteString(" else {\n")
				if err := emitProbes(sb, t.Else, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
					return err
				}
				sb.WriteString("}")
			}
			sb.WriteString("\n")
		case *gsxast.SwitchMarkup:
			if strings.TrimSpace(t.Tag) != "" {
				emitSkeletonClauseLine(sb, fset, t.TagPos, len("switch "))
				ctrlOff[t] = sb.Len() + len("switch ")
			}
			writeSkeletonGenerated(sb, "switch ")
			if strings.TrimSpace(t.Tag) != "" {
				if err := writeSkeletonAuthoredAt(sb, fset, t.TagPos, t.Tag, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
					return err
				}
			}
			writeSkeletonGenerated(sb, " {\n")
			for _, cc := range t.Cases {
				if cc.Default {
					sb.WriteString("default:\n")
				} else {
					emitSkeletonClauseLine(sb, fset, cc.ListPos, len("case "))
					ctrlOff[cc] = sb.Len() + len("case ")
					writeSkeletonGenerated(sb, "case ")
					if err := writeSkeletonAuthoredAt(sb, fset, cc.ListPos, cc.List, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
						return err
					}
					writeSkeletonGenerated(sb, ":\n")
				}
				if err := emitProbes(sb, cc.Body, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, enclosingAttrsBound); err != nil {
					return err
				}
			}
			sb.WriteString("}\n")
		case *gsxast.GoBlock:
			switch {
			case t.UnsupportedMarkup != nil:
				// preprocessComponentCallSites owns the single positioned
				// unsupported-node diagnostic. Skip this whole block so its
				// incomplete Go cannot produce misleading probe errors.
			case t.Embedded == nil:
				// No embedded literal: the whole block is verbatim Go (unchanged).
				emitSkeletonClauseLine(sb, fset, t.CodePos, 0)
				ctrlOff[t] = sb.Len()
				if err := writeSkeletonAuthoredAt(sb, fset, t.CodePos, t.Code, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
					return err
				}
				sb.WriteString("\n")
			default:
				// The block carries one or more f`/js`/css` literals: reconstruct it
				// from its split parts. Each GoText run gets a fresh block-form
				// //line anchor (positions must keep mapping to .gsx source once an
				// IIFE splice shifts byte offsets), and each *EmbeddedInterp becomes
				// the SAME Lang-typed probe IIFE the GoWithElements/Interp.Embedded
				// sites splice (probeEmbeddedInterpIIFE — one lowering, three sites).
				ctrlOff[t] = sb.Len()
				for _, part := range t.Embedded {
					switch p := part.(type) {
					case gsxast.GoText:
						emitSkeletonBlockLine(sb, fset, p.Pos())
						if err := writeSkeletonAuthoredAt(sb, fset, p.Pos(), p.Src, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
							return err
						}
					case *gsxast.EmbeddedInterp:
						if len(p.Stages) > 0 {
							return fmt.Errorf("codegen: whole-literal pipelines on a Go-expression backtick literal are not supported")
						}
						if err := probeEmbeddedInterpIIFE(sb, p.Segments, p.Lang, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp); err != nil {
							return err
						}
					}
				}
				sb.WriteString("\n")
			}
		}
	}
	return nil
}

// emitSkeletonComponentNameLine anchors the skeleton's `func … Name(` declaration
// so the Name token maps to the component's .gsx NamePos column-precisely, letting
// LSP go-to-definition land on the component name. genNameCol is the name's column
// within the generated func line: 6 for `func <Name>` (after "func "), or
// 7+len(Recv) for `func <Recv> <Name>`. The directive is shifted left by that
// prefix. (Only the skeleton needs this; the emit-side anchor stays at c.Pos().)
func emitSkeletonComponentNameLine(sb skeletonWriter, fset *token.FileSet, c *gsxast.Component) {
	if fset == nil || !c.NamePos.IsValid() {
		return
	}
	genNameCol := 6 // func <Name>
	if c.Recv != "" {
		genNameCol = 7 + len(c.Recv) // func <Recv> <Name>
	}
	p := fset.Position(c.NamePos)
	col := max(p.Column-genNameCol+1, 1)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}

// emitSkeletonClauseLine emits a //line anchored so the clause/cond/code text
// (which the skeleton emits verbatim starting `prefixLen` bytes into the line)
// maps to its .gsx position pos. col = clauseCol - prefixLen.
func emitSkeletonClauseLine(sb skeletonWriter, fset *token.FileSet, pos token.Pos, prefixLen int) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	col := max(p.Column-prefixLen, 1)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}

// emitSkeletonLine writes a //line directive into a skeleton strings.Builder,
// mapping subsequent source to the .gsx file position, so go/types errors
// produced by the type-checker (via pkg.Fset which honors //line directives)
// resolve to .gsx file:line:col instead of the generated overlay .x.go.
// fset may be nil (e.g. in test-only callers); in that case no directive is emitted.
//
// Column accuracy: interpolation expression columns are now exact via
// Interp.ExprPos + compensated //line (col = exprCol - len("_gsxuse(")).
// Component-prop and pipeline-staged probes remain coarse: synthesized
// props-literal fields and rewritten pipeline expressions have no faithful
// source column, so they still emit //line at the node's Pos().
func emitSkeletonLine(sb skeletonWriter, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, p.Column)
}

// emitSkeletonBlockLine emits a BLOCK-form `/*line file:line:col*/` directive
// (no trailing newline), used to re-sync the position of verbatim GoText
// spliced around a GoWithElements-embedded element's inline IIFE. Unlike the
// `//line` form (which spans to end of line and needs its own line), the block
// form is valid mid-expression, so it does not force a newline that would trip
// Go's automatic semicolon insertion when the GoText attaches to the IIFE's
// `}()` (e.g. the trailing `)` of `Wrap(<Foo/>)`). It sets the position of the
// character immediately following the comment — the GoText's first byte.
func emitSkeletonBlockLine(sb skeletonWriter, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	p := fset.Position(pos)
	fmt.Fprintf(sb, "/*line %s:%d:%d*/", p.Filename, p.Line, p.Column)
}

// emitSkeletonLineImport emits a //line directive ahead of a hoisted user
// import so go/types import errors (notably "imported and not used") resolve to
// the .gsx source instead of the synthesized overlay .x.go. The skeleton spec
// sits at column 8 (after the literal "import "), so the directive column is
// compensated by that 7-char prefix; when the source column is ≤ 7 (the common
// indented-import case) the compensated column would be < 1, so a line-only
// directive (column 1) is emitted rather than a misleading offset.
func emitSkeletonLineImport(sb skeletonWriter, fset *token.FileSet, pos token.Pos) {
	if fset == nil || !pos.IsValid() {
		return
	}
	const prefixLen = len("import ")
	p := fset.Position(pos)
	col := max(p.Column-prefixLen, 1)
	fmt.Fprintf(sb, "//line %s:%d:%d\n", p.Filename, p.Line, col)
}

// probeExpr returns the Go expression to probe for an interpolation / expr-attr.
// Without stages it is the trimmed seed; with stages it is the lowered pipeline
// (the SAME lowerPipe output the emitter uses), so the harvested type is the
// pipeline's RESULT type and resolution stays aligned with emission. The
// pipeline's used filter packages are merged into usedFilters so the skeleton
// imports each referenced package under its reserved alias.
// An unknown filter is TOLERATED here: the probe falls back to the bare seed so
// the skeleton still type-checks and generation proceeds to the POSITIONED
// unknown-filter diagnostic emit reports (the probe's bare error must not pre-empt
// the positioned bag.Errorf in generateFile). A valid pipeline lowers exactly as
// emit does, keeping emit ≡ probe.
//
// probeExpr is the SINGLE choke point every pipe stage's Args passes through
// before skeleton assembly, across every context (top-level interp/expr-attr
// pipelines, a literal's own whole-pipe, a hole's own pipe via holeProbeSeed,
// class-part/CF-arm pipelines, spread pipelines) — every caller hands it its
// own `.Stages`. A prefixed embedded literal (f`/js`/css`) inside a stage's
// Args (`x |> printf(f`%s!`)`) is NOT lowerable: st.Args is spliced VERBATIM
// as Go source by lowerPipe, and a literal's gsx-only syntax (the @{ } hole,
// the bare backtick-as-string-delimiter reinterpretation) is not valid Go —
// splicing it would hand the skeleton parser invalid syntax and abort with a
// cryptic, unpositioned cascade. Caught HERE, before lowerPipe ever sees it:
// bag.Errorf reports the positioned "literal-in-stage-args" diagnostic
// (Error severity — module.go's two generateFile gates additionally check
// !bag.HasErrors(), so this alone stops real emission from independently
// calling THIS SAME lowerPipe over the SAME bad st.Args and either splicing
// invalid Go or crashing gofmt's format.Source; in production gen/poison.go
// also poisons the .gsx's .x.go on any such diagnostic), and — exactly like
// the unknown-filter path above — the probe falls back to the bare seed so
// the skeleton still type-checks and no OTHER, unrelated "undefined" cascade
// drowns out this diagnostic.
func probeExpr(seed string, stages []gsxast.PipeStage, table funcTables, usedFilters map[string]string, owner gsxast.Node, bag *diag.Bag) (string, error) {
	if len(stages) == 0 {
		return strings.TrimSpace(seed), nil
	}
	for _, st := range stages {
		if strings.TrimSpace(st.Args) == "" {
			continue
		}
		if _, ok := gsxparser.ContainsEmbeddedLiteral(st.Args); ok {
			bag.Errorf(owner.Pos(), owner.End(), "literal-in-stage-args",
				"prefixed literals in pipe-stage arguments are not supported; assign the literal to a variable first")
			return strings.TrimSpace(seed), nil
		}
	}
	lowered, used, err := lowerPipe(seed, stages, table, probePipeWrap)
	if err != nil {
		return strings.TrimSpace(seed), nil
	}
	maps.Copy(usedFilters, used)
	return lowered, nil
}

type skeletonAuthoredRewrite struct {
	open  string
	close string
	pos   token.Pos
	text  string
}

func writeSkeletonProbeExpr(sb skeletonWriter, fset *token.FileSet, seedPos token.Pos, seed string, stages []gsxast.PipeStage, table funcTables, usedFilters map[string]string, owner gsxast.Node, bag *diag.Bag) error {
	if mapped, ok := sb.(*skeletonSourceWriter); !ok || !mapped.enabled {
		probe, err := probeExpr(seed, stages, table, usedFilters, owner, bag)
		if err != nil {
			return err
		}
		writeSkeletonGenerated(sb, probe)
		return nil
	}

	rewrites := make([]skeletonAuthoredRewrite, 0, 1+len(stages))
	mark := func(pos token.Pos, text string) string {
		index := len(rewrites) + 1
		boundary := componentTargetSeedBoundary{
			open:  fmt.Sprintf("\x00gsx-authored-%d-open\x00", index),
			close: fmt.Sprintf("\x00gsx-authored-%d-close\x00", index),
		}
		rewrites = append(rewrites, skeletonAuthoredRewrite{open: boundary.open, close: boundary.close, pos: pos, text: text})
		return boundary.open + text + boundary.close
	}

	markedSeed := seed
	if seedPos.IsValid() {
		markedSeed = mark(seedPos, seed)
	}
	markedStages := append([]gsxast.PipeStage(nil), stages...)
	for i := range markedStages {
		if markedStages[i].Args == "" || !markedStages[i].ArgsPos.IsValid() {
			continue
		}
		markedStages[i].Args = mark(markedStages[i].ArgsPos, markedStages[i].Args)
	}
	probe, err := probeExpr(markedSeed, markedStages, table, usedFilters, owner, bag)
	if err != nil {
		return err
	}
	for _, rewrite := range rewrites {
		if strings.Count(probe, rewrite.open) != 1 || strings.Count(probe, rewrite.close) != 1 {
			return fmt.Errorf("codegen: authored probe rewrite did not preserve exact source boundaries")
		}
	}

	remaining := probe
	seen := make(map[int]bool, len(rewrites))
	for len(remaining) > 0 {
		next := -1
		nextStart := len(remaining)
		for i, rewrite := range rewrites {
			count := strings.Count(remaining, rewrite.open)
			if count > 1 {
				return fmt.Errorf("codegen: authored probe rewrite duplicated source boundary")
			}
			if count == 1 {
				start := strings.Index(remaining, rewrite.open)
				if start < nextStart {
					next = i
					nextStart = start
				}
			}
		}
		if next < 0 {
			writeSkeletonGenerated(sb, remaining)
			break
		}
		rewrite := rewrites[next]
		if seen[next] {
			return fmt.Errorf("codegen: authored probe rewrite repeated source boundary")
		}
		closeRel := strings.Index(remaining[nextStart+len(rewrite.open):], rewrite.close)
		if closeRel < 0 {
			return fmt.Errorf("codegen: authored probe rewrite lost source boundary")
		}
		contentStart := nextStart + len(rewrite.open)
		contentEnd := contentStart + closeRel
		if remaining[contentStart:contentEnd] != rewrite.text {
			return fmt.Errorf("codegen: authored probe rewrite changed source bytes")
		}
		writeSkeletonGenerated(sb, remaining[:nextStart])
		if err := writeSkeletonAuthoredAt(sb, fset, rewrite.pos, rewrite.text, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion); err != nil {
			return err
		}
		seen[next] = true
		remaining = remaining[contentEnd+len(rewrite.close):]
	}
	if len(seen) != len(rewrites) {
		return fmt.Errorf("codegen: authored probe rewrite did not preserve every source boundary")
	}
	return nil
}

func writeSkeletonCanonicalProbe(sb skeletonWriter, helper string, fset *token.FileSet, seedPos token.Pos, seed string, stages []gsxast.PipeStage, table funcTables, usedFilters map[string]string, owner gsxast.Node, bag *diag.Bag) error {
	writeSkeletonGenerated(sb, helper+"(")
	if err := writeSkeletonProbeExpr(sb, fset, seedPos, seed, stages, table, usedFilters, owner, bag); err != nil {
		return err
	}
	writeSkeletonGenerated(sb, ")\n")
	return nil
}

// embeddedProbeSeed builds the Go source text probed as the SEED for a
// whole-literal pipeline's `lowerPipe(seed, stages)` call — an
// EmbeddedInterp's or EmbeddedAttr's node-level `|> f` — mirroring, at the
// TYPE level, what codegen's embeddedTextValueExpr (emit.go) assembles from
// the SAME segments: static *Text becomes the identical quoted string
// literal, joined with " + ".
//
// Each *Interp hole becomes _gsxstr(holeProbe), where holeProbe is the SAME
// probeExpr the individual-hole probe already uses (so a hole's own
// pipeline/tuple handling is identical, and it stays live/harvested exactly
// as it would be probed on its own). _gsxstr(any, ...any) string is a
// package-level skeleton helper (module_importer.go) that always yields a
// `string` — this is not an approximation: every successful branch of the
// REAL emit-time holeStringExpr (string(x), strconv.Format*, (x).String())
// ALSO always yields a Go expression of exactly the built-in `string` type.
// So this seed and codegen's later, precisely-typed seed differ only in
// WHICH string-producing snippet appears per hole, never in the resulting
// static type — the seed's overall type is string either way, which is all
// lowerPipe's stage lowering (and thus resolved[node]) depends on. This lets
// the probe resolve the node's piped RESULT type without first knowing each
// hole's real type, which is impossible at skeleton-build time (hole types
// are only known once THIS SAME skeleton has been type-checked and
// harvested — a later, one-shot step, not available mid-build).
func embeddedProbeSeed(segments []gsxast.Markup, table funcTables, usedFilters map[string]string, bag *diag.Bag) string {
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch s := seg.(type) {
		case *gsxast.Text:
			if s.Value == "" {
				continue
			}
			parts = append(parts, strconv.Quote(s.Value))
		case *gsxast.Interp:
			parts = append(parts, "_gsxstr("+holeProbeSeed(s, table, usedFilters, bag)+")")
		}
	}
	if len(parts) == 0 {
		return `""`
	}
	return strings.Join(parts, " + ")
}

// holeProbeSeed reconstructs one hole's Go expression at the TYPE level for
// embeddedProbeSeed, mirroring emit's assembleHoleSeed (emit.go): a plain hole
// is its Expr (via probeExpr, honoring its own `|>` pipeline); a hole carrying a
// nested prefixed literal (Interp.Embedded, seated by preprocessComponentCallSites)
// splices GoText verbatim and each nested literal as WRAP(embeddedProbeSeed(
// parts)) — the SAME WRAP embeddedProbeType gives that literal in emit, so the
// reconstructed seed has emit's exact static type (emit ≡ probe). A hole's own
// pipeline then applies over the reassembled seed, matching holeStringExpr /
// embeddedHoleExpr, which seed lowerPipe with the assembled expr. Element /
// Fragment parts cannot be a string seed and are rejected by emit's
// assembleHoleSeed with a positioned diagnostic; here they lower to a valid Go
// value placeholder (a nil-returning `_gsxrt.Node` IIFE) rather than the raw
// markup Expr — splicing the raw `<tag>` would produce invalid Go and abort the
// skeleton parse with a cryptic cascade BEFORE emit's positioned diagnostic can
// surface. The placeholder keeps the skeleton valid so exactly emit's one
// "element literals are not supported…" diagnostic reaches the user. It consumes
// no `_gsxelem` index and needs no probing: the element's own interps are
// already probed via the enclosing literal's emitProbes Element/Fragment case
// (the `_gsxuse` path), and emit rejects the hole regardless, so its harvested
// type is never read.
func holeProbeSeed(n *gsxast.Interp, table funcTables, usedFilters map[string]string, bag *diag.Bag) string {
	if n.Embedded == nil {
		probe, _ := probeExpr(n.Expr, n.Stages, table, usedFilters, n, bag)
		return probe
	}
	var sb strings.Builder
	for _, part := range n.Embedded {
		switch p := part.(type) {
		case gsxast.GoText:
			sb.WriteString(p.Src)
		case *gsxast.EmbeddedInterp:
			_, wrapOpen, wrapClose := embeddedProbeType(p.Lang)
			sb.WriteString(wrapOpen)
			sb.WriteString(embeddedProbeSeed(p.Segments, table, usedFilters, bag))
			sb.WriteString(wrapClose)
		default:
			// *Element/*Fragment: unsupported in a string-seed hole. Emit's
			// assembleHoleSeed rejects the whole hole on the first such part with a
			// positioned diagnostic, so return a type-valid placeholder for the
			// entire hole and let that single emit diagnostic surface.
			return "func() _gsxrt.Node { return nil }()"
		}
	}
	probe, _ := probeExpr(strings.TrimSpace(sb.String()), n.Stages, table, usedFilters, n, bag)
	return probe
}

// embeddedProbeType returns the probe IIFE's return type and the seed wrapper
// (open/close) for a prefixed backtick literal in Go-expression position, keyed
// by its language. It MUST stay in lockstep with emit's lowering (emit ≡ probe):
//   - EmbeddedText (f`) → the built-in `string`, no wrapper (embeddedValueExpr
//     assembles a plain Go string concat).
//   - EmbeddedJS (js`)  → `_gsxrt.RawJS`, wrapping the string seed in a
//     _gsxrt.RawJS(...) conversion (emit lowers to _gsxrt.RawJS(concat)).
//   - EmbeddedCSS (css`) → `_gsxrt.RawCSS`, wrapping in _gsxrt.RawCSS(...).
//
// The seed itself is always a `string` expression (embeddedProbeSeed), and
// RawJS/RawCSS are defined `type RawJS string` / `type RawCSS string`, so the
// conversion type-checks and gives the IIFE — hence the surrounding Go
// expression — the exact static type emit produces.
func embeddedProbeType(lang gsxast.EmbeddedLang) (retType, wrapOpen, wrapClose string) {
	switch lang {
	case gsxast.EmbeddedJS:
		return "_gsxrt.RawJS", "_gsxrt.RawJS(", ")"
	case gsxast.EmbeddedCSS:
		return "_gsxrt.RawCSS", "_gsxrt.RawCSS(", ")"
	default:
		return "string", "", ""
	}
}

// probeEmbeddedInterpIIFE splices the analyze-side probe IIFE for a prefixed
// backtick literal (f`/js`/css`) sitting in a Go-expression position — the ONE
// lowering shared by all three container kinds that carry such literals:
// top-level GoWithElements parts (buildSkeleton's region loop), an
// interpolation's Interp.Embedded split (emitProbes), and a `{{ }}` Go block's
// GoBlock.Embedded split (emitProbes). It registers the literal's segments at
// index N in gw, emits `func() T { _gsxelem(N); …probes…; return WRAP(seed) }()`
// where T/WRAP come from embeddedProbeType(lang) (so the IIFE — hence the
// surrounding Go expression — gets the exact static type emit produces, keeping
// emit ≡ probe), and probes each @{…} hole against the enclosing scope
// (recvVar/recvTypeName). Its parameter list mirrors emitProbes so any of the
// three sites can call it with its own scope. The whole-literal-pipeline guard
// (p.Stages) stays at each call site, which words that diagnostic per-context.
func probeEmbeddedInterpIIFE(sb skeletonWriter, segs []gsxast.Markup, lang gsxast.EmbeddedLang, table funcTables, recvVar, recvTypeName string, usedFilters map[string]string, fset *token.FileSet, ctrlOff map[gsxast.Node]int, targetRegistry *componentTargetMarkerRegistry, gw *[][]gsxast.Markup, bag *diag.Bag, cfTemp *int) error {
	idx := len(*gw)
	*gw = append(*gw, segs)
	retType, wrapOpen, wrapClose := embeddedProbeType(lang)
	fmt.Fprintf(sb, "func() %s {\n", retType)
	fmt.Fprintf(sb, "_gsxelem(%d)\n", idx)
	sb.WriteString("var ctx _gsxctx.Context\n_ = ctx\n")
	// Expression-position literal segments carry only text + Go-expr holes, never
	// component-call markup, so no `{ attrs... }` fallthrough can appear here:
	// enclosingAttrsBound is irrelevant, pass false (the pre-#104 behavior).
	if err := emitProbes(sb, segs, table, recvVar, recvTypeName, usedFilters, fset, ctrlOff, targetRegistry, gw, bag, cfTemp, false); err != nil {
		return err
	}
	fmt.Fprintf(sb, "return %s%s%s\n}()", wrapOpen, embeddedProbeSeed(segs, table, usedFilters, bag), wrapClose)
	return nil
}

// firstDirectGoBlockMarkup returns the first direct `<tag>` element or fragment
// literal in a split `{{ }}` block. materializeEmbeddedMarkup calls it once and
// stores the result on GoBlock.UnsupportedMarkup; every later consumer reads
// that annotation instead of independently reimplementing this policy.
func firstDirectGoBlockMarkup(parts []gsxast.GoPart) gsxast.GoPart {
	for _, p := range parts {
		switch p.(type) {
		case *gsxast.Element, *gsxast.Fragment:
			return p
		}
	}
	return nil
}

// harvest reads each interpolation's resolved type from a type-checked skeleton
// file. An interpolation probe is now an ExprStmt whose call target is the
// identifier `_gsxuse`; harvest the single argument's type.
func harvest(f *goast.File, comps []*gsxast.Component, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr, plan *componentTargetPlan, candidates *callSiteRegistry) {
	// Key by receiver-type + method name, not name alone: two method components
	// with the same method name on different receivers (e.g. (UsersPage) Row and
	// (OrdersPage) Row) are distinct, and their skeleton funcs are distinct
	// methods — keying on name alone would map both skeleton funcs to one
	// component and leave the other's interps unresolved.
	byKey := map[string]*gsxast.Component{}
	for _, c := range comps {
		byKey[componentKey(c)] = c
		if plan != nil {
			if emission, ok := plan.emission(c); ok && emission.splitBody {
				byKey[componentKeyWithName(c, emission.bodyName)] = c
			}
		}
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*goast.FuncDecl)
		if !ok {
			continue
		}
		c, ok := byKey[funcDeclKey(fd)]
		if !ok || fd.Body == nil {
			continue
		}
		harvestBody(fd.Body, c.Body, info, out, exprOut, candidates)
	}
}

// harvestBody resolves one skeleton func/closure body's probe calls back onto
// the gsx nodes of the markup it was generated from. bodyMarkup is the markup
// whose emitProbes output produced body — a component's Body, or (for an
// embedded element, via harvestEmbeddedElements) a single-element markup
// slice. Extracted from harvest so BOTH a component's top-level skeleton func
// and a GoWithElements-embedded element's inline IIFE share ONE resolution
// path (emit≡probe: the same probe shapes, harvested the same way).
func harvestBody(body *goast.BlockStmt, bodyMarkup []gsxast.Markup, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr, candidates *callSiteRegistry) {
	var nodes []gsxast.Node
	collectExprs(bodyMarkup, &nodes, candidates)
	k := 0
	goast.Inspect(body, func(node goast.Node) bool {
		// A GoWithElements-embedded value's probe IIFE (`func() _gsxrt.Node {
		// _gsxelem(N); … }()`, spliced into a tag-carrying interp's probe) carries
		// its OWN _gsxuse/_gsxuseq calls for the embedded element's interps.
		// collectExprs does NOT recurse into Interp.Embedded, so those probes have
		// no slot in `nodes`; harvestEmbeddedElements resolves them separately
		// (keyed on the _gsxelem(N) marker). SKIP the IIFE subtree here, or its
		// inner probes would over-run k and misalign every sibling after a
		// tag-carrying interp.
		if fl, ok := node.(*goast.FuncLit); ok && isEmbeddedElemProbeFuncLit(fl) {
			return false
		}
		call, ok := node.(*goast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*goast.Ident)
		// _gsxuseq is the quiet child-prop harvest variant; it carries a node's
		// type identically to _gsxuse and occupies the SAME k-ordering slot
		// (collectExprs adds child-prop ExprAttr nodes in source order), so both
		// must be matched here or the k alignment would drift.
		if !ok || (id.Name != "_gsxuse" && id.Name != "_gsxuseq") || len(call.Args) != 1 {
			return true
		}
		if k < len(nodes) {
			out[nodes[k]] = info.Types[call.Args[0]].Type
			if exprOut != nil {
				exprOut[nodes[k]] = call.Args[0]
			}
			k++
		}
		return true
	})

}

// harvestEmbeddedElements resolves the probe calls inside each
// GoWithElements-embedded value's inline IIFE (see buildSkeleton) back onto
// that value's markup list. Each such IIFE is a func literal whose FIRST
// statement is the marker call `_gsxelem(N)`, where N indexes `markups` in
// source order; the marker is what distinguishes an embedded-value probe IIFE
// from any func literal the user wrote verbatim in the surrounding Go. For
// each marked IIFE, harvestBody runs over its body with that entry's markup
// list — the SAME resolution a component body gets, so a mistyped
// interpolation/prop inside an embedded element or fragment resolves (and is
// diagnosed) identically.
// isEmbeddedElemProbeFuncLit reports whether fl is a GoWithElements-embedded
// value's probe IIFE — a func literal whose FIRST body statement is the marker
// call `_gsxelem(N)` (buildSkeleton / emitProbes' Interp.Embedded case). This
// is the single predicate BOTH harvestEmbeddedElements (which harvests these
// IIFEs, keyed on the marker) and harvestBody (which SKIPs their subtree to keep
// its k-counter aligned) use, so the two can never disagree about what counts as
// an embedded probe IIFE.
func isEmbeddedElemProbeFuncLit(fl *goast.FuncLit) bool {
	if fl.Body == nil || len(fl.Body.List) == 0 {
		return false
	}
	es, ok := fl.Body.List[0].(*goast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*goast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*goast.Ident)
	return ok && id.Name == "_gsxelem" && len(call.Args) == 1
}

func harvestEmbeddedElements(f *goast.File, markups [][]gsxast.Markup, info *types.Info, out map[gsxast.Node]types.Type, exprOut map[gsxast.Node]goast.Expr, candidates *callSiteRegistry) {
	if len(markups) == 0 {
		return
	}
	goast.Inspect(f, func(node goast.Node) bool {
		fl, ok := node.(*goast.FuncLit)
		if !ok || !isEmbeddedElemProbeFuncLit(fl) {
			return true
		}
		call := fl.Body.List[0].(*goast.ExprStmt).X.(*goast.CallExpr)
		lit, ok := call.Args[0].(*goast.BasicLit)
		if !ok || lit.Kind != token.INT {
			return true
		}
		idx, err := strconv.Atoi(lit.Value)
		if err != nil || idx < 0 || idx >= len(markups) {
			return true
		}
		harvestBody(fl.Body, markups[idx], info, out, exprOut, candidates)
		return true
	})
}

// componentKey identifies a component by receiver-type + name, so same-named
// methods on different receivers are distinct. A function component (no receiver)
// keys on its name alone (with a leading "." marker so it can never collide with
// a method named the same on a receiver type called "").
func componentKey(c *gsxast.Component) string {
	return componentKeyWithName(c, c.Name)
}

func componentKeyWithName(c *gsxast.Component, name string) string {
	if c.Recv == "" {
		return "." + name
	}
	_, _, recvTypeName, err := parseRecv(c.Recv)
	if err != nil {
		// Keep syntactically invalid method receivers isolated until the positioned
		// receiver diagnostic is produced. They must never enter the package-
		// function namespace or collapse with a different invalid receiver.
		return fmt.Sprintf("!invalid-receiver:%d:%s.%s", len(c.Recv), c.Recv, name)
	}
	return recvTypeName + "." + name
}

// funcDeclKey mirrors componentKey for a type-checked skeleton FuncDecl: a method
// keys on its receiver type name + method name; a plain func on "." + name.
func funcDeclKey(fd *goast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return "." + fd.Name.Name
	}
	return recvTypeIdent(fd.Recv.List[0].Type) + "." + fd.Name.Name
}

// recvTypeIdent extracts the receiver type's base name from a skeleton method's
// receiver expression: T, *T, T[X], *T[X] → "T".
func recvTypeIdent(e goast.Expr) string {
	switch t := e.(type) {
	case *goast.Ident:
		return t.Name
	case *goast.StarExpr:
		return recvTypeIdent(t.X)
	case *goast.IndexExpr:
		return recvTypeIdent(t.X)
	case *goast.IndexListExpr:
		return recvTypeIdent(t.X)
	}
	return ""
}

// buildSigTypeRefs pairs each navigable signature type/name region in component
// c — parameter types, type-parameter names/constraints, and (for a method
// component) its receiver type — with the corresponding type-checked skeleton
// expression, for the LSP's go-to-definition / hover. Parameter types live in
// the exact authored function signature and the receiver type lives in the
// skeleton method's receiver. Returns nil when c's skeleton shape cannot be
// located (a skipped/stub component) or it carries no navigable types.
func buildSigTypeRefs(gf *goast.File, c *gsxast.Component, plan *componentTargetPlan) []SigTypeRef {
	key := componentKey(c)
	if plan != nil {
		if emission, ok := plan.emission(c); ok && emission.splitBody && !emission.public {
			key = componentKeyWithName(c, emission.bodyName)
		}
	}
	fd := funcDeclForKey(gf, key)
	if fd == nil {
		return nil
	}
	var refs []SigTypeRef

	// Type-parameter names and constraints. The skeleton emits the type-param
	// list verbatim, so offsets from a synthetic parse of c.TypeParams align with
	// the matching skeleton TypeParams fields.
	if c.TypeParams != "" && c.TypeParamsPos.IsValid() && fd.Type.TypeParams != nil {
		if tpl, tpFset, err := parseTypeParamFieldList(c.TypeParams); err == nil && tpl != nil {
			off := func(p token.Pos) int { return tpFset.Position(p).Offset - len(typeParamSynthPrefix) }
			for i, field := range tpl.List {
				if i >= len(fd.Type.TypeParams.List) {
					break
				}
				skelField := fd.Type.TypeParams.List[i]
				for j, name := range field.Names {
					if j >= len(skelField.Names) {
						break
					}
					refs = append(refs, SigTypeRef{
						GSXPos:  c.TypeParamsPos + token.Pos(off(name.Pos())),
						Len:     len(name.Name),
						SkelTyp: skelField.Names[j],
					})
				}
				if field.Type != nil && skelField.Type != nil {
					refs = append(refs, SigTypeRef{
						GSXPos:  c.TypeParamsPos + token.Pos(off(field.Type.Pos())),
						Len:     off(field.Type.End()) - off(field.Type.Pos()),
						SkelTyp: skelField.Type,
					})
				}
			}
		}
	}

	// Parameter types.
	if params, err := parseComponentParamDecls(c.Params); err == nil && len(params) > 0 {
		if skel := paramSkelTypes(fd, params); skel != nil {
			for i, p := range params {
				refs = append(refs, SigTypeRef{
					GSXPos:  c.ParamsPos + token.Pos(p.typeOff),
					Len:     p.typeLen,
					SkelTyp: skel[i],
				})
			}
		}
	}

	// Method receiver type. The skeleton emits the receiver clause verbatim, so
	// its bytes match the .gsx; bridge into it like a param type. (Go forbids
	// methods on non-local types, so a receiver type is always same-package.)
	if c.Recv != "" && c.RecvPos.IsValid() && fd.Recv != nil && len(fd.Recv.List) == 1 {
		if off, length, ok := recvTypeSpan(c.Recv); ok {
			refs = append(refs, SigTypeRef{
				GSXPos:  c.RecvPos + token.Pos(off),
				Len:     length,
				SkelTyp: fd.Recv.List[0].Type,
			})
		}
	}
	return refs
}

// paramSkelTypes returns the skeleton type expression for each logical authored
// parameter in declaration order. A grouped Go field contributes its type once
// per name. Returns nil when the exact skeleton shape cannot be located.
func paramSkelTypes(fd *goast.FuncDecl, params []componentParamDecl) []goast.Expr {
	if fd.Type.Params == nil {
		return nil
	}
	var skel []goast.Expr
	for _, field := range fd.Type.Params.List {
		for range field.Names {
			skel = append(skel, field.Type)
		}
	}
	if len(skel) != len(params) {
		return nil
	}
	return skel
}

// recvTypeSpan parses a method-component receiver clause (e.g. "(p *Page)") and
// returns the byte offset and length of the receiver TYPE within that clause
// string (e.g. "*Page"), for locating it in the .gsx. ok is false if the clause
// does not parse as a single receiver.
func recvTypeSpan(recv string) (off, length int, ok bool) {
	src := strings.TrimSpace(recv)
	if src == "" {
		return 0, 0, false
	}
	const prefix = "package _\nfunc "
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+src+" _m() {}", 0)
	if err != nil {
		return 0, 0, false
	}
	fn, isFn := f.Decls[0].(*goast.FuncDecl)
	if !isFn || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return 0, 0, false
	}
	t := fn.Recv.List[0].Type
	tStart := fset.Position(t.Pos()).Offset
	tEnd := fset.Position(t.End()).Offset
	return tStart - len(prefix), tEnd - tStart, true
}

// funcDeclForKey returns the skeleton FuncDecl whose component key matches key,
// or nil. Used to locate a component's generated signature in its skeleton file.
func funcDeclForKey(gf *goast.File, key string) *goast.FuncDecl {
	for _, d := range gf.Decls {
		if fd, ok := d.(*goast.FuncDecl); ok && funcDeclKey(fd) == key {
			return fd
		}
	}
	return nil
}

// isRawCSS reports whether t is the named type github.com/gsxhq/gsx.RawCSS —
// the author-vouched safe-CSS string, emitted raw in a CSS context.
func isRawCSS(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "RawCSS" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "github.com/gsxhq/gsx"
}

type category int

const (
	catUnsupported category = iota
	catString
	catBytes
	catStringSlice // []string (any string-kinded elem) → joined with single spaces
	catInt
	catUint
	catFloat
	catBool
	catNode
	catNodeSlice
	catStringer
	catAnyMixed // type param whose non-tilde type set mixes renderable basic kinds → runtime dispatch
)

// classify maps a resolved type to a render category using structural checks
// (method sets), so it needs no handle to the gsx.Node / fmt.Stringer interface
// types.
func classify(t types.Type) category {
	if t == nil {
		return catUnsupported
	}
	if implementsNode(t) {
		return catNode
	}
	if s, ok := t.Underlying().(*types.Slice); ok && implementsNode(s.Elem()) {
		return catNodeSlice
	}
	if implementsStringer(t) {
		return catStringer
	}
	if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
		return classifyTypeParam(tp)
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch {
		case u.Info()&types.IsString != 0:
			return catString
		case u.Info()&types.IsUnsigned != 0:
			return catUint
		case u.Info()&types.IsInteger != 0:
			return catInt
		case u.Info()&types.IsFloat != 0:
			return catFloat
		case u.Info()&types.IsBoolean != 0:
			return catBool
		}
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			return catBytes
		}
		// []string joins with single spaces — the token-list reading, which is what
		// class/style have always done for a bag's []string value. The JSON reading
		// is reached explicitly via a js`@{v}` literal, so the plain form is free to
		// be the friendly HTML one.
		//
		// The ELEMENT must be exactly `string`, not merely string-kinded: the emit
		// lowers to strings.Join, whose parameter is []string, and a named element
		// ([]Slug) is not assignable to it — it would classify here and then fail to
		// compile. A named SLICE (type Tags []string) is fine: its element is still
		// string, so it is assignable. anyRenderVal applies the identical rule at
		// runtime; TestDispatchAgreement pins that they match.
		if types.Identical(u.Elem(), types.Typ[types.String]) {
			return catStringSlice
		}
	}
	return catUnsupported
}

// classifyTypeParam maps a type parameter to a render category from its
// constraint's normalized term set (typeparams.NormalTerms — real
// normalization of unions/embeddings, not an approximation).
//
//   - every term classifies to the SAME basic-kind category → that category:
//     the static conversions the emitter writes (string(v), int64(v), …)
//     compile for the whole type set, tilde or not.
//   - terms mix renderable categories, are ALL non-tilde, AND every term is
//     runtime-dispatchable (isRuntimeDispatchableTerm: an unnamed predeclared
//     type, an unnamed []byte, or a Stringer) → catAnyMixed: anyRenderString's
//     dynamic type switch matches every term in the set, so the runtime
//     dispatch (Writer.TextAny/AttrAny) is total.
//   - any ~term in a mixed set, or any non-tilde term that is NOT
//     runtime-dispatchable (e.g. a named scalar like `type Slug string`,
//     which classifies via Underlying but has no case in anyRenderString's
//     exact-type switch) → catUnsupported: reject statically rather than
//     fail mid-render.
//   - a term classifying to catNode/catNodeSlice/catStringer contributes no
//     METHOD to T (term ≠ embedded method), so it has no static call path;
//     Stringer terms are still fine in the runtime switch, Node terms are not
//     (rendering needs ctx). Method-BASED constraints (T fmt.Stringer,
//     T gsx.Node) never reach here — classify's method-set checks above run
//     first and see constraint methods via types.NewMethodSet.
func classifyTypeParam(tp *types.TypeParam) category {
	terms, err := typeparams.NormalTerms(tp)
	if err != nil || len(terms) == 0 {
		return catUnsupported // empty/invalid type set, or `any`
	}
	uniform := true
	hasTilde := false
	allDispatchable := true
	var first category
	for i, tm := range terms {
		c := classify(tm.Type())
		switch c {
		case catUnsupported, catNode, catNodeSlice:
			return catUnsupported
		}
		if tm.Tilde() {
			hasTilde = true
		}
		if !isRuntimeDispatchableTerm(tm.Type()) {
			allDispatchable = false
		}
		if i == 0 {
			first = c
		} else if c != first {
			uniform = false
		}
	}
	if uniform && first != catStringer {
		return first
	}
	if hasTilde || !allDispatchable {
		return catUnsupported
	}
	return catAnyMixed
}

// isRuntimeDispatchableTerm reports whether a mixed-constraint term's type
// has a matching case in anyRenderString's dynamic type switch (writer.go):
// either an UNNAMED predeclared type (*types.Basic — string, int, float64,
// …), the unnamed []byte (unnamed slice of unnamed byte), or any type
// implementing fmt.Stringer (matched via anyRenderString's `case
// fmt.Stringer`, which catches named Stringer types too). A named type is
// none of these — anyRenderString only has cases for the exact predeclared
// types, not arbitrary named types with matching Underlying — so it must
// disqualify the term's constraint from the mixed runtime-dispatch path.
// That goes for named types at EITHER level of a slice term: a defined
// `type Bytes []byte` (named slice — Unalias(t) is *types.Named, not
// *types.Slice) and a defined `type MyByte byte` element (`[]MyByte` is a
// distinct type from []byte at runtime) both fail. The element check uses
// Unalias, NOT Underlying: an alias `type B = byte` keeps []B identical to
// []byte (dispatchable), while Underlying would wrongly admit defined types.
func isRuntimeDispatchableTerm(t types.Type) bool {
	if classify(t) == catStringer {
		return true
	}
	switch u := types.Unalias(t).(type) {
	case *types.Basic:
		return true
	case *types.Slice:
		b, ok := types.Unalias(u.Elem()).(*types.Basic)
		return ok && b.Kind() == types.Byte
	}
	return false
}

// implementsNode reports whether t has a method Render(context.Context, io.Writer) error.
func implementsNode(t types.Type) bool {
	m := lookupMethod(t, "Render")
	if m == nil {
		return false
	}
	sig := m.Type().(*types.Signature)
	if sig.Params().Len() != 2 || sig.Results().Len() != 1 {
		return false
	}
	if sig.Params().At(0).Type().String() != "context.Context" {
		return false
	}
	if sig.Params().At(1).Type().String() != "io.Writer" {
		return false
	}
	return sig.Results().At(0).Type().String() == "error"
}

// implementsStringer reports whether t has a method String() string.
func implementsStringer(t types.Type) bool {
	m := lookupMethod(t, "String")
	if m == nil {
		return false
	}
	sig := m.Type().(*types.Signature)
	return sig.Params().Len() == 0 && sig.Results().Len() == 1 &&
		sig.Results().At(0).Type().String() == "string"
}

// lookupMethod returns the method `name` in t's VALUE method set, or nil. It
// deliberately does NOT probe the pointer method set: classify uses this to
// decide whether to emit `gw.Node(ctx, expr)` / `(expr).String()`, both of which
// pass the value BY VALUE — Go does not auto-address an interface/method-value
// argument, so a pointer-receiver method is not callable there. A value type
// whose pointer (but not value) implements Render must be passed as `*T`.
func lookupMethod(t types.Type, name string) *types.Func {
	ms := types.NewMethodSet(t)
	if sel := ms.Lookup(nil, name); sel != nil {
		if fn, ok := sel.Obj().(*types.Func); ok {
			return fn
		}
	}
	return nil
}

// collectExprs gathers the type-needing expression nodes (*Interp and *ExprAttr)
// in depth-first source order — per element, attribute expressions BEFORE
// children — matching emitProbes/genNode traversal so the k-th probe aligns with
// the k-th node.
func collectExprs(nodes []gsxast.Markup, out *[]gsxast.Node, candidates *callSiteRegistry) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Interp:
			*out = append(*out, t)
		case *gsxast.EmbeddedInterp:
			// Holes first (matching emitProbes' order), then the node itself
			// ONLY when it carries a whole-literal pipeline — a Stages-less
			// literal renders per-segment and needs no node-level type.
			collectExprs(t.Segments, out, candidates)
			if len(t.Stages) > 0 {
				*out = append(*out, t)
			}
		case *gsxast.Element:
			if t.IsComponent || candidates != nil && candidates.hasCandidate(t) {
				// Child component: collect ExprAttr nodes (prop values) first, then
				// OrderedPair nodes (pair values, one per pair per OrderedAttrsAttr),
				// then class-attr CF arms + plain parts (walkClassAttrs, recursing
				// CondAttr), then cond-attr BRANCH ExprAttr values (walkBranchAttrExprs),
				// then slot content (markup-attr values and children). emitProbes emits
				// _gsxuseq/_gsxuse probes in the SAME order — ExprAttrs, pairs, parts,
				// branch ExprAttrs, then slot content — so the k-th probe aligns with
				// the k-th node for ALL child-component branches. The ExprAttr types in
				// resolved let genChildComponent detect and hoist (T, error) tuple-valued
				// props; the OrderedPair types let it detect and hoist tuple pair values;
				// the branch ExprAttr / class-part types let a cond-attr branch hoist its
				// own (T, error) values (Task 3 consumer).
				for _, a := range t.Attrs {
					if ea, ok := a.(*gsxast.ExprAttr); ok {
						*out = append(*out, ea)
					}
				}
				walkSpreadAttrs(t.Attrs, func(sa *gsxast.SpreadAttr) {
					*out = append(*out, sa)
				})
				// Collect pair nodes AFTER all ExprAttrs, in attr source order then
				// pair order — matching the emitProbes ordering exactly.
				for _, a := range t.Attrs {
					if oa, ok := a.(*gsxast.OrderedAttrsAttr); ok {
						for i := range oa.Pairs {
							*out = append(*out, &oa.Pairs[i])
						}
					}
				}
				// Collect *ValueArm nodes for CF arms and *ClassPart nodes for EVERY
				// plain part (conditional or not) AFTER all OrderedPair nodes —
				// matching the _gsxuse probes emitProbes emits after the pair probes
				// (the shared walkClassAttrs recurses CondAttr on both sides).
				// classEntryExpr reads resolved[arm] for (T, error) CF-arm unwrap,
				// resolved[part] for plain-part tuple unwrap, AND (#85)
				// resolved[part] to dispatch applyClassRenderer for a conditional
				// part's value — a conditional part is stubbed in the props-literal
				// probe exactly like an unconditional one, so it needs the same
				// harvest.
				walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
					for i := range ca.Parts {
						if ca.Parts[i].CF != nil {
							for _, arm := range valueFormArms(ca.Parts[i].CF) {
								*out = append(*out, arm)
							}
						} else if ca.Parts[i].CSSSegments == nil {
							*out = append(*out, &ca.Parts[i])
						}
					}
				})
				// Collect ExprAttr nodes nested in a component cond-attr branch
				// (`{ if C { attr={expr} } }`) AFTER the parts pass — the leading
				// ExprAttr pass above is top-level-only, and the positional call probe embeds
				// the whole AttrsCond(...) expression without a
				// per-value harvest probe, so branch ExprAttr values would otherwise
				// have no resolved entry. emitProbes emits the matching _gsxuseq probes
				// in the SAME position and Then→Else order. (Branch class parts / CF
				// arms are already covered by the walkClassAttrs parts pass above, which
				// recurses CondAttr, so they are NOT re-collected here.)
				walkBranchAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
					*out = append(*out, ea)
				})
				walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
					collectExprs(value, out, candidates)
				})
				// Collect each braced-attr whole-literal pipeline node AFTER the
				// markup-attr/hole nodes above — emitProbes emits the matching
				// node-level _gsxuse probe in the SAME position (via
				// walkEmbeddedAttrStages), so the k-th probe stays aligned.
				walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
					*out = append(*out, ea)
				})
				collectExprs(t.Children, out, candidates)
				continue
			}
			// Collect each attr-expr (top-level and CondAttr-nested) in canonical
			// order, before the element's children — emitProbes walks identically.
			walkAttrExprs(t.Attrs, func(ea *gsxast.ExprAttr) {
				*out = append(*out, ea)
			})
			// Then each element-spread expr, AFTER all ExprAttrs — emitProbes emits the
			// _gsxuseq spread probes in the SAME position (after the _gsxuse ExprAttr
			// probes), so the k-th spread node maps to the k-th spread probe.
			walkSpreadAttrs(t.Attrs, func(sa *gsxast.SpreadAttr) {
				*out = append(*out, sa)
			})
			// Collect ValueArm nodes for value-form CF parts, and *ClassPart nodes
			// for EVERY plain part (conditional or not, #88), in source order: for
			// each ClassAttr in attr order, for each part, in arm source order for
			// CF parts. emitProbes emits _gsxuse probes in the SAME order so the
			// k-th probe aligns with the k-th node, populating resolved[arm] for
			// hoistValueCF's unwrap and resolved[part] for (T, error) auto-unwrap
			// AND (#88) so composedParts' applyRenderer call for a conditional
			// part's value has a non-nil resolved[part] to dispatch on. The
			// liveness path (walkLivenessAttrExprs) skips ALL plain parts (they now
			// get _gsxuse probes which also serve as liveness refs; a conditional
			// part's cond guard is still referenced separately). walkClassAttrs
			// recurses CondAttr Then/Else in lockstep with emitProbes, so class
			// attrs nested in a conditional attr group collect too.
			walkClassAttrs(t.Attrs, func(ca *gsxast.ClassAttr) {
				for i := range ca.Parts {
					if ca.Parts[i].CF != nil {
						for _, arm := range valueFormArms(ca.Parts[i].CF) {
							*out = append(*out, arm)
						}
					} else if ca.Parts[i].CSSSegments == nil {
						*out = append(*out, &ca.Parts[i])
					}
				}
			})
			// Then each explicit JS attribute literal (e.g. x-data=js`…@{x}…`) interp, in
			// attr source order — emitProbes walks identically (same walkMarkupAttrs).
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				collectExprs(value, out, candidates)
			})
			// Collect each braced-attr whole-literal pipeline node AFTER the
			// markup-attr/hole nodes above — matching emitProbes' ordering.
			walkEmbeddedAttrStages(t.Attrs, func(ea *gsxast.EmbeddedAttr) {
				*out = append(*out, ea)
			})
			collectExprs(t.Children, out, candidates)
		case *gsxast.Fragment:
			collectExprs(t.Children, out, candidates)
		case *gsxast.ForMarkup:
			collectExprs(t.Body, out, candidates)
		case *gsxast.IfMarkup:
			collectExprs(t.Then, out, candidates)
			collectExprs(t.Else, out, candidates)
		case *gsxast.SwitchMarkup:
			for _, cc := range t.Cases {
				collectExprs(cc.Body, out, candidates)
			}
		}
	}
}

// walkAttrExprs invokes fn for each type-needing *ExprAttr in an element's attr
// list, in canonical source order: each top-level *ExprAttr where it sits, and —
// for a *CondAttr — its Then attr-exprs then its Else attr-exprs (recursing
// nested *CondAttrs, so an else-if chain is visited in order). Other attr kinds
// (Static/Bool/Class/Spread) contribute no expr node. This is the SINGLE walk
// shared by collectExprs (builds the ordered node list) and emitProbes (emits one
// _gsxuse per node) so the k-th probe always maps to the k-th node — no drift.
func walkAttrExprs(attrs []gsxast.Attr, fn func(*gsxast.ExprAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ExprAttr:
			fn(at)
		case *gsxast.CondAttr:
			walkAttrExprs(at.Then, fn)
			walkAttrExprs(at.Else, fn)
		}
	}
}

// walkBranchAttrExprs invokes fn for each *ExprAttr nested inside a component
// cond-attr group (`{ if C { attr={expr} } else { … } }`) — the Then attr-exprs
// then the Else attr-exprs of every *CondAttr, recursing nested *CondAttrs via
// walkAttrExprs so an else-if chain is visited in order. TOP-LEVEL ExprAttrs are
// deliberately NOT visited (they are collected/probed separately in the
// component case's leading ExprAttr pass). Branch class parts and value-form CF
// arms are ALSO not visited here — the shared walkClassAttrs already recurses
// CondAttr Then/Else, so those branch positions are covered by the component
// case's parts pass; only branch ExprAttr values need this dedicated walk. It is
// the SINGLE walk shared by collectExprs (which appends each branch ExprAttr
// node AFTER the parts pass) and emitProbes (which emits one _gsxuseq harvest
// probe per branch ExprAttr in the SAME position), so the k-th branch-ExprAttr
// probe always maps to the k-th collected branch-ExprAttr node.
func walkBranchAttrExprs(attrs []gsxast.Attr, fn func(*gsxast.ExprAttr)) {
	for _, a := range attrs {
		if ca, ok := a.(*gsxast.CondAttr); ok {
			walkAttrExprs(ca.Then, fn)
			walkAttrExprs(ca.Else, fn)
		}
	}
}

// walkSpreadAttrs invokes fn for each *SpreadAttr in an element's attr list, in
// canonical source order (recursing *CondAttr Then→Else, like walkAttrExprs). It is
// the SINGLE walk shared by collectExprs (which appends each spread node AFTER all
// the element's ExprAttrs) and emitProbes (which emits one _gsxuseq harvest probe
// per spread, AFTER all the element's _gsxuse ExprAttr probes), so the k-th spread
// probe always maps to the k-th collected spread node.
func walkSpreadAttrs(attrs []gsxast.Attr, fn func(*gsxast.SpreadAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.SpreadAttr:
			fn(at)
		case *gsxast.CondAttr:
			walkSpreadAttrs(at.Then, fn)
			walkSpreadAttrs(at.Else, fn)
		}
	}
}

// walkClassAttrs invokes fn for each *ClassAttr in an element's attr list, in
// canonical source order (recursing *CondAttr Then→Else, like walkAttrExprs).
// It is the SINGLE walk shared by collectExprs (which appends each CF-arm /
// plain-part node, conditional or not, #88) and emitProbes (which emits one
// _gsxuse probe per such node), so the k-th probe always maps to the k-th
// collected node — including for class/style attrs nested inside a
// conditional attr group.
func walkClassAttrs(attrs []gsxast.Attr, fn func(*gsxast.ClassAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ClassAttr:
			fn(at)
		case *gsxast.CondAttr:
			walkClassAttrs(at.Then, fn)
			walkClassAttrs(at.Else, fn)
		}
	}
}

// walkLivenessAttrExprs invokes fnCF for each value-form CF part and fnCond
// for each cond guard (a ClassPart's `: cond`, incl. on css literals, or an
// in-tag conditional-attribute *CondAttr) in an element's attr list, in
// source order (recursing CondAttr Then/Else) — the attr fragments that
// walkAttrExprs does NOT yield, and that carry no type harvest. (SpreadAttr
// exprs ARE harvested, via walkSpreadAttrs + _gsxuseq, which doubles as their
// liveness reference, so they are not handled here.) Every ClassPart VALUE
// expr — CF arm or plain part, conditional or not (#88) — now gets its own
// _gsxuse probe in emitProbes, which both harvests its type (for renderer
// application and (T, error) unwrap) and keeps it live, so this walk no
// longer references any ClassPart value expr directly: a second `_ = (expr)`
// reference here would fail to type-check for a (T, error) multi-return
// call. A cond guard is emitted verbatim by codegen (never probed, never
// piped), so it still needs its own liveness reference: fnCond receives the
// owning node and source position so the caller can emit an `if cond {\n}`
// statement and record a ctrlOff entry (the LSP's CtrlMap bridge); a
// value-form CF part is yielded whole to fnCF, which emits its own
// empty-bodied control statement(s) (see emitValueCFControl — its tag and
// case lists are only legal in statement position) the same way. Both forms
// are invisible to the k-th-probe→k-th-node type-harvest alignment, unlike
// _gsxuse.
func walkLivenessAttrExprs(attrs []gsxast.Attr, fnCF func(cf *gsxast.ValueCF), fnCond func(node gsxast.Node, cond string, condPos token.Pos)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.ClassAttr:
			// Index (not range-copy) so fnCond is keyed by the SAME *ClassPart
			// pointer ast.Inspect yields — the identity the LSP looks up in CtrlMap.
			for i := range at.Parts {
				p := &at.Parts[i]
				if p.CSSSegments != nil {
					fnCond(p, p.Cond, p.CondPos)
					continue
				}
				if p.CF != nil {
					fnCF(p.CF)
					continue
				}
				if p.Cond != "" {
					fnCond(p, p.Cond, p.CondPos)
				}
			}
		case *gsxast.CondAttr:
			fnCond(at, at.Cond, at.CondPos)
			walkLivenessAttrExprs(at.Then, fnCF, fnCond)
			walkLivenessAttrExprs(at.Else, fnCF, fnCond)
		}
	}
}

// walkMarkupAttrs invokes fn with the Value (markup node list) of each
// *MarkupAttr in an element's attr list, in source order. A markup attr is a
// NAMED slot: its value renders in the PARENT scope and carries interps needing
// types, so it must be collected/probed/bound BEFORE the element's children. This
// is the SINGLE walk shared by collectExprs and emitProbes (and the binding
// walks) so the markup-value recursion order cannot drift — exactly as
// walkAttrExprs unifies the CondAttr recursion.
func walkMarkupAttrs(attrs []gsxast.Attr, fn func(value []gsxast.Markup)) {
	for _, a := range attrs {
		switch t := a.(type) {
		case *gsxast.MarkupAttr:
			fn(t.Value)
		case *gsxast.EmbeddedAttr:
			// Explicit embedded-language attribute values carry @{ } interps that
			// need types — yield their Segments so they are collected and probed in
			// the SAME order by collectExprs and emitProbes.
			fn(t.Segments)
		case *gsxast.ClassAttr:
			for i := range t.Parts {
				if t.Parts[i].CSSSegments != nil {
					fn(t.Parts[i].CSSSegments)
				}
			}
		case *gsxast.CondAttr:
			walkMarkupAttrs(t.Then, fn)
			walkMarkupAttrs(t.Else, fn)
		}
	}
}

// walkEmbeddedAttrStages invokes fn for each *EmbeddedAttr in an element's
// attr list whose Stages (whole-literal `|> f` pipeline) is non-empty, in
// canonical source order (recursing *CondAttr Then→Else, like
// walkAttrExprs). A Stages-less EmbeddedAttr is skipped here — its holes are
// already collected/probed via walkMarkupAttrs, and it needs no node-level
// type. It is the SINGLE walk shared by collectExprs (which appends each
// such node, AFTER the element's walkMarkupAttrs pass) and emitProbes
// (which emits one _gsxuse probe per node, assembling+lowering its Segments
// the SAME way via embeddedProbeSeed+probeExpr), so the k-th probe always
// maps to the k-th collected node.
func walkEmbeddedAttrStages(attrs []gsxast.Attr, fn func(*gsxast.EmbeddedAttr)) {
	for _, a := range attrs {
		switch at := a.(type) {
		case *gsxast.EmbeddedAttr:
			if len(at.Stages) > 0 {
				fn(at)
			}
		case *gsxast.CondAttr:
			walkEmbeddedAttrStages(at.Then, fn)
			walkEmbeddedAttrStages(at.Else, fn)
		}
	}
}

// collectClauseSrc visits markup in depth-first source order and feeds every Go
// control-flow clause source (for clause, if cond, switch tag, case list, GoBlock
// code) to add. These fragments are emitted verbatim, so the idents they
// reference must be in scope wherever the markup renders.
func collectClauseSrc(nodes []gsxast.Markup, add func(string)) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *gsxast.Element:
			// Recurse children for BOTH plain elements and child components: a
			// component's slot content renders in THIS parent scope, so a control-flow
			// clause inside the slot (e.g. `for ... range items`) references a parent
			// local and must be bound. A component's MARKUP-attr (named slot) values
			// also render in this parent scope, so recurse them too. (A component's
			// SIMPLE attrs are props, not slot content, so they are not visited.)
			walkMarkupAttrs(t.Attrs, func(value []gsxast.Markup) {
				collectClauseSrc(value, add)
			})
			collectClauseSrc(t.Children, add)
		case *gsxast.Fragment:
			collectClauseSrc(t.Children, add)
		case *gsxast.ForMarkup:
			add(t.Clause)
			collectClauseSrc(t.Body, add)
		case *gsxast.IfMarkup:
			add(t.Cond)
			collectClauseSrc(t.Then, add)
			collectClauseSrc(t.Else, add)
		case *gsxast.SwitchMarkup:
			add(t.Tag)
			for _, cc := range t.Cases {
				add(cc.List)
				collectClauseSrc(cc.Body, add)
			}
		case *gsxast.GoBlock:
			if t.UnsupportedMarkup != nil {
				// The package preprocessor rejects and excludes this whole block.
				// It must not contribute hidden parameter-use facts.
				continue
			}
			add(t.Code)
			// The verbatim Code fed above hides each embedded literal's @{…} holes
			// inside a raw string, so the normal Go-expression analysis cannot see an
			// ident referenced ONLY there (`{{ x := js`f(@{param})` }}`). Feed each hole's expr (and
			// its filter args) explicitly, mirroring usedParams' Interp.Embedded
			// handling, so such a param/local is still bound in the render closure.
			for _, part := range t.Embedded {
				lit, ok := part.(*gsxast.EmbeddedInterp)
				if !ok {
					continue
				}
				for _, seg := range lit.Segments {
					hole, ok := seg.(*gsxast.Interp)
					if !ok {
						continue
					}
					add(hole.Expr)
					for _, st := range hole.Stages {
						if st.Args != "" {
							add(st.Args)
						}
					}
				}
			}
		}
	}
}

// emitCondLiveness writes the empty-bodied `if <cond> {\n}` statement that
// keeps a guard condition's identifiers live in the skeleton, with a
// compensated //line and a ctrlOff entry keyed by node — the CtrlMap bridge
// the LSP uses for go-to-definition/hover inside the condition. Used for
// in-tag conditional-attribute conds (*CondAttr), class/style `: cond` guards
// (*ClassPart), and value-form if conditions (*ValueIf).
func emitCondLiveness(sb skeletonWriter, fset *token.FileSet, node gsxast.Node, cond string, condPos token.Pos, ctrlOff map[gsxast.Node]int) {
	if strings.TrimSpace(cond) == "" {
		return
	}
	emitSkeletonClauseLine(sb, fset, condPos, len("if "))
	ctrlOff[node] = sb.Len() + len("if ")
	writeSkeletonGenerated(sb, "if ")
	_ = writeSkeletonAuthoredAt(sb, fset, condPos, cond, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion)
	writeSkeletonGenerated(sb, " {\n}\n")
}

// emitValueCFControl writes the empty-bodied skeleton statement(s) that
// reference a value-form CF part's control expressions in their natural
// grammatical positions. Statement position (not `_ = (expr)`) is required —
// case lists may contain `nil`, type names under a `.(type)` tag, or untyped
// constants that only fit the tag's type, none of which are legal as a bare
// expression. Arm expressions are excluded: emitProbes harvests them with
// _gsxuse so value-form arms preserve (T, error) auto-unwrapping without
// disturbing probe order.
//
// The if-form emits each condition in the chain as its OWN `if <cond> {\n}`
// statement (not an `else if` chain — the conds are independent bool
// expressions, so type-checking is identical) so every condition carries a
// compensated //line and a ctrlOff entry keyed by its *ValueIf. The switch
// form records ctrlOff for the tag (keyed by the *ValueSwitch) and each case
// list (keyed by its *ValueSwitchCase) the same way. That is the same CtrlMap
// bridge IfMarkup uses, making go-to-definition (and positioned type errors)
// work inside value-form control expressions.
func emitValueCFControl(sb skeletonWriter, fset *token.FileSet, cf *gsxast.ValueCF, ctrlOff map[gsxast.Node]int) {
	if cf.If != nil {
		for vi := cf.If; vi != nil; vi = vi.ElseIf {
			emitCondLiveness(sb, fset, vi, vi.Cond, vi.CondPos, ctrlOff)
		}
		return
	}
	if vs := cf.Switch; vs != nil {
		if strings.TrimSpace(vs.Tag) != "" {
			emitSkeletonClauseLine(sb, fset, vs.TagPos, len("switch "))
			ctrlOff[vs] = sb.Len() + len("switch ")
		}
		writeSkeletonGenerated(sb, "switch ")
		if strings.TrimSpace(vs.Tag) != "" {
			_ = writeSkeletonAuthoredAt(sb, fset, vs.TagPos, strings.TrimSpace(vs.Tag), sourceintel.Definition|sourceintel.Hover|sourceintel.Completion)
		}
		writeSkeletonGenerated(sb, " {\n")
		for _, c := range vs.Cases {
			if c.Default {
				sb.WriteString("default:\n")
				continue
			}
			emitSkeletonClauseLine(sb, fset, c.ListPos, len("case "))
			ctrlOff[c] = sb.Len() + len("case ")
			writeSkeletonGenerated(sb, "case ")
			_ = writeSkeletonAuthoredAt(sb, fset, c.ListPos, c.List, sourceintel.Definition|sourceintel.Hover|sourceintel.Completion)
			writeSkeletonGenerated(sb, ":\n")
		}
		sb.WriteString("}\n")
	}
}

// valueFormArms returns the arm value-expression nodes of a value-form part in
// source order, for type-harvest alignment. For an if-form, the order is:
// Then, then the ElseIf chain (recursively), then Else. For a switch, each
// case's Value in case-declaration order. nil arms are skipped.
func valueFormArms(cf *gsxast.ValueCF) []*gsxast.ValueArm {
	var out []*gsxast.ValueArm
	if cf.If != nil {
		var walk func(vi *gsxast.ValueIf)
		walk = func(vi *gsxast.ValueIf) {
			if vi.Then != nil {
				out = append(out, vi.Then)
			}
			if vi.ElseIf != nil {
				walk(vi.ElseIf)
			}
			if vi.Else != nil {
				out = append(out, vi.Else)
			}
		}
		walk(cf.If)
		return out
	}
	for _, c := range cf.Switch.Cases {
		if c.Value != nil {
			out = append(out, c.Value)
		}
	}
	return out
}

// parseRecv parses a method-component receiver clause (INCLUDING parens, e.g.
// "(p UsersPage)", "(f *Form)") into its variable name, full receiver type, and
// the bare receiver type name used to resolve receiver-qualified component
// calls. It reuses go/parser on a synthesized method so it handles `*T`,
// named/unnamed, and spacing robustly.
//
// For "(p UsersPage)"  → recvVar "p", recvType "UsersPage",  recvTypeName "UsersPage".
// For "(f *Form)"      → recvVar "f", recvType "*Form",       recvTypeName "Form".
//
// An UNNAMED receiver ("(UsersPage)" / "(*Form)") is rejected: a method
// component needs the receiver var as its page-data handle (referenced in the
// body as `p.Field`). It is shared by genComponent (emit) and buildSkeleton
// (skeleton) so both agree on the signature, receiver type, and reserved
// receiver-var check.
func parseRecv(recv string) (recvVar, recvType, recvTypeName string, err error) {
	src := strings.TrimSpace(recv)
	if src == "" {
		return "", "", "", fmt.Errorf("codegen: empty method-component receiver")
	}
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, "", "package _\nfunc "+src+" _m() {}", 0)
	if perr != nil {
		return "", "", "", fmt.Errorf("codegen: parse method-component receiver %q: %w", recv, perr)
	}
	fn, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
		return "", "", "", fmt.Errorf("codegen: invalid method-component receiver %q", recv)
	}
	field := fn.Recv.List[0]
	if len(field.Names) != 1 || field.Names[0].Name == "_" {
		return "", "", "", fmt.Errorf("codegen: method component receiver must be named, e.g. (p T) — got %q", recv)
	}
	recvVar = field.Names[0].Name
	var tb strings.Builder
	if err := printer.Fprint(&tb, fset, field.Type); err != nil {
		return "", "", "", err
	}
	recvType = tb.String()
	recvTypeName = strings.TrimPrefix(recvType, "*")
	return recvVar, recvType, recvTypeName, nil
}

// checkReservedRecvVar rejects a method-component receiver var that would
// collide with the ambient closure context (`ctx`) or the generator's reserved
// `_gsx` namespace — either of which would break the emitted method body where
// the receiver var is in scope. `children` and `attrs` are special only when
// they are value parameters; receiver names remain ordinary Go bindings.
// (Generator-emitted package references are _gsx-aliased, so a receiver var can
// no longer shadow one — see rtImports.)
func checkReservedRecvVar(recvVar string) error {
	if recvVar == "ctx" {
		return fmt.Errorf("codegen: method-component receiver var %q is reserved (ambient context)", recvVar)
	}
	if strings.HasPrefix(recvVar, reservedPrefix) {
		return fmt.Errorf("codegen: method-component receiver var %q uses the reserved _gsx prefix", recvVar)
	}
	return nil
}

// typeParamSynthPrefix is the synthetic wrapper prepended to a component's raw
// type-param source so it parses as a generic func declaration. Offsets in the
// parsed result relate to the trimmed source by subtracting len(typeParamSynthPrefix).
const typeParamSynthPrefix = "package p\nfunc _["

// parseTypeParamFieldList parses a component's raw type-param source (the text
// between the signature's brackets) via the synthetic wrapper and returns the
// type-param field list plus the FileSet that resolves its positions. The
// FieldList is nil (with a nil error) for an empty or bracket-less list.
func parseTypeParamFieldList(src string) (*goast.FieldList, *token.FileSet, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, nil, nil
	}
	fset := token.NewFileSet()
	synth := typeParamSynthPrefix + src + "]() {}"
	f, err := parser.ParseFile(fset, "", synth, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("codegen: parse type params %q: %w", src, err)
	}
	fd, ok := f.Decls[0].(*goast.FuncDecl)
	if !ok {
		return nil, nil, fmt.Errorf("codegen: parse type params %q: unexpected declaration shape", src)
	}
	return fd.Type.TypeParams, fset, nil
}

func typeParamDecl(src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	return "[" + src + "]"
}

// goDeclWrapPrefix wraps a top-level Go region as a parseable file for
// splitChunk, which peels the region's leading imports off to hoist them ahead
// of all other declarations.
const goDeclWrapPrefix = "package _gsxdecl\n"

// importSpec is one parsed import hoisted from a pass-through Go chunk: an
// import path with an optional explicit name ("", a package alias, "." or "_").
type importSpec struct {
	name      string    // "" for the default import name
	path      string    // import path, unquoted
	srcOff    int       // byte offset of the spec's start within the chunk src
	nameOff   int       // byte offset of the explicit name, or -1
	pathOff   int       // byte offset of the literal's first content byte, or -1
	pathExact bool      // path is byte-identical to the authored literal content
	pos       token.Pos // resolved .gsx position of the spec (set by buildSkeleton)
	namePos   token.Pos
	pathPos   token.Pos
}

// splitChunk separates a pass-through Go chunk into its imports (to hoist ahead
// of all other declarations) and the remaining source (decls, comments) to emit
// verbatim in the body. The remainder is produced by byte-excising the import
// declarations from the chunk, so non-import content is preserved exactly.
//
// A chunk may freely mix an import with following type/func declarations (the
// common top-of-file layout); both parts are returned. If the chunk carries no
// imports, it is passed through unchanged as the body. If the chunk is invalid
// Go (e.g. an import after a func), an error is returned so the caller can
// surface a clean diagnostic instead of leaking it into a later resolution pass.
//
// bodyOff is the byte offset WITHIN src of the body's first character, so a
// caller holding the chunk's source position can emit a //line directive that
// maps the body back to its .gsx origin. Valid Go requires every import to
// precede all other declarations, so the body is the single contiguous span
// after the last import (any comments before/between imports carry no symbols
// and are dropped); a single //line anchor therefore maps the whole body.
func splitChunk(src string) (imports []importSpec, body string, bodyOff int, err error) {
	const prefix = goDeclWrapPrefix
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+src, parser.ParseComments)
	if err != nil {
		return nil, "", 0, fmt.Errorf("codegen: invalid Go in pass-through block: %w", err)
	}
	const shift = len(prefix)
	lastImportEnd := 0
	for _, d := range f.Decls {
		gd, ok := d.(*goast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		for _, s := range gd.Specs {
			is := s.(*goast.ImportSpec)
			path, err := strconv.Unquote(is.Path.Value)
			if err != nil {
				continue
			}
			var name string
			nameOff := -1
			if is.Name != nil {
				name = is.Name.Name
				nameOff = fset.Position(is.Name.Pos()).Offset - shift
			}
			pathOff := fset.Position(is.Path.Pos()).Offset - shift + 1
			pathExact := len(is.Path.Value) >= 2 && is.Path.Value[1:len(is.Path.Value)-1] == path
			imports = append(imports, importSpec{
				name:      name,
				path:      path,
				srcOff:    fset.Position(is.Pos()).Offset - shift,
				nameOff:   nameOff,
				pathOff:   pathOff,
				pathExact: pathExact,
			})
		}
		if end := fset.Position(gd.End()).Offset - shift; end > lastImportEnd {
			lastImportEnd = end
		}
	}
	tail := src[lastImportEnd:]
	left := strings.TrimLeft(tail, " \t\r\n")
	bodyOff = lastImportEnd + (len(tail) - len(left))
	body = strings.TrimRight(left, " \t\r\n")
	return imports, body, bodyOff, nil
}
