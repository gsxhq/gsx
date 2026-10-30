package sourceview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeWitnessFixture lays out the module every witness test starts from.
func writeWitnessFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n")
	writeTestFile(t, root, "a/x.go", "package a\n\nimport \"strings\"\n\nfunc X() string { return strings.ToUpper(\"x\") }\n")
	writeTestFile(t, root, "a/embed.go", "package a\n\nimport _ \"embed\"\n\n//go:embed data.txt\nvar data string\n")
	writeTestFile(t, root, "a/data.txt", "data\n")
	writeTestFile(t, root, "a/x_test.go", "package a\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")
	writeTestFile(t, root, "a/page.gsx", "package a\ncomponent Page() { <p/> }\n")
	writeTestFile(t, root, "a/page.x.go", "package a\n\n// generated v1\n")
	writeTestFile(t, root, "b/b.go", "package b\n\nimport \"example.com/app/a\"\n\nvar _ = a.X\n")
	writeTestFile(t, root, "testdata/x/x.go", "package x\n")
	writeTestFile(t, root, "nested/go.mod", "module example.com/app/nested\n")
	writeTestFile(t, root, "nested/n/n.go", "package n\n")
	writeTestFile(t, root, ".hidden/h/h.go", "package h\n")
	return root
}

func buildWitnessManifest(t *testing.T, root string) *Manifest {
	t.Helper()
	manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func graphWitness(t *testing.T, manifest *Manifest, input GraphWitnessInput) string {
	t.Helper()
	digest, err := manifest.GraphWitness(input)
	if err != nil {
		t.Fatalf("GraphWitness(%v): %v", input, err)
	}
	return digest
}

func TestGraphWitnessDetectsGraphInputs(t *testing.T) {
	ab := GraphWitnessInput{Dirs: []string{"a", "b"}}
	tests := []struct {
		name  string
		input GraphWitnessInput
		edit  func(t *testing.T, root string)
		same  bool
	}{
		{name: "body edit", input: ab, same: true, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x.go", "package a\n\nimport \"strings\"\n\nfunc X() string { return strings.ToLower(\"y\") }\n")
		}},
		{name: "import added", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x.go", "package a\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nvar _ = fmt.Sprint\n\nfunc X() string { return strings.ToUpper(\"x\") }\n")
		}},
		{name: "build constraint added", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x.go", "//go:build linux\n\npackage a\n\nimport \"strings\"\n\nfunc X() string { return strings.ToUpper(\"x\") }\n")
		}},
		{name: "header line ending changed", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x.go", "package a\r\n\nimport \"strings\"\n\nfunc X() string { return strings.ToUpper(\"x\") }\n")
		}},
		{name: "go file added", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/y.go", "package a\n")
		}},
		{name: "go file removed", input: ab, edit: func(t *testing.T, root string) {
			removeTestFile(t, root, "b/b.go")
		}},
		{name: "c file added", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x.c", "int x;\n")
		}},
		// A _test.go header can fail a live go list (a third package name, a
		// broken import block, import "C"), so test files are witnessed like
		// any other Go file; only their bodies are free.
		{name: "test file added", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/y_test.go", "package zzz\n")
		}},
		{name: "test file header edited", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x_test.go", "package a\n\nimport \"C\"\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")
		}},
		{name: "test file body edited", input: ab, same: true, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x_test.go", "package a\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { t.Log(\"x\") }\n")
		}},
		{name: "embed line edited", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/embed.go", "package a\n\nimport _ \"embed\"\n\n//go:embed data.txt other.txt\nvar data string\n")
		}},
		{name: "embed target deleted", input: GraphWitnessInput{Dirs: ab.Dirs, EmbedFiles: []string{"a/data.txt"}}, edit: func(t *testing.T, root string) {
			removeTestFile(t, root, "a/data.txt")
		}},
		{name: "nested go.mod added", input: ab, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/go.mod", "module example.com/app/a\n")
		}},
		{name: "ancestor go.mod added", input: GraphWitnessInput{Dirs: []string{"a/sub"}}, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/go.mod", "module example.com/app/a\n")
		}},
		{name: "shadow dir created", input: GraphWitnessInput{Dirs: []string{"a", "b", "c"}}, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "c/c.go", "package c\n")
		}},
		{name: "shadow dir under nested module", input: GraphWitnessInput{Dirs: []string{"a", "nested/n"}}, same: true, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "nested/n/n.go", "package n\n\nimport \"fmt\"\n")
		}},
		{name: "nested module boundary removed", input: GraphWitnessInput{Dirs: []string{"a", "nested/n"}}, edit: func(t *testing.T, root string) {
			removeTestFile(t, root, "nested/go.mod")
		}},
		{name: "testdata header edited", input: GraphWitnessInput{Dirs: []string{"testdata/x"}}, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "testdata/x/x.go", "package x\n\nimport \"fmt\"\n")
		}},
		{name: "paired output regenerated", input: ab, same: true, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/page.x.go", "package a\n\nimport \"fmt\"\n\n// generated v2\nvar _ = fmt.Sprint\n")
		}},
		{name: "paired output deleted", input: ab, same: true, edit: func(t *testing.T, root string) {
			removeTestFile(t, root, "a/page.x.go")
		}},
		{name: "unrelated dir edited", input: ab, same: true, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "testdata/x/y.go", "package x\n")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeWitnessFixture(t)
			base := graphWitness(t, buildWitnessManifest(t, root), test.input)
			test.edit(t, root)
			got := graphWitness(t, buildWitnessManifest(t, root), test.input)
			if (got == base) != test.same {
				t.Fatalf("digest same = %v, want %v", got == base, test.same)
			}
		})
	}
}

func TestGraphWitnessIsOrderAndCheckoutIndependent(t *testing.T) {
	input := GraphWitnessInput{Dirs: []string{"b", "a", "c"}, EmbedFiles: []string{"a/data.txt"}}
	first := graphWitness(t, buildWitnessManifest(t, writeWitnessFixture(t)), input)
	reordered := GraphWitnessInput{Dirs: []string{"c", "a", "b"}, EmbedFiles: input.EmbedFiles}
	if second := graphWitness(t, buildWitnessManifest(t, writeWitnessFixture(t)), reordered); second != first {
		t.Fatalf("digest differs across checkouts or Dirs order: %s vs %s", first, second)
	}
}

func TestDiskGraphWitnessMatchesManifest(t *testing.T) {
	input := GraphWitnessInput{Dirs: []string{"a", "b", "c", "nested/n", "testdata/x"}, EmbedFiles: []string{"a/data.txt", "a/missing.txt"}}
	root := writeWitnessFixture(t)
	manifest := buildWitnessManifest(t, root)
	want := graphWitness(t, manifest, input)
	got, err := manifest.DiskGraphWitness(input)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DiskGraphWitness = %s, want GraphWitness %s on an unchanged tree", got, want)
	}

	// A regenerated paired output is witnessed by its overlay bytes on both sides.
	writeTestFile(t, root, "a/page.x.go", "package a\n\nimport \"fmt\"\n")
	writeTestFile(t, root, "a/x.go", "package a\n\nimport \"strings\"\n\nfunc X() string { return strings.Repeat(\"x\", 2) }\n")
	if got, err := manifest.DiskGraphWitness(input); err != nil || got != want {
		t.Fatalf("after overlay/body edits DiskGraphWitness = %s, %v; want %s", got, err, want)
	}

	writeTestFile(t, root, "b/b.go", "package b\n\nimport (\n\t\"fmt\"\n\t\"example.com/app/a\"\n)\n\nvar _ = a.X\nvar _ = fmt.Sprint\n")
	got, err = manifest.DiskGraphWitness(input)
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("DiskGraphWitness unchanged after a header edit on disk")
	}
	if again := graphWitness(t, manifest, input); again != want {
		t.Fatal("GraphWitness read disk: it changed after a disk edit")
	}
}

func TestDiskGraphWitnessSeesDiskStructure(t *testing.T) {
	tests := []struct {
		name  string
		input GraphWitnessInput
		edit  func(t *testing.T, root string)
	}{
		{name: "file added", input: GraphWitnessInput{Dirs: []string{"a"}}, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/x.s", "\n")
		}},
		{name: "dir removed", input: GraphWitnessInput{Dirs: []string{"b"}}, edit: func(t *testing.T, root string) {
			if err := os.RemoveAll(filepath.Join(root, "b")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "go.mod added", input: GraphWitnessInput{Dirs: []string{"a"}}, edit: func(t *testing.T, root string) {
			writeTestFile(t, root, "a/go.mod", "module example.com/app/a\n")
		}},
		{name: "nested boundary removed", input: GraphWitnessInput{Dirs: []string{"nested/n"}}, edit: func(t *testing.T, root string) {
			removeTestFile(t, root, "nested/go.mod")
		}},
		{name: "embed target deleted", input: GraphWitnessInput{Dirs: []string{"a"}, EmbedFiles: []string{"a/data.txt"}}, edit: func(t *testing.T, root string) {
			removeTestFile(t, root, "a/data.txt")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeWitnessFixture(t)
			manifest := buildWitnessManifest(t, root)
			want := graphWitness(t, manifest, test.input)
			test.edit(t, root)
			got, err := manifest.DiskGraphWitness(test.input)
			if err == nil && got == want {
				t.Fatal("DiskGraphWitness equals the manifest digest after a disk edit")
			}
		})
	}
}

func TestGraphWitnessUnavailable(t *testing.T) {
	tests := []struct {
		name     string
		input    GraphWitnessInput
		manifest func(t *testing.T, root string) *Manifest
		// diskReadable rows change the disk after the manifest: a fresh disk
		// read sees a consistent tree, and only the manifest is unavailable.
		diskReadable bool
	}{
		{name: "unimportable dir", input: GraphWitnessInput{Dirs: []string{".hidden/h"}}},
		{name: "vendor dir", input: GraphWitnessInput{Dirs: []string{"vendor/example.com/v"}}, manifest: func(t *testing.T, root string) *Manifest {
			writeTestFile(t, root, "vendor/example.com/v/v.go", "package v\n")
			return buildWitnessManifest(t, root)
		}},
		{name: "dir created after build", input: GraphWitnessInput{Dirs: []string{"c"}}, diskReadable: true, manifest: func(t *testing.T, root string) *Manifest {
			manifest := buildWitnessManifest(t, root)
			writeTestFile(t, root, "c/c.go", "package c\n")
			return manifest
		}},
		{name: "subdir created after refresh", input: GraphWitnessInput{Dirs: []string{"a/sub"}}, diskReadable: true, manifest: func(t *testing.T, root string) *Manifest {
			manifest := buildWitnessManifest(t, root)
			writeTestFile(t, root, "a/sub/s.go", "package sub\n")
			refreshed, err := manifest.RefreshDirs([]string{filepath.Join(root, "a")})
			if err != nil {
				t.Fatal(err)
			}
			return refreshed
		}},
		{name: "build overrides", input: GraphWitnessInput{Dirs: []string{"a"}}, manifest: func(t *testing.T, root string) *Manifest {
			manifest, err := Build(BuildOptions{ModuleRoot: root, ModulePath: "example.com/app", Overrides: map[string][]byte{
				filepath.Join(root, "a", "page.gsx"): []byte("package a\ncomponent Page() { <p>edited</p> }\n"),
			}})
			if err != nil {
				t.Fatal(err)
			}
			return manifest
		}},
		{name: "derived overrides", input: GraphWitnessInput{Dirs: []string{"a"}}, manifest: func(t *testing.T, root string) *Manifest {
			manifest, err := buildWitnessManifest(t, root).WithOverrides(map[string][]byte{
				filepath.Join(root, "a", "x.go"): []byte("package a\n\nimport \"os\"\n"),
			})
			if err != nil {
				t.Fatal(err)
			}
			return manifest
		}},
		{name: "unreadable go file", input: GraphWitnessInput{Dirs: []string{"a"}}, manifest: func(t *testing.T, root string) *Manifest {
			path := filepath.Join(root, "a", "x.go")
			if err := os.Chmod(path, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
			return buildWitnessManifest(t, root)
		}},
		{name: "escaping dir", input: GraphWitnessInput{Dirs: []string{"../a"}}},
		{name: "unclean dir", input: GraphWitnessInput{Dirs: []string{"a/../b"}}},
		{name: "absolute dir", input: GraphWitnessInput{Dirs: []string{"/a"}}},
		{name: "escaping embed file", input: GraphWitnessInput{Dirs: []string{"a"}, EmbedFiles: []string{"../data.txt"}}},
		{name: "refreshed listing of skipped dir", input: GraphWitnessInput{Dirs: []string{"a"}}, manifest: func(t *testing.T, root string) *Manifest {
			manifest := buildWitnessManifest(t, root)
			manifest.dirListings["nested"] = freezeListing("nested", []string{"go.mod"})
			return manifest
		}},
		{name: "listing outside the root", input: GraphWitnessInput{Dirs: []string{"a"}}, manifest: func(t *testing.T, root string) *Manifest {
			manifest := buildWitnessManifest(t, root)
			manifest.dirListings["../elsewhere/a"] = freezeListing("../elsewhere/a", []string{"x.go"})
			return manifest
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeWitnessFixture(t)
			var manifest *Manifest
			if test.manifest != nil {
				manifest = test.manifest(t, root)
			} else {
				manifest = buildWitnessManifest(t, root)
			}
			if _, err := manifest.GraphWitness(test.input); !errors.Is(err, ErrWitnessUnavailable) {
				t.Fatalf("GraphWitness err = %v, want ErrWitnessUnavailable", err)
			}
			if test.diskReadable {
				return
			}
			if _, err := manifest.DiskGraphWitness(test.input); !errors.Is(err, ErrWitnessUnavailable) {
				t.Fatalf("DiskGraphWitness err = %v, want ErrWitnessUnavailable", err)
			}
		})
	}
}

func removeTestFile(t *testing.T, root, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}
