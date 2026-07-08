package codegen

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// parseGsxDecls parses src as a .gsx file and returns its top-level decls.
func parseGsxDecls(t *testing.T, src string) (*gsxast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return f, fset
}

// goWithElementsDecls returns every *gsxast.GoWithElements in f, failing if none.
func goWithElementsDecls(t *testing.T, f *gsxast.File) []*gsxast.GoWithElements {
	t.Helper()
	var out []*gsxast.GoWithElements
	for _, d := range f.Decls {
		if gw, ok := d.(*gsxast.GoWithElements); ok {
			out = append(out, gw)
		}
	}
	if len(out) == 0 {
		t.Fatal("no *gsxast.GoWithElements decl produced")
	}
	return out
}

// TestGoWithElementsSrcIsByteExact pins the one property the reconstruction owes
// its caller: every byte offset in the reconstructed Go is the same byte offset in
// the .gsx source. reservedDeclsInGo maps a parsed name's position back with
// `base + offset` and nothing else, so a single byte of drift silently mis-positions
// (or, if the drift breaks the parse, silently drops) every diagnostic after it.
//
// It also covers the placeholder's length floor: `_()` is 3 bytes, and the shortest
// literal that can appear in a GoWithElements part — an empty f-literal — is 3 too.
// A shorter part would make the space padding negative.
func TestGoWithElementsSrcIsByteExact(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{"shortest element", "package demo\n\nvar a = <a/>\n"},
		{"shortest fragment", "package demo\n\nvar b = <></>\n"},
		{"shortest f-literal backtick", "package demo\n\nvar c = f``\n"},
		{"shortest f-literal quoted", "package demo\n\nvar d = f\"\"\n"},
		{"multiline element", "package demo\n\nvar e = <b>\n\thi\n</b>\n"},
		{"multiline in composite literal", "package demo\n\nvar g = []any{\n\t<b>\n\t\tx\n\t</b>,\n}\n"},
		{"multiline in call argument", "package demo\n\nfunc wrap(v any) any { return v }\n\nvar h = wrap(<b>\n\tx\n</b>)\n"},
		{"element in go statement", "package demo\n\nfunc spawn() {\n\tgo <b/>\n}\n"},
		{"element in defer statement", "package demo\n\nfunc later() {\n\tdefer <b/>\n}\n"},
		{"two literals plus trailing decl", "package demo\n\nvar i = <b>\n\thi\n</b>\n\nvar j = <i>x</i>\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, fset := parseGsxDecls(t, tc.src)
			for _, gw := range goWithElementsDecls(t, f) {
				tf := fset.File(gw.Pos())
				regionStart, regionEnd := tf.Offset(gw.Pos()), tf.Offset(gw.End())
				got, _ := goWithElementsSrc(gw)
				if len(got) != regionEnd-regionStart {
					t.Fatalf("reconstruction is %d bytes, region is %d:\n%q", len(got), regionEnd-regionStart, got)
				}
				// Every non-GoText part must sit at its own offset, replaced by the
				// placeholder and nothing but spaces after it.
				for _, part := range gw.Parts {
					if _, ok := part.(gsxast.GoText); ok {
						continue
					}
					off := tf.Offset(part.Pos()) - regionStart
					n := tf.Offset(part.End()) - tf.Offset(part.Pos())
					if n < len(reservedDeclPlaceholder) {
						t.Fatalf("part at offset %d spans %d bytes, below the %d-byte placeholder floor", off, n, len(reservedDeclPlaceholder))
					}
					blanked := got[off : off+n]
					if want := reservedDeclPlaceholder + strings.Repeat(" ", n-len(reservedDeclPlaceholder)); blanked != want {
						t.Fatalf("part at offset %d reconstructed as %q, want %q", off, blanked, want)
					}
				}
				// And the whole thing must be Go the parser accepts — the reason the
				// reconstruction exists at all.
				if _, err := parser.ParseFile(token.NewFileSet(), "", goDeclWrapPrefix+got, parser.SkipObjectResolution); err != nil {
					t.Fatalf("reconstruction does not parse: %v\n%q", err, got)
				}
			}
		})
	}
}

// TestGoWithElementsSrcGoTextVerbatim pins that only the literals are rewritten:
// the Go the author wrote is spliced through byte for byte, newlines included. A
// reconstruction that touched GoText would break the enclosing statement structure
// rather than the literal's.
func TestGoWithElementsSrcGoTextVerbatim(t *testing.T) {
	t.Parallel()
	const src = "package demo\n\nvar a = <b>\n\thi\n</b>\n\nvar b = 1\n"
	f, _ := parseGsxDecls(t, src)
	gw := goWithElementsDecls(t, f)[0]
	got, _ := goWithElementsSrc(gw)
	for _, part := range gw.Parts {
		gt, ok := part.(gsxast.GoText)
		if !ok {
			continue
		}
		if !strings.Contains(got, gt.Src) {
			t.Fatalf("GoText %q missing from reconstruction %q", gt.Src, got)
		}
	}
	if !strings.Contains(got, "\n\nvar b = 1\n") {
		t.Fatalf("declaration after the literal lost its own line break: %q", got)
	}
}

// TestCheckReservedDeclsFindsNamesAcrossRegionShapes is the unit-level companion of
// the imports/reserved_prefix_decl_element_* corpus cases: a multi-line literal in
// a composite literal, in a call argument, and in `go`/`defer` position must not
// hide the names declared in the same region.
func TestCheckReservedDeclsFindsNamesAcrossRegionShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"composite literal", "package demo\n\nvar _gsxlist = []any{\n\t<b>\n\t\tx\n\t</b>,\n}\n", []string{"_gsxlist"}},
		{"call argument", "package demo\n\nfunc wrap(v any) any { return v }\n\nvar _gsxcall = wrap(<b>\n\tx\n</b>)\n", []string{"_gsxcall"}},
		{"go statement", "package demo\n\nfunc spawn() {\n\tgo <b/>\n}\n\nvar _gsxrt = 1\n", []string{"_gsxrt"}},
		{"defer statement", "package demo\n\nfunc later() {\n\tdefer <b/>\n}\n\nvar _gsxrt = 1\n", []string{"_gsxrt"}},
		{"literal then decl", "package demo\n\nvar _gsxnode = <b>\n\thi\n</b>\n\nvar _gsxafter = <i>x</i>\n", []string{"_gsxnode", "_gsxafter"}},
		{"clean file", "package demo\n\nvar list = []any{\n\t<b>\n\t\tx\n\t</b>,\n}\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, fset := parseGsxDecls(t, tc.src)
			rds, gerrs := checkReservedDecls(f)
			if len(gerrs) != 0 {
				t.Fatalf("region failed to reconstruct: %+v", gerrs)
			}
			var got []string
			for _, rd := range rds {
				got = append(got, rd.name)
				// The reported position must land on the name in the .gsx source.
				off := fset.Position(rd.pos).Offset
				if off < 0 {
					t.Fatalf("%s reported at negative offset %d", rd.name, off)
				}
				if off+len(rd.name) > len(tc.src) || tc.src[off:off+len(rd.name)] != rd.name {
					t.Fatalf("%s reported at offset %d, which reads %q", rd.name, off, tc.src[min(off, len(tc.src)):])
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("names = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCheckReservedDeclsReportsUnparseableRegion pins that a region the check
// cannot read is reported, not skipped. Silence here was the shape of the bug this
// pass keeps re-acquiring: no names found, no error, generation proceeds.
func TestCheckReservedDeclsReportsUnparseableRegion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		src     string
		wantMsg string
		// wantAt is the exact text the reported position must sit on.
		wantAt string
	}{
		{
			name:    "go chunk: import after decl",
			src:     "package demo\n\nfunc helper() string { return \"x\" }\n\nimport \"fmt\"\n",
			wantMsg: "imports must appear before other declarations",
			wantAt:  "import \"fmt\"",
		},
		{
			name:    "go-with-elements: missing if condition",
			src:     "package demo\n\nvar a = func() any { if { return <b/> } }()\n",
			wantMsg: "missing condition in if statement",
			wantAt:  "{ return",
		},
		// When the parser stops ON a substituted literal it is complaining about
		// reservedDeclPlaceholder, a token the author never typed. go/parser would
		// say "expected declaration, found _" at a byte that reads `<`. Name the
		// user's construct instead, and snap the caret to the literal's first byte.
		{
			name:    "placeholder: element in declaration position",
			src:     "package demo\n\n<b/>\n",
			wantMsg: "element is not valid in this position",
			wantAt:  "<b/>",
		},
		{
			name:    "placeholder: fragment in declaration position",
			src:     "package demo\n\n<></>\n",
			wantMsg: "fragment is not valid in this position",
			wantAt:  "<></>",
		},
		{
			name:    "placeholder: f-literal in declaration position",
			src:     "package demo\n\nf`hi`\n",
			wantMsg: "f-literal is not valid in this position",
			wantAt:  "f`hi`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, fset := parseGsxDecls(t, tc.src)
			_, gerrs := checkReservedDecls(f)
			if len(gerrs) != 1 {
				t.Fatalf("got %d region errors, want 1: %+v", len(gerrs), gerrs)
			}
			ge := gerrs[0]
			if ge.msg != tc.wantMsg {
				t.Fatalf("msg = %q, want %q", ge.msg, tc.wantMsg)
			}
			off := fset.Position(ge.pos).Offset
			if !strings.HasPrefix(tc.src[off:], tc.wantAt) {
				t.Fatalf("position %d reads %q, want it to start %q", off, tc.src[off:], tc.wantAt)
			}
		})
	}
}
