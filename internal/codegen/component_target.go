package codegen

import (
	"errors"
	"fmt"
	"go/token"
	"maps"
	"sort"
	"sync/atomic"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/attrclass"
	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/jsx"
)

// parsedGSXPackage owns one freshly parsed package AST through its codegen
// lifecycle. Its constructor copies the file map; production supplies only
// nodes created by that parse and never shares them with another owner. The AST
// is mutable during component-call preprocessing, so the transition is
// package-wide and one-shot rather than state stored on public ast.File nodes.
type parsedGSXPackage struct {
	name  string
	files map[string]*gsxast.File

	preprocessingClaimed atomic.Bool
}

func newParsedGSXPackage(name string, files map[string]*gsxast.File) *parsedGSXPackage {
	owned := make(map[string]*gsxast.File, len(files))
	maps.Copy(owned, files)
	return &parsedGSXPackage{name: name, files: owned}
}

func (p *parsedGSXPackage) preprocessComponentCallSites(declNames map[string]bool, fset *token.FileSet, classifier *attrclass.Classifier, bag *diag.Bag) (callSitePreprocessResult, error) {
	if p == nil {
		return callSitePreprocessResult{}, fmt.Errorf("codegen: nil parsed GSX package")
	}
	if !p.preprocessingClaimed.CompareAndSwap(false, true) {
		return callSitePreprocessResult{}, fmt.Errorf("codegen: component-call preprocessing already claimed package %q", p.name)
	}
	return preprocessClaimedComponentCallSites(p.files, declNames, fset, classifier, bag)
}

type callSiteID uint32

const invalidCallSiteID callSiteID = 0

type callSiteDisposition uint8

const (
	callSitePlanned callSiteDisposition = iota
	callSitePreserveUnsupportedGoBlock
)

type callSiteRecord struct {
	id          callSiteID
	path        string
	element     *gsxast.Element
	disposition callSiteDisposition
}

type callSiteRegistry struct {
	byElement map[*gsxast.Element]callSiteID
	records   []callSiteRecord
}

type callSitePreprocessResult struct {
	registry  *callSiteRegistry
	syntaxOK  bool
	scriptsOK bool
}

func (r callSitePreprocessResult) analysisReady() bool {
	return r.syntaxOK && r.scriptsOK
}

// preprocessClaimedComponentCallSites is the mutation body behind
// parsedGSXPackage.preprocessComponentCallSites, the only package-analysis
// transition allowed to
// materialize markup embedded in Go expressions. It completes that mutation
// for every file first, validates exact GoWithElements exclusion mappings,
// resolves JavaScript context on the expanded tree, stamps component tags, and
// only then allocates stable one-based call-site IDs in path and authored source
// order.
func preprocessClaimedComponentCallSites(files map[string]*gsxast.File, declNames map[string]bool, fset *token.FileSet, classifier *attrclass.Classifier, bag *diag.Bag) (callSitePreprocessResult, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	seenFiles := make(map[*gsxast.File]string, len(paths))
	for _, path := range paths {
		file := files[path]
		if file == nil {
			return callSitePreprocessResult{}, fmt.Errorf("codegen: nil gsx AST for %s", path)
		}
		if prior, exists := seenFiles[file]; exists {
			return callSitePreprocessResult{}, fmt.Errorf("codegen: the same gsx AST is registered as both %s and %s", prior, path)
		}
		seenFiles[file] = path
	}

	syntaxOK := true
	for _, path := range paths {
		if !materializeEmbeddedMarkup(files[path], classifier, fset, bag) {
			syntaxOK = false
		}
	}
	if !syntaxOK {
		return callSitePreprocessResult{syntaxOK: false, scriptsOK: true}, nil
	}
	goExclusions, syntaxOK, err := packageGoWithElementsExclusions(paths, files, bag)
	if err != nil {
		return callSitePreprocessResult{}, err
	}
	if !syntaxOK {
		return callSitePreprocessResult{syntaxOK: false, scriptsOK: true}, nil
	}
	scriptsOK := true
	for _, path := range paths {
		if !jsx.ResolveScripts(files[path], bag) {
			scriptsOK = false
		}
	}
	if !scriptsOK {
		return callSitePreprocessResult{syntaxOK: true, scriptsOK: false}, nil
	}
	for _, path := range paths {
		if err := stampMaterializedComponentTags(files[path], declNames, goExclusions, bag); err != nil {
			return callSitePreprocessResult{}, err
		}
	}

	registry := &callSiteRegistry{byElement: make(map[*gsxast.Element]callSiteID)}
	for _, path := range paths {
		if err := registry.collectFile(path, files[path], bag); err != nil {
			return callSitePreprocessResult{}, err
		}
	}
	return callSitePreprocessResult{registry: registry, syntaxOK: syntaxOK, scriptsOK: scriptsOK}, nil
}

// packageGoWithElementsExclusions computes every top-level self-exclusion fact
// before JavaScript analysis or component stamping begins. This is a
// package-wide syntax gate: a recovered Go parser AST is never allowed to feed
// semantic analysis for only part of the package.
func packageGoWithElementsExclusions(paths []string, files map[string]*gsxast.File, bag *diag.Bag) (map[*gsxast.GoWithElements]map[int]componentExclusions, bool, error) {
	out := make(map[*gsxast.GoWithElements]map[int]componentExclusions)
	syntaxOK := true
	for _, path := range paths {
		for _, decl := range files[path].Decls {
			withElements, ok := decl.(*gsxast.GoWithElements)
			if !ok {
				continue
			}
			exclusions, err := goWithElementsExcludes(withElements)
			if err != nil {
				var sourceDiagnostic *goWithElementsDiagnostic
				if !errors.As(err, &sourceDiagnostic) {
					return nil, false, err
				}
				syntaxOK = false
				bag.Report(sourceDiagnostic.pos, sourceDiagnostic.end, diag.Error, sourceDiagnostic.code, sourceDiagnostic.source, "%s", sourceDiagnostic.message)
				continue
			}
			out[withElements] = exclusions
		}
	}
	return out, syntaxOK, nil
}

// stampMaterializedComponentTags walks every markup-bearing field, including
// Interp.Embedded and GoBlock.Embedded, which gsxast.Inspect deliberately does
// not traverse. All elements therefore share one classification rule whether
// they came from the original parse or expression preprocessing.
func stampMaterializedComponentTags(file *gsxast.File, declNames map[string]bool, goExclusions map[*gsxast.GoWithElements]map[int]componentExclusions, bag *diag.Bag) error {
	var walk func([]gsxast.Markup, componentExclusions, bool)
	var walkParts func([]gsxast.GoPart, componentExclusions, bool)
	walkParts = func(parts []gsxast.GoPart, exclusions componentExclusions, reportDiagnostics bool) {
		for _, part := range parts {
			if markup, ok := part.(gsxast.Markup); ok {
				walk([]gsxast.Markup{markup}, exclusions, reportDiagnostics)
			}
		}
	}
	walk = func(nodes []gsxast.Markup, exclusions componentExclusions, reportDiagnostics bool) {
		for _, node := range nodes {
			switch node := node.(type) {
			case *gsxast.Element:
				stampComponentTag(node, declNames, exclusions, bag, reportDiagnostics)
				walkMarkupAttrs(node.Attrs, func(value []gsxast.Markup) { walk(value, exclusions, reportDiagnostics) })
				walk(node.Children, exclusions, reportDiagnostics)
			case *gsxast.Fragment:
				walk(node.Children, exclusions, reportDiagnostics)
			case *gsxast.Interp:
				walkParts(node.Embedded, exclusions, reportDiagnostics)
			case *gsxast.EmbeddedInterp:
				walk(node.Segments, exclusions, reportDiagnostics)
			case *gsxast.ForMarkup:
				walk(node.Body, exclusions, reportDiagnostics)
			case *gsxast.IfMarkup:
				walk(node.Then, exclusions, reportDiagnostics)
				walk(node.Else, exclusions, reportDiagnostics)
			case *gsxast.SwitchMarkup:
				for _, clause := range node.Cases {
					walk(clause.Body, exclusions, reportDiagnostics)
				}
			case *gsxast.GoBlock:
				// Direct element/fragment parts make the entire block an
				// unsupported preserve region. Still stamp every element so the
				// AST is total, but suppress secondary validation diagnostics; the
				// registry collector owns the block's one rejection.
				blockDiagnostics := reportDiagnostics && node.UnsupportedMarkup == nil
				walkParts(node.Embedded, exclusions, blockDiagnostics)
			}
		}
	}

	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *gsxast.Component:
			walk(decl.Body, oneComponentExclusion(decl.Name), true)
		case *gsxast.GoWithElements:
			excludes, ok := goExclusions[decl]
			if !ok {
				return fmt.Errorf("codegen: missing GoWithElements exclusion facts for declaration at %s", file.Package)
			}
			for i, part := range decl.Parts {
				if markup, ok := part.(gsxast.Markup); ok {
					walk([]gsxast.Markup{markup}, excludes[i], true)
				}
			}
		}
	}
	return nil
}

func (r *callSiteRegistry) add(path string, element *gsxast.Element, disposition callSiteDisposition) error {
	if prior, exists := r.byElement[element]; exists {
		return fmt.Errorf("codegen: element <%s> in %s was visited twice while assigning call-site IDs (first ID %d)", element.Tag, path, prior)
	}
	id := callSiteID(len(r.records) + 1)
	if id == invalidCallSiteID {
		return fmt.Errorf("codegen: call-site ID overflow")
	}
	r.byElement[element] = id
	r.records = append(r.records, callSiteRecord{id: id, path: path, element: element, disposition: disposition})
	return nil
}

func (r *callSiteRegistry) collectFile(path string, file *gsxast.File, bag *diag.Bag) error {
	var walk func([]gsxast.Markup) error
	var walkParts func([]gsxast.GoPart) error
	walkParts = func(parts []gsxast.GoPart) error {
		for _, part := range parts {
			if markup, ok := part.(gsxast.Markup); ok {
				if err := walk([]gsxast.Markup{markup}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	walk = func(nodes []gsxast.Markup) error {
		for _, node := range nodes {
			switch node := node.(type) {
			case *gsxast.Element:
				if node.IsComponent {
					if err := r.add(path, node, callSitePlanned); err != nil {
						return err
					}
				}
				var attrErr error
				walkMarkupAttrs(node.Attrs, func(value []gsxast.Markup) {
					if attrErr == nil {
						attrErr = walk(value)
					}
				})
				if attrErr != nil {
					return attrErr
				}
				if err := walk(node.Children); err != nil {
					return err
				}
			case *gsxast.Fragment:
				if err := walk(node.Children); err != nil {
					return err
				}
			case *gsxast.Interp:
				if err := walkParts(node.Embedded); err != nil {
					return err
				}
			case *gsxast.EmbeddedInterp:
				if err := walk(node.Segments); err != nil {
					return err
				}
			case *gsxast.ForMarkup:
				if err := walk(node.Body); err != nil {
					return err
				}
			case *gsxast.IfMarkup:
				if err := walk(node.Then); err != nil {
					return err
				}
				if err := walk(node.Else); err != nil {
					return err
				}
			case *gsxast.SwitchMarkup:
				for _, clause := range node.Cases {
					if err := walk(clause.Body); err != nil {
						return err
					}
				}
			case *gsxast.GoBlock:
				first := node.UnsupportedMarkup
				if first != nil {
					bag.Errorf(first.Pos(), first.End(), "unsupported-node", "element literals inside {{ }} blocks are not supported yet")
					for _, part := range node.Embedded {
						if element, ok := part.(*gsxast.Element); ok {
							if err := r.add(path, element, callSitePreserveUnsupportedGoBlock); err != nil {
								return err
							}
						}
					}
					continue
				}
				if err := walkParts(node.Embedded); err != nil {
					return err
				}
			}
		}
		return nil
	}

	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *gsxast.Component:
			if err := walk(decl.Body); err != nil {
				return err
			}
		case *gsxast.GoWithElements:
			if err := walkParts(decl.Parts); err != nil {
				return err
			}
		}
	}
	return nil
}
