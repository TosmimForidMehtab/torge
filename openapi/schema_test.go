package openapi_test

import (
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge/openapi"
)

type Money struct{ Cents int64 }

func (Money) OpenAPISchema() *openapi.Schema {
	return &openapi.Schema{Type: "string", Pattern: `^\d+\.\d{2}$`}
}

type Base struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
}

type Page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

type Item struct {
	Base
	Price    Money           `json:"price"`
	Raw      json.RawMessage `json:"raw"`
	Data     []byte          `json:"data"`
	IP       net.IP          `json:"ip"`
	Labels   map[string]int  `json:"labels" validate:"max=3,dive,gte=0"`
	Grid     [2]float32      `json:"grid"`
	Any      any             `json:"any"`
	Wait     time.Duration   `json:"wait"`
	Skip     string          `json:"-"`
	Optional *string         `json:"optional" validate:"omitempty,min=1"`
	//lint:ignore U1000 verifies that unexported fields are not documented
	private string
}

func TestGenerator(t *testing.T) {
	g := openapi.NewGenerator()
	ref := g.Schema(reflect.TypeFor[Page[Item]]())
	name := openapi.RefName(ref)
	if !strings.HasPrefix(name, "Page_") {
		t.Fatalf("generic component name %q", name)
	}
	page := g.Schemas()[name]
	if page.Properties["items"].Items.Ref != "#/components/schemas/Item" {
		t.Fatalf("items: %+v", page.Properties["items"])
	}
	item := g.Schemas()["Item"].Properties
	checks := map[string]func(*openapi.Schema) bool{
		"id":       func(s *openapi.Schema) bool { return s.Type == "string" }, // embedded fields are flattened
		"created":  func(s *openapi.Schema) bool { return s.Format == "date-time" },
		"price":    func(s *openapi.Schema) bool { return s.Pattern != "" },
		"raw":      func(s *openapi.Schema) bool { return s.Type == "" },
		"data":     func(s *openapi.Schema) bool { return s.Format == "byte" },
		"ip":       func(s *openapi.Schema) bool { return s.Type == "string" },
		"labels":   func(s *openapi.Schema) bool { return *s.MaxProperties == 3 && *s.AdditionalProperties.Minimum == 0 },
		"grid":     func(s *openapi.Schema) bool { return *s.MinItems == 2 && s.Items.Format == "float" },
		"any":      func(s *openapi.Schema) bool { return s.Type == "" },
		"wait":     func(s *openapi.Schema) bool { return s.Type == "integer" },
		"optional": func(s *openapi.Schema) bool { return *s.MinLength == 1 },
	}
	for field, ok := range checks {
		s, found := item[field]
		if !found || !ok(s) {
			t.Errorf("field %s: %+v", field, s)
		}
	}
	for _, hidden := range []string{"Skip", "-", "private", "Base"} {
		if _, found := item[hidden]; found {
			t.Errorf("field %s must not be documented", hidden)
		}
	}
}

func TestComponentNameCollisions(t *testing.T) {
	type Item struct{ A int }
	g := openapi.NewGenerator()
	a := openapi.RefName(g.Schema(reflect.TypeFor[Item]()))
	b := openapi.RefName(g.Schema(reflect.TypeFor[openapiItem]()))
	if a == b {
		t.Fatalf("distinct types must get distinct names: %q %q", a, b)
	}
}

type openapiItem = Item

func TestDocsHTMLEscapes(t *testing.T) {
	page, err := openapi.DocsHTML(openapi.Scalar, "<script>", "/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), "<script>alert") || strings.Contains(string(page), "<title><script>") {
		t.Fatal("titles must be escaped")
	}
}
