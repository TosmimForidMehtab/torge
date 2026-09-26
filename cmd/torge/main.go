// Command torge is the optional Torge CLI. The framework never requires it.
//
//	torge new my-api                    create a project
//	torge dev [package]                 run with reload on file changes
//	torge generate module users         add a module skeleton
//	torge generate resource users       add a CRUD resource with tests
//	torge generate middleware auth      add a middleware skeleton
//	torge routes [package] [-json] [--check golden.json]   print the route table
//	torge doctor                        check the project and toolchain
//	torge version                       print the CLI version
//
// Unknown commands run "torge-<command>" from PATH (like git), which is how
// extensions add CLI commands.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
)

// version is set by release builds with -ldflags "-X main.version=...".
var version = "dev"

// cliVersion returns the stamped version, or the module version recorded by
// "go install ...@vX.Y.Z", or "dev" for local builds.
func cliVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

const usage = `Torge CLI

Usage:
  torge new <name> [-module path] [-local dir]   create a project
  torge dev [package]                            run with reload on changes
  torge generate module <name>                   add a module skeleton
  torge generate resource <name>                 add a CRUD resource with tests
  torge generate middleware <name>               add a middleware skeleton
  torge routes [package] [-json] [--check golden.json]   print the route table
  torge doctor                                   check the project and toolchain
  torge version                                  print the CLI version

Any other command runs "torge-<command>" from PATH, so extensions can add
commands without modifying Torge.
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "torge:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("invalid usage; run 'torge help'")

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "version":
		fmt.Fprintln(stdout, "torge", cliVersion())
		return nil
	case "new":
		return cmdNew(args[1:], stdout)
	case "generate", "gen", "g":
		return cmdGenerate(args[1:], stdout)
	case "routes":
		return cmdRoutes(args[1:], stdout, stderr)
	case "dev":
		return cmdDev(args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(stdout)
	default:
		return runPlugin(args, stdout, stderr)
	}
}

// runPlugin executes "torge-<command>" from PATH, git-style, so extensions
// can add CLI commands without modifying Torge.
func runPlugin(args []string, stdout, stderr io.Writer) error {
	bin, err := exec.LookPath("torge-" + args[0])
	if err != nil {
		return fmt.Errorf("unknown command %q; run 'torge help'", args[0])
	}
	cmd := exec.Command(bin, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	return cmd.Run()
}

func cmdRoutes(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("routes", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	checkPath := fs.String("check", "", "compare route output against `golden.json` and exit non-zero on drift")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pkg := "."
	if fs.NArg() > 0 {
		pkg = fs.Arg(0)
	}
	checking := *checkPath != ""
	if checking {
		// Fail fast on a missing golden file: it is an error, not a
		// match, and this keeps the check hermetic without running the app.
		if _, err := os.Stat(*checkPath); err != nil {
			return fmt.Errorf("routes --check: %w", err)
		}
	}
	mode := "1"
	if *asJSON || checking {
		// --check always compares the JSON table so the gate is
		// independent of the human-readable format.
		mode = "json"
	}
	cmd := exec.Command("go", "run", pkg)
	cmd.Env = append(os.Environ(), "TORGE_ROUTES="+mode)
	cmd.Stderr = stderr
	var captured bytes.Buffer
	if checking {
		cmd.Stdout = &captured
	} else {
		cmd.Stdout = stdout
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go run %s: %w (the app must call app.Listen or app.Serve)", pkg, err)
	}
	if checking {
		if err := compareRouteOutput(captured.Bytes(), *checkPath); err != nil {
			return err
		}
	}
	return nil
}

// normalizeRouteOutput trims trailing whitespace per line and drops trailing
// blank lines, so goldens are insensitive to a final newline.
func normalizeRouteOutput(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// compareRouteOutput compares app output against the golden file at
// goldenPath after normalization. A missing golden file is an error, and a
// mismatch reports the first differing lines.
func compareRouteOutput(got []byte, goldenPath string) error {
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		return fmt.Errorf("routes --check: read golden file: %w", err)
	}
	want, have := normalizeRouteOutput(wantBytes), normalizeRouteOutput(got)
	if want == have {
		return nil
	}
	const maxLines = 10
	wlines := strings.Split(want, "\n")
	hlines := strings.Split(have, "\n")
	n := len(wlines)
	if len(hlines) > n {
		n = len(hlines)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "routes drift: output differs from golden %q (golden %d lines, got %d lines)", goldenPath, len(wlines), len(hlines))
	shown, more := 0, 0
	for i := 0; i < n; i++ {
		var w, h string
		if i < len(wlines) {
			w = wlines[i]
		}
		if i < len(hlines) {
			h = hlines[i]
		}
		if w == h {
			continue
		}
		if shown < maxLines {
			fmt.Fprintf(&b, "\nline %d:\n- %s\n+ %s", i+1, w, h)
			shown++
		} else {
			more++
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n... and %d more differing lines", more)
	}
	return errors.New(b.String())
}

// identifier converts a user-supplied name to a Go identifier fragment.
func identifier(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			if upper {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			upper = false
		case r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' && b.Len() > 0:
			b.WriteRune(r)
			upper = false
		default:
			upper = true
		}
	}
	return b.String()
}

// packageName converts a name to a lower-case Go package name.
func packageName(name string) string {
	return strings.ToLower(identifier(name))
}
