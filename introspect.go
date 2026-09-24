package torge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// PrintRoutes writes the route table to w as an aligned table.
func (a *App) PrintRoutes(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "METHOD\tPATH\tNAME\tHANDLER\tMIDDLEWARE\tMODULE")
	for _, r := range a.Routes() {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Method, r.Path, dash(r.Name), r.Handler, dash(strings.Join(r.Middleware, ",")), dash(r.Module))
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printRoutes implements TORGE_ROUTES: it performs the registration part of
// startup (dependency construction and Invoke functions) without running
// start hooks, prints the routes and returns. TORGE_ROUTES=json prints JSON.
func (a *App) printRoutes(w io.Writer) error {
	if a.State() == StateBuilding {
		ctx := context.Background()
		if err := a.container.build(ctx, a); err != nil {
			return err
		}
		if err := a.runInvokes(ctx); err != nil {
			return err
		}
	}
	if os.Getenv("TORGE_ROUTES") == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(a.Routes())
	}
	return a.PrintRoutes(w)
}
