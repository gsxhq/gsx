package codegen

import (
	"crypto/sha256"
	"fmt"
	"go/token"
	"path/filepath"
	"sort"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

type componentTargetDeclarationProvenance struct {
	targetDecls  []sourceintel.VersionedSpan
	paramDecls   map[int][]sourceintel.VersionedSpan
	presentation string
	direct       *directComponentFamily
}

type componentTargetProvenanceCache map[string]map[string]componentTargetDeclarationProvenance

func componentTargetDeclarationProvenances(
	files map[string]*gsxast.File,
	sources map[string][]byte,
	fset *token.FileSet,
	plan componentTargetPlan,
) (map[string]componentTargetDeclarationProvenance, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, filepath.Clean(path))
	}
	sort.Strings(paths)
	result := make(map[string]componentTargetDeclarationProvenance)
	for _, path := range paths {
		file := files[path]
		source, ok := sources[path]
		if file == nil || !ok {
			return nil, fmt.Errorf("codegen: exact target provenance has no authoritative source for %s", path)
		}
		version := sourceintel.SourceVersion{Size: len(source), SHA256: sha256.Sum256(source)}
		for _, declaration := range file.Decls {
			component, ok := declaration.(*gsxast.Component)
			if !ok {
				continue
			}
			if _, ok := plan.emission(component); !ok {
				return nil, fmt.Errorf("codegen: exact target provenance component %s is absent from the finalized plan", component.Name)
			}
			key := plan.logicalKey(component)
			provenance := result[key]
			emission, _ := plan.emission(component)
			if emission.direct != nil {
				family := emission.direct.family
				if provenance.direct != nil && *provenance.direct != family {
					return nil, fmt.Errorf("codegen: direct component family %s has inconsistent helper metadata", key)
				}
				provenance.direct = &family
			}
			if provenance.paramDecls == nil {
				provenance.paramDecls = make(map[int][]sourceintel.VersionedSpan)
			}
			name, err := exactComponentAuthoredSpan(component.NamePos, len(component.Name), component.Name, path, source, fset, version)
			if err != nil {
				return nil, err
			}
			provenance.targetDecls = append(provenance.targetDecls, name)
			if provenance.presentation == "" {
				provenance.presentation = componentAuthoredPresentation(component)
			}
			params, err := parseComponentParamDecls(component.Params)
			if err != nil {
				return nil, err
			}
			for ordinal, parameter := range params {
				if parameter.name == "" || parameter.nameOff < 0 || !component.ParamsPos.IsValid() {
					continue
				}
				span, err := exactComponentAuthoredSpan(
					component.ParamsPos+token.Pos(parameter.nameOff), len(parameter.name), parameter.name, path, source, fset, version,
				)
				if err != nil {
					return nil, err
				}
				provenance.paramDecls[ordinal] = append(provenance.paramDecls[ordinal], span)
			}
			result[key] = provenance
		}
	}
	for key, provenance := range result {
		provenance.targetDecls = sortedUniqueVersionedSpans(provenance.targetDecls)
		for ordinal, declarations := range provenance.paramDecls {
			provenance.paramDecls[ordinal] = sortedUniqueVersionedSpans(declarations)
		}
		result[key] = provenance
	}
	return result, nil
}

func componentAuthoredPresentation(component *gsxast.Component) string {
	presentation := "component "
	if component.Recv != "" {
		presentation += component.Recv + " "
	}
	presentation += component.Name
	if component.TypeParams != "" {
		presentation += "[" + component.TypeParams + "]"
	}
	return presentation + "(" + component.Params + ")"
}

func exactComponentAuthoredSpan(
	pos token.Pos,
	length int,
	expected, path string,
	source []byte,
	fset *token.FileSet,
	version sourceintel.SourceVersion,
) (sourceintel.VersionedSpan, error) {
	if fset == nil || !pos.IsValid() || length < 0 {
		return sourceintel.VersionedSpan{}, fmt.Errorf("codegen: exact target provenance has invalid authored position for %q", expected)
	}
	position := fset.Position(pos)
	if filepath.Clean(position.Filename) != path || position.Offset < 0 || position.Offset+length > len(source) || string(source[position.Offset:position.Offset+length]) != expected {
		return sourceintel.VersionedSpan{}, fmt.Errorf("codegen: exact target provenance does not match authoritative bytes for %q in %s", expected, path)
	}
	return sourceintel.VersionedSpan{
		Span:          sourceintel.Span{Path: path, Start: position.Offset, End: position.Offset + length},
		SourceVersion: version,
	}, nil
}

func sortedUniqueVersionedSpans(spans []sourceintel.VersionedSpan) []sourceintel.VersionedSpan {
	result := append([]sourceintel.VersionedSpan(nil), spans...)
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].Span, result[j].Span
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Start != right.Start {
			return left.Start < right.Start
		}
		return left.End < right.End
	})
	write := 0
	for _, span := range result {
		if write > 0 && result[write-1] == span {
			continue
		}
		result[write] = span
		write++
	}
	return result[:write]
}

func cloneComponentTargetDeclarationProvenance(provenance componentTargetDeclarationProvenance) componentTargetDeclarationProvenance {
	cloned := componentTargetDeclarationProvenance{
		targetDecls:  append([]sourceintel.VersionedSpan(nil), provenance.targetDecls...),
		paramDecls:   make(map[int][]sourceintel.VersionedSpan, len(provenance.paramDecls)),
		presentation: provenance.presentation,
		direct:       nil,
	}
	if provenance.direct != nil {
		direct := *provenance.direct
		cloned.direct = &direct
	}
	for ordinal, declarations := range provenance.paramDecls {
		cloned.paramDecls[ordinal] = append([]sourceintel.VersionedSpan(nil), declarations...)
	}
	return cloned
}
