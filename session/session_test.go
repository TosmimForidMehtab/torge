package session_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/auth"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/session"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func sessionCookie(t *testing.T, res *torgetest.Response) *http.Cookie {
	t.Helper()
	for _, ck := range res.Result.Cookies() {
		if ck.Name == "torge_session" {
			return ck
		}
	}
	return nil
}

func TestSessions(t *testing.T) {
	store := cache.NewMemory()
	defer store.Close()
	sessions := session.New(session.Config{Store: store})
	app := torgetest.NewApp(t)
	app.Use(sessions.Middleware())
	app.POST("/login", func(c *torge.Context) error {
		s := session.Get(c)
		s.Regenerate()
		if err := s.Set("user_id", "u1"); err != nil {
			return err
		}
		return c.NoContent(204)
	})
	app.GET("/me", func(c *torge.Context) error {
		return c.String(200, c.User().ID())
	}, auth.Required(session.Authenticator("user_id", func(_ context.Context, id string) (torge.Principal, error) {
		return &auth.User{Subject: id}, nil
	})))
	app.POST("/logout", func(c *torge.Context) error {
		session.Get(c).Destroy()
		return c.NoContent(204)
	})
	app.GET("/anonymous", func(c *torge.Context) error { return c.String(200, "hi") })
	tc := torgetest.New(t, app)

	if ck := sessionCookie(t, tc.GET("/anonymous").Do()); ck != nil {
		t.Fatal("untouched sessions must not create cookies or store entries")
	}
	tc.GET("/me").Do().ExpectStatus(401)

	// A pre-existing (attacker-chosen) session ID is replaced at login.
	fixed := &http.Cookie{Name: "torge_session", Value: strings.Repeat("A", 43)}
	login := tc.POST("/login").Cookie(fixed).Do().ExpectStatus(204)
	ck := sessionCookie(t, login)
	if ck == nil || ck.Value == fixed.Value || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected cookie %+v", ck)
	}
	tc.GET("/me").Cookie(&http.Cookie{Name: ck.Name, Value: ck.Value}).Do().ExpectStatus(200).ExpectBody("u1")

	logout := tc.POST("/logout").Cookie(&http.Cookie{Name: ck.Name, Value: ck.Value}).Do().ExpectStatus(204)
	if cleared := sessionCookie(t, logout); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatal("logout must expire the cookie")
	}
	tc.GET("/me").Cookie(&http.Cookie{Name: ck.Name, Value: ck.Value}).Do().ExpectStatus(401)
}
