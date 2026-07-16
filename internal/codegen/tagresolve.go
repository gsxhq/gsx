package codegen

import (
	"errors"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	"go/scanner"
	"go/token"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/goexprshape"
)

type componentExclusions map[string]bool

type componentCandidateKind uint8

const (
	componentCandidateNone componentCandidateKind = iota
	componentCandidateExplicit
	componentCandidateLowercasePackage
)

func oneComponentExclusion(name string) componentExclusions {
	if name == "" {
		return nil
	}
	return componentExclusions{name: true}
}

// componentCandidateFor classifies only the syntax/declaration evidence that
// warrants exact target discovery. It never decides component identity.
func componentCandidateFor(tag string, declNames map[string]bool, exclusions componentExclusions) componentCandidateKind {
	if gsxast.IsComponentTag(tag) {
		return componentCandidateExplicit
	}
	if exclusions[tag] {
		return componentCandidateNone
	}
	if token.IsIdentifier(tag) && declNames[tag] {
		return componentCandidateLowercasePackage
	}
	return componentCandidateNone
}

// isSelfExcluded reports whether tag hits componentCandidateFor's exclusion
// specifically (as opposed to simply not being a declared name at all): tag
// equals the enclosing declaration's own name, is a plain Go identifier
// (never a component-tag shape), and IS a real package-level declaration.
// Split out so candidate collection can distinguish
// self-exclusion from an ordinary leaf and drive the warning below.
func isSelfExcluded(tag string, declNames map[string]bool, exclusions componentExclusions) bool {
	return exclusions[tag] && token.IsIdentifier(tag) && !gsxast.IsComponentTag(tag) && declNames[tag]
}

// reportSelfRefWarning emits the self-reference-leaf diagnostic for a
// self-excluded element (isSelfExcluded(el.Tag, ...) == true) whose tag is
// NOT a real HTML element (htmlnames.go): a self-named tag that isn't a
// living-standard element almost certainly meant recursion, not the
// wrapper-pattern div/span shape.
func reportSelfRefWarning(bag *diag.Bag, el *gsxast.Element, exclude string) {
	if htmlElementNames[el.Tag] {
		return
	}
	bag.Report(el.Pos(), el.End(), diag.Warning, "self-reference-leaf", "codegen",
		"<%s> inside the declaration of %q renders as a leaf element; for recursion call %s(...) in a Go hole",
		el.Tag, exclude, el.Tag)
}

// reportLeafTypeArgs emits the type-args-on-element codegen error for el: the
// parser admits `[...]` on any tag (resolution alone can tell a component tag
// from an HTML/leaf one), so a leaf element carrying type args is always a
// mistake.
func reportLeafTypeArgs(bag *diag.Bag, el *gsxast.Element) {
	bag.Errorf(el.Pos(), el.End(), "type-args-on-element",
		"type arguments on HTML element <%s>: type args are only valid on component tags", el.Tag)
}

// recordComponentCandidate records discovery membership without mutating the
// element's final semantic stamp.
func recordComponentCandidate(candidates map[*gsxast.Element]componentCandidateKind, el *gsxast.Element, declNames map[string]bool, exclusions componentExclusions, bag *diag.Bag, reportDiagnostics bool) {
	excluded := isSelfExcluded(el.Tag, declNames, exclusions)
	if candidate := componentCandidateFor(el.Tag, declNames, exclusions); candidate != componentCandidateNone {
		candidates[el] = candidate
	}
	if reportDiagnostics && excluded {
		reportSelfRefWarning(bag, el, el.Tag)
	}
}

type goWithElementsDiagnostic struct {
	pos, end     token.Pos
	code, source string
	message      string
}

func (d *goWithElementsDiagnostic) Error() string { return d.message }

type goWithElementsSourceSpan struct {
	outputStart, outputEnd int
	sourceStart, sourceEnd token.Pos
	linear                 bool
}

type goWithElementsReconstruction struct {
	source                   string
	partOffsets              map[int]int
	spans                    []goWithElementsSourceSpan
	sanitizedToReconstructed []int
	start, end               token.Pos
}

// originalRange maps one byte offset in reconstructed source back to the
// authored GSX range that produced it. The sanitizer's output is first mapped
// back to the reconstructed input; retained GoText then maps linearly after
// canonical decorative-paren removal. A marker expression stands for one
// complete non-text part and maps to that part's range. Parser EOF positions
// map to the end of the original region.
func (r goWithElementsReconstruction) originalRange(offset int) (token.Pos, token.Pos) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(r.sanitizedToReconstructed) {
		offset = len(r.sanitizedToReconstructed) - 1
	}
	offset = r.sanitizedToReconstructed[offset]
	for _, span := range r.spans {
		if offset < span.outputStart {
			return span.sourceStart, span.sourceStart
		}
		if offset >= span.outputEnd {
			continue
		}
		if !span.linear {
			return span.sourceStart, span.sourceEnd
		}
		pos := span.sourceStart + token.Pos(offset-span.outputStart)
		return pos, pos
	}
	if len(r.spans) == 0 || offset < r.spans[0].outputStart {
		return r.start, r.start
	}
	return r.end, r.end
}

func goSourceWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// whitespaceTransformOffsets returns the exact sanitized-byte to input-byte
// mapping for goexprshape.Sanitize's documented whitespace-only transform. It
// also validates that no non-whitespace byte changed, so a future broadening of
// Sanitize cannot silently corrupt diagnostic positions.
func whitespaceTransformOffsets(input, sanitized string) ([]int, error) {
	offsets := make([]int, len(sanitized)+1)
	in, out := 0, 0
	for in < len(input) && out < len(sanitized) {
		inputSpace := goSourceWhitespace(input[in])
		sanitizedSpace := goSourceWhitespace(sanitized[out])
		if inputSpace || sanitizedSpace {
			if !inputSpace || !sanitizedSpace {
				return nil, fmt.Errorf("codegen: go expression sanitizer changed non-whitespace at input byte %d and output byte %d", in, out)
			}
			inputEnd := in
			for inputEnd < len(input) && goSourceWhitespace(input[inputEnd]) {
				inputEnd++
			}
			sanitizedEnd := out
			for sanitizedEnd < len(sanitized) && goSourceWhitespace(sanitized[sanitizedEnd]) {
				sanitizedEnd++
			}
			for i := out; i < sanitizedEnd; i++ {
				delta := i - out
				if delta >= inputEnd-in {
					delta = inputEnd - in - 1
				}
				offsets[i] = in + delta
			}
			in, out = inputEnd, sanitizedEnd
			continue
		}
		if input[in] != sanitized[out] {
			return nil, fmt.Errorf("codegen: go expression sanitizer changed byte %d from %q to %q", in, input[in], sanitized[out])
		}
		offsets[out] = in
		in++
		out++
	}
	if in != len(input) || out != len(sanitized) {
		return nil, fmt.Errorf("codegen: go expression sanitizer changed source structure (%d/%d input bytes, %d/%d output bytes)", in, len(input), out, len(sanitized))
	}
	offsets[len(sanitized)] = len(input)
	return offsets, nil
}

// reconstructGoWithElements replaces every GSX value with an expression of
// the same call/non-call syntax category as its final lowering. It shares
// emission's paren-shape and decorative-paren rules, then applies
// goexprshape's canonical substituted-hole sanitizer. This is the syntax model
// for the same GSX-extended Go expression, not an approximation used only by
// target discovery.
func reconstructGoWithElements(g *gsxast.GoWithElements) (goWithElementsReconstruction, error) {
	const header = "package _gsxdecl\n"
	var b strings.Builder
	b.WriteString(header)
	r := goWithElementsReconstruction{
		partOffsets: make(map[int]int),
		start:       g.Pos(),
		end:         g.End(),
	}
	shapes := goWithElementsParenShapes(g)
	partIndices := make([]int, 0, len(g.Parts))
	holes := make([]goexprshape.Hole, 0, len(g.Parts))
	for i, part := range g.Parts {
		switch part := part.(type) {
		case gsxast.GoText:
			src := part.Src
			prefixBytes := 0
			if i > 0 && parenWrappable(g.Parts[i-1], shapes, i-1) {
				stripped := goexprshape.StripLeadingParen(src)
				prefixBytes = len(src) - len(stripped)
				src = stripped
			}
			if i < len(g.Parts)-1 && parenWrappable(g.Parts[i+1], shapes, i+1) {
				src = goexprshape.StripTrailingParen(src)
			}
			if src == "" {
				continue
			}
			start := b.Len()
			b.WriteString(src)
			r.spans = append(r.spans, goWithElementsSourceSpan{
				outputStart: start,
				outputEnd:   b.Len(),
				sourceStart: part.Pos() + token.Pos(prefixBytes),
				sourceEnd:   part.Pos() + token.Pos(prefixBytes+len(src)),
				linear:      true,
			})
		default:
			// Every GSX expression lowers to a value, not an invoked function:
			// Element/Fragment become a _gsxrt.Func conversion and f/js/css become
			// string/RawJS/RawCSS values. A unique quoted basic literal preserves
			// that shared non-call expression category while remaining invalid in
			// type grammar; an identifier would incorrectly satisfy both type and
			// expression productions.
			switch part := part.(type) {
			case *gsxast.Element, *gsxast.Fragment:
			case *gsxast.EmbeddedInterp:
				switch part.Lang {
				case gsxast.EmbeddedText, gsxast.EmbeddedJS, gsxast.EmbeddedCSS:
				default:
					return goWithElementsReconstruction{}, fmt.Errorf("codegen: invalid embedded-literal language %d in GoWithElements part %d", part.Lang, i)
				}
			default:
				return goWithElementsReconstruction{}, fmt.Errorf("codegen: unsupported GoWithElements part %T", part)
			}
			start := b.Len()
			fmt.Fprintf(&b, `"_gsxdeclvalue%d"`, i)
			partIndices = append(partIndices, i)
			holes = append(holes, goexprshape.Hole{Start: start, End: b.Len()})
			r.spans = append(r.spans, goWithElementsSourceSpan{
				outputStart: start,
				outputEnd:   b.Len(),
				sourceStart: part.Pos(),
				sourceEnd:   part.End(),
			})
		}
	}
	reconstructed := b.String()
	sanitized, sanitizedHoles := goexprshape.Sanitize(reconstructed, holes)
	offsets, err := whitespaceTransformOffsets(reconstructed, sanitized)
	if err != nil {
		return goWithElementsReconstruction{}, err
	}
	for i, hole := range sanitizedHoles {
		r.partOffsets[partIndices[i]] = hole.Start
	}
	r.source = sanitized
	r.sanitizedToReconstructed = offsets
	return r, nil
}

// goWithElementsExcludes maps each non-text part index of g to the exact
// top-level Go declaration enclosing it. Reconstruction uses the same GSX
// expression lowering rules as skeleton construction and emission, then
// matches marker offsets against the parsed declaration spans. Any parse error
// rejects the whole result: a recovery AST is partial and cannot define
// component self-exclusion safely.
func goWithElementsExcludes(g *gsxast.GoWithElements) (map[int]componentExclusions, error) {
	out := map[int]componentExclusions{}
	reconstructed, err := reconstructGoWithElements(g)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "", reconstructed.source, 0)
	if err != nil {
		var list scanner.ErrorList
		if !errors.As(err, &list) || len(list) == 0 {
			return nil, fmt.Errorf("codegen: GoWithElements parser returned an unpositioned error: %w", err)
		}
		pos, end := reconstructed.originalRange(list[0].Pos.Offset)
		return nil, &goWithElementsDiagnostic{
			pos: pos, end: end,
			code: "parse-error", source: "parser",
			message: list[0].Msg,
		}
	}
	if f == nil {
		return nil, fmt.Errorf("codegen: GoWithElements parser returned no file")
	}
	declExclusions := func(d goast.Decl, pos token.Pos) (componentExclusions, error) {
		switch dd := d.(type) {
		case *goast.FuncDecl:
			if dd.Name == nil {
				return nil, fmt.Errorf("enclosing function declaration has no name")
			}
			// Methods are included per spec: exclusion is keyed by name
			// regardless of whether the FuncDecl has a receiver.
			return oneComponentExclusion(dd.Name.Name), nil
		case *goast.GenDecl:
			// A GenDecl may group several specs. Find the exact ValueSpec and
			// exact RHS placeholder containing pos, then apply Go's structural
			// name/value relationship: one-to-one RHSs map by index; a sole
			// tuple-valued RHS belongs to every declared name. Any other arity is
			// invalid Go and has no well-defined enclosing name, so preprocessing
			// rejects the declaration before any partial classification escapes.
			for _, spec := range dd.Specs {
				if pos < spec.Pos() || spec.End() <= pos {
					continue
				}
				vs, ok := spec.(*goast.ValueSpec)
				if !ok {
					return nil, fmt.Errorf("embedded markup is not inside a var or const value")
				}
				if len(vs.Names) == 0 {
					return nil, fmt.Errorf("enclosing value declaration has no names")
				}
				valueIndex := -1
				for i, value := range vs.Values {
					if value.Pos() <= pos && pos < value.End() {
						valueIndex = i
						break
					}
				}
				switch {
				case valueIndex < 0:
					return nil, fmt.Errorf("embedded markup is outside the declaration's value expressions")
				case len(vs.Names) == len(vs.Values):
					return oneComponentExclusion(vs.Names[valueIndex].Name), nil
				case len(vs.Values) == 1:
					exclusions := make(componentExclusions, len(vs.Names))
					for _, name := range vs.Names {
						exclusions[name.Name] = true
					}
					return exclusions, nil
				default:
					return nil, fmt.Errorf("enclosing value declaration has %d names and %d values", len(vs.Names), len(vs.Values))
				}
			}
			return nil, fmt.Errorf("embedded markup is not inside an exact declaration spec")
		default:
			return nil, fmt.Errorf("unsupported enclosing declaration %T", d)
		}
	}
	tf := fset.File(f.Pos())
	if tf == nil {
		return nil, fmt.Errorf("codegen: GoWithElements parser returned no token file")
	}
	for i := range g.Parts {
		part := g.Parts[i]
		if _, text := part.(gsxast.GoText); text {
			continue
		}
		offset, exists := reconstructed.partOffsets[i]
		if !exists {
			return nil, fmt.Errorf("codegen: missing reconstructed marker offset for GoWithElements part %d", i)
		}
		pos := tf.Pos(offset)
		mapped := false
		for _, d := range f.Decls {
			if d.Pos() <= pos && pos < d.End() {
				exclusions, err := declExclusions(d, pos)
				if err != nil {
					return nil, &goWithElementsDiagnostic{
						pos: part.Pos(), end: part.End(),
						code: "invalid-go-declaration", source: "codegen",
						message: fmt.Sprintf("cannot determine component self-exclusion for part %d: %v", i, err),
					}
				}
				out[i] = exclusions
				mapped = true
				break
			}
		}
		if !mapped {
			return nil, &goWithElementsDiagnostic{
				pos: part.Pos(), end: part.End(),
				code: "invalid-go-declaration", source: "codegen",
				message: fmt.Sprintf("cannot determine component self-exclusion for part %d: no enclosing top-level declaration", i),
			}
		}
	}
	return out, nil
}
