package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestFilterTableFromExtMatchesGoList is the faithfulness harness for replacing
// the filter table's packages.Load with a harvest from the external importer's
// already-loaded types. The two must agree EXACTLY — same names, same winning
// package, same ctx/err classification, and critically the same `_gsxf<i>` import
// alias, since that alias is emitted into every .x.go.
//
// It exercises the properties that make the two paths capable of diverging:
// last-wins precedence across packages, ctx-taking filters, (T, error) filters,
// generics, and explicit WithFilter aliases pointing at a package that appears
// nowhere else.
func TestFilterTableFromExtMatchesGoList(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	repoRoot = filepath.Dir(repoRoot)
	must := func(p, c string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	// `shout` is defined in BOTH a and b: last-wins must pick b.
	must("a/a.go", "package a\n\nfunc Shout(s string) string { return s }\nfunc Only(s string) string { return s }\n")
	must("b/b.go", "package b\n\nimport \"context\"\n\n"+
		"func Shout(s string) string { return s }\n"+
		"func Ctxy(ctx context.Context, s string) (string, error) { return s, nil }\n"+
		"func Gen[T any](v []T) (T, error) { var z T; return z, nil }\n")
	// aliasonly is referenced ONLY through an explicit WithFilter alias.
	must("aliasonly/c.go", "package aliasonly\n\nfunc URLFor(s string) string { return s }\n")
	must("views/v.gsx", "package views\n\ncomponent V(s string) {\n\t<p>{ s |> shout }</p>\n}\n")

	filterPkgs := []string{stdImportPath, "example.com/x/a", "example.com/x/b"}
	aliases := []FilterAlias{{Name: "url", PkgPath: "example.com/x/aliasonly", FuncName: "URLFor"}}

	// Path 1: the standalone packages.Load harvest (the old behavior).
	viaGoList, err := loadFilterTableMulti(root, dedupFilterPkgs(filterPkgs), aliases)
	if err != nil {
		t.Fatalf("loadFilterTableMulti: %v", err)
	}

	// Path 2: harvested from the external importer's loaded types.
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/x", FilterPkgs: filterPkgs, Aliases: aliases})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.externalImporter(); err != nil {
		t.Fatalf("externalImporter: %v", err)
	}
	viaTypes, err := m.filterTableFromExt(dedupFilterPkgs(filterPkgs))
	if err != nil {
		t.Fatalf("filterTableFromExt: %v", err)
	}

	if !reflect.DeepEqual(viaGoList, viaTypes) {
		t.Errorf("filter tables diverge\n--- go list ---\n%s\n--- from types ---\n%s",
			dumpTable(viaGoList), dumpTable(viaTypes))
	}

	// Sanity: the harness would be vacuous if the table were empty or if last-wins
	// / aliases were not actually exercised.
	if len(viaTypes) == 0 {
		t.Fatal("empty table: harness proves nothing")
	}
	if e, ok := viaTypes["shout"]; !ok || e.pkgPath != "example.com/x/b" {
		t.Errorf("last-wins not exercised: shout = %+v", e)
	}
	if e, ok := viaTypes["url"]; !ok || e.pkgPath != "example.com/x/aliasonly" {
		t.Errorf("explicit alias not exercised: url = %+v", e)
	}
	if e, ok := viaTypes["ctxy"]; !ok || !e.wantsCtx || !e.hasErr {
		t.Errorf("ctx/err classification not exercised: ctxy = %+v", e)
	}
}

// TestFilterHarvestErrorsMatch pins the failure modes, not just the happy path.
// The two harvests silently disagreed here: only the go-list copy recognized the
// removed curried shape, so the SAME bad WithFilter produced a migration hint or
// an unhelpful "does not match the contract" depending on which path ran. Now
// both delegate to harvestFromTypes, so this asserts identical error text.
func TestFilterHarvestErrorsMatch(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	cases := []struct {
		name, src string
		alias     FilterAlias
		want      string
	}{
		{
			name:  "curried",
			src:   "package bad\n\nfunc Old(n int) func(string) string { return func(s string) string { return s } }\n",
			alias: FilterAlias{Name: "old", PkgPath: "example.com/x/bad", FuncName: "Old"},
			want:  "removed curried shape",
		},
		{
			name:  "not_a_func",
			src:   "package bad\n\nvar Old = 3\n",
			alias: FilterAlias{Name: "old", PkgPath: "example.com/x/bad", FuncName: "Old"},
			want:  "is not a function",
		},
		{
			name:  "missing_func",
			src:   "package bad\n\nfunc Other(s string) string { return s }\n",
			alias: FilterAlias{Name: "old", PkgPath: "example.com/x/bad", FuncName: "Old"},
			want:  "not found in package",
		},
		{
			name:  "nonconforming",
			src:   "package bad\n\nfunc Old() {}\n",
			alias: FilterAlias{Name: "old", PkgPath: "example.com/x/bad", FuncName: "Old"},
			want:  "seed-first filter contract",
		},
		{
			// A broken ALIAS package must be reported against the WithFilter that
			// named it — nothing else in the config mentions the package, so
			// "filter package X failed" leaves the author no thread to pull.
			name:  "broken_alias_pkg",
			src:   "package bad\n\nfunc Old(s string) string { return undefinedIdent(s) }\n",
			alias: FilterAlias{Name: "old", PkgPath: "example.com/x/bad", FuncName: "Old"},
			want:  `WithFilter "old": package "example.com/x/bad" type resolution failed`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			repoRoot, _ := filepath.Abs("..")
			repoRoot = filepath.Dir(repoRoot)
			must := func(p, c string) {
				full := filepath.Join(root, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
			must("bad/bad.go", tc.src)

			filterPkgs := []string{stdImportPath}
			aliases := []FilterAlias{tc.alias}

			_, goListErr := loadFilterTableMulti(root, dedupFilterPkgs(filterPkgs), aliases)

			m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/x", FilterPkgs: filterPkgs, Aliases: aliases})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.externalImporter(); err != nil {
				t.Fatalf("externalImporter: %v", err)
			}
			_, typesErr := m.filterTableFromExt(dedupFilterPkgs(filterPkgs))

			if goListErr == nil || typesErr == nil {
				t.Fatalf("expected both paths to error; goList=%v types=%v", goListErr, typesErr)
			}
			if goListErr.Error() != typesErr.Error() {
				t.Errorf("error text diverges\n  go list: %v\nfrom types: %v", goListErr, typesErr)
			}
			if !strings.Contains(typesErr.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", typesErr, tc.want)
			}
		})
	}
}

func dumpTable(tbl filterTable) string {
	names := make([]string, 0, len(tbl))
	for n := range tbl {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		e := tbl[n]
		fmt.Fprintf(&sb, "%s -> %s.%s alias=%s ctx=%v err=%v\n", n, e.pkgPath, e.funcName, e.alias, e.wantsCtx, e.hasErr)
	}
	return sb.String()
}

// TestFilterTableFromExtRejectsBrokenPkg: packages.Load hands back PARTIAL Types
// for a package that fails to type-check, so a from-types harvest would silently
// yield a thinner table and the pipe would fail later as "unknown filter". The
// go-list path errors; the types path must too.
func TestFilterTableFromExtRejectsBrokenPkg(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	root := t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	repoRoot = filepath.Dir(repoRoot)
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/x\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("broken/b.go", "package broken\n\nfunc Shout(s string) string { return undefinedIdent(s) }\n")
	must("views/v.gsx", "package views\n\ncomponent V(s string) {\n\t<p>{ s |> shout }</p>\n}\n")

	filterPkgs := []string{stdImportPath, "example.com/x/broken"}
	m, err := Open(Options{ModuleRoot: root, ModulePath: "example.com/x", FilterPkgs: filterPkgs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.externalImporter(); err != nil {
		t.Fatalf("externalImporter: %v", err)
	}
	if _, err := m.filterTableFromExt(dedupFilterPkgs(filterPkgs)); err == nil {
		t.Fatal("a broken filter package yielded a table instead of an error")
	}
}
