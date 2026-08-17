package sourceintel

// IdentitySourceMap maps an authored Go file onto itself: the "generated" text
// is the authored text, so every byte range maps 1:1 with every capability.
// Hand-written .go siblings and reverse-dependency Go packages enter the index
// through this map; their token.File offsets are their authored offsets.
func IdentitySourceMap(path string, size int) (*SourceMap, error) {
	return NewSourceMap(size, size, path, []Segment{{
		Source:         Span{Path: path, Start: 0, End: size},
		GeneratedStart: 0,
		GeneratedEnd:   size,
		Capabilities:   Definition | Hover | Symbol | Completion,
	}}, nil)
}
