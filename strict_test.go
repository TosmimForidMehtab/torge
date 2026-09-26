package torge_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
	"github.com/TosmimForidMehtab/torge/validate"
)

type StrictSearchInput struct {
	torge.Strict
	Q string `query:"q" validate:"max=20"`
}

type StrictBody struct {
	Name string `json:"name" validate:"required"`
}

type StrictCreateInput struct {
	torge.Strict
	Body StrictBody
}

type CommaSearchInput struct {
	Tags []string `query:"tag,comma"`
	Nums []int    `query:"n,comma"`
}

type LayoutQueryInput struct {
	Day time.Time  `query:"day" layout:"2006-01-02"`
	At  *time.Time `query:"at" layout:"15:04"`
}

func TestStrictQuery(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/s", func(c *torge.Context, in *StrictSearchInput) (*User, error) {
		return &User{Name: in.Q}, nil
	})
	tc := torgetest.New(t, app)

	tc.GET("/s?q=hi").Do().ExpectStatus(200).ExpectJSONPath("name", "hi")
	res := tc.GET("/s?q=hi&bogus=1").Do().ExpectStatus(422).ExpectErrorCode(torge.CodeValidation)
	var body struct {
		Error struct {
			Details []validate.FieldError `json:"details"`
		} `json:"error"`
	}
	res.DecodeJSON(&body)
	if len(body.Error.Details) != 1 || body.Error.Details[0].Field != "query.bogus" {
		t.Fatalf("unexpected strict-query details %+v", body.Error.Details)
	}
}

func TestStrictQueryOffByDefault(t *testing.T) {
	app := torgetest.NewApp(t)
	app.GET("/lenient", func(c *torge.Context) error {
		var in struct {
			Q string `query:"q"`
		}
		if err := c.Bind(&in); err != nil {
			return err
		}
		return c.JSON(200, map[string]string{"q": in.Q})
	})
	tc := torgetest.New(t, app)
	tc.GET("/lenient?q=a&whatever=1").Do().ExpectStatus(200).ExpectJSONPath("q", "a")
}

func TestStrictBody(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Post(app, "/users", func(c *torge.Context, in *StrictCreateInput) (*User, error) {
		return &User{Name: in.Body.Name}, nil
	}, torge.Status(201))
	tc := torgetest.New(t, app)

	tc.POST("/users").JSON(map[string]string{"name": "Ada"}).Do().
		ExpectStatus(201).ExpectJSONPath("name", "Ada")
	tc.POST("/users").JSON(map[string]string{"name": "Ada", "nick": "Al"}).Do().
		ExpectStatus(400).ExpectErrorCode(torge.CodeInvalidJSON)
}

func TestStrictBodyJSON(t *testing.T) {
	app := torgetest.NewApp(t)
	app.POST("/raw", func(c *torge.Context) error {
		var in StrictCreateInput
		if err := c.BindJSON(&in); err != nil {
			return err
		}
		return c.JSON(200, map[string]string{"name": in.Body.Name})
	})
	tc := torgetest.New(t, app)
	tc.POST("/raw").JSON(map[string]string{"name": "Ada", "x": "1"}).Do().
		ExpectStatus(400).ExpectErrorCode(torge.CodeInvalidJSON)
}

func TestCommaSeparatedParams(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/tags", func(c *torge.Context, in *CommaSearchInput) (*CommaSearchInput, error) {
		return in, nil
	})
	tc := torgetest.New(t, app)

	tc.GET("/tags?tag=a,b&tag=c&n=1,2").Do().
		ExpectStatus(200).
		ExpectJSONPath("Tags.0", "a").
		ExpectJSONPath("Tags.2", "c").
		ExpectJSONPath("Nums.1", 2)
}

func TestTimeLayoutParam(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/day", func(c *torge.Context, in *LayoutQueryInput) (*map[string]string, error) {
		out := map[string]string{"day": in.Day.Format("2006-01-02")}
		if in.At != nil {
			out["at"] = in.At.Format("15:04")
		}
		return &out, nil
	})
	tc := torgetest.New(t, app)

	tc.GET("/day?day=2026-09-26&at=08:30").Do().
		ExpectStatus(200).
		ExpectJSONPath("day", "2026-09-26").
		ExpectJSONPath("at", "08:30")
	tc.GET("/day?day=09/26/2026").Do().ExpectStatus(422).ExpectErrorCode(torge.CodeValidation)
}

func formTestContext(t *testing.T) *torge.Context {
	t.Helper()
	app := torgetest.NewApp(t)
	return app.NewContext(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
}

func TestBindFormValues(t *testing.T) {
	c := formTestContext(t)
	type Signup struct {
		Name string   `query:"name" validate:"required,min=2"`
		Tags []string `query:"tag,comma"`
	}
	var s Signup
	if err := c.BindFormValues(&s, url.Values{"name": {"Ada"}, "tag": {"a,b"}}); err != nil {
		t.Fatal(err)
	}
	if s.Name != "Ada" || len(s.Tags) != 2 || s.Tags[1] != "b" {
		t.Fatalf("bad form binding %+v", s)
	}

	var bad Signup
	err := c.BindFormValues(&bad, url.Values{"name": {"x"}})
	if got := torge.AsError(err); got.Code != torge.CodeValidation {
		t.Fatalf("expected validation failure, got %+v", err)
	}

	var strict StrictSearchInput
	err = c.BindFormValues(&strict, url.Values{"q": {"a"}, "nope": {"1"}})
	if got := torge.AsError(err); got.Code != torge.CodeValidation {
		t.Fatalf("expected strict rejection, got %+v", err)
	}

	var notStruct string
	if err := c.BindFormValues(&notStruct, url.Values{}); err == nil {
		t.Fatal("expected pointer-to-struct error")
	}
}

func TestBindingCompileErrors(t *testing.T) {
	c := formTestContext(t)
	type BadComma struct {
		Tag string `query:"tag,comma"`
	}
	if err := c.BindFormValues(&BadComma{}, url.Values{}); err == nil ||
		!strings.Contains(err.Error(), "comma") {
		t.Fatalf("expected comma error, got %v", err)
	}

	type BadOpt struct {
		Tag []string `query:"tag,frobnicate"`
	}
	if err := c.BindFormValues(&BadOpt{}, url.Values{}); err == nil ||
		!strings.Contains(err.Error(), "unknown tag option") {
		t.Fatalf("expected option error, got %v", err)
	}

	type BadLayout struct {
		N int `query:"n" layout:"2006-01-02"`
	}
	if err := c.BindFormValues(&BadLayout{}, url.Values{}); err == nil ||
		!strings.Contains(err.Error(), "layout") {
		t.Fatalf("expected layout error, got %v", err)
	}
}

type StrictBodyOnlyInput struct {
	torge.Strict
	Name string `json:"name"`
}

func TestStrictQueryWithoutParams(t *testing.T) {
	app := torgetest.NewApp(t)
	app.POST("/body-only", func(c *torge.Context) error {
		var in StrictBodyOnlyInput
		if err := c.Bind(&in); err != nil {
			return err
		}
		return c.JSON(200, map[string]string{"name": in.Name})
	})
	tc := torgetest.New(t, app)

	// No query fields declared: every query parameter is undeclared, and
	// details must arrive in a deterministic (sorted) order.
	res := tc.POST("/body-only?z=1&a=1").JSON(map[string]string{"name": "Ada"}).Do().
		ExpectStatus(422).ExpectErrorCode(torge.CodeValidation)
	var body struct {
		Error struct {
			Details []validate.FieldError `json:"details"`
		} `json:"error"`
	}
	res.DecodeJSON(&body)
	if len(body.Error.Details) != 2 ||
		body.Error.Details[0].Field != "query.a" ||
		body.Error.Details[1].Field != "query.z" {
		t.Fatalf("details must be sorted, got %+v", body.Error.Details)
	}
	tc.POST("/body-only").JSON(map[string]string{"name": "Ada"}).Do().ExpectStatus(200)
}
