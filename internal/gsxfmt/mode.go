package gsxfmt

import "fmt"

// ImportsMode selects how `gsx fmt` and the language server treat the import
// declarations of a .gsx file's pass-through Go chunks. It mirrors gopls, which
// offers gofmt (format only) and goimports (organize), rather than a set of
// independent knobs.
//
// The zero value is ImportsUnset so that an absent gsx.toml key is
// distinguishable from an explicit "gofmt"; callers resolve it with Or.
type ImportsMode int

const (
	// ImportsUnset means no mode was configured. Resolve it with Or before use;
	// its predicates report false so an unresolved mode never silently rewrites.
	ImportsUnset ImportsMode = iota
	// ImportsGofmt formats with gofmt only: imports are sorted within their
	// existing parenthesized group, but separate import declarations are never
	// merged, duplicates are never dropped, and no std/third-party split is made.
	ImportsGofmt
	// ImportsGoimports removes unused imports and reorders the rest: merge every
	// import declaration into one block, dedup identical specs, group standard
	// library separately from everything else, sort within each group.
	ImportsGoimports
)

// DefaultImportsMode is the mode used when gsx.toml says nothing.
const DefaultImportsMode = ImportsGoimports

// ParseImportsMode converts a gsx.toml / CLI spelling into an ImportsMode. Any
// other string is an error naming both valid spellings.
func ParseImportsMode(s string) (ImportsMode, error) {
	switch s {
	case "gofmt":
		return ImportsGofmt, nil
	case "goimports":
		return ImportsGoimports, nil
	default:
		return ImportsUnset, fmt.Errorf("invalid imports mode %q (want %q or %q)", s, "gofmt", "goimports")
	}
}

// Or returns m, or def when m is ImportsUnset. It is how a CLI flag layers over
// a config value, and a config value over the built-in default.
func (m ImportsMode) Or(def ImportsMode) ImportsMode {
	if m == ImportsUnset {
		return def
	}
	return m
}

// RemoveUnused reports whether this mode drops imports the file never uses.
// Only goimports does; gofmt never removes an import.
func (m ImportsMode) RemoveUnused() bool { return m == ImportsGoimports }

// Reorder reports whether this mode merges, dedups, groups and sorts imports.
// Only goimports does.
func (m ImportsMode) Reorder() bool { return m == ImportsGoimports }

// String returns the gsx.toml spelling for ImportsGofmt ("gofmt") and ImportsGoimports
// ("goimports"), which round-trip through ParseImportsMode. ImportsUnset and any
// out-of-range value stringify to "unset", a diagnostic spelling that ParseImportsMode
// deliberately does not accept.
func (m ImportsMode) String() string {
	switch m {
	case ImportsGofmt:
		return "gofmt"
	case ImportsGoimports:
		return "goimports"
	default:
		return "unset"
	}
}
