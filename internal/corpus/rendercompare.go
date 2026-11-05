package corpus

import (
	"fmt"

	"github.com/gsxhq/gsx/internal/rendergolden"
)

// renderDiff compares literal output bytes. Structural comparison only explains
// mismatches; its whitespace normalization must never make a case pass.
func renderDiff(got string, golden []byte) (string, error) {
	want, err := rendergolden.Decode(golden)
	if err != nil {
		return "", err
	}
	structural, err := htmlStructuralDiff(got, want)
	if err != nil {
		return "", err
	}
	if got == want {
		return structural, nil // retain the duplicate-attribute check
	}
	if structural == "" {
		structural = "literal bytes differ"
	}
	return fmt.Sprintf("%s\ngot:  %q\nwant: %q", structural, got, want), nil
}
