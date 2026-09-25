package torge_test

import (
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/openapi"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type ListUsersInput struct {
	torge.PageParams
	torge.Search
	Role string `query:"role" validate:"omitempty,oneof=admin member"`
	Sort string `query:"sort"`
}

func listUsersHandler(c *torge.Context, in *ListUsersInput) (*torge.Page[User], error) {
	if in.Sort != "" {
		if _, err := torge.ParseSort(in.Sort, "name", "email"); err != nil {
			return nil, err
		}
	}
	total := 53
	c.SetPageLinks("/users", in.PageParams, total)
	users := []User{{ID: "1", Name: "Ada", Email: "ada@example.com"}}
	return torge.NewPage(users, total, in.PageParams), nil
}

func TestPageParamsDefaultsAndEnvelope(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/users", listUsersHandler)
	tc := torgetest.New(t, app)

	tc.GET("/users").Do().
		ExpectStatus(200).
		ExpectJSONPath("page", 1).
		ExpectJSONPath("page_size", 20).
		ExpectJSONPath("total", 53).
		ExpectJSONPath("pages", 3).
		ExpectJSONPath("items.0.name", "Ada").
		ExpectHeaderPresent("Link")

	tc.GET("/users?page=2&page_size=25").Do().
		ExpectStatus(200).
		ExpectJSONPath("page", 2).
		ExpectJSONPath("page_size", 25).
		ExpectJSONPath("pages", 3)
}

func TestPageParamsValidation(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/users", listUsersHandler)
	tc := torgetest.New(t, app)

	tc.GET("/users?page=0").Do().ExpectStatus(422).ExpectErrorCode(torge.CodeValidation)
	tc.GET("/users?page_size=500").Do().ExpectStatus(422).ExpectErrorCode(torge.CodeValidation)
	tc.GET("/users?role=owner").Do().ExpectStatus(422).ExpectErrorCode(torge.CodeValidation)
	tc.GET("/users?sort=-name,email").Do().ExpectStatus(200)
	tc.GET("/users?sort=bogus").Do().ExpectStatus(400).ExpectErrorCode(torge.CodeBadRequest)
}

func TestNewPageMath(t *testing.T) {
	p := torge.NewPage([]string{"a"}, 53, torge.PageParams{Page: 1, PageSize: 20})
	if p.Pages != 3 || !p.HasNext() || p.HasPrev() {
		t.Fatalf("bad first page %+v", p)
	}
	last := torge.NewPage([]string{"a"}, 53, torge.PageParams{Page: 3, PageSize: 20})
	if !last.HasPrev() || last.HasNext() {
		t.Fatalf("bad last page %+v", last)
	}
	empty := torge.NewPage[string](nil, 0, torge.PageParams{Page: 1, PageSize: 20})
	if empty.Pages != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("bad empty page %+v", empty)
	}
	var nilPage *torge.Page[string]
	if nilPage.HasNext() || nilPage.HasPrev() {
		t.Fatal("nil page must report no next/prev")
	}
}

func TestCursorPage(t *testing.T) {
	p := torge.NewCursorPage([]string{"a", "b"}, "opaque-cursor")
	if !p.HasMore || p.NextCursor != "opaque-cursor" {
		t.Fatalf("bad cursor page %+v", p)
	}
	done := torge.NewCursorPage([]string{"a"}, "")
	if done.HasMore {
		t.Fatalf("final cursor page must not have more %+v", done)
	}
	params := torge.CursorParams{}
	if params.CursorLimit() != torge.DefaultPageSize {
		t.Fatalf("bad default cursor limit %d", params.CursorLimit())
	}
	huge := torge.CursorParams{Limit: 10000}
	if huge.CursorLimit() != torge.MaxPageSize {
		t.Fatalf("cursor limit not clamped %d", huge.CursorLimit())
	}
}

func TestParseSort(t *testing.T) {
	clauses, err := torge.ParseSort("-name,email", "name", "email")
	if err != nil {
		t.Fatal(err)
	}
	if len(clauses) != 2 || !clauses[0].Desc || clauses[0].Field != "name" || clauses[1].Desc {
		t.Fatalf("bad clauses %+v", clauses)
	}
	if _, err := torge.ParseSort("", "name"); err != nil {
		t.Fatal(err)
	} else if clauses, _ := torge.ParseSort("  ", "name"); clauses != nil {
		t.Fatalf("empty sort must yield nil, got %+v", clauses)
	}
	err = mustSortError(t, "age", "name")
	if got := torge.AsError(err); got.Code != torge.CodeBadRequest {
		t.Fatalf("bad sort code %+v", got)
	}
}

func mustSortError(t *testing.T, param, allowed string) error {
	t.Helper()
	_, err := torge.ParseSort(param, allowed)
	if err == nil {
		t.Fatalf("expected error for sort %q", param)
	}
	return err
}

func TestSetPageLinks(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/users", listUsersHandler)
	tc := torgetest.New(t, app)

	tc.GET("/users?page=2&page_size=10").Do().
		ExpectStatus(200).
		ExpectHeader("Link", `</users?page=1&page_size=10>; rel="first", </users?page=6&page_size=10>; rel="last", </users?page=1&page_size=10>; rel="prev", </users?page=3&page_size=10>; rel="next"`)

	res := tc.GET("/users?page=6&page_size=10").Do().ExpectStatus(200)
	link := res.Header.Get("Link")
	if link == "" {
		t.Fatal("missing Link header on last page")
	}
	for _, want := range []string{`rel="first"`, `rel="last"`, `rel="prev"`} {
		if !strings.Contains(link, want) {
			t.Fatalf("link %q missing %s", link, want)
		}
	}
	if strings.Contains(link, `rel="next"`) {
		t.Fatalf("last page must not link next: %q", link)
	}
}

func TestPageOpenAPI(t *testing.T) {
	app := torgetest.NewApp(t)
	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "Users", Version: "1.0.0"}})
	torge.Get(app, "/users", listUsersHandler, torge.Summary("List users"))
	tc := torgetest.New(t, app)

	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	tc.GET("/openapi.json").Do().ExpectStatus(200).DecodeJSON(&doc)
	params := doc.Paths["/users"]["get"].Parameters
	names := map[string]string{}
	for _, p := range params {
		names[p.Name] = p.In
	}
	for name, loc := range map[string]string{"page": "query", "page_size": "query", "q": "query", "role": "query"} {
		if names[name] != loc {
			t.Fatalf("openapi missing %s query param, got %+v", name, names)
		}
	}
}
