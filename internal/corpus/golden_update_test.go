package corpus

import (
	"bytes"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
)

func TestGoldenSectionUpdateKeepsCaseMetadataAligned(t *testing.T) {
	t.Run("add render facet", func(t *testing.T) {
		c := &caseDoc{
			name:    "new-render",
			archive: &txtar.Archive{},
			goldens: map[string][]byte{},
			invoke:  []byte("Page()"),
		}

		c.setGoldenSection("render.golden", []byte("<p>hi</p>"))

		want := "new-render\tdiag render\nTOTAL: 1 cases (render: 1, error: 0, gen-pinned: 0)\n"
		if got := string(coverageReport([]*caseDoc{c})); got != want {
			t.Fatalf("coverage after one update:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("replace existing facet", func(t *testing.T) {
		oldData := []byte("old")
		c := &caseDoc{
			archive: &txtar.Archive{Files: []txtar.File{{Name: "render.golden", Data: oldData}}},
			goldens: map[string][]byte{"render.golden": oldData},
		}
		newData := []byte("new")

		c.setGoldenSection("render.golden", newData)

		if got := c.goldens["render.golden"]; !bytes.Equal(got, newData) {
			t.Errorf("cached render golden = %q, want %q", got, newData)
		}
		if got := archiveSection(c.archive, "render.golden"); !bytes.Equal(got, newData) {
			t.Errorf("archive render golden = %q, want %q", got, newData)
		}
	})
}

func archiveSection(archive *txtar.Archive, name string) []byte {
	for _, file := range archive.Files {
		if file.Name == name {
			return file.Data
		}
	}
	return nil
}
