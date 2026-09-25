package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type tplData struct {
	Module   string // Go module path of the project
	Name     string // user-supplied name, e.g. "blog-posts"
	Package  string // Go package name, e.g. "blogposts"
	Type     string // exported type fragment, e.g. "BlogPosts"
	Path     string // URL path segment, e.g. "blog-posts"
	Torge    string // framework import path
	LocalDir string // replace directive target, if any
}

const torgeImport = "github.com/TosmimForidMehtab/torge"

func cmdNew(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	module := fs.String("module", "", "Go module path (default: the project name)")
	local := fs.String("local", "", "use a local Torge checkout via a replace directive")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errUsage
	}
	name := fs.Arg(0)
	if *module == "" {
		*module = name
	}
	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("%s already exists", name)
	}
	data := tplData{Module: *module, Name: "users", Package: "users", Type: "User", Path: "users", Torge: torgeImport}
	if *local != "" {
		abs, err := filepath.Abs(*local)
		if err != nil {
			return err
		}
		data.LocalDir = filepath.ToSlash(abs)
	}
	files := map[string]string{
		"go.mod":                       goModTpl,
		"main.go":                      mainTpl,
		"internal/config/config.go":    configTpl,
		"internal/users/module.go":     resourceTpl,
		"internal/users/users_test.go": resourceTestTpl,
		".gitignore":                   "/bin/\n*.test\n.env\n.env.local\n",
		".env.example":                 dotEnvTpl, // committed template
		".env":                         dotEnvTpl, // local settings, git-ignored
		"Dockerfile":                   dockerfileTpl,
		".dockerignore":                dockerignoreTpl,
	}
	for path, tpl := range files {
		if err := writeTemplate(filepath.Join(name, path), tpl, data); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "Created %s.\n\n  cd %s\n  go mod tidy\n  go run .\n\nLocal settings are in .env. Then open http://localhost:8080/docs\n", name, name)
	return nil
}

func cmdGenerate(args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return errUsage
	}
	kind, name := args[0], args[1]
	module, err := currentModule()
	if err != nil {
		return err
	}
	pkg := packageName(name)
	if pkg == "" {
		return fmt.Errorf("invalid name %q", name)
	}
	data := tplData{
		Module: module, Name: name, Package: pkg, Type: singular(identifier(name)),
		Path: strings.ToLower(name), Torge: torgeImport,
	}
	var files map[string]string
	switch kind {
	case "module":
		files = map[string]string{filepath.Join("internal", pkg, "module.go"): moduleTpl}
	case "resource":
		files = map[string]string{
			filepath.Join("internal", pkg, "module.go"):    resourceTpl,
			filepath.Join("internal", pkg, pkg+"_test.go"): resourceTestTpl,
		}
	case "middleware":
		files = map[string]string{filepath.Join("internal", "middleware", pkg+".go"): middlewareTpl}
	default:
		return fmt.Errorf("unknown generator %q (want module, resource or middleware)", kind)
	}
	for path, tpl := range files {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists", path)
		}
		if err := writeTemplate(path, tpl, data); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "created", filepath.ToSlash(path))
	}
	if kind != "middleware" {
		fmt.Fprintf(stdout, "\nRegister it in main.go:\n\n  app.Register(%s.Module{})\n", pkg)
	}
	return nil
}

// reorderFlags moves flags before positional arguments so "new app -local x"
// works like "new -local x app".
func reorderFlags(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if !strings.Contains(args[i], "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, args[i])
	}
	return append(flags, positional...)
}

func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies"):
		return strings.TrimSuffix(s, "ies") + "y"
	case strings.HasSuffix(s, "ses"):
		return strings.TrimSuffix(s, "es")
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss"):
		return strings.TrimSuffix(s, "s")
	}
	return s
}

func currentModule() (string, error) {
	f, err := os.Open("go.mod")
	if err != nil {
		return "", errors.New("go.mod not found; run generators from the project root")
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}
	return "", errors.New("go.mod has no module directive")
}

func writeTemplate(path, tpl string, data tplData) error {
	t, err := template.New(filepath.Base(path)).Delims("[[", "]]").
		Funcs(template.FuncMap{"upper": strings.ToUpper}).Parse(tpl)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := t.Execute(f, data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
