package codegen

import "testing"

func TestImportPathDirRoundTrip(t *testing.T) {
	root := "/m"
	mod := "example.com/app"
	// dir under root → import path
	if got, ok := importPathForDir(root, mod, "/m/ui/admin"); !ok || got != "example.com/app/ui/admin" {
		t.Fatalf("importPathForDir = %q,%v; want example.com/app/ui/admin,true", got, ok)
	}
	// module root dir → bare module path
	if got, ok := importPathForDir(root, mod, "/m"); !ok || got != "example.com/app" {
		t.Fatalf("root dir: got %q,%v", got, ok)
	}
	// dir outside the module → not ok
	if _, ok := importPathForDir(root, mod, "/other/x"); ok {
		t.Fatalf("outside dir should be !ok")
	}
	// inverse
	if got, ok := dirForImportPath(root, mod, "example.com/app/ui/admin"); !ok || got != "/m/ui/admin" {
		t.Fatalf("dirForImportPath = %q,%v; want /m/ui/admin,true", got, ok)
	}
	if _, ok := dirForImportPath(root, mod, "fmt"); ok {
		t.Fatalf("stdlib path should be !ok")
	}
}
