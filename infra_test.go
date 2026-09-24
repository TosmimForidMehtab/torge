package torge_test

import (
	"context"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/jobs"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type welcome struct{ done chan string }

func (w welcome) Handle(_ context.Context, j *jobs.Job) error {
	var p struct{ Name string }
	if err := j.Decode(&p); err != nil {
		return err
	}
	w.done <- p.Name
	return nil
}

func TestJobsAndCacheIntegration(t *testing.T) {
	app := torgetest.NewApp(t)
	done := make(chan string, 1)
	app.Job("welcome", welcome{done: done})
	store := cache.NewMemory()
	app.Cache(store)
	app.POST("/signup", func(c *torge.Context) error {
		m := torge.MustDep[*jobs.Manager](c)
		if err := m.Dispatch(c.Context(), "welcome", map[string]string{"Name": "Ada"}); err != nil {
			return err
		}
		s := torge.MustDep[cache.Store](c)
		if err := s.Set(c.Context(), "last", []byte("Ada"), time.Minute); err != nil {
			return err
		}
		return c.NoContent(202)
	})
	tc := torgetest.New(t, app)
	tc.POST("/signup").Do().ExpectStatus(202)
	select {
	case name := <-done:
		if name != "Ada" {
			t.Fatalf("got %q", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("job did not run")
	}
	if v, _ := store.Get(context.Background(), "last"); string(v) != "Ada" {
		t.Fatal("cache must be injectable")
	}

	dup := torgetest.NewApp(t)
	dup.Job("x", welcome{})
	dup.Job("x", welcome{})
	if err := dup.Start(context.Background()); diagCodes(err) != torge.DiagInvalidHandler {
		t.Fatalf("got %v", err)
	}
}
