package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/TosmimForidMehtab/torge/config"
)

type check struct {
	name string
	run  func() (ok bool, detail string)
}

// cmdDoctor checks the toolchain and project setup and explains how to fix
// problems.
func cmdDoctor(stdout io.Writer) error {
	checks := []check{
		{"Go toolchain >= 1.26", checkGo},
		{"go.mod present", func() (bool, string) {
			m, err := currentModule()
			if err != nil {
				return false, err.Error()
			}
			return true, "module " + m
		}},
		{"Torge dependency", checkDependency},
		{"TORGE_ENV / APP_ENV", func() (bool, string) {
			env, err := config.CurrentEnv()
			if err != nil {
				return false, err.Error() + "; fix: set it to development, test or production"
			}
			note := ""
			if os.Getenv("TORGE_ENV") == "" && os.Getenv("APP_ENV") == "" {
				note = " (unset: production defaults apply; set TORGE_ENV=development locally)"
			}
			return true, string(env) + note
		}},
	}
	failed := 0
	for _, c := range checks {
		ok, detail := c.run()
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			failed++
		}
		fmt.Fprintf(stdout, "[%s] %-22s %s\n", mark, c.name, detail)
	}
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

func checkGo() (bool, string) {
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		return false, "go not found in PATH; install Go from https://go.dev/dl"
	}
	v := strings.TrimSpace(string(out))
	parts := strings.Split(strings.TrimPrefix(v, "go"), ".")
	if len(parts) < 2 {
		return true, v
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	if major == 1 && minor < 26 {
		return false, v + "; fix: upgrade to Go 1.26 or newer"
	}
	return true, v
}

func checkDependency() (bool, string) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		return false, "no go.mod"
	}
	if !strings.Contains(string(data), torgeImport) {
		return false, "not required; fix: go get " + torgeImport
	}
	return true, torgeImport
}
