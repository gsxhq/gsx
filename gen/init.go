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

// initTemplate is one registered starter: a name, a one-line description, and
// the root path of its subtree within the embedded template FS.
type initTemplate struct {
	name string
	desc string
	root string
}

const defaultTemplate = "simple"

// templates is the registry. The embedded FS and the "simple" subtree land in
// the template task; the registry entry is declared here.
var templates = map[string]initTemplate{
	"simple": {
		name: "simple",
		desc: "Stock net/http ServeMux + gsx + Vite dev loop.",
		root: "templates/init/simple",
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

func initWith(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool, run stepRunner, workDir string) int {
	ifs := flag.NewFlagSet("init", flag.ContinueOnError)
	ifs.SetOutput(stderr)
	var templateName, module string
	var force, yes bool
	ifs.StringVar(&templateName, "template", defaultTemplate, "starter template")
	ifs.StringVar(&module, "module", "", "Go module path (default: target dir basename)")
	ifs.BoolVar(&force, "force", false, "overwrite an existing go.mod/package.json")
	ifs.BoolVar(&yes, "yes", false, "run setup steps without prompting")
	ifs.BoolVar(&yes, "y", false, "run setup steps without prompting (shorthand)")
	valueFlag := map[string]bool{"-template": true, "--template": true, "-module": true, "--module": true}
	var flagArgs []string
	dir := ""
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
	if err := ifs.Parse(flagArgs); err != nil {
		return 2
	}

	tpl, ok := templates[templateName]
	if !ok {
		fmt.Fprintf(stderr, "gsx: unknown template %q. Available:\n", templateName)
		for _, t := range templateList() {
			fmt.Fprintf(stderr, "  %-12s %s\n", t.name, t.desc)
		}
		return 2
	}

	reader := bufio.NewReader(stdin)
	if dir == "" {
		if interactive && !yes {
			dir = promptText(reader, stdout, "Project name", "gsx-app")
		} else {
			dir = "."
		}
	}

	// Anchor a relative target dir (and the default ".") at workDir rather than the
	// process-global cwd, so init is reentrant under -C.
	abs := absAgainst(workDir, dir)
	if module == "" {
		module = filepath.Base(abs)
	}

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
		fmt.Fprintf(stderr, "gsx: init: %v\n", err)
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
// "Run: task dev" block and returns 0.
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
	fmt.Fprintln(stdout, "  task dev")
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

// templateList returns the registered templates sorted by name.
func templateList() []initTemplate {
	out := make([]initTemplate, 0, len(templates))
	for _, t := range templates {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
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
	fmt.Fprintln(stdout, "  task dev")
}
