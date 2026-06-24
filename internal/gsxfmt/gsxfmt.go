// Package gsxfmt is the single source-formatting engine shared by the `gsx fmt`
// CLI and the language server's textDocument/formatting: parse → whitespace-
// normalize → print, producing the canonical, idempotent form of a .gsx file.
package gsxfmt

import (
	"bytes"
	"go/token"

	"github.com/gsxhq/gsx/internal/printer"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// Format parses src (named for diagnostics), normalizes whitespace, and returns
// the canonical gsx source. A non-nil error is a parse or print failure; callers
// formatting unsaved buffers should treat that as "leave the buffer untouched"
// rather than a hard failure.
func Format(name string, src []byte) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if err := printer.Fprint(&b, f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
