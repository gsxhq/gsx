// Package sourceintel maps generated Go byte ranges to authored GSX byte ranges.
package sourceintel

import (
	"fmt"
	"sort"
)

type Capability uint8

const (
	Definition Capability = 1 << iota
	Hover
	Symbol
	Completion
)

type Span struct {
	Path  string
	Start int
	End   int
}

type Segment struct {
	Source         Span
	GeneratedStart int
	GeneratedEnd   int
	Capabilities   Capability
}

type DeclarationRegion struct {
	Source         Span
	GeneratedStart int
	GeneratedEnd   int
}

type SourceMap struct {
	generatedSize int
	sourceSize    int
	sourcePath    string
	segments      []Segment
	regions       []DeclarationRegion
}

func NewSourceMap(generatedSize, sourceSize int, sourcePath string, segments []Segment, regions []DeclarationRegion) (*SourceMap, error) {
	if generatedSize < 0 {
		return nil, fmt.Errorf("generated size must not be negative: %d", generatedSize)
	}
	if sourceSize < 0 {
		return nil, fmt.Errorf("source size must not be negative: %d", sourceSize)
	}

	m := &SourceMap{
		generatedSize: generatedSize,
		sourceSize:    sourceSize,
		sourcePath:    sourcePath,
		segments:      append([]Segment(nil), segments...),
		regions:       append([]DeclarationRegion(nil), regions...),
	}
	if err := m.validateSegments(); err != nil {
		return nil, err
	}
	if err := m.validateRegions(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *SourceMap) validateSegments() error {
	previousEnd := 0
	for i, segment := range m.segments {
		if err := m.validateSpan(segment.Source); err != nil {
			return fmt.Errorf("segment %d: %w", i, err)
		}
		if segment.GeneratedStart < 0 || segment.GeneratedEnd < segment.GeneratedStart || segment.GeneratedEnd > m.generatedSize {
			return fmt.Errorf("segment %d: generated range [%d, %d) is outside generated size %d", i, segment.GeneratedStart, segment.GeneratedEnd, m.generatedSize)
		}
		if segment.Source.End-segment.Source.Start != segment.GeneratedEnd-segment.GeneratedStart {
			return fmt.Errorf("segment %d: source and generated ranges have different lengths", i)
		}
		if segment.Capabilities == 0 {
			return fmt.Errorf("segment %d: capabilities must not be zero", i)
		}
		if i > 0 && segment.GeneratedStart < previousEnd {
			return fmt.Errorf("segment %d: generated range is not ordered and non-overlapping", i)
		}
		previousEnd = segment.GeneratedEnd
	}
	return nil
}

func (m *SourceMap) validateRegions() error {
	previousEnd := 0
	for i, region := range m.regions {
		if err := m.validateSpan(region.Source); err != nil {
			return fmt.Errorf("declaration region %d: %w", i, err)
		}
		if region.GeneratedStart < 0 || region.GeneratedEnd < region.GeneratedStart || region.GeneratedEnd > m.generatedSize {
			return fmt.Errorf("declaration region %d: generated range [%d, %d) is outside generated size %d", i, region.GeneratedStart, region.GeneratedEnd, m.generatedSize)
		}
		if i > 0 && region.GeneratedStart < previousEnd {
			return fmt.Errorf("declaration region %d: generated range is not ordered and non-overlapping", i)
		}
		previousEnd = region.GeneratedEnd
	}
	return nil
}

func (m *SourceMap) validateSpan(span Span) error {
	if span.Path != m.sourcePath {
		return fmt.Errorf("source path %q does not match %q", span.Path, m.sourcePath)
	}
	if span.Start < 0 || span.End < span.Start || span.End > m.sourceSize {
		return fmt.Errorf("source range [%d, %d) is outside source size %d", span.Start, span.End, m.sourceSize)
	}
	return nil
}

func (m *SourceMap) SourceSpan(generatedStart, generatedEnd int, capability Capability) (Span, bool) {
	if generatedStart < 0 || generatedEnd < generatedStart || generatedEnd > m.generatedSize || capability == 0 {
		return Span{}, false
	}

	index := sort.Search(len(m.segments), func(i int) bool {
		return m.segments[i].GeneratedStart > generatedStart
	}) - 1
	if index < 0 {
		return Span{}, false
	}
	segment := m.segments[index]
	if segment.GeneratedStart > generatedStart || segment.GeneratedEnd < generatedStart || segment.Capabilities&capability != capability {
		return Span{}, false
	}

	start := segment.Source.Start + generatedStart - segment.GeneratedStart
	if generatedEnd <= segment.GeneratedEnd {
		return Span{Path: m.sourcePath, Start: start, End: segment.Source.Start + generatedEnd - segment.GeneratedStart}, true
	}

	end := segment.Source.End
	nextGeneratedStart := segment.GeneratedEnd
	for index++; index < len(m.segments); index++ {
		segment = m.segments[index]
		if segment.GeneratedStart != nextGeneratedStart || segment.Source.Path != m.sourcePath || segment.Source.Start != end || segment.Capabilities&capability != capability {
			return Span{}, false
		}
		if generatedEnd <= segment.GeneratedEnd {
			return Span{Path: m.sourcePath, Start: start, End: segment.Source.Start + generatedEnd - segment.GeneratedStart}, true
		}
		end = segment.Source.End
		nextGeneratedStart = segment.GeneratedEnd
	}
	return Span{}, false
}

func (m *SourceMap) DeclarationSpan(generatedStart, generatedEnd int) (Span, bool) {
	if generatedStart < 0 || generatedEnd < generatedStart || generatedEnd > m.generatedSize {
		return Span{}, false
	}
	index := sort.Search(len(m.regions), func(i int) bool {
		return m.regions[i].GeneratedStart > generatedStart
	}) - 1
	if index < 0 {
		return Span{}, false
	}
	region := m.regions[index]
	if generatedEnd > region.GeneratedEnd {
		return Span{}, false
	}
	return region.Source, true
}
