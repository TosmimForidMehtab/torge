package torge_test

import (
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/openapi"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type AvatarForm struct {
	File string `json:"file" format:"binary" doc:"Image to upload"`
	Alt  string `json:"alt" validate:"max=140"`
}

type CardPayment struct {
	Method string `json:"method"`
	Card   string `json:"card"`
}

type PolymorphicPayment struct{}

func (PolymorphicPayment) OpenAPISchema() *openapi.Schema {
	return &openapi.Schema{
		OneOf: []*openapi.Schema{
			{Ref: "#/components/schemas/CardPayment"},
			{Ref: "#/components/schemas/BankPayment"},
		},
		Discriminator: &openapi.Discriminator{PropertyName: "method"},
	}
}

func openAPIExtraDoc(t *testing.T, app *torge.App) openapi.Document {
	t.Helper()
	tc := torgetest.New(t, app)
	var doc openapi.Document
	tc.GET("/openapi.json").Do().ExpectStatus(200).DecodeJSON(&doc)
	return doc
}

func TestOpenAPIMultipartBody(t *testing.T) {
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "T", Version: "1"}})
	app.POST("/avatar", func(c *torge.Context) error { return c.Status(201) },
		torge.Body[AvatarForm](),
		torge.RequestContentType("multipart/form-data"),
		torge.Status(201))
	doc := openAPIExtraDoc(t, app)

	body := doc.Paths["/avatar"]["post"].RequestBody
	media := body.Content["multipart/form-data"]
	if media == nil {
		t.Fatalf("multipart content missing: %+v", body.Content)
	}
	form := doc.Components.Schemas["AvatarForm"]
	if form == nil || form.Properties["file"] == nil || form.Properties["file"].Format != "binary" {
		t.Fatalf("file upload schema missing binary format: %+v", form)
	}
}

type ExampleCreateInput struct {
	Body Profile
}

func TestOpenAPIExamples(t *testing.T) {
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "T", Version: "1"}})
	torge.Post(app, "/users", func(c *torge.Context, in *ExampleCreateInput) (*Profile, error) {
		return &in.Body, nil
	},
		torge.Status(201),
		torge.Returns[Profile](201, "Created"),
		torge.ResponseExample(201, map[string]string{"name": "Ada"}),
		torge.RequestExample(map[string]string{"name": "Ada"}))
	doc := openAPIExtraDoc(t, app)

	post := doc.Paths["/users"]["post"]
	resp := post.Responses["201"]
	ex, _ := resp.Content["application/json"].Example.(map[string]any)
	if ex["name"] != "Ada" {
		t.Fatalf("response example missing: %+v", resp.Content["application/json"].Example)
	}
	if post.RequestBody == nil {
		t.Fatal("request body missing")
	}
	rex, _ := post.RequestBody.Content["application/json"].Example.(map[string]any)
	if rex["name"] != "Ada" {
		t.Fatalf("request example missing: %+v", post.RequestBody.Content["application/json"].Example)
	}
	// The typed success schema must survive the example-only merge.
	schema := resp.Content["application/json"].Schema
	if schema == nil || (schema.Ref == "" && schema.Type == "") {
		t.Fatalf("success schema lost: %+v", schema)
	}
}

func TestOpenAPIScopes(t *testing.T) {
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "T", Version: "1"}})
	api := app.Group("/v1", torge.Security("oauth"))
	api.GET("/me", func(c *torge.Context) error { return c.Status(200) },
		torge.Scopes("oauth", "profile:read"))
	doc := openAPIExtraDoc(t, app)

	sec := *doc.Paths["/v1/me"]["get"].Security
	if len(sec) != 1 || len(sec[0]["oauth"]) != 1 || sec[0]["oauth"][0] != "profile:read" {
		t.Fatalf("scopes missing: %+v", sec)
	}
}

func TestOpenAPIDiscriminator(t *testing.T) {
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "T", Version: "1"}})
	torge.Get(app, "/payment", func(c *torge.Context, in *torge.Empty) (*PolymorphicPayment, error) {
		return &PolymorphicPayment{}, nil
	})
	doc := openAPIExtraDoc(t, app)

	schema := doc.Paths["/payment"]["get"].Responses["200"].Content["application/json"].Schema
	if schema == nil || len(schema.OneOf) != 2 || schema.Discriminator == nil ||
		schema.Discriminator.PropertyName != "method" {
		t.Fatalf("discriminator missing: %+v", schema)
	}
}
