package torge

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

func TestLimitedBodyMatchesMaxBytesReader(t *testing.T) {
	readers := map[string]func(string) io.Reader{
		"whole":   func(s string) io.Reader { return strings.NewReader(s) },
		"onebyte": func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) },
		"half":    func(s string) io.Reader { return iotest.HalfReader(strings.NewReader(s)) },
	}
	for name, mk := range readers {
		for size := range 12 {
			for limit := int64(0); limit < 10; limit++ {
				body := strings.Repeat("x", size)
				want, wantErr := io.ReadAll(http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(mk(body)), limit))
				lb := &limitedBody{rc: io.NopCloser(mk(body)), left: limit, limit: limit}
				got, gotErr := io.ReadAll(lb)
				if string(got) != string(want) || errors.Is(gotErr, &http.MaxBytesError{}) != errors.Is(wantErr, &http.MaxBytesError{}) ||
					(gotErr == nil) != (wantErr == nil) {
					t.Fatalf("%s size=%d limit=%d: got (%q, %v), want (%q, %v)", name, size, limit, got, gotErr, want, wantErr)
				}
				var mbe *http.MaxBytesError
				if gotErr != nil && (!errors.As(gotErr, &mbe) || mbe.Limit != limit) {
					t.Fatalf("%s size=%d limit=%d: error %v, want *http.MaxBytesError{%d}", name, size, limit, gotErr, limit)
				}
			}
		}
	}
}

func TestJSONDecodeEdgeCases(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}
	tests := []struct {
		body    string
		strict  bool
		wantEOF bool
		wantErr bool
	}{
		{body: "", wantEOF: true},
		{body: " \r\n\t ", wantEOF: true},
		{body: `{"name":"ada"}`},
		{body: " {\"name\":\"ada\"}\n"},
		{body: `{"name":"ada"} {}`, wantErr: true},
		{body: `{"name":"ada"}}`, wantErr: true},
		{body: `{"name":"ada"}]`, wantErr: true},
		{body: `{"name":"ada"`, wantErr: true},
		{body: `{"name":1}`, wantErr: true},
		{body: `{"name":"ada","x":1}`},
		{body: `{"name":"ada","x":1}`, strict: true, wantErr: true},
		{body: `{"name":"ada"} x`, strict: true, wantErr: true},
		{body: `{"name":"ada"}  `, strict: true},
	}
	for _, tt := range tests {
		for _, hint := range []int64{-1, 0, int64(len(tt.body)), int64(len(tt.body)) + 7, 3} {
			var p payload
			err := JSONSerializer{DisallowUnknownFields: tt.strict}.decode(iotest.HalfReader(strings.NewReader(tt.body)), hint, &p)
			switch {
			case tt.wantEOF:
				if !errors.Is(err, io.EOF) {
					t.Errorf("decode(%q, hint %d) = %v, want io.EOF", tt.body, hint, err)
				}
			case tt.wantErr:
				if err == nil || errors.Is(err, io.EOF) {
					t.Errorf("decode(%q, hint %d) = %v, want an error", tt.body, hint, err)
				}
			default:
				if err != nil || p.Name != "ada" {
					t.Errorf("decode(%q, hint %d) = %v, %+v", tt.body, hint, err, p)
				}
			}
		}
	}
}
