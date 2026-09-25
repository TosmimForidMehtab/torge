package torge

import (
	"mime"
	"strings"
	"testing"
)

// oldContentTypeMatches is the original implementation, kept as an oracle.
func oldContentTypeMatches(got, want string) bool {
	base := func(ct string) string {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil {
			return ct
		}
		return mt
	}
	g, w := base(got), base(want)
	if strings.EqualFold(g, w) {
		return true
	}
	return w == "application/json" && strings.HasSuffix(strings.ToLower(g), "+json")
}

func TestContentTypeMatchesAgreesWithMime(t *testing.T) {
	types := []string{
		"application/json", "APPLICATION/JSON", "Application/Json", " application/json ",
		"application/json; charset=utf-8", "application/json;charset=UTF-8", "application/json ;  charset=utf-8 ",
		"application/json; charset=latin1", "application/json; charset", "application/json; charset=\"utf-8\"",
		"application/json; foo=bar; charset=utf-8", "application/merge-patch+json", "application/vnd.api+JSON",
		"application/problem+json; charset=utf-8", "text/plain", "text/json", "application/x-www-form-urlencoded",
		"multipart/form-data; boundary=abc", "application/", "/json", "application", "", " ", ";", "application/json;",
		"application//json", "application/js on", "application/json\x00", "application/jsón", "+json", "a/b+json",
		"application/(json)", "application/json; charset=utf-8; charset=utf-8",
	}
	wants := []string{"application/json; charset=utf-8", "application/xml", "application/msgpack"}
	for _, want := range wants {
		for _, got := range types {
			if a, b := contentTypeMatches(got, want), oldContentTypeMatches(got, want); a != b {
				t.Errorf("contentTypeMatches(%q, %q) = %v, original = %v", got, want, a, b)
			}
		}
	}
}

func BenchmarkContentTypeMatches(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		contentTypeMatches("application/json; charset=utf-8", "application/json; charset=utf-8")
	}
}
