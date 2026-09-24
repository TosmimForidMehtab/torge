package torge_test

import (
	"encoding/json"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/openapi"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type Address struct {
	City string `json:"city" validate:"required" doc:"City name" example:"Berlin"`
}

type Profile struct {
	ID        string    `json:"id" format:"uuid" readonly:"true"`
	Name      string    `json:"name" validate:"required,min=2,max=50"`
	Age       int       `json:"age,omitempty" validate:"gte=0,lte=150"`
	Role      string    `json:"role" validate:"oneof=admin member"`
	Addresses []Address `json:"addresses" validate:"max=5,dive"`
	Manager   *Profile  `json:"manager,omitempty"`
}

type ListProfilesInput struct {
	Org   string `path:"org"`
	Limit int    `query:"limit" default:"20" validate:"min=1,max=100" doc:"Page size"`
	Trace string `header:"X-Trace"`
}

type CreateProfileInput struct {
	Org  string `path:"org"`
	Body Profile
}

func TestOpenAPIGeneration(t *testing.T) {
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{
		Info:            openapi.Info{Title: "Profiles", Version: "2.0.0"},
		SecuritySchemes: map[string]*openapi.SecurityScheme{"bearer": openapi.BearerAuth("JWT")},
	})
	orgs := app.Group("/orgs/:org", torge.Tags("profiles"), torge.Security("bearer"))
	torge.Get(orgs, "/profiles", func(c *torge.Context, in *ListProfilesInput) (*[]Profile, error) {
		return &[]Profile{}, nil
	}, torge.Summary("List profiles"), torge.Name("profiles.list"))
	torge.Post(orgs, "/profiles", func(c *torge.Context, in *CreateProfileInput) (*Profile, error) {
		return &in.Body, nil
	}, torge.Status(201), torge.Returns[torge.ErrorBody](409, "Profile exists"))
	app.GET("/public/:slug", func(c *torge.Context) error { return nil }, torge.Public(), torge.Returns[Profile](200, "A profile"))
	app.GET("/internal", func(c *torge.Context) error { return nil }, torge.Hidden())

	tc := torgetest.New(t, app)
	res := tc.GET("/openapi.json").Do().ExpectStatus(200)
	tc.GET("/docs").Do().ExpectStatus(200).ExpectBodyContains("swagger-ui")

	var doc openapi.Document
	res.DecodeJSON(&doc)
	if doc.OpenAPI != "3.1.0" || doc.Info.Title != "Profiles" {
		t.Fatalf("bad header %+v", doc.Info)
	}
	if _, ok := doc.Paths["/internal"]; ok {
		t.Fatal("hidden routes must not be documented")
	}
	if _, ok := doc.Paths["/openapi.json"]; ok {
		t.Fatal("spec route must be hidden")
	}

	list := doc.Paths["/orgs/{org}/profiles"]["get"]
	if list == nil || list.OperationID != "profiles.list" || list.Summary != "List profiles" || list.Tags[0] != "profiles" {
		t.Fatalf("list operation: %+v", list)
	}
	params := map[string]*openapi.Parameter{}
	for _, p := range list.Parameters {
		params[p.In+":"+p.Name] = p
	}
	limit := params["query:limit"]
	if limit == nil || *limit.Schema.Minimum != 1 || *limit.Schema.Maximum != 100 || limit.Schema.Default != float64(20) || limit.Description != "Page size" {
		t.Fatalf("limit parameter: %+v", limit)
	}
	if p := params["path:org"]; p == nil || !p.Required {
		t.Fatal("path parameters must be required")
	}
	if params["header:X-Trace"] == nil {
		t.Fatal("header parameter missing")
	}
	if list.Security == nil || (*list.Security)[0]["bearer"] == nil {
		t.Fatal("group security must be documented")
	}
	if list.Responses["200"].Content["application/json"].Schema.Items.Ref != "#/components/schemas/Profile" {
		t.Fatalf("list response schema: %+v", list.Responses["200"])
	}

	create := doc.Paths["/orgs/{org}/profiles"]["post"]
	if create.RequestBody == nil || !create.RequestBody.Required ||
		create.RequestBody.Content["application/json"].Schema.Ref != "#/components/schemas/Profile" {
		t.Fatalf("create body: %+v", create.RequestBody)
	}
	for _, code := range []string{"201", "409", "422", "default"} {
		if create.Responses[code] == nil {
			t.Fatalf("missing response %s in %v", code, create.Responses)
		}
	}
	public := doc.Paths["/public/{slug}"]["get"]
	if public.Security == nil || len(*public.Security) != 0 {
		t.Fatal("Public() must document an empty security requirement")
	}

	profile := doc.Components.Schemas["Profile"]
	name := profile.Properties["name"]
	if *name.MinLength != 2 || *name.MaxLength != 50 {
		t.Fatalf("name constraints: %+v", name)
	}
	if len(profile.Required) != 1 || profile.Required[0] != "name" {
		t.Fatalf("required: %v", profile.Required)
	}
	if role := profile.Properties["role"]; len(role.Enum) != 2 {
		t.Fatalf("role enum: %+v", role)
	}
	if addrs := profile.Properties["addresses"]; *addrs.MaxItems != 5 || addrs.Items.Ref != "#/components/schemas/Address" {
		t.Fatalf("addresses: %+v", addrs)
	}
	if profile.Properties["manager"].Ref != "#/components/schemas/Profile" {
		t.Fatal("recursive types must use references")
	}
	if !profile.Properties["id"].ReadOnly || profile.Properties["id"].Format != "uuid" {
		t.Fatal("doc tags must apply")
	}
	city := doc.Components.Schemas["Address"].Properties["city"]
	if city.Description != "City name" || city.Examples[0] != "Berlin" {
		t.Fatalf("city: %+v", city)
	}
	if doc.Components.SecuritySchemes["bearer"].Scheme != "bearer" {
		t.Fatal("security scheme missing")
	}
	if _, err := app.OpenAPIDocument(); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
}
