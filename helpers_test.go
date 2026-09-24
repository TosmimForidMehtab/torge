package torge_test

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newLocalListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func waitRunning(t *testing.T, app *torge.App) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for app.State() != torge.StateRunning {
		if time.Now().After(deadline) {
			t.Fatalf("app did not start, state %s", app.State())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
