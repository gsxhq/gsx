// Command gsx-typebundle produces a self-describing type archive for the
// browser playground. Its context query and packages.Load share one immutable
// Go command configuration, so the archived provenance is the source-selection
// context that actually produced the exported packages.
package main

import (
	"flag"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/internal/codegen"
	"github.com/gsxhq/gsx/internal/golauncher"
	"github.com/gsxhq/gsx/internal/typebundle"
)

type producerConfig struct {
	launcher   *golauncher.Launcher
	dir        string
	env        []string
	buildFlags []string
}

type targetRequest struct {
	compiler        string
	goos            string
	goarch          string
	languageVersion string
	cgoEnabled      bool
	tags            string
}

func main() {
	out := flag.String("o", "playground.typebundle", "output bundle path")
	compiler := flag.String("compiler", "", "required target Go compiler (for example gc)")
	goos := flag.String("goos", "", "required target GOOS")
	goarch := flag.String("goarch", "", "required target GOARCH")
	languageVersion := flag.String("language-version", "", "required snippet language version (for example go1.26.1)")
	cgo := flag.String("cgo", "", "required target CGO_ENABLED value (0 or 1)")
	tags := flag.String("tags", "", "explicit comma-separated build tags")
	flag.Parse()
	if *compiler == "" || *goos == "" || *goarch == "" || *languageVersion == "" || *cgo == "" {
		fatal("-compiler, -goos, -goarch, -language-version, and -cgo are required")
	}
	if *cgo != "0" && *cgo != "1" {
		fatal("-cgo must be 0 or 1, got %q", *cgo)
	}
	request := targetRequest{
		compiler:        *compiler,
		goos:            *goos,
		goarch:          *goarch,
		languageVersion: *languageVersion,
		cgoEnabled:      *cgo == "1",
		tags:            *tags,
	}

	config, err := newProducerConfig(request)
	if err != nil {
		fatal("configure producer: %v", err)
	}
	target, err := config.queryTarget(request.languageVersion)
	if err != nil {
		fatal("query target: %v", err)
	}
	if err := verifyObservedTarget(target, request); err != nil {
		fatal("target mismatch: %v", err)
	}
	if _, err := target.Sizes(); err != nil {
		fatal("target: %v", err)
	}

	imports := []string{"github.com/gsxhq/gsx", codegen.StdImportPath}
	imports = append(imports, gen.DefaultPlaygroundImports...)
	fset := token.NewFileSet()
	loadConfig := &packages.Config{
		Mode:       packages.NeedName | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedImports | packages.NeedDeps,
		Fset:       fset,
		Dir:        config.dir,
		Env:        append([]string(nil), config.env...),
		BuildFlags: append([]string(nil), config.buildFlags...),
	}
	if err := config.verifyGoCommand(); err != nil {
		fatal("Go command changed before package load: %v", err)
	}
	pkgs, err := packages.Load(loadConfig, imports...)
	if err != nil {
		fatal("load import set: %v", err)
	}
	if err := config.verifyGoCommand(); err != nil {
		fatal("Go command changed during package load: %v", err)
	}
	closure := map[string]*types.Package{}
	var hadErr bool
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, loadErr := range p.Errors {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p.PkgPath, loadErr)
			hadErr = true
		}
		if p.Types != nil {
			closure[p.PkgPath] = p.Types
		}
		if p.TypesSizes == nil {
			fmt.Fprintf(os.Stderr, "%s: target type sizes are missing\n", p.PkgPath)
			hadErr = true
		}
	})
	if hadErr {
		fatal("type errors or incomplete target metadata while loading the playground import set")
	}
	list := make([]*types.Package, 0, len(closure))
	for _, p := range closure {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path() < list[j].Path() })
	data, err := typebundle.Write(fset, target, list)
	if err != nil {
		fatal("write bundle: %v", err)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fatal("write file: %v", err)
	}
	fmt.Fprintf(os.Stderr, "gsx-typebundle: wrote %s (%d packages, %d bytes)\n", *out, len(list), len(data))
}

func newProducerConfig(request targetRequest) (producerConfig, error) {
	if request.compiler != "gc" {
		return producerConfig{}, fmt.Errorf("compiler %q is unsupported; only gc type bundles are produced", request.compiler)
	}
	driver, _ := os.LookupEnv("GOPACKAGESDRIVER")
	switch {
	case driver == "off":
	case driver != "":
		return producerConfig{}, fmt.Errorf("external GOPACKAGESDRIVER %q is incompatible with exact target capture", driver)
	default:
		if path, err := exec.LookPath("gopackagesdriver"); err == nil {
			return producerConfig{}, fmt.Errorf("PATH-discovered gopackagesdriver %q is incompatible with exact target capture", path)
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return producerConfig{}, fmt.Errorf("resolve producer directory: %w", err)
	}
	env := make([]string, 0, len(os.Environ())+12)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "GO") {
			env = append(env, entry)
		}
	}
	cgo := "0"
	if request.cgoEnabled {
		cgo = "1"
	}
	env = append(env,
		"GOENV=off",
		"GOWORK=off",
		"GOTOOLCHAIN=local",
		"GO111MODULE=on",
		"GOFLAGS=",
		"GOEXPERIMENT=",
		"GOPACKAGESDRIVER=off",
		"GOOS="+request.goos,
		"GOARCH="+request.goarch,
		"CGO_ENABLED="+cgo,
	)
	buildFlags := []string{"-compiler=" + request.compiler}
	if request.tags != "" {
		buildFlags = append(buildFlags, "-tags="+request.tags)
	}
	snapshot, err := golauncher.SnapshotLive()
	if err != nil {
		return producerConfig{}, err
	}
	launcher, err := snapshot.Seal(dir, env)
	if err != nil {
		return producerConfig{}, err
	}
	return producerConfig{launcher: launcher, dir: dir, env: env, buildFlags: buildFlags}, nil
}

const contextTemplate = `{{context.GOOS}}
{{context.GOARCH}}
{{context.Compiler}}
{{context.CgoEnabled}}
{{context.BuildTags}}
{{context.ToolTags}}
{{context.ReleaseTags}}`

func (config producerConfig) queryTarget(languageVersion string) (typebundle.Target, error) {
	toolchainBytes, err := config.runGo("env", "GOVERSION")
	if err != nil {
		return typebundle.Target{}, err
	}
	args := append([]string{"list"}, config.buildFlags...)
	args = append(args, "-f", contextTemplate, "--", "unsafe")
	contextBytes, err := config.runGo(args...)
	if err != nil {
		return typebundle.Target{}, err
	}
	lines := strings.Split(strings.TrimSuffix(string(contextBytes), "\n"), "\n")
	if len(lines) != 7 {
		return typebundle.Target{}, fmt.Errorf("unexpected go list context output with %d lines", len(lines))
	}
	cgoEnabled, err := strconv.ParseBool(lines[3])
	if err != nil {
		return typebundle.Target{}, fmt.Errorf("parse context CgoEnabled %q: %w", lines[3], err)
	}
	buildTags, err := parseContextTags(lines[4])
	if err != nil {
		return typebundle.Target{}, fmt.Errorf("parse context BuildTags: %w", err)
	}
	toolTags, err := parseContextTags(lines[5])
	if err != nil {
		return typebundle.Target{}, fmt.Errorf("parse context ToolTags: %w", err)
	}
	releaseTags, err := parseContextTags(lines[6])
	if err != nil {
		return typebundle.Target{}, fmt.Errorf("parse context ReleaseTags: %w", err)
	}
	return typebundle.Target{
		Compiler:         lines[2],
		GOOS:             lines[0],
		GOARCH:           lines[1],
		CGOEnabled:       cgoEnabled,
		ToolchainVersion: strings.TrimSpace(string(toolchainBytes)),
		LanguageVersion:  languageVersion,
		BuildTags:        buildTags,
		ToolTags:         toolTags,
		ReleaseTags:      releaseTags,
	}, nil
}

func parseContextTags(value string) ([]string, error) {
	if len(value) < 2 || value[0] != '[' || value[len(value)-1] != ']' {
		return nil, fmt.Errorf("invalid tag list %q", value)
	}
	return strings.Fields(value[1 : len(value)-1]), nil
}

func verifyObservedTarget(target typebundle.Target, request targetRequest) error {
	wantCGO := request.cgoEnabled
	if target.Compiler != request.compiler || target.GOOS != request.goos || target.GOARCH != request.goarch || target.CGOEnabled != wantCGO {
		return fmt.Errorf("observed %s/%s/%s cgo=%t, requested %s/%s/%s cgo=%t",
			target.Compiler, target.GOOS, target.GOARCH, target.CGOEnabled,
			request.compiler, request.goos, request.goarch, wantCGO)
	}
	return nil
}

func (config producerConfig) runGo(args ...string) ([]byte, error) {
	return config.launcher.Run(config.dir, config.env, args...)
}

func (config producerConfig) verifyGoCommand() error {
	return config.launcher.Validate(config.dir, config.env)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "gsx-typebundle: "+format+"\n", a...)
	os.Exit(1)
}
