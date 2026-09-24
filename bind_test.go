package torge_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
	"github.com/TosmimForidMehtab/torge/validate"
)

type CreateUserRequest struct {
	Name  string   `json:"name" validate:"required,min=3"`
	Email string   `json:"email" validate:"required,email"`
	Tags  []string `json:"tags" validate:"max=3,dive,min=2"`
}

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func TestBindJSONValidation(t *testing.T) {
	app := torgetest.NewApp(t)
	app.POST("/users", func(c *torge.Context) error {
		var req CreateUserRequest
		if err := c.BindJSON(&req); err != nil {
			return err
		}
		return c.JSON(201, User{ID: "1", Name: req.Name, Email: req.Email})
	})
	tc := torgetest.New(t, app)
	tc.POST("/users").JSON(map[string]any{"name": "Ada", "email": "ada@example.com"}).Do().
		ExpectStatus(201).ExpectJSONPath("name", "Ada")

	res := tc.POST("/users").JSON(map[string]any{"name": "A", "email": "nope", "tags": []string{"x"}}).Do().
		ExpectStatus(422).ExpectErrorCode(torge.CodeValidation)
	var body struct {
		Error struct {
			Details []validate.FieldError `json:"details"`
		} `json:"error"`
	}
	res.DecodeJSON(&body)
	fields := map[string]string{}
	for _, d := range body.Error.Details {
		fields[d.Field] = d.Rule
	}
	if fields["name"] != "min" || fields["email"] != "email" || fields["tags[0]"] != "min" {
		t.Fatalf("unexpected details %+v", body.Error.Details)
	}

	tc.POST("/users").Body(strings.NewReader(`{"name":`), "application/json").Do().
		ExpectStatus(400).ExpectErrorCode(torge.CodeInvalidJSON)
	tc.POST("/users").Body(strings.NewReader(`{"name":"Ada"} {}`), "application/json").Do().
		ExpectStatus(400).ExpectErrorCode(torge.CodeInvalidJSON)
	tc.POST("/users").Body(strings.NewReader(`{"name":5}`), "application/json").Do().
		ExpectStatus(422).ExpectJSONPath("error.details.0.field", "name").ExpectJSONPath("error.details.0.message", "must be a string")
	tc.POST("/users").Body(strings.NewReader(`name=x`), "application/x-www-form-urlencoded").Do().
		ExpectStatus(415).ExpectErrorCode(torge.CodeUnsupportedMedia)
	tc.POST("/users").Do().ExpectStatus(400).ExpectJSONPath("error.message", "Request body is required")
	tc.POST("/users").Body(strings.NewReader(`{"name":"Ada","email":"a@b.co"}`), "application/merge-patch+json").Do().
		ExpectStatus(201)
}

type SearchInput struct {
	OrgID   string        `path:"org" validate:"required"`
	Query   string        `query:"q" validate:"max=20"`
	Page    int           `query:"page" default:"1" validate:"min=1"`
	Tags    []string      `query:"tag"`
	Since   *time.Time    `query:"since"`
	Timeout time.Duration `query:"timeout" default:"5s"`
	Trace   string        `header:"X-Trace"`
	Session string        `cookie:"session"`
}

func TestBindParameters(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/orgs/:org/search", func(c *torge.Context) error {
		var in SearchInput
		if err := c.Bind(&in); err != nil {
			return err
		}
		since := ""
		if in.Since != nil {
			since = in.Since.UTC().Format(time.RFC3339)
		}
		return c.JSON(200, map[string]any{
			"org": in.OrgID, "q": in.Query, "page": in.Page, "tags": in.Tags, "since": since,
			"timeout": in.Timeout.String(), "trace": in.Trace, "session": in.Session,
		})
	})
	tc := torgetest.New(t, app)
	tc.GET("/orgs/acme/search").Query("q", "go").Query("tag", "a").Query("tag", "b").
		Query("since", "2024-01-02T03:04:05Z").Header("X-Trace", "t1").
		Cookie(&http.Cookie{Name: "session", Value: "s1"}).Do().
		ExpectStatus(200).
		ExpectJSON(map[string]any{
			"org": "acme", "q": "go", "page": 1, "tags": []string{"a", "b"}, "since": "2024-01-02T03:04:05Z",
			"timeout": "5s", "trace": "t1", "session": "s1",
		})
	tc.GET("/orgs/acme/search").Query("page", "x").Query("since", "yesterday").Do().
		ExpectStatus(422).
		ExpectJSONPath("error.details.0.field", "query.page").
		ExpectJSONPath("error.details.1.field", "query.since")
	tc.GET("/orgs/acme/search").Query("page", "0").Do().ExpectStatus(422).
		ExpectJSONPath("error.details.0.field", "page")
}

type UpdateInput struct {
	ID   string `path:"id" json:"id"`
	Name string `json:"name" validate:"required"`
}

func TestBodyCannotOverrideParameters(t *testing.T) {
	app := torgetest.NewApp(t)
	app.PUT("/items/:id", func(c *torge.Context) error {
		var in UpdateInput
		if err := c.Bind(&in); err != nil {
			return err
		}
		return c.JSON(200, in)
	})
	torgetest.New(t, app).PUT("/items/real").JSON(map[string]string{"id": "forged", "name": "x"}).Do().
		ExpectStatus(200).ExpectJSON(map[string]string{"id": "real", "name": "x"})
}

type GetUserInput struct {
	ID string `path:"id" validate:"uuid"`
}

type CreateUserInput struct {
	Org  string `path:"org"`
	Body CreateUserRequest
}

func TestTypedHandlers(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/users/:id", func(c *torge.Context, in *GetUserInput) (*User, error) {
		if in.ID == "00000000-0000-0000-0000-000000000000" {
			return nil, torge.NotFound("USER_NOT_FOUND", "User does not exist")
		}
		return &User{ID: in.ID, Name: "Ada"}, nil
	})
	orgs := app.Group("/orgs/:org")
	torge.Post(orgs, "/users", func(c *torge.Context, in *CreateUserInput) (*User, error) {
		return &User{ID: in.Org + "-1", Name: in.Body.Name, Email: in.Body.Email}, nil
	}, torge.Status(http.StatusCreated))
	torge.Delete(app, "/users/:id", func(c *torge.Context, in *GetUserInput) (*torge.Empty, error) {
		return nil, nil
	})

	tc := torgetest.New(t, app)
	id := "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	tc.GET("/users/"+id).Do().ExpectStatus(200).ExpectJSONPath("id", id)
	tc.GET("/users/not-a-uuid").Do().ExpectStatus(422).ExpectJSONPath("error.details.0.rule", "uuid")
	tc.GET("/users/00000000-0000-0000-0000-000000000000").Do().ExpectStatus(404).ExpectErrorCode("USER_NOT_FOUND")
	tc.POST("/orgs/acme/users").JSON(map[string]string{"name": "Grace", "email": "g@example.com"}).Do().
		ExpectStatus(201).ExpectJSON(User{ID: "acme-1", Name: "Grace", Email: "g@example.com"})
	tc.POST("/orgs/acme/users").JSON(map[string]string{"name": "G"}).Do().ExpectStatus(422)
	tc.DELETE("/users/" + id).Do().ExpectStatus(204)

	routes := app.Routes()
	if routes[0].Handler != "torge_test.TestTypedHandlers" {
		t.Fatalf("typed routes should report the user's handler, got %q", routes[0].Handler)
	}
}

type BadInput struct {
	Ch chan int `query:"c"`
}

type BadTags struct {
	Name string `json:"name" validate:"min=abc"`
}

func TestTypedHandlerSetupErrorsAreDiagnostics(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/bad", func(c *torge.Context, in *BadInput) (*torge.Empty, error) { return nil, nil })
	torge.Post(app, "/tags", func(c *torge.Context, in *BadTags) (*torge.Empty, error) { return nil, nil })
	err := app.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "unsupported parameter type") {
		t.Fatalf("expected unsupported parameter diagnostic, got %v", err)
	}
	app2 := torgetest.NewApp(t)
	torge.Post(app2, "/tags", func(c *torge.Context, in *BadTags) (*torge.Empty, error) { return nil, nil })
	err = app2.Start(t.Context())
	var diags torge.Diagnostics
	if !errors.As(err, &diags) || !strings.Contains(err.Error(), "invalid numeric parameter") {
		t.Fatalf("expected tag diagnostic, got %v", err)
	}
}
