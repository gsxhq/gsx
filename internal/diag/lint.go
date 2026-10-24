package diag

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// LintLevel is the configured severity of a lint: a diagnostic code whose
// default severity is Warning and that the gsx.toml [lint] table may switch
// off or promote to an error. Every other diagnostic code keeps its built-in
// severity.
type LintLevel uint8

const (
	// LintDefault leaves the lint at its built-in severity (Warning).
	LintDefault LintLevel = iota
	LintOff
	LintWarning
	LintError
)

func (l LintLevel) String() string {
	switch l {
	case LintOff:
		return "off"
	case LintWarning:
		return "warning"
	case LintError:
		return "error"
	default:
		return "default"
	}
}

// ParseLintLevel parses a [lint] value: "off", "warning" or "error".
func ParseLintLevel(s string) (LintLevel, error) {
	switch s {
	case "off":
		return LintOff, nil
	case "warning":
		return LintWarning, nil
	case "error":
		return LintError, nil
	}
	return LintDefault, fmt.Errorf("invalid level %q (want \"off\", \"warning\" or \"error\")", s)
}

// The lint codes: diagnostics reported at Warning severity by default whose
// level the [lint] table configures. Emit sites use these constants so a
// configured code always names a real diagnostic.
const (
	CodeChildElementSpacing = "child-element-spacing"
	CodeDuplicateAttribute  = "duplicate-attribute"
	CodeSelfReferenceLeaf   = "self-reference-leaf"
	CodeURLAlwaysBlocked    = "url-always-blocked"
	CodeWrapperCycle        = "wrapper-cycle"
)

var lintCodes = []string{
	CodeChildElementSpacing,
	CodeDuplicateAttribute,
	CodeSelfReferenceLeaf,
	CodeURLAlwaysBlocked,
	CodeWrapperCycle,
}

// LintCodes returns the configurable diagnostic codes, sorted.
func LintCodes() []string { return slices.Clone(lintCodes) }

// IsLint reports whether code is a configurable diagnostic code.
func IsLint(code string) bool { return slices.Contains(lintCodes, code) }

// LintPolicy maps lint codes to their configured level. A code absent from
// the map (or mapped to LintDefault) keeps its built-in Warning severity. The
// zero value is the default policy.
type LintPolicy map[string]LintLevel

// Level returns the effective level of code: LintWarning unless configured.
func (p LintPolicy) Level(code string) LintLevel {
	if l := p[code]; l != LintDefault {
		return l
	}
	return LintWarning
}

// Apply returns diags with the policy applied: a lint diagnostic configured
// "off" is dropped, one configured "error" becomes Error severity. Non-lint
// diagnostics pass through unchanged. diags is never mutated.
func (p LintPolicy) Apply(diags []Diagnostic) []Diagnostic {
	if len(p) == 0 {
		return diags
	}
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		if d.Severity == Warning && IsLint(d.Code) {
			switch p.Level(d.Code) {
			case LintOff:
				continue
			case LintError:
				d.Severity = Error
			}
		}
		out = append(out, d)
	}
	return out
}

// Fingerprint is a canonical string of the non-default entries, for cache keys
// and configuration identity: two policies that classify every diagnostic the
// same way have the same fingerprint.
func (p LintPolicy) Fingerprint() string {
	var parts []string
	for code := range p {
		if l := p.Level(code); l != LintWarning {
			parts = append(parts, code+"="+l.String())
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
