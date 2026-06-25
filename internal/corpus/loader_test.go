package corpus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCaseSinglePackage(t *testing.T) {
	c, err := loadCase("testdata/loadertest/single.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if c.name != "loadertest/single" {
		t.Errorf("name = %q, want loadertest/single", c.name)
	}
	if c.dir != "loadertest_single" {
		t.Errorf("dir = %q, want loadertest_single", c.dir)
	}
	if c.multiPkg {
		t.Errorf("multiPkg = true, want false")
	}
	if string(c.invoke) != "Greeting(GreetingProps{Name: \"X\"})\n" {
		t.Errorf("invoke = %q", c.invoke)
	}
	if _, ok := c.files["input.gsx"]; !ok {
		t.Errorf("missing input.gsx in files")
	}
	if _, ok := c.goldens["render.golden"]; !ok {
		t.Errorf("missing render.golden in goldens")
	}
	if !c.renderable() {
		t.Errorf("renderable() = false, want true")
	}
}

func TestLoadCaseMultiPackage(t *testing.T) {
	c, err := loadCase("testdata/loadertest/multi.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if !c.multiPkg {
		t.Errorf("multiPkg = false, want true")
	}
	if c.modulePath != "example.com/app" {
		t.Errorf("modulePath = %q, want example.com/app", c.modulePath)
	}
	if _, ok := c.files["ui/button.gsx"]; !ok {
		t.Errorf("missing ui/button.gsx")
	}
}

func TestLoadCaseDocSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ex.txtar")
	src := "-- doc --\nname: Demo\norder: 5\n-- input.gsx --\npackage views\n\ncomponent A() { <p>x</p> }\n-- invoke --\nA(AProps{})\n-- render.golden --\n<p>x</p>\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, isFile := c.files["doc"]; isFile {
		t.Fatal("doc must not be a source file")
	}
	if m := parseDocMeta(c.doc); m.Name != "Demo" || m.Order != 5 {
		t.Fatalf("doc meta = %+v", m)
	}
}
