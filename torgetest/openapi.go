// OpenAPI snapshot helpers for Torge tests: fetch the generated document
// in-process and compare it against a checked-in golden file.
package torgetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge/openapi"
)

// FetchOpenAPI GETs /openapi.json in-process through the given started client
// and decodes it, failing the test on a non-200 status or invalid JSON.
//
// The client is typically built with New, which starts the app:
//
//	app := torgetest.NewApp(t)
//	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "API", Version: "1.0.0"}})
//	torge.Get(app, "/items/:id", getItem)
//	tc := torgetest.New(t, app)
//	doc := torgetest.FetchOpenAPI(t, tc)
func FetchOpenAPI(t testing.TB, c *Client) openapi.Document {
	t.Helper()
	res := c.GET("/openapi.json").Do()
	if res.Status != 200 {
		t.Fatalf("torgetest: GET /openapi.json: expected status 200, got %d\nbody: %s", res.Status, res.Body)
	}
	var doc openapi.Document
	if err := json.Unmarshal(res.Body, &doc); err != nil {
		t.Fatalf("torgetest: decode /openapi.json: %v\nbody: %s", err, res.Body)
	}
	return doc
}

// marshalOpenAPICanonical marshals doc the same way for every comparison:
// indented JSON with a trailing newline.
func marshalOpenAPICanonical(doc openapi.Document) ([]byte, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// AssertOpenAPIGolden compares doc (canonically marshalled with indent)
// against the golden file at path, failing the test with a short diff
// excerpt on mismatch.
//
// Regeneration: with TORGE_UPDATE_GOLDEN=1 the golden file (and any missing
// parent directories) is written and the test passes with a log line instead
// of failing, so `TORGE_UPDATE_GOLDEN=1 go test ./...` refreshes goldens
// after an intentional spec change. Review the diff before committing.
//
// Example:
//
//	doc := torgetest.FetchOpenAPI(t, tc)
//	torgetest.AssertOpenAPIGolden(t, doc, "testdata/openapi.json")
func AssertOpenAPIGolden(t testing.TB, doc openapi.Document, path string) {
	t.Helper()
	got, err := marshalOpenAPICanonical(doc)
	if err != nil {
		t.Fatalf("torgetest: encode OpenAPI document: %v", err)
	}
	if os.Getenv("TORGE_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("torgetest: create golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("torgetest: write golden file: %v", err)
		}
		t.Logf("torgetest: updated golden file %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("torgetest: read golden file %s: %v (run with TORGE_UPDATE_GOLDEN=1 to create it)", path, err)
	}
	// Goldens may check out with CRLF on Windows; normalize both sides so
	// the comparison only sees content.
	got, want = normalizeLineEndings(got), normalizeLineEndings(want)
	if bytes.Equal(got, want) {
		return
	}
	t.Fatalf("torgetest: OpenAPI snapshot %s differs:\n%s", path, diffExcerpt(string(want), string(got)))
}

// normalizeLineEndings maps CRLF (and lone CR) to LF.
func normalizeLineEndings(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
}

// diffExcerpt renders the first differing lines between want and got,
// capped so failure output stays short.
func diffExcerpt(want, got string) string {
	const maxLines = 10
	wlines := strings.Split(want, "\n")
	glines := strings.Split(got, "\n")
	n := longer(wlines, glines)
	var b strings.Builder
	fmt.Fprintf(&b, "want %d lines, got %d lines", len(wlines), len(glines))
	shown := 0
	more := 0
	for i := 0; i < n; i++ {
		var w, g string
		if i < len(wlines) {
			w = wlines[i]
		}
		if i < len(glines) {
			g = glines[i]
		}
		if w == g {
			continue
		}
		if shown < maxLines {
			fmt.Fprintf(&b, "\nline %d:\n- %s\n+ %s", i+1, w, g)
			shown++
		} else {
			more++
		}
	}
	if more > 0 {
		fmt.Fprintf(&b, "\n... and %d more differing lines", more)
	}
	return b.String()
}

func longer(a, b []string) int {
	if len(a) > len(b) {
		return len(a)
	}
	return len(b)
}
