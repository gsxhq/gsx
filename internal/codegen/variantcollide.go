package codegen

import (
	"bytes"
	"errors"
	"fmt"
	goast "go/ast"
	"go/build"
	"go/build/constraint"
	"go/token"
	"go/types"
	"io"
	"path/filepath"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
)

type componentVariantMember struct {
	path      string
	component *gsxast.Component
}

type componentVariantFamily struct {
	key     string
	members []componentVariantMember
}

// syntacticComponentTargetPlan is the importer-free lane's deliberately
// non-semantic emission plan. It makes every parsed component public and never
// recognizes, validates, or folds variant families. Its only consumer builds
// per-file parse/probe skeletons for syntactic editor features; the finalized
// semantic plan remains the sole authority for package acceptance and codegen.
func syntacticComponentTargetPlan(files map[string]*gsxast.File) componentTargetPlan {
	plan := componentTargetPlan{
		emissions:   map[*gsxast.Component]componentTargetEmission{},
		logicalKeys: map[*gsxast.Component]string{},
	}
	for _, file := range files {
		for _, declaration := range file.Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok {
				continue
			}
			plan.emissions[component] = componentTargetEmission{public: true}
			plan.logicalKeys[component] = componentKey(component)
		}
	}
	return plan
}

func reportInvalidComponentVariantFamily(key string, members []componentVariantMember, files map[string]*gsxast.File, sources map[string][]byte, bag *diag.Bag) {
	if bag == nil {
		return
	}
	filenames := make([]string, 0, len(members))
	for _, member := range members {
		filenames = append(filenames, filepath.Base(member.path))
	}
	name := strings.TrimPrefix(key, ".")
	for _, member := range members {
		constrained, err := componentFileHasEffectiveConstraint(member.path, files[member.path], sources[member.path])
		detail := "every member must be in a distinct file with a valid Go build constraint"
		if err != nil {
			detail = err.Error()
		} else if !constrained {
			detail = filepath.Base(member.path) + " has no effective Go build constraint"
		}
		bag.Errorf(member.component.NamePos, member.component.NamePos+token.Pos(len(member.component.Name)), "duplicate-component",
			"component %s cannot form a build variant family across %s: %s", name, strings.Join(filenames, ", "), detail)
	}
}

func componentFileHasEffectiveConstraint(path string, file *gsxast.File, source []byte) (bool, error) {
	if file == nil {
		return false, fmt.Errorf("missing parsed source for %s", path)
	}
	if len(source) == 0 {
		source = []byte(file.Doc + "\n\npackage " + file.Package + "\n")
	}
	sourceConstrained, err := sourceHasEffectiveBuildConstraint(source)
	if err != nil {
		return false, fmt.Errorf("invalid build constraint in %s: %w", filepath.Base(path), err)
	}
	if sourceConstrained {
		return true, nil
	}
	return generatedFilenameHasBuildConstraint(path)
}

var errMultipleGoBuildConstraints = errors.New("multiple //go:build comments")

// sourceHasEffectiveBuildConstraint follows go/build's leading-header rules.
// The parser's File.Doc is the exact byte prefix before package, so appending a
// package clause reconstructs the boundary on which the Go command operates.
func sourceHasEffectiveBuildConstraint(source []byte) (bool, error) {
	trimmed, goBuild, err := parseBuildConstraintHeader(source)
	if err != nil {
		return false, err
	}
	if goBuild != nil {
		_, err := constraint.Parse(string(goBuild))
		return err == nil, err
	}
	for len(trimmed) > 0 {
		line := trimmed
		if index := bytes.IndexByte(line, '\n'); index >= 0 {
			line, trimmed = line[:index], trimmed[index+1:]
		} else {
			trimmed = nil
		}
		text := string(bytes.TrimSpace(line))
		if !constraint.IsPlusBuild(text) {
			continue
		}
		if _, err := constraint.Parse(text); err == nil {
			return true, nil
		}
	}
	return false, nil
}

// parseBuildConstraintHeader is a focused port of go/build.parseFileHeader.
// It deliberately retains the standard library's blank-line and comment-block
// rules instead of approximating directive placement.
func parseBuildConstraintHeader(content []byte) (trimmed, goBuild []byte, err error) {
	end := 0
	pending := content
	ended := false
	inSlashStar := false

lines:
	for len(pending) > 0 {
		line := pending
		if index := bytes.IndexByte(line, '\n'); index >= 0 {
			line, pending = line[:index], pending[index+1:]
		} else {
			pending = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 && !ended {
			end = len(content) - len(pending)
			continue lines
		}
		if !bytes.HasPrefix(line, []byte("//")) {
			ended = true
		}
		if !inSlashStar && constraint.IsGoBuild(string(line)) {
			if goBuild != nil {
				return nil, nil, errMultipleGoBuildConstraints
			}
			goBuild = append([]byte(nil), line...)
		}

	comments:
		for len(line) > 0 {
			if inSlashStar {
				if index := bytes.Index(line, []byte("*/")); index >= 0 {
					inSlashStar = false
					line = bytes.TrimSpace(line[index+2:])
					continue comments
				}
				continue lines
			}
			if bytes.HasPrefix(line, []byte("//")) {
				continue lines
			}
			if bytes.HasPrefix(line, []byte("/*")) {
				inSlashStar = true
				line = bytes.TrimSpace(line[2:])
				continue comments
			}
			break lines
		}
	}
	return content[:end], goBuild, nil
}

func generatedFilenameHasBuildConstraint(path string) (bool, error) {
	name := strings.TrimSuffix(filepath.Base(path), ".gsx") + ".x.go"
	stem, _, _ := strings.Cut(name, ".")
	_, suffix, found := strings.Cut(stem, "_")
	if !found {
		return false, nil
	}
	parts := strings.Split(suffix, "_")
	if len(parts) > 0 && parts[len(parts)-1] == "test" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return false, nil
	}
	const invalid = "gsx_invalid_platform"
	neutral, err := generatedFilenameMatches(name, invalid, invalid)
	if err != nil || neutral {
		return false, err
	}
	last := parts[len(parts)-1]
	if matches, err := generatedFilenameMatches(name, last, invalid); err != nil || matches {
		return matches, err
	}
	if matches, err := generatedFilenameMatches(name, invalid, last); err != nil || matches {
		return matches, err
	}
	if len(parts) >= 2 {
		return generatedFilenameMatches(name, parts[len(parts)-2], last)
	}
	return false, nil
}

func generatedFilenameMatches(name, goos, goarch string) (bool, error) {
	context := build.Default
	context.GOOS = goos
	context.GOARCH = goarch
	context.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("package p\n")), nil
	}
	return context.MatchFile(".", name)
}

type variantParamIdentity struct {
	name string
	role declarationParamRole
}

func componentVariantParamIdentity(component *gsxast.Component) ([]variantParamIdentity, error) {
	declaration, err := componentDeclarationFor(component)
	if err != nil {
		return nil, err
	}
	identity := make([]variantParamIdentity, 0, len(declaration.params))
	for _, parameter := range declaration.params {
		identity = append(identity, variantParamIdentity{name: parameter.name, role: parameter.role})
	}
	return identity, nil
}

func variantFuncObjects(files []*goast.File, info *types.Info, plan componentTargetPlan) map[*gsxast.Component]*types.Func {
	byName := make(map[string]*gsxast.Component)
	for component, emission := range plan.emissions {
		if emission.splitBody && emission.bodyName != "" {
			byName[emission.bodyName] = component
		}
	}
	objects := make(map[*gsxast.Component]*types.Func, len(byName))
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*goast.FuncDecl)
			if !ok {
				continue
			}
			component := byName[function.Name.Name]
			if component == nil {
				continue
			}
			if object, ok := info.Defs[function.Name].(*types.Func); ok {
				objects[component] = object
			}
		}
	}
	return objects
}

func componentVariantFamilySignaturesMatch(
	members []componentVariantMember,
	objects map[*gsxast.Component]*types.Func,
	signatureErrors map[*gsxast.Component]bool,
) bool {
	var firstSignature *types.Signature
	var firstParams []variantParamIdentity
	for index, member := range members {
		if signatureErrors[member.component] {
			return false
		}
		object := objects[member.component]
		if object == nil {
			return false
		}
		signature, ok := object.Type().(*types.Signature)
		if !ok || !componentVariantSignatureUsable(signature) {
			return false
		}
		params, err := componentVariantParamIdentity(member.component)
		if err != nil {
			return false
		}
		if index == 0 {
			firstSignature = signature
			firstParams = params
			continue
		}
		if !equalVariantParamIdentity(firstParams, params) || !identicalComponentVariantSignatures(firstSignature, signature) {
			return false
		}
	}
	return len(members) > 0
}

func reportComponentVariantSignatureMismatch(members []componentVariantMember, bag *diag.Bag) {
	if bag == nil {
		return
	}
	filenames := make([]string, 0, len(members))
	for _, member := range members {
		filenames = append(filenames, filepath.Base(member.path))
	}
	for _, member := range members {
		bag.Errorf(member.component.NamePos, member.component.NamePos+token.Pos(len(member.component.Name)), "duplicate-component",
			"component %s has different or unresolved semantic signatures across build variants (%s); parameter names and roles, function types, constraints, and receiver types must be valid and match",
			member.component.Name, strings.Join(filenames, ", "))
	}
}

func equalVariantParamIdentity(left, right []variantParamIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
