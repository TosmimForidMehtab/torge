package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/auth"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func verifyToken(_ context.Context, token string) (torge.Principal, error) {
	switch token {
	case "admin-token":
		return &auth.User{Subject: "u1", Roles: []string{"admin"}, Scopes: []string{"read", "write"}}, nil
	case "reader-token":
		return &auth.User{Subject: "u2", Scopes: []string{"read"}}, nil
	case "suspended":
		return nil, torge.Forbidden("ACCOUNT_SUSPENDED", "Account suspended")
	}
	return nil, auth.ErrInvalidCredentials
}

func newApp(t *testing.T) *torgetest.Client {
	app := torgetest.NewApp(t)
	keys := auth.StaticKeys(map[string]torge.Principal{"key-1": &auth.User{Subject: "svc"}})
	api := app.Group("/api", auth.Required(auth.Bearer(verifyToken), auth.APIKey(auth.APIKeyConfig{Lookup: keys})))
	api.GET("/me", func(c *torge.Context) error {
		u, _ := torge.UserAs[*auth.User](c)
		if torge.UserFrom(c.Context()) != c.User() {
			t.Error("principal must be attached to the request context")
		}
		return c.String(200, u.Subject)
	})
	api.DELETE("/things", func(c *torge.Context) error { return c.NoContent(204) }, auth.RequireRoles("admin"))
	api.POST("/things", func(c *torge.Context) error { return c.NoContent(201) }, auth.RequireScopes("read", "write"))
	app.GET("/public", func(c *torge.Context) error {
		if c.User() == nil {
			return c.String(200, "anonymous")
		}
		return c.String(200, c.User().ID())
	}, auth.Optional(auth.Bearer(verifyToken)))
	app.GET("/basic", func(c *torge.Context) error { return c.String(200, c.User().ID()) },
		auth.BasicAuth("admin", map[string]string{"alice": "s3cret"}))
	return torgetest.New(t, app)
}

func TestAuthenticators(t *testing.T) {
	tc := newApp(t)
	tc.GET("/api/me").Do().ExpectStatus(401).ExpectErrorCode(auth.CodeAuthenticationRequired).
		ExpectHeader("WWW-Authenticate", "Bearer")
	tc.GET("/api/me").BearerToken("admin-token").Do().ExpectStatus(200).ExpectBody("u1")
	tc.GET("/api/me").BearerToken("wrong").Do().ExpectStatus(401).ExpectErrorCode(auth.CodeInvalidCredentials)
	tc.GET("/api/me").BearerToken("suspended").Do().ExpectStatus(403).ExpectErrorCode("ACCOUNT_SUSPENDED")
	tc.GET("/api/me").Header("X-API-Key", "key-1").Do().ExpectStatus(200).ExpectBody("svc")
	tc.GET("/api/me").Header("X-API-Key", "key-2").Do().ExpectStatus(401)

	tc.GET("/public").Do().ExpectBody("anonymous")
	tc.GET("/public").BearerToken("reader-token").Do().ExpectBody("u2")
	tc.GET("/public").BearerToken("wrong").Do().ExpectStatus(401)

	tc.GET("/basic").BasicAuth("alice", "s3cret").Do().ExpectStatus(200).ExpectBody("alice")
	tc.GET("/basic").BasicAuth("alice", "nope").Do().ExpectStatus(401).
		ExpectHeader("WWW-Authenticate", `Basic realm="admin", charset="UTF-8"`)
}

func TestAuthorization(t *testing.T) {
	tc := newApp(t)
	tc.DELETE("/api/things").BearerToken("admin-token").Do().ExpectStatus(204)
	tc.DELETE("/api/things").BearerToken("reader-token").Do().ExpectStatus(403).ExpectErrorCode(auth.CodeForbidden)
	tc.POST("/api/things").BearerToken("admin-token").Do().ExpectStatus(201)
	tc.POST("/api/things").BearerToken("reader-token").Do().ExpectStatus(403)
}

func TestRequirePolicy(t *testing.T) {
	app := torgetest.NewApp(t)
	owner := auth.Require(func(c *torge.Context, p torge.Principal) error {
		if p.ID() != c.Param("id") {
			return errors.New("not the owner")
		}
		return nil
	})
	app.GET("/users/:id", func(c *torge.Context) error { return c.NoContent(204) },
		auth.Required(auth.Bearer(verifyToken)), owner)
	tc := torgetest.New(t, app)
	tc.GET("/users/u1").BearerToken("admin-token").Do().ExpectStatus(204)
	tc.GET("/users/u2").BearerToken("admin-token").Do().ExpectStatus(403)
}

func TestConstantTimeCredentialChecks(t *testing.T) {
	ctx := context.Background()
	for i := range 2 { // two verifiers, each with its own random key
		verify := auth.BasicUsers(map[string]string{"alice": "s3cret", "bob": ""})
		if p, err := verify(ctx, "alice", "s3cret"); err != nil || p.ID() != "alice" {
			t.Fatalf("verifier %d: valid credentials rejected: %v", i, err)
		}
		if _, err := verify(ctx, "bob", ""); err != nil {
			t.Fatalf("verifier %d: empty configured password must still match exactly", i)
		}
		for _, bad := range [][2]string{
			{"alice", "s3cre"}, {"alice", "s3cret!"}, {"alice", ""}, {"alice", "S3CRET"},
			{"s3cret", "alice"}, {"alic", "s3cret"}, {"bob", "x"}, {"", ""},
		} {
			if _, err := verify(ctx, bad[0], bad[1]); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Errorf("verifier %d: %q/%q must be rejected, got %v", i, bad[0], bad[1], err)
			}
		}
	}

	lookup := auth.StaticKeys(map[string]torge.Principal{"key-123": &auth.User{Subject: "svc"}})
	if p, err := lookup(ctx, "key-123"); err != nil || p.ID() != "svc" {
		t.Fatalf("valid key rejected: %v", err)
	}
	for _, bad := range []string{"key-12", "key-1234", "", "KEY-123"} {
		if _, err := lookup(ctx, bad); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("key %q must be rejected, got %v", bad, err)
		}
	}
}
