// Package skillscripts tests the shell scripts shipped with the agent skills.
package skillscripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const componentCallTitle = "== component called as a Go function"

// componentCallHits runs find-smells.sh over files and returns the trimmed
// source text of each hit in the component-call section.
func componentCallHits(t *testing.T, files map[string]string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	for name, src := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(bash, "../../skills/gsx/scripts/find-smells.sh", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("find-smells.sh: %v\n%s", err, out)
	}
	var hits []string
	in := false
	for line := range strings.SplitSeq(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, componentCallTitle):
			in = true
		case strings.HasPrefix(line, "== ") || line == "":
			in = false
		case in:
			_, text, ok := strings.Cut(strings.TrimPrefix(line, dir+string(filepath.Separator)), ":")
			if !ok {
				t.Fatalf("unexpected hit line %q", line)
			}
			_, text, _ = strings.Cut(text, ":")
			hits = append(hits, strings.TrimSpace(text))
		}
	}
	return hits
}

func TestFindSmellsComponentCalls(t *testing.T) {
	got := componentCallHits(t, map[string]string{
		"ui2/card.gsx": `package ui2

component Card() {
	<div/>
}
`,
		"views/page.gsx": `package views

component (p Page) Body() {
	<main/>
}

component List[T any](xs []T) {
	<ul/>
}

component Greeting(n int) {
	<p>
		{ Card() }
	</p>
	<p>
		{ ui2.Card() }
	</p>
	<p>
		{ p.Body() }
	</p>
	<p>
		{ List[int](nil) }
	</p>
	<p>
		{ msg.ItemsSelected(n) }
	</p>
	<p>
		{ i18n.Rich("greeting") }
	</p>
	<p>
		{ gsx.Card() }
	</p>
}
`,
	})
	want := []string{"{ Card() }", "{ ui2.Card() }", "{ p.Body() }", "{ List[int](nil) }"}
	if !slices.Equal(got, want) {
		t.Errorf("hits = %q, want %q", got, want)
	}
}

func TestFindSmellsComponentCallsWithoutComponents(t *testing.T) {
	got := componentCallHits(t, map[string]string{
		"page.gsx": `package views

func Greeting(n int) gsx.Node {
	return <p>
		{ msg.ItemsSelected(n) }
	</p>
}
`,
	})
	if len(got) != 0 {
		t.Errorf("hits = %q, want none", got)
	}
}
