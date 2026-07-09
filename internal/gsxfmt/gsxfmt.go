// Package gsxfmt is the single source-formatting engine shared by the `gsx fmt`
// CLI and the language server's textDocument/formatting: parse → whitespace-
// normalize → print, producing the canonical, idempotent form of a .gsx file.
package gsxfmt

import (
	"bytes"
	"go/token"

	"github.com/gsxhq/gsx/internal/printer"
	"github.com/gsxhq/gsx/internal/rawfmt"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// Format parses src (named for diagnostics), normalizes whitespace, and returns
// the canonical gsx source. A non-nil error is a parse or print failure; callers
// formatting unsaved buffers should treat that as "leave the buffer untouched"
// rather than a hard failure. Imports get gofmt mode: no import is removed,
// merged, deduped or regrouped, though gofmt still sorts within an existing
// parenthesized group when the printer runs go/format over each Go chunk.
func Format(name string, src []byte, width int) ([]byte, error) {
	return FormatWith(name, src, FormatOptions{Width: width})
}

// FormatRemovingImports formats src exactly like Format, but first removes every
// import listed in `unused` from the file's pass-through Go chunks. With an empty
// or nil `unused` it is identical to Format. A parse error is returned unchanged
// (the caller decides whether to surface or ignore it). It never reorders.
func FormatRemovingImports(name string, src []byte, unused []ImportRef, width int) ([]byte, error) {
	return FormatWith(name, src, FormatOptions{Unused: unused, Width: width})
}

// FormatRemovingImportsWith is FormatRemovingImports with explicit CSS and JS
// formatters for <style>/<script> bodies (nil → built-in default at width). It
// never reorders.
func FormatRemovingImportsWith(name string, src []byte, unused []ImportRef, width int, cssFmt, jsFmt rawfmt.Formatter) ([]byte, error) {
	return FormatWith(name, src, FormatOptions{Unused: unused, Width: width, CSSFmt: cssFmt, JSFmt: jsFmt})
}

// FormatOptions carries the knobs of FormatWith. The zero value is the safe one:
// no imports removed, no reorder, printer defaults for <style>/<script>.
type FormatOptions struct {
	// Unused lists imports to delete from the file's Go chunks; nil removes none.
	Unused []ImportRef
	// Width is the printer's target line width (0 → printer default).
	Width int
	// TabWidth is how many columns one tab occupies when measuring a line
	// (0 → pretty.DefaultTabWidth). It does not change what is emitted —
	// indentation is always tabs — only where lines are judged too long.
	TabWidth int
	// CSSFmt/JSFmt format <style>/<script> bodies; nil uses the printer default.
	CSSFmt rawfmt.Formatter
	JSFmt  rawfmt.Formatter
	// Reorder runs the goimports pass (merge/dedup/group/sort). It is a plain
	// bool, not an ImportsMode: gsxfmt stays mechanical, and callers map
	// ImportsMode.Reorder() onto it.
	Reorder bool
}

// FormatWith is the one formatting entry point: parse → remove unused imports →
// (optionally) reorder imports → whitespace-normalize → print. A non-nil error is
// a parse or print failure; callers formatting unsaved buffers should treat that
// as "leave the buffer untouched".
func FormatWith(name string, src []byte, opts FormatOptions) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return nil, err
	}
	// Remove first, then reorder: an import that was both unused and duplicated is
	// gone before the merge, so reorder only canonicalizes what survives.
	removeImports(f, opts.Unused)
	if opts.Reorder {
		reorderImports(f)
	}
	wsnorm.Normalize(f)
	var b bytes.Buffer
	if opts.CSSFmt == nil && opts.JSFmt == nil {
		if err := printer.Fprint(&b, f, opts.Width, opts.TabWidth); err != nil {
			return nil, err
		}
	} else {
		if err := printer.FprintWith(&b, f, opts.Width, opts.TabWidth, opts.CSSFmt, opts.JSFmt); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
