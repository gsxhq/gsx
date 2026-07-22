package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type componentBenchmarkFixture struct {
	opts         Options
	targetDir    string
	overridePath string
	variants     [2][]byte
}

var componentBenchmarkOutput map[string][]byte

func setupComponentBenchmarkFixture(b *testing.B, kind string) componentBenchmarkFixture {
	b.Helper()
	root := b.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	write := func(path, contents string) {
		b.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	write("go.mod", "module example.com/componentbench\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	fixture := componentBenchmarkFixture{
		opts: Options{
			ModuleRoot: root,
			ModulePath: "example.com/componentbench",
			FilterPkgs: []string{StdImportPath},
		},
	}
	switch kind {
	case "same-package":
		write("app/card.gsx", "package app\n\ncomponent Card(title string) {\n\t<article>{ title }</article>\n}\n")
		fixture.targetDir = filepath.Join(root, "app")
		fixture.overridePath = filepath.Join(fixture.targetDir, "page.gsx")
		fixture.variants = [2][]byte{
			[]byte("package app\n\n// warm variant a\ncomponent Page(label string) {\n\t<main><Card title={ label }/></main>\n}\n"),
			[]byte("package app\n\n// warm variant b\ncomponent Page(label string) {\n\t<main><Card title={ label }/></main>\n}\n"),
		}
	case "imported":
		write("components/card.gsx", "package components\n\ncomponent Card(title string) {\n\t<article>{ title }</article>\n}\n")
		fixture.targetDir = filepath.Join(root, "pages")
		fixture.overridePath = filepath.Join(fixture.targetDir, "page.gsx")
		fixture.variants = [2][]byte{
			[]byte("package pages\n\nimport \"example.com/componentbench/components\"\n\n// warm variant a\ncomponent Page(label string) {\n\t<main><components.Card title={ label }/></main>\n}\n"),
			[]byte("package pages\n\nimport \"example.com/componentbench/components\"\n\n// warm variant b\ncomponent Page(label string) {\n\t<main><components.Card title={ label }/></main>\n}\n"),
		}
	case "embedded":
		write("app/card.gsx", "package app\n\nimport \"github.com/gsxhq/gsx\"\n\nfunc hold(n gsx.Node) gsx.Node { return n }\n\ncomponent Card(title string) {\n\t<article>{ title }</article>\n}\n")
		fixture.targetDir = filepath.Join(root, "app")
		fixture.overridePath = filepath.Join(fixture.targetDir, "page.gsx")
		fixture.variants = [2][]byte{
			[]byte("package app\n\n// warm variant a\ncomponent Page(label string) {\n\t<main>{ hold(<Card title={ label }/>) }</main>\n}\n"),
			[]byte("package app\n\n// warm variant b\ncomponent Page(label string) {\n\t<main>{ hold(<Card title={ label }/>) }</main>\n}\n"),
		}
	case "attrs-stream":
		write("app/card.gsx", "package app\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Card(title string, attrs gsx.Attrs) {\n\t<article {attrs...}>{title}</article>\n}\n")
		fixture.targetDir = filepath.Join(root, "app")
		fixture.overridePath = filepath.Join(fixture.targetDir, "page.gsx")
		fixture.variants = [2][]byte{
			[]byte("package app\n\nimport \"github.com/gsxhq/gsx\"\n\n// warm variant a\ncomponent Page(label string, attrs gsx.Attrs) {\n\t<main><Card title={label} attrs={attrs} data-bench=\"a\"/></main>\n}\n"),
			[]byte("package app\n\nimport \"github.com/gsxhq/gsx\"\n\n// warm variant b\ncomponent Page(label string, attrs gsx.Attrs) {\n\t<main><Card title={label} attrs={attrs} data-bench=\"a\"/></main>\n}\n"),
		}
	case "variadic-children":
		write("app/tabs.gsx", "package app\n\nimport \"github.com/gsxhq/gsx\"\n\ncomponent Tabs(children ...gsx.Node) {\n\t<ul>{ for _, child := range children { <li>{child}</li> } }</ul>\n}\n")
		fixture.targetDir = filepath.Join(root, "app")
		fixture.overridePath = filepath.Join(fixture.targetDir, "page.gsx")
		fixture.variants = [2][]byte{
			[]byte("package app\n\n// warm variant a\ncomponent Page(label string) {\n\t<Tabs><span>{label}</span><strong>fixed</strong></Tabs>\n}\n"),
			[]byte("package app\n\n// warm variant b\ncomponent Page(label string) {\n\t<Tabs><span>{label}</span><strong>fixed</strong></Tabs>\n}\n"),
		}
	default:
		b.Fatalf("unknown component benchmark fixture %q", kind)
	}
	overrideRel, err := filepath.Rel(root, fixture.overridePath)
	if err != nil {
		b.Fatal(err)
	}
	write(overrideRel, string(fixture.variants[0]))
	return fixture
}

// setupManyComponentCallsFixture builds one flat package with many component
// definitions and a page that calls them hundreds of times. Every call site
// drives planComponentPositionalCalls, whose per-call cost historically included
// a maps.Clone of the WHOLE-PACKAGE expression-fact map — O(calls × facts). With
// many calls contributing facts, the package fact map is large and cloned once
// per call, so this fixture makes that quadratic allocation dominate. Warm
// regeneration isolates the analyze/plan cost from packages.Load.
func setupManyComponentCallsFixture(b *testing.B, components, calls int) componentBenchmarkFixture {
	b.Helper()
	root := b.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	write := func(path, contents string) {
		b.Helper()
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	write("go.mod", "module example.com/componentbench\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	var defs strings.Builder
	defs.WriteString("package app\n\n")
	for i := range components {
		fmt.Fprintf(&defs, "component Comp%d(title string, subtitle string, count int) {\n\t<article><h1>{ title }</h1><p>{ subtitle }</p><span>{ count }</span></article>\n}\n\n", i)
	}
	write("app/components.gsx", defs.String())

	page := func(marker string) []byte {
		var b strings.Builder
		fmt.Fprintf(&b, "package app\n\n// warm variant %s\ncomponent Page(label string, n int) {\n\t<main>\n", marker)
		for i := range calls {
			fmt.Fprintf(&b, "\t\t<Comp%d title={ label } subtitle={ label } count={ n + %d }/>\n", i%components, i)
		}
		b.WriteString("\t</main>\n}\n")
		return []byte(b.String())
	}

	fixture := componentBenchmarkFixture{
		opts: Options{
			ModuleRoot: root,
			ModulePath: "example.com/componentbench",
			FilterPkgs: []string{StdImportPath},
		},
		targetDir:    filepath.Join(root, "app"),
		overridePath: filepath.Join(root, "app", "page.gsx"),
		variants:     [2][]byte{page("a"), page("b")},
	}
	write("app/page.gsx", string(fixture.variants[0]))
	return fixture
}

func BenchmarkModuleGenerateManyComponentCalls(b *testing.B) {
	fixture := setupManyComponentCallsFixture(b, 50, 200)
	m, err := Open(fixture.opts)
	if err != nil {
		b.Fatal(err)
	}
	out, diags, err := m.Generate(fixture.targetDir)
	if err != nil {
		b.Fatal(err)
	}
	if len(diags) != 0 {
		b.Fatalf("prime Generate diagnostics: %v", diags)
	}
	if len(out) == 0 {
		b.Fatal("prime Generate produced no component output")
	}
	componentBenchmarkOutput = out
	b.ReportAllocs()
	for i := range b.N {
		m.SetOverride(fixture.overridePath, fixture.variants[(i+1)%len(fixture.variants)])
		b.StartTimer()
		out, diags, err := m.Generate(fixture.targetDir)
		b.StopTimer()
		if err != nil {
			b.Fatal(err)
		}
		if len(diags) != 0 {
			b.Fatalf("Generate diagnostics: %v", diags)
		}
		if len(out) == 0 {
			b.Fatal("Generate produced no component output")
		}
		componentBenchmarkOutput = out
	}
}

func BenchmarkModuleGenerateComponentCold(b *testing.B) {
	for _, kind := range []string{"same-package", "imported", "embedded", "attrs-stream", "variadic-children"} {
		b.Run(kind, func(b *testing.B) {
			b.StopTimer()
			fixture := setupComponentBenchmarkFixture(b, kind)
			b.ReportAllocs()
			for range b.N {
				b.StartTimer()
				m, err := Open(fixture.opts)
				if err != nil {
					b.StopTimer()
					b.Fatal(err)
				}
				out, diags, err := m.Generate(fixture.targetDir)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if len(diags) != 0 {
					b.Fatalf("Generate diagnostics: %v", diags)
				}
				if len(out) == 0 {
					b.Fatal("Generate produced no component output")
				}
				componentBenchmarkOutput = out
			}
		})
	}
}

func BenchmarkModuleGenerateComponentWarm(b *testing.B) {
	for _, kind := range []string{"same-package", "imported", "embedded", "attrs-stream", "variadic-children"} {
		b.Run(kind, func(b *testing.B) {
			b.StopTimer()
			fixture := setupComponentBenchmarkFixture(b, kind)
			m, err := Open(fixture.opts)
			if err != nil {
				b.Fatal(err)
			}
			out, diags, err := m.Generate(fixture.targetDir)
			if err != nil {
				b.Fatal(err)
			}
			if len(diags) != 0 {
				b.Fatalf("prime Generate diagnostics: %v", diags)
			}
			if len(out) == 0 {
				b.Fatal("prime Generate produced no component output")
			}
			componentBenchmarkOutput = out
			b.ReportAllocs()
			for i := range b.N {
				m.SetOverride(fixture.overridePath, fixture.variants[(i+1)%len(fixture.variants)])
				b.StartTimer()
				out, diags, err := m.Generate(fixture.targetDir)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if len(diags) != 0 {
					b.Fatalf("Generate diagnostics: %v", diags)
				}
				if len(out) == 0 {
					b.Fatal("Generate produced no component output")
				}
				componentBenchmarkOutput = out
			}
		})
	}
}

// TestWarmRegenDoesNoGoListReloads is a performance regression guard. The two
// expensive, non-incremental costs in analyze() are go-list / packages.Load
// calls: the external importer (externalImporter) and the filter table
// (cachedFuncTables). Each is ~150ms; they depend only on Module-immutable
// inputs, so they MUST be loaded once and reused across every warm regen. If a
// future change reintroduces a per-edit go-list, a `gsx generate --watch` cycle
// (or an LSP keystroke) regresses from ~10ms back to hundreds of ms.
//
// This guards the invariant by COUNT, not wall-clock (which is machine-flaky):
// across a cold generate plus many edit-driven regens, externalLoads() and
// filterTableLoads() must each stay at 1.
func TestWarmRegenDoesNoGoListReloads(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	pages := filepath.Join(root, "pages")
	comp := filepath.Join(root, "components")
	card := filepath.Join(comp, "card.gsx")

	// Cold generate the whole chain: this is where the ONE allowed go-list happens.
	// The filter table is harvested from that load's types, so it costs zero.
	if _, _, err := m.Generate(pages); err != nil {
		t.Fatalf("cold generate: %v", err)
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 0 {
		t.Fatalf("after cold generate: externalLoads=%d filterTableLoads=%d; want 1,0", el, fl)
	}

	// 10 edit-driven warm regens of the components package. Each is a real
	// content change → applyDirty → re-analyze. None may reload ext or filters.
	for i := range 10 {
		m.SetOverride(card, fmt.Appendf(nil,
			"package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div>%d<util.Y label={ title }/></div>\n}\n", i))
		if _, _, err := m.Generate(comp); err != nil {
			t.Fatalf("warm regen #%d: %v", i, err)
		}
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 0 {
		t.Fatalf("after 10 warm regens: externalLoads=%d filterTableLoads=%d; want 1,0 "+
			"(a value > 1 means a per-edit go-list crept back in — the watch/LSP slowdown)", el, fl)
	}

	// Generating a DIFFERENT package in the same module must also reuse both
	// caches — the go-list inputs are module-wide, not per-package.
	if _, _, err := m.Generate(pages); err != nil {
		t.Fatalf("regen pages: %v", err)
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 0 {
		t.Fatalf("after cross-package regen: externalLoads=%d filterTableLoads=%d; want 1,0", el, fl)
	}
}

func TestWarmLSPSourceIndexDoesNoGoListReloads(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	m, root := setupChainModule(t)
	comp := filepath.Join(root, "components")
	card := filepath.Join(comp, "card.gsx")

	result, err := m.Package(comp)
	if err != nil {
		t.Fatalf("cold Package: %v", err)
	}
	if result.SourceIndex == nil {
		t.Fatal("cold Package retained no source index")
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 0 {
		t.Fatalf("after cold Package: externalLoads=%d filterTableLoads=%d; want 1,0", el, fl)
	}

	for i := range 10 {
		m.SetOverride(card, fmt.Appendf(nil,
			"package components\n\nimport \"example.com/x/util\"\n\ncomponent X(title string) {\n\t<div>%d<util.Y label={ title }/></div>\n}\n", i))
		result, err = m.Package(comp)
		if err != nil {
			t.Fatalf("warm Package #%d: %v", i, err)
		}
		if result.SourceIndex == nil {
			t.Fatalf("warm Package #%d retained no source index", i)
		}
	}
	if el, fl := m.externalLoads(), m.filterTableLoads(); el != 1 || fl != 0 {
		t.Fatalf("after indexed warm Packages: externalLoads=%d filterTableLoads=%d; want 1,0", el, fl)
	}
}
