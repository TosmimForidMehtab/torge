// Package autoload loads .env files when imported, the Go equivalent of
// Node's require("dotenv/config"):
//
//	import _ "github.com/TosmimForidMehtab/torge/config/autoload"
//
// It calls config.LoadDotEnv during package initialization, before main runs.
// A malformed file stops the program with the parse error, so a broken
// configuration never starts serving.
package autoload

import (
	"fmt"
	"os"

	"github.com/TosmimForidMehtab/torge/config"
)

func init() {
	if err := config.LoadDotEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
