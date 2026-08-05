package gen

import (
	"bufio"
	"bytes"
	"embed"
	"flag"
	"fmt"
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
)

//go:embed all:templates/init
var initFS embed.FS

// tmplData is the substitution context for init templates.
type tmplData struct {
	Module string // full Go module path, e.g. "github.com/me/app"
	Name   string // path.Base(Module), e.g. "app" (npm name, etc.)
}

// initTemplate is one registered starter: a name, a one-line description, the
// root path of its subtree within the embedded template FS, and a display
// order for the interactive picker (lower sorts first; ties break by name).
type initTemplate struct {
	name  string
	desc  string
	root  string
	order int
}

const defaultTemplate = "simple"

// templates is the registry. The embedded FS and the "simple" subtree land in
// the template task; the registry entry is declared here.
var templates = map[string]initTemplate{
	"simple": {
		name:  "simple",
		desc:  "Stock net/http ServeMux + gsx + Vite dev loop.",
		root:  "templates/init/simple",
		order: 0,
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

// splitDirFlags partitions args into flag tokens and (at most) one positional
// directory argument, so flags may appear before or after the positional arg.
// It is shared by initWith (which rejects a non-empty dir) and newWith (which
// requires or prompts for one).
func splitDirFlags(args []string) (dir string, flagArgs []string) {
	valueFlag := map[string]bool{"-template": true, "--template": true, "-module": true, "--module": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			if valueFlag[a] && !strings.Contains(a, "=") && i+1 < len(args) {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
		} else {
			dir = a
		}
	}
	return dir, flagArgs
}

// initFlagSet builds the flag.FlagSet shared by init and new: -template,
// -module, -force, -yes/-y. name distinguishes the two in flag-parsing error
// output (e.g. `-h`).
func initFlagSet(name string, stderr io.Writer) (fs *flag.FlagSet, templateName, module *string, force, yes *bool) {
	fs = flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	templateName = new(string)
	module = new(string)
	force = new(bool)
	yes = new(bool)
	fs.StringVar(templateName, "template", defaultTemplate, "starter template")
	fs.StringVar(module, "module", "", "Go module path (default: target dir basename)")
	fs.BoolVar(force, "force", false, "overwrite an existing go.mod/package.json")
	fs.BoolVar(yes, "yes", false, "run setup steps without prompting")
	fs.BoolVar(yes, "y", false, "run setup steps without prompting (shorthand)")
	return fs, templateName, module, force, yes
}

// lookupTemplate resolves templateName in the registry, printing the
// available-templates listing to stderr on a miss.
func lookupTemplate(templateName string, stderr io.Writer) (initTemplate, bool) {
	tpl, ok := templates[templateName]
	if !ok {
		fmt.Fprintf(stderr, "gsx: unknown template %q. Available:\n", templateName)
		for _, t := range templateList() {
			fmt.Fprintf(stderr, "  %-12s %s\n", t.name, t.desc)
		}
	}
	return tpl, ok
}

// initWith is the cwd-only `gsx init` core: it scaffolds directly into
// workDir and never accepts a positional directory argument (that's `gsx
// new`'s job).
func initWith(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool, run stepRunner, workDir string) int {
	dir, flagArgs := splitDirFlags(args)
	fs, templateName, module, force, yes := initFlagSet("init", stderr)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if dir != "" {
		fmt.Fprintln(stderr, "gsx: init scaffolds into the current directory; use 'gsx new <dir>' to create a project directory")
		return 2
	}

	tpl, ok := lookupTemplate(*templateName, stderr)
	if !ok {
		return 2
	}

	abs := workDir
	mod := *module
	if mod == "" {
		mod = filepath.Base(abs)
	}

	reader := bufio.NewReader(stdin)
	return scaffoldCore(reader, "init", tpl, abs, ".", mod, *force, stdout, stderr, interactive, *yes, run)
}

// newWith is the `gsx new <dir>` core: the target directory is a required
// positional arg (or, interactively, a prompted project name); it scaffolds
// into <workDir>/<dir>. When run interactively without an explicit
// --template, it prompts for the project name first and then offers the
// template picker (promptTemplate) — asking "what" before "which template"
// mirrors the order a user would naturally answer them. An explicit
// --template is validated immediately, before any prompting, so a typo fails
// fast.
func newWith(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool, run stepRunner, workDir string) int {
	dir, flagArgs := splitDirFlags(args)
	fs, templateName, module, force, yes := initFlagSet("new", stderr)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	explicitTemplate := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "template" {
			explicitTemplate = true
		}
	})
	if explicitTemplate {
		if _, ok := lookupTemplate(*templateName, stderr); !ok {
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

	name := *templateName
	if interactive && !*yes && !explicitTemplate {
		name = promptTemplate(reader, stdout, defaultTemplate)
	}
	tpl, ok := lookupTemplate(name, stderr)
	if !ok {
		return 2
	}

	// Anchor a relative target dir at workDir rather than the process-global
	// cwd, so new is reentrant under -C.
	abs := absAgainst(workDir, dir)
	mod := *module
	if mod == "" {
		mod = filepath.Base(abs)
	}

	return scaffoldCore(reader, "new", tpl, abs, dir, mod, *force, stdout, stderr, interactive, *yes, run)
}

// scaffoldCore is the shared body of init and new once the target directory
// and module path are resolved: the existence guard, the template scaffold,
// and the post-scaffold setup steps (or next-steps printout). cmdName ("init"
// or "new") labels operational-error output; dir is the display-only
// directory name used in "cd <dir>" hints ("." for init, which never shows a
// cd line).
func scaffoldCore(reader *bufio.Reader, cmdName string, tpl initTemplate, abs, dir, module string, force bool, stdout, stderr io.Writer, interactive, yes bool, run stepRunner) int {
	if !force {
		for _, f := range []string{"go.mod", "package.json"} {
			if _, err := os.Stat(filepath.Join(abs, f)); err == nil {
				fmt.Fprintf(stderr, "gsx: %s already exists in %s (use --force to overwrite)\n", f, dir)
				return 2
			}
		}
	}

	data := tmplData{Module: module, Name: path.Base(filepath.ToSlash(module))}
	if err := scaffold(initFS, tpl.root, abs, data); err != nil {
		fmt.Fprintf(stderr, "gsx: %s: %v\n", cmdName, err)
		return 1
	}

	// Non-interactive without --yes keeps the v1 behavior.
	if !interactive && !yes {
		printNextSteps(stdout, dir)
		return 0
	}
	return runSteps(reader, abs, dir, stdout, stderr, interactive && !yes, run)
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
	for attempt := 0; attempt < 2; attempt++ {
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
