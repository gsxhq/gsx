package corpus

import (
	"path/filepath"
	"testing"
)

// TestExamples compiles and renders every examples/*.txtar through the real
// pipeline (codegen + go run) and asserts its render.golden — the same harness
// TestCorpus uses. Run with -update to (re)generate the render.golden sections.
func TestExamples(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	examplesDir := filepath.Join(repoRoot, "examples")

	files, err := txtarFiles(examplesDir)
	if err != nil {
		t.Fatalf("walk examples: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no examples/*.txtar found")
	}

	var cases []*caseDoc
	paths := map[string]string{}
	for _, p := range files {
		c, err := loadCase(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if !c.renderable() {
			t.Fatalf("%s: example has no -- invoke --", c.name)
		}
		cases = append(cases, c)
		paths[c.name] = p
	}

	cg, err := batchCodegen(repoRoot, cases)
	if err != nil {
		t.Fatalf("batchCodegen: %v", err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := cg[c.name]
			if r == nil {
				t.Fatalf("no codegen result")
			}
			if len(r.diag) > 0 {
				t.Fatalf("example produced diagnostics (examples must be clean):\n%s", r.diag)
			}
			if *update {
				setSection(c.archive, "render.golden", []byte(r.html))
				writeArchive(t, paths[c.name], c.archive)
				return
			}
			diff, derr := htmlStructuralDiff(r.html, string(c.goldens["render.golden"]))
			if derr != nil {
				t.Fatal(derr)
			}
			if diff != "" {
				t.Errorf("render mismatch (%s)\n--- got ---\n%s\n--- want ---\n%s",
					diff, r.html, c.goldens["render.golden"])
			}
		})
	}
}
