// Command torge is the optional Torge CLI. The framework never requires it.
//
//	torge new my-api                    create a project
//	torge dev [package]                 run with reload on file changes
//	torge generate module users         add a module skeleton
//	torge generate resource users       add a CRUD resource with tests
//	torge generate middleware auth      add a middleware skeleton
//	torge routes [package]              print the route table
//	torge doctor                        check the project and toolchain
//	torge version                       print the CLI version
//
// Unknown commands run "torge-<command>" from PATH (like git), which is how
// extensions add CLI commands.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Torge CLI

Usage:
  torge new <name> [-module path] [-local dir]   create a project
  torge dev [package]                            run with reload on changes
  torge generate module <name>                   add a module skeleton
  torge generate resource <name>                 add a CRUD resource with tests
  torge generate middleware <name>               add a middleware skeleton
  torge routes [package] [-json]                 print the route table
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
		fmt.Fprintln(stdout, "torge", version)
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	pkg := "."
	if fs.NArg() > 0 {
		pkg = fs.Arg(0)
	}
	mode := "1"
	if *asJSON {
		mode = "json"
	}
	cmd := exec.Command("go", "run", pkg)
	cmd.Env = append(os.Environ(), "TORGE_ROUTES="+mode)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go run %s: %w (the app must call app.Listen or app.Serve)", pkg, err)
	}
	return nil
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
