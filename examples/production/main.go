// Command production is a production-style Torge application: typed and
// validated REST endpoints, authentication, rate limiting, idempotent
// payments, signed webhooks, background jobs, caching, CORS, compression,
// health checks, OpenAPI docs, structured logs with request IDs and graceful
// shutdown.
//
//	TORGE_ENV=development API_TOKENS=dev-token WEBHOOK_SECRET=whsec go run ./examples/production
//	curl -H 'Authorization: Bearer dev-token' localhost:8080/v1/accounts/acc_x
//	open http://localhost:8080/docs
package main

import (
	"fmt"
	"os"

	"github.com/TosmimForidMehtab/torge/config"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/app"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/platform"
)

func main() {
	if err := config.LoadDotEnv(); err != nil { // development convenience; ignored in production
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var cfg platform.Config
	if err := config.Load(&cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := app.New(&cfg).Listen(cfg.Addr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
