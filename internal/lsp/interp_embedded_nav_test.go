package lsp

import (
	"go/token"
	"go/types"
	"slices"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

// interpEmbeddedSrc exercises the interpolation-embedded nav positions the
// interp-embedded-literals branch created: a <tag>/<> literal and a prefixed
// backtick literal used in operand position INSIDE a body `{ }` interpolation,
// where they ride in Interp.Embedded rather than the direct body-child path.
// The last two lines put a literal in a control-flow header and a
// conditional-attribute condition (their CondEmbedded overlays).
const interpEmbeddedSrc = `package page

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }

func emphasize(s string) string { return "*" + s + "*" }

func ok(s string) bool { return s != "" }

func wrapS(s string) string { return s }

func wrapN(n gsx.Node) string { return "n" }

type U struct {
	Name string
	Age  int
}

component Badge(count int, name string) {
	<b>{name}: {count}</b>
}

component Uses(n int, label string, u U) {
	<div>{ wrap(<Badge count={n} name={label}/>) }</div>
	<p>{ emphasize(f` + "`hi @{label}`" + `) }</p>
	{ if emphasize(f` + "`x-@{n}`" + `) != label {
		<i>h</i>
	} }
	<i { if emphasize(f` + "`y-@{n}`" + `) != label { title="t" } }></i>
	<p>{ emphasize(f` + "`w-@{n}`" + `) + label }</p>
	<a title={wrapS(f` + "`/z/@{n}`" + `) + label}></a>
	<a title={wrapS(f` + "`p-@{n}`" + `) + label |> truncate(n)}></a>
	<a class={ "c", wrapS(f` + "`k-@{n}`" + `) + label }></a>
	<a class={ "c", if ok(f` + "`e-@{n}`" + `) && ok(label) { "a" } }></a>
	{ if ok(f` + "`q-@{n}`" + `) && ok(label) {
		<i>q</i>
	} }
	<a h={wrapN(<b title={label}/>) + label}></a>
	{{ hv := wrapN(<b title={label}/>) + label }}
	<p>{hv}</p>
	<a title={wrapS(f` + "`m1-@{n}`" + `) + u.Name}></a>
	<p>{ wrapS(f` + "`m2-@{n}`" + `) + u.Name }</p>
	{ if ok(f` + "`m3-@{n}`" + `) && ok(u.Name) {
		<i>m</i>
	} }
	{{ mv := wrapS(f` + "`m4-@{n}`" + `) + u.Name }}
	<p>{mv}</p>
}
`

// TestInterpEmbeddedNav runs definition, hover and completion over one
// analysis of interpEmbeddedSrc (one codegen Module open for all three).
func TestInterpEmbeddedNav(t *testing.T) {
	src := interpEmbeddedSrc
	pkg, path := analyzedLSPPackage(t, src)
	t.Run("definition", func(t *testing.T) { testInterpEmbeddedDefinition(t, src, pkg, path) })
	t.Run("hover", func(t *testing.T) { testInterpEmbeddedHover(t, src, pkg, path) })
	t.Run("completion", func(t *testing.T) { testInterpEmbeddedCompletion(t, src, pkg, path) })
}

// nestedNavRow is one cursor inside (or after) a nested construct in a
// Go-expression field, with the declaration it must navigate to and the
// hover text it must show.
type nestedNavRow struct {
	name      string
	off       int    // cursor byte offset
	declStart int    // declaration byte offset
	declLen   int    // declaration name length
	hover     string // substring the hover must contain
}

// nestedNavRows are cursors on a plain-Go identifier AFTER a nested
// construct, on an identifier INSIDE a nested hole or element attribute, and
// on a callee before the construct, one family per row group: body
// interpolation, attribute value, class part, value-form header, cond-attr
// header, control-flow header, {{ }} block element.
func nestedNavRows(src string) []nestedNavRow {
	after := func(anchor, prefix string) int {
		i := strings.Index(src, anchor)
		if i < 0 {
			panic("anchor not found: " + anchor)
		}
		return i + len(prefix)
	}
	paramN := strings.Index(src, "n int, label string")
	paramLabel := strings.Index(src, "label string")
	fn := func(name string) int { return strings.Index(src, "func "+name+"(") + len("func ") }
	label := func(name string, off int) nestedNavRow {
		return nestedNavRow{name, off, paramLabel, len("label"), "var label string"}
	}
	n := func(name string, off int) nestedNavRow {
		return nestedNavRow{name, off, paramN, len("n"), "var n int"}
	}
	callee := func(name string, off int, f, sig string) nestedNavRow {
		return nestedNavRow{name, off, fn(f), len(f), sig}
	}
	return []nestedNavRow{
		label("if header: ident after literal", after("`x-@{n}`) != label", "`x-@{n}`) != ")),
		label("cond-attr cond: ident after literal", after("`y-@{n}`) != label", "`y-@{n}`) != ")),
		n("cond-attr cond: ident inside hole", after("`y-@{n}`", "`y-@{")),

		n("body interp: ident inside hole", after("`w-@{n}`", "`w-@{")),
		label("body interp: ident after literal", after("`w-@{n}`) + label", "`w-@{n}`) + ")),

		callee("attr value: callee before literal", after("title={wrapS(", "title={"), "wrapS", "func wrapS(s string) string"),
		n("attr value: ident inside hole", after("`/z/@{n}`", "`/z/@{")),
		label("attr value: ident after literal", after("`/z/@{n}`) + label", "`/z/@{n}`) + ")),

		label("piped attr value: ident after literal", after("`p-@{n}`) + label", "`p-@{n}`) + ")),
		n("piped attr value: stage arg", after("|> truncate(n)", "|> truncate(")),

		n("class part: ident inside hole", after("`k-@{n}`", "`k-@{")),
		label("class part: ident after literal", after("`k-@{n}`) + label", "`k-@{n}`) + ")),

		callee("value-form header: callee after literal", after("`e-@{n}`) && ok(", "`e-@{n}`) && "), "ok", "func ok(s string) bool"),
		label("value-form header: ident after literal", after("`e-@{n}`) && ok(label", "`e-@{n}`) && ok(")),
		n("value-form header: ident inside hole", after("`e-@{n}`", "`e-@{")),

		callee("control-flow header: callee after literal", after("`q-@{n}`) && ok(", "`q-@{n}`) && "), "ok", "func ok(s string) bool"),
		label("control-flow header: ident after literal", after("`q-@{n}`) && ok(label", "`q-@{n}`) && ok(")),
		n("control-flow header: ident inside hole", after("`q-@{n}`", "`q-@{")),

		callee("attr value element: callee before element", after("h={wrapN(", "h={"), "wrapN", "func wrapN(n gsx.Node) string"),
		label("attr value element: ident in element attr", after("h={wrapN(<b title={label}", "h={wrapN(<b title={")),
		label("attr value element: ident after element", after("h={wrapN(<b title={label}/>) + label", "h={wrapN(<b title={label}/>) + ")),

		callee("go block element: callee before element", after("hv := wrapN(", "hv := "), "wrapN", "func wrapN(n gsx.Node) string"),
		label("go block element: ident in element attr", after("hv := wrapN(<b title={label}", "hv := wrapN(<b title={")),
		label("go block element: ident after element", after("hv := wrapN(<b title={label}/>) + label", "hv := wrapN(<b title={label}/>) + ")),
	}
}

// testInterpEmbeddedDefinition asserts go-to-definition descends into
// Interp.Embedded: an embedded component tag jumps to its declaration, an
// identifier in an embedded tag's prop/interp resolves to its enclosing-scope
// declaration, and an @{ } hole inside an embedded f-literal resolves too.
func testInterpEmbeddedDefinition(t *testing.T, src string, pkg *Package, path string) {

	lineCol := func(off int) (int, int) {
		return strings.Count(src[:off], "\n") + 1, off - strings.LastIndexByte(src[:off], '\n')
	}

	compBadge := strings.Index(src, "component Badge") + len("component ")
	paramN := strings.Index(src, "n int, label string") // Uses' param n
	paramLabel := strings.Index(src, "label string")    // Uses' param label

	// 1. Embedded component tag name → component Badge declaration.
	t.Run("embedded component tag", func(t *testing.T) {
		off := strings.Index(src, "wrap(<Badge") + len("wrap(<")
		decls, ok := componentTagDeclAt(pkg, path, []byte(src), off)
		if !ok || len(decls) == 0 {
			t.Fatal("embedded component tag did not resolve to a declaration")
		}
		want := sourceintel.Span{Path: path, Start: compBadge, End: compBadge + len("Badge")}
		if decls[0] != want {
			t.Errorf("Badge decl span = %+v, want %+v", decls[0], want)
		}
	})

	// 2a. Identifier inside an embedded tag's prop value: `count={n}` → param n.
	t.Run("embedded prop expr ident", func(t *testing.T) {
		off := strings.Index(src, "count={n}") + len("count={")
		dp, ok := exprDefinitionAt(pkg, path, off)
		if !ok {
			t.Fatal("embedded prop expr ident did not resolve")
		}
		wantLine, wantCol := lineCol(paramN)
		if !strings.HasSuffix(dp.Filename, ".gsx") || dp.Line != wantLine || dp.Column != wantCol {
			t.Errorf("n resolved to %s:%d:%d, want .gsx %d:%d", dp.Filename, dp.Line, dp.Column, wantLine, wantCol)
		}
	})

	// 2b. Identifier inside another embedded prop value: `name={label}` → param label.
	t.Run("embedded prop expr ident label", func(t *testing.T) {
		off := strings.Index(src, "name={label}") + len("name={")
		dp, ok := exprDefinitionAt(pkg, path, off)
		if !ok {
			t.Fatal("embedded prop expr ident (label) did not resolve")
		}
		wantLine, wantCol := lineCol(paramLabel)
		if dp.Line != wantLine || dp.Column != wantCol {
			t.Errorf("label resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
		}
	})

	// Go-to-definition inside a nested construct, and on plain Go around it,
	// in every Go-expression family (handler-driven: the full cascade).
	for _, row := range nestedNavRows(src) {
		t.Run(row.name, func(t *testing.T) {
			uri := pathToURI(path)
			cursor := positionForByteOffset(src, row.off, encUTF16)
			out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, src)+definitionFrame(2, uri, cursor)+exitFrame())
			got := definitionLocation(t, out, 2)
			want := rangeForSpan(src, row.declStart, row.declStart+row.declLen, encUTF16)
			if got == nil || got.URI != uri || got.Range != want {
				t.Fatalf("definition = %+v, want %s at %+v; output:\n%s", got, uri, want, out)
			}
		})
	}

	// Find-references on a parameter lists its uses in the plain-Go runs of
	// split fields, inside nested holes and in nested element attributes.
	t.Run("references to a parameter", func(t *testing.T) {
		uri := pathToURI(path)
		body := strings.Index(src, "component Uses")
		var want []Range
		for i := body; ; {
			j := strings.Index(src[i:], "label")
			if j < 0 {
				break
			}
			at := i + j
			i = at + len("label")
			if at == paramLabel {
				continue
			}
			want = append(want, rangeForSpan(src, at, at+len("label"), encUTF16))
		}
		cursor := positionForByteOffset(src, paramLabel, encUTF16)
		out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, src)+refsFrame(2, uri, cursor.Line, cursor.Character)+exitFrame())
		var got []Range
		for _, location := range referenceLocations(t, out, 2) {
			if location.URI != uri {
				t.Fatalf("reference in %s, want %s", location.URI, uri)
			}
			got = append(got, location.Range)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("references = %+v\nwant %+v", got, want)
		}
	})

	// 3. @{ } hole inside an embedded f-literal: `f`hi @{label}`` → param label.
	t.Run("embedded f-literal hole", func(t *testing.T) {
		off := strings.Index(src, "@{label}") + len("@{")
		dp, ok := exprDefinitionAt(pkg, path, off)
		if !ok {
			t.Fatal("embedded f-literal hole did not resolve")
		}
		wantLine, wantCol := lineCol(paramLabel)
		if dp.Line != wantLine || dp.Column != wantCol {
			t.Errorf("hole label resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
		}
	})
}

// testInterpEmbeddedHover asserts hover descends into Interp.Embedded for the
// same three positions: the embedded component tag shows its signature, and the
// embedded prop ident / f-literal hole show the resolved object's Go type.
func testInterpEmbeddedHover(t *testing.T, src string, pkg *Package, path string) {

	// 1. Embedded component tag → component signature (AST-only path).
	t.Run("embedded component tag sig", func(t *testing.T) {
		off := strings.Index(src, "wrap(<Badge") + len("wrap(<")
		c, nameStart, nameLen, ok := componentAtTag(pkg, path, off)
		if !ok {
			t.Fatal("componentAtTag did not resolve an embedded tag")
		}
		if c.Name != "Badge" {
			t.Errorf("hovered component = %q, want Badge", c.Name)
		}
		if got := src[nameStart : nameStart+nameLen]; got != "Badge" {
			t.Errorf("hover range = %q, want Badge", got)
		}
	})

	// 2. Embedded prop ident hover → resolved object via ExprMap bridge.
	t.Run("embedded prop ident hover", func(t *testing.T) {
		off := strings.Index(src, "count={n}") + len("count={")
		obj := hoverObjectAt(t, pkg, path, off)
		if obj == nil || obj.Name() != "n" {
			t.Fatalf("prop ident hover obj = %v, want n", obj)
		}
	})

	// Hover inside a nested construct, and on plain Go around it, in every
	// Go-expression family (handler-driven).
	for _, row := range nestedNavRows(src) {
		t.Run(row.name, func(t *testing.T) {
			uri := pathToURI(path)
			cursor := positionForByteOffset(src, row.off, encUTF16)
			out := drive(t, &moduleRefsAnalyzer{pkg: pkg}, initFrame()+didOpenFrame(uri, src)+hoverFrame(2, uri, cursor)+exitFrame())
			got := hoverResult(t, out, 2)
			if got == nil || !strings.Contains(got.Contents.Value, row.hover) {
				t.Fatalf("hover = %+v, want %q; output:\n%s", got, row.hover, out)
			}
		})
	}

	// 3. Embedded f-literal hole hover → resolved object.
	t.Run("embedded f-literal hole hover", func(t *testing.T) {
		off := strings.Index(src, "@{label}") + len("@{")
		obj := hoverObjectAt(t, pkg, path, off)
		if obj == nil || obj.Name() != "label" {
			t.Fatalf("hole hover obj = %v, want label", obj)
		}
	})
}

// hoverObjectAt mirrors handleHover's ExprMap bridge to return the go/types
// object under the cursor for a plain (non-ctrl, non-piped) expression span.
func hoverObjectAt(t *testing.T, pkg *Package, path string, off int) types.Object {
	t.Helper()
	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		t.Fatal("no expr node at cursor")
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		t.Fatal("no ExprMap entry for embedded node")
	}
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)
	id := innermostIdent(skel, skelPos)
	if id == nil {
		t.Fatal("no identifier under cursor in skeleton")
	}
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	return obj
}

// testInterpEmbeddedCompletion drives Go completion after a nested construct
// in a split field: member completion lists the receiver's fields and
// identifier completion lists the component scope, as in a field with no
// nested construct. The fake analyzer serves the fixture's own analysis as
// the ephemeral package: every cursor is mid-token, so completion applies no
// repair patch and the ephemeral buffer is the fixture itself.
func testInterpEmbeddedCompletion(t *testing.T, src string, pkg *Package, path string) {
	uri := pathToURI(path)
	after := func(anchor, prefix string) int {
		i := strings.Index(src, anchor)
		if i < 0 {
			t.Fatalf("anchor not found: %s", anchor)
		}
		return i + len(prefix)
	}
	for _, row := range []struct {
		name    string
		off     int
		want    []string
		notWant []string
	}{
		{"attr value: member after literal", after("`m1-@{n}`) + u.Name", "`m1-@{n}`) + u.N"), []string{"Name", "Age"}, []string{"label"}},
		{"body interp: member after literal", after("`m2-@{n}`) + u.Name", "`m2-@{n}`) + u.N"), []string{"Name", "Age"}, []string{"label"}},
		{"control-flow header: member after literal", after("`m3-@{n}`) && ok(u.Name", "`m3-@{n}`) && ok(u.N"), []string{"Name", "Age"}, []string{"label"}},
		{"go block: member after literal", after("`m4-@{n}`) + u.Name", "`m4-@{n}`) + u.N"), []string{"Name", "Age"}, []string{"label"}},
		{"attr value: ident after literal", after("`/z/@{n}`) + label", "`/z/@{n}`) + lab"), []string{"label", "n", "u"}, []string{"Name"}},
		{"body interp: ident after literal", after("`w-@{n}`) + label", "`w-@{n}`) + lab"), []string{"label", "n", "u"}, []string{"Name"}},
		{"control-flow header: ident after literal", after("`q-@{n}`) && ok(label", "`q-@{n}`) && ok(lab"), []string{"label", "n", "u"}, []string{"Name"}},
		{"go block: ident after element", after("hv := wrapN(<b title={label}/>) + label", "hv := wrapN(<b title={label}/>) + lab"), []string{"label", "n", "u"}, []string{"Name"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			a := pipeFilterAnalyzer{ephPkg: pkg, analyzed: pkg}
			cursor := positionForByteOffset(src, row.off, encUTF16)
			out := drive(t, a, initFrame()+didOpenFrame(uri, src)+completionFrame(2, uri, cursor)+exitFrame())
			labels := map[string]bool{}
			for _, item := range decodeCompletionItems(t, out, 2) {
				labels[item.Label] = true
			}
			for _, name := range row.want {
				if !labels[name] {
					t.Errorf("completion missing %q; labels=%v", name, labels)
				}
			}
			for _, name := range row.notWant {
				if labels[name] {
					t.Errorf("completion offers %q; labels=%v", name, labels)
				}
			}
		})
	}
}
