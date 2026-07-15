package gen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/codegen"
)

// ctxFilterSrc is a seed-first, ctx-injecting, error-returning filter package
// body, parameterized by the module path so the alias resolves.
const ctxFilterSrc = `package myfilters

import "context"

type ctxKey struct{}

func WithGreeting(ctx context.Context, g string) context.Context {
	return context.WithValue(ctx, ctxKey{}, g)
}

// F is a seed-first, ctx-injecting, error-returning filter.
func F(ctx context.Context, v string, k string) (string, error) {
	g, _ := ctx.Value(ctxKey{}).(string)
	return g + ":" + v + ":" + k, nil
}
`

const ctxViewsSrc = `package views

component C(s string) { <p>{ s |> f("k") }</p> }
`

// TestStockGenerateHonorsConfigAcrossModuleBoundary is the end-to-end proof that
// the STOCK run path (no programmatic opts) honors a repo-root gsx.toml found by
// walking up ACROSS a go.mod module boundary, and lowers the ctx-injecting alias.
func TestStockGenerateHonorsConfigAcrossModuleBoundary(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-run e2e in -short mode")
	}
	tmp := t.TempDir()
	// .git at the repo root bounds the discovery walk.
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Repo-root gsx.toml aliasing the ctx-injecting filter. NOTE: it lives ABOVE
	// the sub-module's go.mod — discovery must cross that boundary to find it.
	mkfile(t, filepath.Join(tmp, "gsx.toml"), "[filters]\nf = \"gsxsub/myfilters.F\"\n")

	sub := filepath.Join(tmp, "sub")
	mkfile(t, filepath.Join(sub, "go.mod"), "module gsxsub\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	mkfile(t, filepath.Join(sub, "myfilters", "myfilters.go"), ctxFilterSrc)
	mkfile(t, filepath.Join(sub, "views", "views.gsx"), ctxViewsSrc)

	// Stock path: -C into the sub module, generate ./views. No opts.
	var out, errb bytes.Buffer
	code := run([]string{"-C", sub, "generate", "./views"}, &out, &errb)
	if code != 0 {
		t.Fatalf("run generate exit=%d stderr=%q stdout=%q", code, errb.String(), out.String())
	}

	gen, err := os.ReadFile(filepath.Join(sub, "views", "views.x.go"))
	if err != nil {
		t.Fatalf("read generated: %v", err)
	}
	if !strings.Contains(string(gen), ".F(ctx, (s), \"k\")") {
		t.Fatalf("generated .x.go missing ctx-injected lowered call .F(ctx, (s), \"k\"); got:\n%s", gen)
	}

	// Prove it renders through the injected ctx.
	mkfile(t, filepath.Join(sub, "main.go"), `package main

import (
	"context"
	"os"

	"github.com/gsxhq/gsx"
	mf "gsxsub/myfilters"
	p "gsxsub/views"
)

var _ = gsx.Raw

func main() {
	ctx := mf.WithGreeting(context.Background(), "hi")
	_ = p.C(p.CProps{S: "v"}).Render(ctx, os.Stdout)
}
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = sub
	rout, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, rout)
	}
	if !strings.Contains(string(rout), "hi:v:k") {
		t.Fatalf("expected rendered \"hi:v:k\"; got:\n%s", rout)
	}
}

// TestMainMergeConfigAndOpts proves gen.Main-style merge: a gsx.toml alias AND a
// programmatic opt alias both apply, and an opt overrides a same-named config
// alias (opt wins, last-wins). Driven through runConfig (Main without os.Exit).
func TestMainMergeConfigAndOpts(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping go-run e2e in -short mode")
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(tmp, "go.mod"), "module gsxmerge\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	// Config aliases both "shout" and "f"; the opt below overrides "shout".
	mkfile(t, filepath.Join(tmp, "gsx.toml"), "[filters]\nshout = \"gsxmerge/myfilters.ShoutConfig\"\nf = \"gsxmerge/myfilters.F\"\n")
	mkfile(t, filepath.Join(tmp, "myfilters", "myfilters.go"), `package myfilters

func ShoutConfig(s string) string { return "CONFIG:" + s }
func ShoutOpt(s string) string    { return "OPT:" + s }
func F(s string) string           { return "F:" + s }
`)
	mkfile(t, filepath.Join(tmp, "views", "views.gsx"), `package views

component C(s string) { <p>{ s |> shout } { s |> f }</p> }
`)

	// Opt alias overrides "shout"; config alias "f" still applies. Built directly
	// (as gen.Main would, post-reflection) because the opt target lives in the
	// temp module, which the gen package cannot reflect a func value into.
	cfg := config{aliases: []codegen.FilterAlias{{Name: "shout", PkgPath: "gsxmerge/myfilters", FuncName: "ShoutOpt"}}}
	var out, errb bytes.Buffer
	code := runConfig([]string{"-C", tmp, "generate", "./views"}, &out, &errb, cfg)
	if code != 0 {
		t.Fatalf("runConfig exit=%d stderr=%q", code, errb.String())
	}
	gen, err := os.ReadFile(filepath.Join(tmp, "views", "views.x.go"))
	if err != nil {
		t.Fatalf("read generated: %v", err)
	}
	src := string(gen)
	// Opt won for "shout" (ShoutOpt), config supplied "f" (F).
	if !strings.Contains(src, "ShoutOpt") {
		t.Fatalf("expected opt alias ShoutOpt to win over config ShoutConfig; got:\n%s", src)
	}
	if strings.Contains(src, "ShoutConfig") {
		t.Fatalf("config ShoutConfig should be overridden by opt; got:\n%s", src)
	}
	if !strings.Contains(src, ".F(") {
		t.Fatalf("expected config alias f→F to apply; got:\n%s", src)
	}
}

// TestConfigChangeBustsCache proves editing an alias in gsx.toml busts the cache
// (regenerates), while an unrelated edit elsewhere does not regenerate views.
func TestConfigChangeBustsCache(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-run e2e in -short mode")
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(tmp, "go.mod"), "module gsxcache\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	mkfile(t, filepath.Join(tmp, "myfilters", "myfilters.go"), `package myfilters

func A(s string) string { return "A:" + s }
func B(s string) string { return "B:" + s }
`)
	mkfile(t, filepath.Join(tmp, "views", "views.gsx"), `package views

component C(s string) { <p>{ s |> shout }</p> }
`)
	cfgPath := filepath.Join(tmp, "gsx.toml")
	mkfile(t, cfgPath, "[filters]\nshout = \"gsxcache/myfilters.A\"\n")
	t.Setenv("GSXCACHE", t.TempDir())

	gen := func() (int, string) {
		var out, errb bytes.Buffer
		code := run([]string{"-C", tmp, "generate", "./views"}, &out, &errb)
		return code, out.String() + errb.String()
	}

	// cold: writes
	if code, o := gen(); code != 0 || !strings.Contains(o, "wrote") {
		t.Fatalf("cold gen: code=%d out=%q", code, o)
	}
	// warm no-op: nothing written
	if code, o := gen(); code != 0 || strings.Contains(o, "wrote") {
		t.Fatalf("warm gen should be no-op; code=%d out=%q", code, o)
	}
	// change the alias target in gsx.toml → cache busted → regenerates
	mkfile(t, cfgPath, "[filters]\nshout = \"gsxcache/myfilters.B\"\n")
	if code, o := gen(); code != 0 || !strings.Contains(o, "wrote") {
		t.Fatalf("after config change, expected regen; code=%d out=%q", code, o)
	}
	gensrc, _ := os.ReadFile(filepath.Join(tmp, "views", "views.x.go"))
	if !strings.Contains(string(gensrc), ".B(") {
		t.Fatalf("expected new alias B in regenerated output; got:\n%s", gensrc)
	}
}

// TestInfoPrintsConfigPathAndAliases proves `gsx info` prints the discovered
// gsx.toml path and the resolved alias (name → pkg.Func).
func TestInfoPrintsConfigPathAndAliases(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-load info test in -short mode")
	}
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(tmp, "go.mod"), "module gsxinfo\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	mkfile(t, filepath.Join(tmp, "myfilters", "myfilters.go"), "package myfilters\n\nfunc Shout(s string) string { return s + \"!\" }\n")
	cfgPath := filepath.Join(tmp, "gsx.toml")
	mkfile(t, cfgPath, "[filters]\nshout = \"gsxinfo/myfilters.Shout\"\n")

	var out, errb bytes.Buffer
	code := run([]string{"-C", tmp, "info"}, &out, &errb)
	if code != 0 {
		t.Fatalf("info exit=%d stderr=%q", code, errb.String())
	}
	got := out.String()
	// -C chdir + Getwd resolves symlinks (macOS /var → /private/var), so compare
	// against the symlink-resolved config path.
	resolved := cfgPath
	if r, err := filepath.EvalSymlinks(cfgPath); err == nil {
		resolved = r
	}
	if !strings.Contains(got, "config: "+resolved) && !strings.Contains(got, "config: "+cfgPath) {
		t.Fatalf("info should print discovered config path %q; got:\n%s", cfgPath, got)
	}
	if !strings.Contains(got, "shout") || !strings.Contains(got, "Shout") {
		t.Fatalf("info should print resolved alias shout→Shout; got:\n%s", got)
	}
}

// TestInfoNoConfig proves `gsx info` prints "config: none" when no gsx.toml.
func TestInfoNoConfig(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-load info test in -short mode")
	}
	tmp := t.TempDir()
	mkfile(t, filepath.Join(tmp, "go.mod"), "module gsxnone\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")
	mkfile(t, filepath.Join(tmp, "views", "views.gsx"), hiComponent)
	var out, errb bytes.Buffer
	code := run([]string{"-C", tmp, "info"}, &out, &errb)
	if code != 0 {
		t.Fatalf("info exit=%d stderr=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "config: none") {
		t.Fatalf("info should print 'config: none'; got:\n%s", out.String())
	}
}

// TestGenClassMergerE2E is the end-to-end proof that:
//  1. class_merger = "pkg.Func" in gsx.toml wires through generation and the
//     generated .x.go imports the package under _gsxcm and calls _gsxcm.Func.
//  2. A bad-signature merger (variadic) surfaces the generate-time validation
//     error from ValidateClassMerger (wired in GenerateDirs).
func TestGenClassMergerE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build e2e in -short mode")
	}
	t.Setenv("GSXCACHE", t.TempDir())
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mod := "gsxcme2e"
	mkfile(t, filepath.Join(tmp, "go.mod"),
		"module "+mod+"\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot(t)+"\n")

	// Merger package: func Merge(t []string) string — correct signature.
	mkfile(t, filepath.Join(tmp, "mrg", "mrg.go"),
		"package mrg\n\nfunc Merge(t []string) string { return \"\" }\n")

	// gsx.toml pointing at the merger.
	mkfile(t, filepath.Join(tmp, "gsx.toml"),
		"class_merger = \""+mod+"/mrg.Merge\"\n")

	// Component with an explicit attrs spread that triggers a class merge site.
	mkfile(t, filepath.Join(tmp, "views", "card.gsx"),
		"package views\n\ncomponent Card() {\n\t<section class=\"card\" { attrs... }>{children}</section>\n}\n")

	// Run stock generation (reads gsx.toml automatically).
	var out, errb bytes.Buffer
	code := run([]string{"-C", tmp, "generate", "./views"}, &out, &errb)
	if code != 0 {
		t.Fatalf("generate exit=%d stderr=%q stdout=%q", code, errb.String(), out.String())
	}

	gen, err := os.ReadFile(filepath.Join(tmp, "views", "card.x.go"))
	if err != nil {
		t.Fatalf("read generated: %v", err)
	}
	src := string(gen)
	if !strings.Contains(src, `_gsxcm "`+mod+`/mrg"`) {
		t.Fatalf("generated .x.go missing aliased import _gsxcm %q/mrg; got:\n%s", mod, src)
	}
	if !strings.Contains(src, "_gsxgw.Class(_gsxcm.Merge,") {
		t.Fatalf("generated .x.go missing direct merger reference _gsxgw.Class(_gsxcm.Merge,...); got:\n%s", src)
	}

	// Keep the config and generated package unchanged, but break the selected
	// merger's source. A stale persistent hit must not bypass authoritative
	// configured-source validation.
	mkfile(t, filepath.Join(tmp, "mrg", "mrg.go"),
		"package mrg\n\nfunc Merge(t ...any) string { return \"\" }\n")
	var changedOut, changedErr bytes.Buffer
	changedCode := run([]string{"-C", tmp, "generate", "./views"}, &changedOut, &changedErr)
	if changedCode == 0 || !strings.Contains(changedErr.String(), "func([]string) string") {
		t.Fatalf("changed merger source reused stale cache: exit=%d stdout=%q stderr=%q", changedCode, changedOut.String(), changedErr.String())
	}

	// Bad-signature variant: a variadic merger must fail validation.
	mkfile(t, filepath.Join(tmp, "bad", "bad.go"),
		"package bad\n\nfunc BadMerge(t ...any) string { return \"\" }\n")
	mkfile(t, filepath.Join(tmp, "gsx.toml"),
		"class_merger = \""+mod+"/bad.BadMerge\"\n")

	var out2, errb2 bytes.Buffer
	code2 := run([]string{"-C", tmp, "generate", "./views"}, &out2, &errb2)
	if code2 == 0 {
		t.Fatalf("expected non-zero exit for bad-signature merger, got 0; stdout=%q", out2.String())
	}
	if !strings.Contains(errb2.String(), "func([]string) string") {
		t.Fatalf("expected signature-error hint in stderr; got: %q", errb2.String())
	}
}
