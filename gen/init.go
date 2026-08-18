package gen

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"go/build"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"golang.org/x/mod/module"
)

//go:embed all:templates/init
var initFS embed.FS

// tmplData is the substitution context for init templates.
type tmplData struct {
	Module string // full Go module path, e.g. "github.com/me/app"
	Name   string // path.Base(Module), e.g. "app" (npm name, etc.)
}

// initTemplate is one registered starter: a name, a one-line description, a
// display order for the interactive picker (lower sorts first; ties break by
// name), and a source — either an embedded root (module == "") rendered
// through the «»/transformName pipeline, or a module path fetched from the
// proxy (or a local checkout via --from) and personalized in place (see
// personalize.go): module rewrite, gsx-template.json strip list, generated
// env secrets, but no «» rendering or dot- transform — a fetched template is
// a literal repo, not a Go text/template source tree.
type initTemplate struct {
	name   string
	desc   string
	order  int
	root   string // embedded FS subtree root; empty when module is set
	module string // module path to fetch; empty for an embedded template
}

const defaultTemplate = "simple"

// templates is the registry. saas lists first (order 0): it's the flagship
// one-liner this plan exists to deliver. Its module, github.com/gsxhq/saas-template,
// may not exist yet while this plan executes — every code path exercising it
// is tested against local fixtures and --from, never the real proxy.
var templates = map[string]initTemplate{
	"simple": {
		name:  "simple",
		desc:  "Stock net/http ServeMux + gsx + Vite dev loop.",
		root:  "templates/init/simple",
		order: 1,
	},
	"saas": {
		name:   "saas",
		desc:   "Full-stack SaaS starter: auth, dashboard, CRUD, SQLite, htmx (fetched from github.com/gsxhq/saas-template)",
		module: "github.com/gsxhq/saas-template",
		order:  0,
	},
}

// scaffold walks the template subtree rooted at root within srcFS, renders each
// file with render, maps its name with transformName, and writes it under
// destDir (creating parent dirs). It overwrites existing files; the
// project-level existence guard lives in runInit.
func scaffold(srcFS fs.FS, root, destDir string, data tmplData) error {
	return fs.WalkDir(srcFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, root+"/")
		raw, err := fs.ReadFile(srcFS, p)
		if err != nil {
			return err
		}
		rendered, err := render(raw, data)
		if err != nil {
			return err
		}
		dest := filepath.Join(destDir, transformName(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dest, rendered, 0o644)
	})
}

// transformName maps a template-relative path to its output path: a trailing
// ".tmpl" is stripped, and any path segment prefixed "dot-" becomes a dotfile.
func transformName(rel string) string {
	parts := strings.Split(rel, "/")
	for i, seg := range parts {
		if rest, ok := strings.CutPrefix(seg, "dot-"); ok {
			parts[i] = "." + rest
		}
	}
	last := len(parts) - 1
	parts[last] = strings.TrimSuffix(parts[last], ".tmpl")
	return filepath.Join(parts...)
}

// render runs raw through text/template with «» delimiters (so the gsx {{ }} and
// { } in templates pass through untouched) and the given data.
func render(raw []byte, data tmplData) ([]byte, error) {
	t, err := template.New("f").Delims("«", "»").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type stepRunner func(args []string, dir string, stdout, stderr io.Writer) error

// setupSteps are the post-scaffold commands, in run order.
var setupSteps = [][]string{
	{"go", "get", "-tool", "github.com/gsxhq/gsx/cmd/gsx@latest"},
	{"go", "mod", "tidy"},
	{"npm", "install"},
}

func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer, workDir string) int {
	return initWith(args, stdin, stdout, stderr, isTTYReader(stdin), execStep, workDir)
}

func runNew(args []string, stdin io.Reader, stdout, stderr io.Writer, workDir string) int {
	return newWith(args, stdin, stdout, stderr, isTTYReader(stdin), execStep, workDir)
}

// splitDirFlags partitions args into flag tokens and at most one positional
// directory argument, so flags may appear before or after the positional
// arg. Which flags consume a following value token is derived from fset
// itself (a non-boolean flag given as `-name value`), so a flag added to the
// set can never be misread as the dir. A second positional is a usage error
// (returned as err) rather than silently winning: `gsx new saas myapp` must
// not quietly scaffold myapp with the default template.
func splitDirFlags(fset *flag.FlagSet, args []string) (dir string, flagArgs []string, err error) {
	takesValue := func(tok string) bool {
		name := strings.TrimLeft(tok, "-")
		if strings.Contains(name, "=") {
			return false
		}
		f := fset.Lookup(name)
		if f == nil {
			return false
		}
		bf, isBool := f.Value.(interface{ IsBoolFlag() bool })
		return !isBool || !bf.IsBoolFlag()
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flagArgs = append(flagArgs, a)
			if takesValue(a) && i+1 < len(args) {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
			continue
		}
		if dir != "" {
			return "", nil, fmt.Errorf("unexpected argument %q (only one directory may be given)", a)
		}
		dir = a
	}
	return dir, flagArgs, nil
}

// initFlagSet builds the flag.FlagSet shared by init and new: -template,
// -module, -force, -yes/-y. name distinguishes the two in flag-parsing error
// output (e.g. `-h`). The returned set is named fset, not fs — this package
// imports io/fs, and a local fs would shadow that package identifier for the
// rest of the enclosing function.
func initFlagSet(name string, stderr io.Writer) (fset *flag.FlagSet, templateName, module *string, force, yes *bool) {
	fset = flag.NewFlagSet(name, flag.ContinueOnError)
	fset.SetOutput(stderr)
	templateName = new(string)
	module = new(string)
	force = new(bool)
	yes = new(bool)
	fset.StringVar(templateName, "template", defaultTemplate, "starter template")
	fset.StringVar(module, "module", "", "Go module path (default: target dir basename)")
	fset.BoolVar(force, "force", false, "overwrite an existing go.mod/package.json")
	fset.BoolVar(yes, "yes", false, "run setup steps without prompting")
	fset.BoolVar(yes, "y", false, "run setup steps without prompting (shorthand)")
	return fset, templateName, module, force, yes
}

// lookupTemplate resolves templateName among the offered templates (init
// offers only embeddedTemplates; new offers the full templateList), printing
// the offered listing to stderr on a miss.
func lookupTemplate(templateName string, offered []initTemplate, stderr io.Writer) (initTemplate, bool) {
	for _, t := range offered {
		if t.name == templateName {
			return t, true
		}
	}
	if _, registered := templates[templateName]; registered {
		fmt.Fprintf(stderr, "gsx: template %q must be fetched; use 'gsx new <dir> --template %s'. Available here:\n", templateName, templateName)
	} else {
		fmt.Fprintf(stderr, "gsx: unknown template %q. Available:\n", templateName)
	}
	for _, t := range offered {
		fmt.Fprintf(stderr, "  %-12s %s\n", t.name, t.desc)
	}
	return initTemplate{}, false
}

// embeddedTemplates is templateList restricted to templates compiled into
// the binary — the only ones init (which never fetches) can scaffold.
func embeddedTemplates() []initTemplate {
	var out []initTemplate
	for _, t := range templateList() {
		if t.module == "" {
			out = append(out, t)
		}
	}
	return out
}

// initWith is the cwd-only `gsx init` core: it scaffolds directly into
// workDir and never accepts a positional directory argument (that's `gsx
// new`'s job).
func initWith(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool, run stepRunner, workDir string) int {
	fset, templateName, module, force, yes := initFlagSet("init", stderr)
	dir, flagArgs, err := splitDirFlags(fset, args)
	if err != nil {
		fmt.Fprintf(stderr, "gsx: init: %v\n", err)
		return 2
	}
	if err := fset.Parse(flagArgs); err != nil {
		return 2
	}
	if dir != "" {
		fmt.Fprintln(stderr, "gsx: init scaffolds into the current directory; use 'gsx new <dir>' to create a project directory")
		return 2
	}

	// init never fetches: only embedded templates are offered or accepted.
	tpl, ok := lookupTemplate(*templateName, embeddedTemplates(), stderr)
	if !ok {
		return 2
	}

	abs := workDir
	mod := *module
	if mod == "" {
		mod = filepath.Base(abs)
	}

	if !checkNotExisting(abs, ".", *force, stderr) {
		return 2
	}

	reader := bufio.NewReader(stdin)
	return scaffoldCore(reader, "init", tpl, nil, abs, ".", mod, stdout, stderr, interactive, *yes, run)
}

// newWith is the `gsx new <dir>` core: the target directory is a required
// positional arg (or, interactively, a prompted project name); it scaffolds
// into <workDir>/<dir>. When run interactively without an explicit
// --template, it prompts for the project name first and then offers the
// template picker (promptTemplate) — asking "what" before "which template"
// mirrors the order a user would naturally answer them. An explicit
// --template is validated immediately, before any prompting, so a typo fails
// fast. --from bypasses --template's source entirely (see
// resolveTemplateSource), so neither the early validation nor the picker
// runs when it's set.
//
// Everything that can fail without touching the network — flag/dir/template
// resolution, the target module's validity, and the existing-project guard —
// runs BEFORE resolveTemplateSource. A doomed run (bad --module, an existing
// go.mod without --force) must exit 2 without ever hitting the module proxy;
// see checkNotExisting and validateNewModule.
func newWith(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool, run stepRunner, workDir string) int {
	fset, templateName, module, force, yes := initFlagSet("new", stderr)
	// --from <dir-or-module> overrides --template's source; see
	// resolveTemplateSource for how the two forms are told apart.
	var from string
	fset.StringVar(&from, "from", "", "fetch a template from a local directory (./path, ../path, /abs) or a module path (overrides --template)")
	dir, flagArgs, err := splitDirFlags(fset, args)
	if err != nil {
		fmt.Fprintf(stderr, "gsx: new: %v\n", err)
		return 2
	}
	if err := fset.Parse(flagArgs); err != nil {
		return 2
	}
	explicitTemplate := false
	fset.Visit(func(f *flag.Flag) {
		if f.Name == "template" {
			explicitTemplate = true
		}
	})
	if from == "" && explicitTemplate {
		if _, ok := lookupTemplate(*templateName, templateList(), stderr); !ok {
			return 2
		}
	}

	reader := bufio.NewReader(stdin)
	if dir == "" {
		if interactive && !*yes {
			dir = promptText(reader, stdout, "Project name", "gsx-app")
		} else {
			fmt.Fprintln(stderr, "gsx: new requires a directory argument (run interactively with no arguments to be prompted)")
			return 2
		}
	}

	var tpl initTemplate
	if from == "" {
		name := *templateName
		if interactive && !*yes && !explicitTemplate {
			name = promptTemplate(reader, stdout, defaultTemplate)
		}
		var ok bool
		tpl, ok = lookupTemplate(name, templateList(), stderr)
		if !ok {
			return 2
		}
	}

	// Anchor a relative target dir at workDir rather than the process-global
	// cwd, so new is reentrant under -C.
	abs := absAgainst(workDir, dir)
	mod := *module
	if mod == "" {
		mod = filepath.Base(abs)
	}

	// Validate the target module and guard the existing project BEFORE
	// resolveTemplateSource: both apply to every source (embedded, fetched,
	// or --from) equally, and neither needs the fetched/local content to
	// decide, so there is no reason to make a network round trip (or even
	// touch the local --from filesystem) first only to discard the result.
	if err := validateNewModule(mod); err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return 2
	}
	if !checkNotExisting(abs, dir, *force, stderr) {
		return 2
	}

	fetchedSrc, code := resolveTemplateSource(workDir, from, tpl, stderr)
	if code != 0 {
		return code
	}

	return scaffoldCore(reader, "new", tpl, fetchedSrc, abs, dir, mod, stdout, stderr, interactive, *yes, run)
}

// resolveTemplateSource decides where new's scaffold content comes from. An
// explicit --from wins outright and is told apart syntactically, the same way
// the go command separates a local path from an import path: a value that is
// absolute or begins with ./ or ../ (build.IsLocalImport) is a local
// directory and must exist (exit 2 otherwise); anything else is a module
// path, validated with module.CheckPath before any network access (exit 2
// when it isn't one), then fetched at latest via fetchModuleFS. Without
// --from, an embedded template (tpl.module == "", e.g. simple) needs no
// fetch — (nil, 0) tells the caller to fall back to tpl.root — while a
// registry entry with a module (e.g. saas) is fetched the same way a module
// --from would be.
//
// On failure the error is printed to stderr here (not returned) so the
// caller can propagate a plain exit code: 2 for a bad --from value or an
// invalid GOPROXY (the user's input, not a network failure), 1 for an actual
// fetch/proxy operational failure.
func resolveTemplateSource(workDir, from string, tpl initTemplate, stderr io.Writer) (fs.FS, int) {
	modulePath := from
	switch {
	case from == "":
		if tpl.module == "" {
			return nil, 0
		}
		modulePath = tpl.module
	case build.IsLocalImport(from) || filepath.IsAbs(from):
		src, err := localTemplateFS(absAgainst(workDir, from))
		if err != nil {
			fmt.Fprintf(stderr, "gsx: %v\n", err)
			return nil, 2
		}
		return src, 0
	default:
		if err := module.CheckPath(from); err != nil {
			fmt.Fprintf(stderr, "gsx: --from %q is neither a local directory (use ./%s) nor a module path: %v\n", from, from, err)
			return nil, 2
		}
	}

	proxyBase, err := proxyBaseFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return nil, 2
	}
	src, _, err := fetchModuleFS(context.Background(), proxyBase, modulePath, "latest")
	if err != nil {
		fmt.Fprintf(stderr, "gsx: %v\n", err)
		return nil, 1
	}
	return src, 0
}

// checkNotExisting guards abs against silently overwriting an existing
// project: unless force, it fails (printing the standard --force hint to
// stderr) when go.mod or package.json already exists there. Both init and
// new call this — new calls it before resolveTemplateSource specifically, so
// a doomed run (existing project, no --force) never touches the network.
func checkNotExisting(abs, dir string, force bool, stderr io.Writer) bool {
	if force {
		return true
	}
	for _, f := range []string{"go.mod", "package.json"} {
		if _, err := os.Stat(filepath.Join(abs, f)); err == nil {
			fmt.Fprintf(stderr, "gsx: %s already exists in %s (use --force to overwrite)\n", f, dir)
			return false
		}
	}
	return true
}

// scaffoldCore is the shared body of init and new once the target directory,
// module path, and template source are all resolved and validated: the
// template scaffold and the post-scaffold setup steps (or next-steps
// printout). Both the existing-project guard (checkNotExisting) and the
// target module's validity (validateNewModule) are the caller's
// responsibility, run before this — for new, specifically before
// resolveTemplateSource, so neither check costs a network round trip. cmdName
// ("init" or "new") labels operational-error output; dir is the display-only
// directory name used in "cd <dir>" hints ("." for init, which never shows a
// cd line). fetchedSrc, when non-nil, is a fetched module or --from local
// checkout (see fetch.go): it is personalized in place via scaffoldFetched
// instead of rendering tpl's embedded «»/transformName root.
func scaffoldCore(reader *bufio.Reader, cmdName string, tpl initTemplate, fetchedSrc fs.FS, abs, dir, module string, stdout, stderr io.Writer, interactive, yes bool, run stepRunner) int {
	var scaffoldErr error
	if fetchedSrc != nil {
		scaffoldErr = scaffoldFetched(fetchedSrc, abs, module)
	} else {
		data := tmplData{Module: module, Name: path.Base(filepath.ToSlash(module))}
		scaffoldErr = scaffold(initFS, tpl.root, abs, data)
	}
	if scaffoldErr != nil {
		var ime *invalidModuleError
		if errors.As(scaffoldErr, &ime) {
			// A bad --module value is a usage error, not an operational one,
			// regardless of which source produced it.
			fmt.Fprintf(stderr, "gsx: %v\n", scaffoldErr)
			return 2
		}
		fmt.Fprintf(stderr, "gsx: %s: %v\n", cmdName, scaffoldErr)
		return 1
	}

	// Non-interactive without --yes keeps the v1 behavior.
	if !interactive && !yes {
		printNextSteps(stdout, dir)
		return 0
	}
	return runSteps(reader, abs, dir, stdout, stderr, interactive && !yes, run)
}

// scaffoldFetched personalizes a fetched module zip or --from local checkout
// (src) into abs for the given module path. It is the fetched-template
// counterpart of scaffold: unlike scaffold's «»/transformName rendering of
// an embedded Go text/template source tree, a fetched template is a literal
// repo, so personalize copies it byte-for-byte except for the targeted
// module-path/manifest rewrites documented on personalize itself.
func scaffoldFetched(src fs.FS, abs, module string) error {
	return personalize(src, abs, module)
}

// runSteps confirms (when ask) and runs each setup step in abs. On a failed step
// it prints the remaining commands and returns 1; on success prints the final
// "Run: npm run dev" block and returns 0.
func runSteps(reader *bufio.Reader, abs, dir string, stdout, stderr io.Writer, ask bool, run stepRunner) int {
	for i, step := range setupSteps {
		fmt.Fprintf(stdout, "\n> %s\n", strings.Join(step, " "))
		if ask && !promptYes(reader, stdout, "  run this?") {
			fmt.Fprintln(stdout, "  skipped.")
			continue
		}
		if err := run(step, abs, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "\ngsx: step failed: %v\nRun the remaining steps manually:\n", err)
			for _, s := range setupSteps[i:] {
				fmt.Fprintf(stderr, "  %s\n", strings.Join(s, " "))
			}
			return 1
		}
	}
	fmt.Fprintln(stdout, "\n✓ Done!")
	if dir != "." {
		fmt.Fprintf(stdout, "  cd %s\n", dir)
	}
	fmt.Fprintln(stdout, "  npm run dev")
	return 0
}

// promptYes asks a [Y/n] question; empty/`y`/`yes` ⇒ true.
func promptYes(reader *bufio.Reader, stdout io.Writer, q string) bool {
	fmt.Fprintf(stdout, "%s [Y/n] ", q)
	line, _ := reader.ReadString('\n')
	s := strings.ToLower(strings.TrimSpace(line))
	return s == "" || s == "y" || s == "yes"
}

// promptText asks for a value, returning def on empty input.
func promptText(reader *bufio.Reader, stdout io.Writer, q, def string) string {
	fmt.Fprintf(stdout, "%s [%s] ", q, def)
	line, _ := reader.ReadString('\n')
	if s := strings.TrimSpace(line); s != "" {
		return s
	}
	return def
}

// promptTemplate prints a numbered menu of the registered templates (in
// templateList order, so a flagship entry can list first) and reads a
// selection: a 1-based list number or a template name. Empty input returns
// def. Invalid input reprints the "not a valid template" hint and reprompts
// once; a second invalid answer falls back to def.
func promptTemplate(reader *bufio.Reader, stdout io.Writer, def string) string {
	list := templateList()
	fmt.Fprintln(stdout, "Select a template:")
	for i, t := range list {
		fmt.Fprintf(stdout, "  %d) %-12s %s\n", i+1, t.name, t.desc)
	}
	resolve := func(s string) (string, bool) {
		if s == "" {
			return def, true
		}
		if n, err := strconv.Atoi(s); err == nil {
			if n >= 1 && n <= len(list) {
				return list[n-1].name, true
			}
			return "", false
		}
		if _, ok := templates[s]; ok {
			return s, true
		}
		return "", false
	}
	for range 2 {
		fmt.Fprintf(stdout, "Select a template [%s]: ", def)
		line, _ := reader.ReadString('\n')
		if name, ok := resolve(strings.TrimSpace(line)); ok {
			return name
		}
		fmt.Fprintln(stdout, "  not a valid template; try again.")
	}
	return def
}

// isTTYReader reports whether r is a terminal (a character device). Mirrors the
// writer-side isTTY in main.go; avoids a golang.org/x/term dependency.
func isTTYReader(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// execStep runs one setup command in dir, streaming output.
func execStep(args []string, dir string, stdout, stderr io.Writer) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// templateList returns the registered templates sorted by order (so a
// flagship entry can list first), with name as the tiebreaker.
func templateList() []initTemplate {
	out := make([]initTemplate, 0, len(templates))
	for _, t := range templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].order != out[j].order {
			return out[i].order < out[j].order
		}
		return out[i].name < out[j].name
	})
	return out
}

func printNextSteps(stdout io.Writer, dir string) {
	fmt.Fprintln(stdout, "Scaffolded a gsx + Vite app. Next steps:")
	if dir != "." {
		fmt.Fprintf(stdout, "  cd %s\n", dir)
	}
	fmt.Fprintln(stdout, "  go get -tool github.com/gsxhq/gsx/cmd/gsx@latest")
	fmt.Fprintln(stdout, "  go mod tidy")
	fmt.Fprintln(stdout, "  npm install")
	fmt.Fprintln(stdout, "  npm run dev")
}
