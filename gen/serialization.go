package gen

import "fmt"

// Serialization selects how codegen serializes element tag shapes. The
// canonical default emits spec-canonical HTML: self-closed non-void elements
// expand to an explicit open+close pair (browsers ignore the trailing slash on
// non-void elements — issue #144), void elements drop the meaningless slash
// and any authored empty close pair (<br/> and <br></br> both emit <br>).
// Verbatim reproduces authored shapes byte-for-byte.
type Serialization uint8

const (
	SerializationCanonical Serialization = iota
	SerializationVerbatim
)

// parseSerialization maps the gsx.toml / user-facing spelling to the level.
// The empty string (key absent) is the canonical default.
func parseSerialization(s string) (Serialization, error) {
	switch s {
	case "", "canonical":
		return SerializationCanonical, nil
	case "verbatim":
		return SerializationVerbatim, nil
	}
	return 0, fmt.Errorf("serialization: %q: want \"canonical\" or \"verbatim\"", s)
}
