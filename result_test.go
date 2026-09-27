package torge_test

import (
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestResultEnvelope(t *testing.T) {
	app := torgetest.NewApp(t)
	torge.Get(app, "/users/:id", func(c *torge.Context, in *struct {
		ID string `path:"id"`
	}) (*torge.Result[User], error) {
		return torge.NewResult(User{ID: in.ID, Name: "Ada"}).WithMeta("cache", "hit"), nil
	})
	tc := torgetest.New(t, app)

	tc.GET("/users/1").Do().
		ExpectStatus(200).
		ExpectJSONPath("data.name", "Ada").
		ExpectJSONPath("meta.cache", "hit")
}

func TestResultOmitsEmptyMeta(t *testing.T) {
	r := torge.NewResult("x")
	if r.Meta != nil {
		t.Fatalf("fresh result must have nil meta %+v", r)
	}
}

func TestProblemErrorHandler(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithErrorHandler(torge.ProblemErrorHandler))
	app.GET("/missing", func(c *torge.Context) error {
		return torge.NotFound("USER_NOT_FOUND", "User does not exist")
	})
	tc := torgetest.New(t, app)

	res := tc.GET("/missing").Do().
		ExpectStatus(404).
		ExpectHeader("Content-Type", "application/problem+json").
		ExpectJSONPath("type", "about:blank").
		ExpectJSONPath("title", "Not Found").
		ExpectJSONPath("detail", "User does not exist").
		ExpectJSONPath("status", 404).
		ExpectJSONPath("code", "USER_NOT_FOUND").
		ExpectJSONPath("instance", "/missing")
	var problem torge.ProblemBody
	res.DecodeJSON(&problem)
	if problem.RequestID == "" {
		t.Fatalf("problem details must carry a request id %+v", problem)
	}
}
