package txtar_test

import (
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
)

func TestParse(t *testing.T) {
	input := `This is a comment.
More comment.
-- file1.txt --
Hello, file1!
-- file2.go --
package main
`
	a := txtar.Parse([]byte(input))

	wantComment := "This is a comment.\nMore comment.\n"
	if string(a.Comment) != wantComment {
		t.Errorf("Comment = %q, want %q", a.Comment, wantComment)
	}
	if len(a.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(a.Files))
	}
	if a.Files[0].Name != "file1.txt" {
		t.Errorf("Files[0].Name = %q, want %q", a.Files[0].Name, "file1.txt")
	}
	if string(a.Files[0].Data) != "Hello, file1!\n" {
		t.Errorf("Files[0].Data = %q, want %q", a.Files[0].Data, "Hello, file1!\n")
	}
	if a.Files[1].Name != "file2.go" {
		t.Errorf("Files[1].Name = %q, want %q", a.Files[1].Name, "file2.go")
	}
	if string(a.Files[1].Data) != "package main\n" {
		t.Errorf("Files[1].Data = %q, want %q", a.Files[1].Data, "package main\n")
	}
}

func TestRoundTrip(t *testing.T) {
	input := `comment line
-- alpha.txt --
alpha content
-- beta.txt --
beta line 1
beta line 2
`
	a := txtar.Parse([]byte(input))
	formatted := txtar.Format(a)
	a2 := txtar.Parse(formatted)
	if !reflect.DeepEqual(a, a2) {
		t.Errorf("round-trip mismatch:\n  original: %+v\n  reparsed: %+v", a, a2)
	}
}

func TestNotAMarker(t *testing.T) {
	// "-- x" (no trailing " --") and "--nope" are NOT markers — stay in content.
	input := `-- x
--nope
-- real --
data
`
	a := txtar.Parse([]byte(input))
	wantComment := "-- x\n--nope\n"
	if string(a.Comment) != wantComment {
		t.Errorf("Comment = %q, want %q", a.Comment, wantComment)
	}
	if len(a.Files) != 1 {
		t.Fatalf("len(Files) = %d, want 1", len(a.Files))
	}
	if a.Files[0].Name != "real" {
		t.Errorf("Files[0].Name = %q, want %q", a.Files[0].Name, "real")
	}
}
