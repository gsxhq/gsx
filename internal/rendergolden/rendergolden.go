// Package rendergolden frames exact rendered output in newline-terminated txtar
// sections. The final LF belongs to the archive, not to the rendered HTML.
package rendergolden

import "fmt"

// Encode appends one framing LF to nonempty output. Empty output stays empty.
func Encode(html string) []byte {
	if html == "" {
		return nil
	}
	return []byte(html + "\n")
}

// Decode removes exactly one framing LF, preserving all output whitespace.
func Decode(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if data[len(data)-1] != '\n' {
		return "", fmt.Errorf("render golden is missing its framing newline")
	}
	return string(data[:len(data)-1]), nil
}
