package torgetest_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/openapi"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type snapshotItem struct {
	ID   string `json:"id"`
	Name string `json:"name" validate:"required"`
}

type getSnapshotItemInput struct {
	ID string `path:"id"`
}

func snapshotTestApp(t *testing.T) *torge.App {
	t.Helper()
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{
		Info: openapi.Info{Title: "Snapshot", Version: "1.0.0"},
	})
	torge.Get(app, "/items/:id", func(c *torge.Context, in *getSnapshotItemInput) (*snapshotItem, error) {
		return &snapshotItem{ID: in.ID, Name: "demo"}, nil
	}, torge.Summary("Get item"))
	return app
}

// stubTB records Fatalf instead of exiting, so a golden mismatch can be
// asserted without failing the outer test.
type stubTB struct {
	testing.TB
	failed bool
	msg    string
}

func (s *stubTB) Fatalf(format string, args ...any) {
	s.failed = true
	s.msg = fmt.Sprintf(format, args...)
}

func TestFetchOpenAPI(t *testing.T) {
	tc := torgetest.New(t, snapshotTestApp(t))
	doc := torgetest.FetchOpenAPI(t, tc)
	if doc.OpenAPI != "3.1.0" || doc.Info.Title != "Snapshot" {
		t.Fatalf("bad document header %+v", doc.Info)
	}
	op := doc.Paths["/items/{id}"]["get"]
	if op == nil || op.Summary != "Get item" {
		t.Fatalf("typed route missing from snapshot: %v", doc.Paths)
	}
}

func TestAssertOpenAPIGolden(t *testing.T) {
	tc := torgetest.New(t, snapshotTestApp(t))
	doc := torgetest.FetchOpenAPI(t, tc)

	golden := filepath.Join("testdata", "openapi_golden.json")
	torgetest.AssertOpenAPIGolden(t, doc, golden)

	t.Run("crlf golden matches", func(t *testing.T) {
		// Windows checkouts convert goldens to CRLF; the comparison must
		// not care.
		t.Setenv("TORGE_UPDATE_GOLDEN", "")
		raw, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		crlf := bytes.ReplaceAll(raw, []byte("\n"), []byte("\r\n"))
		tmp := filepath.Join(t.TempDir(), "openapi.json")
		if err := os.WriteFile(tmp, crlf, 0o644); err != nil {
			t.Fatal(err)
		}
		torgetest.AssertOpenAPIGolden(t, doc, tmp)
	})

	t.Run("mismatch detected", func(t *testing.T) {
		// Compare against the committed wrong golden even when the outer
		// run regenerates goldens.
		t.Setenv("TORGE_UPDATE_GOLDEN", "")
		stub := &stubTB{TB: t}
		wrong, err := filepath.Abs(filepath.Join("testdata", "openapi_wrong.json"))
		if err != nil {
			t.Fatal(err)
		}
		torgetest.AssertOpenAPIGolden(stub, doc, wrong)
		if !stub.failed {
			t.Fatal("expected golden mismatch to fail the test")
		}
		if !strings.Contains(stub.msg, "differs") {
			t.Fatalf("mismatch report should say the snapshot differs, got:\n%s", stub.msg)
		}
	})
}
