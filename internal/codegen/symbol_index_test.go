package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

// One Module open for the whole per-package symbol-index surface (replaces
// crossindex_test.go + navindex_test.go).
func TestPackageSymbolIndex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	const card = "package x\n\nimport \"github.com/gsxhq/gsx\"\n\n// Card renders a card.\ntype Card struct{ Title string }\n\nfunc (c Card) title() string { return c.Title }\n\ncomponent (c Card) Render(size int) {\n\t<div>{ c.title() |> upper }{ size }</div>\n}\n\ncomponent Badge(label string) {\n\t<span>{ label }</span>\n}\n\ncomponent tiny() {\n\t<i/>\n}\n\ncomponent Box(caption string, attrs gsx.Attrs) {\n\t<div { attrs... }>{ caption }</div>\n}\n"
	const page = "package x\n\ncomponent Page() {\n\t<main>\n\t\t<Badge label=\"a\"/>\n\t\t<tiny/>\n\t\t<Icon label=\"i\"/>\n\t\t<Box caption=\"c\" class=\"forwarded\"/>\n\t\t{ Card{Title: \"t\"}.Render(1) }\n\t</main>\n}\n"
	const helper = "package x\n\nfunc use() Card { c := Card{}; _ = c.title(); _ = Badge; return c }\n"
	// Build-tag variants: one logical component, two authored declarations. The
	// inactive one is a go/types redeclaration and gets NO object, so its
	// declaration span can only reach the index as an extra occurrence.
	const icon = "//go:build !never\n\npackage x\n\ncomponent Icon(label string) {\n\t<i>{ label }</i>\n}\n"
	const iconNever = "//go:build never\n\npackage x\n\ncomponent Icon(label string) {\n\t<b>{ label }</b>\n}\n"
	writeFile(t, dir, "card.gsx", card)
	writeFile(t, dir, "page.gsx", page)
	writeFile(t, dir, "helper.go", helper)
	writeFile(t, dir, "icon.gsx", icon)
	writeFile(t, dir, "icon_never.gsx", iconNever)
	m, err := Open(Options{ModuleRoot: dir, ModulePath: "example.com/x", FilterPkgs: []string{StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) != 0 {
		t.Fatalf("diags: %+v", pr.Diags)
	}
	g := sourceintel.NewSymbolGraph()
	g.AddIndex(pr.SourceIndex, sourceintel.NewKeyer(pr.Types))
	cardPath, pagePath, helperPath := filepath.Join(dir, "card.gsx"), filepath.Join(dir, "page.gsx"), filepath.Join(dir, "helper.go")
	iconPath := filepath.Join(dir, "icon.gsx")
	at := func(path, src, needle string, occurrence int) sourceintel.ObjectKey {
		t.Helper()
		off := nth(src, needle, occurrence)
		key, _, ok := g.At(path, off)
		if !ok {
			t.Fatalf("no occurrence at %s:%d (%q#%d)", filepath.Base(path), off, needle, occurrence)
		}
		return key
	}
	files := func(spans []sourceintel.Span) map[string]int {
		out := map[string]int{}
		for _, s := range spans {
			out[filepath.Base(s.Path)]++
		}
		return out
	}
	// type Card: def in card.gsx; refs in card.gsx (method recv, component recv),
	// page.gsx composite literal, helper.go x2 (result type + composite literal)
	cardKey := at(cardPath, card, "Card struct", 0)
	if string(cardKey) != "example.com/x Card" {
		t.Fatalf("Card key = %q", cardKey)
	}
	if f := files(g.References(cardKey)); f["helper.go"] != 2 || f["page.gsx"] != 1 || f["card.gsx"] != 2 {
		t.Fatalf("Card refs = %v", f)
	}
	// helper.go cursor resolves to the same key (identity-mapped sibling)
	if k := at(helperPath, helper, "Card {", 0); k != cardKey {
		t.Fatalf("helper.go Card key = %q", k)
	}
	// unexported method title: def + 2 refs (card.gsx pipe seed, helper.go)
	titleKey := at(cardPath, card, "title()", 0)
	if f := files(g.References(titleKey)); f["card.gsx"] != 1 || f["helper.go"] != 1 {
		t.Fatalf("title refs = %v", f)
	}
	// component Badge: <Badge/> tag in page.gsx + helper.go value use
	badgeKey := at(cardPath, card, "Badge", 0)
	if f := files(g.References(badgeKey)); f["page.gsx"] != 1 || f["helper.go"] != 1 {
		t.Fatalf("Badge refs = %v", f)
	}
	// The <Badge/> tag site. The occurrence is resolved from the package scope,
	// NOT from the positional call plan (symbol_extras.go's
	// componentTagOccurrences); this pins that the scope-resolved object and the
	// planned call target are the same symbol on a package that plans cleanly.
	if k := at(pagePath, page, "Badge", 0); k != badgeKey {
		t.Fatalf("tag cursor key = %q, want %q", k, badgeKey)
	}
	// param label: def + attr site in page.gsx + body use in card.gsx
	labelKey := at(cardPath, card, "label", 0)
	if f := files(g.References(labelKey)); f["page.gsx"] != 1 || f["card.gsx"] != 1 {
		t.Fatalf("label refs = %v (key %q)", f, labelKey)
	}
	if k := at(pagePath, page, "label=", 0); k != labelKey {
		t.Fatalf("attr cursor key = %q, want %q", k, labelKey)
	}
	// private component tiny: def + <tiny/> tag
	tinyKey := at(pagePath, page, "tiny", 0)
	if len(g.Definitions(tinyKey)) != 1 || files(g.References(tinyKey))["page.gsx"] != 1 {
		t.Fatalf("tiny: defs=%+v refs=%+v", g.Definitions(tinyKey), g.References(tinyKey))
	}
	// method component Render: page.gsx Go call + def
	renderKey := at(cardPath, card, "Render", 0)
	if f := files(g.References(renderKey)); f["page.gsx"] != 1 {
		t.Fatalf("Render refs = %v", f)
	}
	// pipe stage name upper → std filter func key, referenced from card.gsx
	upperKey := at(cardPath, card, "upper", 0)
	if !strings.HasPrefix(string(upperKey), StdImportPath+" ") || files(g.References(upperKey))["card.gsx"] != 1 {
		t.Fatalf("upper key=%q refs=%+v", upperKey, g.References(upperKey))
	}
	// build-tag variants: BOTH authored declarations are definitions of the one
	// logical Icon, and the <Icon/> tag references it.
	iconKey := at(iconPath, icon, "Icon", 0)
	if d := files(g.Definitions(iconKey)); d["icon.gsx"] != 1 || d["icon_never.gsx"] != 1 {
		t.Fatalf("Icon defs = %v (%+v)", d, g.Definitions(iconKey))
	}
	if f := files(g.References(iconKey)); f["page.gsx"] != 1 {
		t.Fatalf("Icon refs = %v", f)
	}
	// the variant's parameter is one symbol too: attr site plus body use.
	iconLabelKey := at(pagePath, page, "label=", 1)
	if f := files(g.References(iconLabelKey)); f["page.gsx"] != 1 || f["icon.gsx"] != 1 {
		t.Fatalf("Icon label refs = %v (key %q)", f, iconLabelKey)
	}
	// An attrs-bag parameter is referenced by its NAME only. `class="forwarded"`
	// is a bag contributor bound to the same parameter by the call plan, but it
	// is forwarded data, not an authored mention of `attrs` — it must not be a
	// reference. `caption=` names its parameter and must be one.
	// An attrs-bag parameter is referenced by NAME only. `class="forwarded"` is
	// forwarded data, not an authored mention of `attrs`, and must not become a
	// reference to it — `gr` on `attrs` would otherwise return every forwarded
	// attribute in the module. Only the `{ attrs... }` body use counts. (Today
	// the call plan already keeps forwarded pairs out of ComponentCalls[].Params;
	// gsxExtraOccurrences' named-binding guard is the second line of defence, and
	// this assertion pins the contract whichever layer enforces it.)
	attrsKey := at(cardPath, card, "attrs gsx.Attrs", 0)
	if f := files(g.References(attrsKey)); f["page.gsx"] != 0 || f["card.gsx"] != 1 {
		t.Fatalf("attrs refs = %v, want only the `{ attrs... }` body use in card.gsx", f)
	}
	// A NAMED binding is a reference: the attr site plus the body use.
	captionKey := at(pagePath, page, "caption=", 0)
	if f := files(g.References(captionKey)); f["page.gsx"] != 1 || f["card.gsx"] != 1 {
		t.Fatalf("caption refs = %v (key %q)", f, captionKey)
	}
	// no hand-written sibling was dropped from the index on a bytes/syntax size
	// disagreement.
	if skips := m.companionIndexSkipCount(); skips != 0 {
		t.Fatalf("companion index skips = %d, want 0", skips)
	}
	// param size: def and body use both in card.gsx
	sizeKey := at(cardPath, card, "size int", 0)
	if len(g.Definitions(sizeKey)) != 1 || files(g.References(sizeKey))["card.gsx"] != 1 {
		t.Fatalf("size: defs=%+v refs=%+v", g.Definitions(sizeKey), g.References(sizeKey))
	}

	// One unrelated type error anywhere in the package skips positional call
	// planning for the WHOLE package (analyze's targetPlanningReady), which is
	// the ordinary state of a file mid-edit. The tag→component edge must
	// survive it: it is resolved by name resolution, not from the plan. Rides
	// this fixture's Module through AnalyzeEphemeral — no second Module open.
	t.Run("tag edge survives planning being skipped", func(t *testing.T) {
		broken := page + "\ncomponent Broken() {\n\t<div>{ undefinedIdent }</div>\n}\n"
		res, err := m.AnalyzeEphemeral(dir, pagePath, []byte(broken))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(diagText(res.Diags), "undefinedIdent") {
			t.Fatalf("fixture no longer carries the unrelated type error: %+v", res.Diags)
		}
		if len(res.ComponentCalls) != 0 {
			t.Fatalf("ComponentCalls = %d, want 0: planning must be OFF for this test to mean anything", len(res.ComponentCalls))
		}
		eg := sourceintel.NewSymbolGraph()
		eg.AddIndex(res.SourceIndex, sourceintel.NewKeyer(res.Types))
		for _, tc := range []struct{ needle, want string }{
			{"Badge", "example.com/x Badge"}, // exported same-package tag
			{"tiny", "example.com/x tiny"},   // lowercase same-package tag
			{"Icon", "example.com/x Icon"},   // build-tag variant family
		} {
			off := nth(broken, "<"+tc.needle, 0) + 1
			key, span, ok := eg.At(pagePath, off)
			if !ok || string(key) != tc.want {
				t.Fatalf("<%s/> tag with planning off: At = %q %+v %v, want %q", tc.needle, key, span, ok, tc.want)
			}
			if len(eg.Definitions(key)) == 0 {
				t.Fatalf("<%s/> tag with planning off: no definitions for %q", tc.needle, key)
			}
		}
		// The attr→parameter edge legitimately goes with the plan (which
		// parameter an attribute binds IS the plan's answer), so `label=` must
		// resolve to nothing here rather than to some guessed object.
		if key, _, ok := eg.At(pagePath, nth(broken, "label=", 0)); ok {
			t.Fatalf("attr binding resolved to %q with planning off; want no occurrence", key)
		}
	})
}

func diagText(diags []diag.Diagnostic) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Message)
		b.WriteByte('\n')
	}
	return b.String()
}

func nth(src, needle string, occurrence int) int {
	off := -1
	for i := 0; i <= occurrence; i++ {
		next := strings.Index(src[off+1:], needle)
		if next < 0 {
			return -1
		}
		off = off + 1 + next
	}
	return off
}
