package sourceintel

import "testing"

func TestNewSourceMapRejectsInvalidSegments(t *testing.T) {
	validSegment := Segment{
		Source:         Span{Path: "view.gsx", Start: 2, End: 5},
		GeneratedStart: 10,
		GeneratedEnd:   13,
		Capabilities:   Definition,
	}
	validRegion := DeclarationRegion{
		Source:         Span{Path: "view.gsx", Start: 0, End: 5},
		GeneratedStart: 8,
		GeneratedEnd:   13,
	}

	tests := []struct {
		name     string
		segments []Segment
		regions  []DeclarationRegion
	}{
		{"negative generated endpoint", []Segment{{Source: validSegment.Source, GeneratedStart: -1, GeneratedEnd: 2, Capabilities: Definition}}, nil},
		{"generated endpoint beyond generated file", []Segment{{Source: validSegment.Source, GeneratedStart: 10, GeneratedEnd: 14, Capabilities: Definition}}, nil},
		{"negative source endpoint", []Segment{{Source: Span{Path: "view.gsx", Start: -1, End: 2}, GeneratedStart: 10, GeneratedEnd: 13, Capabilities: Definition}}, nil},
		{"source endpoint beyond source file", []Segment{{Source: Span{Path: "view.gsx", Start: 2, End: 6}, GeneratedStart: 10, GeneratedEnd: 13, Capabilities: Definition}}, nil},
		{"unequal source and generated lengths", []Segment{{Source: Span{Path: "view.gsx", Start: 2, End: 4}, GeneratedStart: 10, GeneratedEnd: 13, Capabilities: Definition}}, nil},
		{"wrong source path", []Segment{{Source: Span{Path: "other.gsx", Start: 2, End: 5}, GeneratedStart: 10, GeneratedEnd: 13, Capabilities: Definition}}, nil},
		{"unsorted segments", []Segment{validSegment, {Source: Span{Path: "view.gsx", Start: 0, End: 2}, GeneratedStart: 8, GeneratedEnd: 10, Capabilities: Definition}}, nil},
		{"overlapping generated segments", []Segment{validSegment, {Source: Span{Path: "view.gsx", Start: 5, End: 7}, GeneratedStart: 12, GeneratedEnd: 14, Capabilities: Definition}}, nil},
		{"zero capabilities", []Segment{{Source: validSegment.Source, GeneratedStart: 10, GeneratedEnd: 13}}, nil},
		{"invalid declaration region", nil, []DeclarationRegion{{Source: Span{Path: "view.gsx", Start: 0, End: 6}, GeneratedStart: 8, GeneratedEnd: 13}}},
		{"declaration region wrong path", nil, []DeclarationRegion{{Source: Span{Path: "other.gsx", Start: 0, End: 5}, GeneratedStart: 8, GeneratedEnd: 13}}},
		{"declaration region negative generated endpoint", nil, []DeclarationRegion{{Source: Span{Path: "view.gsx", Start: 0, End: 5}, GeneratedStart: -1, GeneratedEnd: 13}}},
		{"declaration region generated endpoint beyond generated file", nil, []DeclarationRegion{{Source: Span{Path: "view.gsx", Start: 0, End: 5}, GeneratedStart: 18, GeneratedEnd: 21}}},
		{"declaration region reversed source endpoint", nil, []DeclarationRegion{{Source: Span{Path: "view.gsx", Start: 5, End: 0}, GeneratedStart: 8, GeneratedEnd: 13}}},
		{"declaration region unsorted", nil, []DeclarationRegion{validRegion, {Source: Span{Path: "view.gsx", Start: 0, End: 2}, GeneratedStart: 6, GeneratedEnd: 8}}},
		{"overlapping declaration regions", nil, []DeclarationRegion{validRegion, {Source: Span{Path: "view.gsx", Start: 2, End: 7}, GeneratedStart: 12, GeneratedEnd: 17}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSourceMap(20, 5, "view.gsx", tt.segments, tt.regions); err == nil {
				t.Fatal("NewSourceMap succeeded")
			}
		})
	}
}

func TestSourceSpanRequiresExactCapabilityCoverage(t *testing.T) {
	const authored = "héab\nxx\nabc\nxyz123!"
	if got, want := len(authored), 20; got != want {
		t.Fatalf("authored byte size = %d, want %d", got, want)
	}

	m, err := NewSourceMap(32, len(authored), "view.gsx", []Segment{
		{Source: Span{Path: "view.gsx", Start: 0, End: 3}, GeneratedStart: 10, GeneratedEnd: 13, Capabilities: Definition | Hover}, // "hé"
		{Source: Span{Path: "view.gsx", Start: 3, End: 5}, GeneratedStart: 13, GeneratedEnd: 15, Capabilities: Definition | Hover},
		{Source: Span{Path: "view.gsx", Start: 8, End: 11}, GeneratedStart: 18, GeneratedEnd: 21, Capabilities: Symbol},
		{Source: Span{Path: "view.gsx", Start: 0, End: 3}, GeneratedStart: 24, GeneratedEnd: 27, Capabilities: Completion},
		{Source: Span{Path: "view.gsx", Start: 11, End: 14}, GeneratedStart: 27, GeneratedEnd: 30, Capabilities: Completion},
	}, nil)
	if err != nil {
		t.Fatalf("NewSourceMap: %v", err)
	}

	tests := []struct {
		name       string
		start, end int
		capability Capability
		want       Span
		ok         bool
	}{
		{"one UTF-8 segment", 10, 13, Hover, Span{Path: "view.gsx", Start: 0, End: 3}, true},
		{"adjacent segments merge", 10, 15, Definition, Span{Path: "view.gsx", Start: 0, End: 5}, true},
		{"half-open left edge", 10, 10, Definition, Span{Path: "view.gsx", Start: 0, End: 0}, true},
		{"half-open right edge", 15, 15, Definition, Span{Path: "view.gsx", Start: 5, End: 5}, true},
		{"mid-expression bytes", 11, 14, Hover, Span{Path: "view.gsx", Start: 1, End: 4}, true},
		{"line-edge bytes", 13, 15, Definition, Span{Path: "view.gsx", Start: 3, End: 5}, true},
		{"generated gap", 15, 18, Definition, Span{}, false},
		{"missing capability", 10, 15, Completion, Span{}, false},
		{"mixed capability mask", 10, 15, Definition | Hover, Span{Path: "view.gsx", Start: 0, End: 5}, true},
		{"repeated source span remains valid", 24, 27, Completion, Span{Path: "view.gsx", Start: 0, End: 3}, true},
		{"source discontinuity", 24, 30, Completion, Span{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := m.SourceSpan(tt.start, tt.end, tt.capability)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("SourceSpan(%d, %d, %d) = (%+v, %t), want (%+v, %t)", tt.start, tt.end, tt.capability, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestDeclarationSpanRequiresOwnedRegionEndpoints(t *testing.T) {
	m, err := NewSourceMap(30, 12, "view.gsx", nil, []DeclarationRegion{
		{Source: Span{Path: "view.gsx", Start: 0, End: 8}, GeneratedStart: 4, GeneratedEnd: 20},
		{Source: Span{Path: "view.gsx", Start: 8, End: 12}, GeneratedStart: 22, GeneratedEnd: 26},
	})
	if err != nil {
		t.Fatalf("NewSourceMap: %v", err)
	}

	tests := []struct {
		name       string
		start, end int
		want       Span
		ok         bool
	}{
		{"owned region includes generated glue", 4, 20, Span{Path: "view.gsx", Start: 0, End: 8}, true},
		{"owned region prefix", 4, 12, Span{Path: "view.gsx", Start: 0, End: 8}, true},
		{"owned region suffix", 12, 20, Span{Path: "view.gsx", Start: 0, End: 8}, true},
		{"crossing GoWithElements IIFE has no owner", 18, 22, Span{}, false},
		{"crossing declaration regions has no owner", 4, 26, Span{}, false},
		{"outside any region", 0, 4, Span{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := m.DeclarationSpan(tt.start, tt.end)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("DeclarationSpan(%d, %d) = (%+v, %t), want (%+v, %t)", tt.start, tt.end, got, ok, tt.want, tt.ok)
			}
		})
	}
}
