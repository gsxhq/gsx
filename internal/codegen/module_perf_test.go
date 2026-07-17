package codegen

import (
	"fmt"
	"os"
	"path/filepath"
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
