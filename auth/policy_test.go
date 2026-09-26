package auth_test

import (
	"errors"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/auth"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

var errTestDenyA = errors.New("deny A")
var errTestDenyB = errors.New("deny B")

func allowPolicy(_ *torge.Context, _ torge.Principal) error { return nil }

func denyPolicy(err error) auth.Policy {
	return func(_ *torge.Context, _ torge.Principal) error { return err }
}

func rolePolicy(role string) auth.Policy {
	return func(_ *torge.Context, p torge.Principal) error {
		r, ok := p.(interface{ HasRole(string) bool })
		if !ok || !r.HasRole(role) {
			return errors.New("missing required role")
		}
		return nil
	}
}

func scopePolicy(scope string) auth.Policy {
	return func(_ *torge.Context, p torge.Principal) error {
		s, ok := p.(interface{ HasScope(string) bool })
		if !ok || !s.HasScope(scope) {
			return errors.New("missing required scope")
		}
		return nil
	}
}

func TestAll(t *testing.T) {
	p := &auth.User{Subject: "u1"}
	if err := auth.All(allowPolicy, allowPolicy)(nil, p); err != nil {
		t.Fatalf("All(all-pass) = %v, want nil", err)
	}
	if err := auth.All()(nil, p); err != nil {
		t.Fatalf("All() = %v, want nil", err)
	}
	if err := auth.All(allowPolicy, denyPolicy(errTestDenyA))(nil, p); !errors.Is(err, errTestDenyA) {
		t.Fatalf("All with one denial = %v, want %v", err, errTestDenyA)
	}
	// First failure wins.
	err := auth.All(denyPolicy(errTestDenyA), denyPolicy(errTestDenyB))(nil, p)
	if !errors.Is(err, errTestDenyA) {
		t.Fatalf("All(first failure wins) = %v, want %v", err, errTestDenyA)
	}
	if err := auth.All(nil, allowPolicy)(nil, p); err == nil {
		t.Fatal("All(nil, allow) must deny, not allow")
	}
}

func TestAny(t *testing.T) {
	p := &auth.User{Subject: "u1"}
	if err := auth.Any(denyPolicy(errTestDenyA), allowPolicy)(nil, p); err != nil {
		t.Fatalf("Any(one-passes) = %v, want nil", err)
	}
	if err := auth.Any(allowPolicy, denyPolicy(errTestDenyA))(nil, p); err != nil {
		t.Fatalf("Any(first success wins) = %v, want nil", err)
	}
	if got := auth.Any(denyPolicy(errTestDenyA), denyPolicy(errTestDenyB))(nil, p); got == nil {
		t.Fatal("Any(all-deny) must deny, got nil")
	}
	if err := auth.Any()(nil, p); err == nil {
		t.Fatal("Any() must deny, got nil")
	}
	// All-deny surfaces the last denial.
	if err := auth.Any(denyPolicy(errTestDenyA), denyPolicy(errTestDenyB))(nil, p); !errors.Is(err, errTestDenyB) {
		t.Fatalf("Any(all-deny) = %v, want last error %v", err, errTestDenyB)
	}
}

func TestNot(t *testing.T) {
	p := &auth.User{Subject: "u1"}
	if err := auth.Not(denyPolicy(errTestDenyA))(nil, p); err != nil {
		t.Fatalf("Not(deny) = %v, want nil", err)
	}
	if err := auth.Not(allowPolicy)(nil, p); err == nil {
		t.Fatal("Not(allow) must deny, got nil")
	}
	if err := auth.Not(auth.Not(allowPolicy))(nil, p); err != nil {
		t.Fatalf("Not(Not(allow)) = %v, want nil", err)
	}
	if err := auth.Not(nil)(nil, p); err != nil {
		t.Fatalf("Not(nil) = %v, want nil (nil counts as denying)", err)
	}
}

func TestOwnerIs(t *testing.T) {
	owner := &auth.User{Subject: "u1"}
	subject := func(id string) func(*torge.Context) string {
		return func(_ *torge.Context) string { return id }
	}
	byID := func(p torge.Principal) string { return p.ID() }

	if err := auth.OwnerIs(subject("u1"), byID)(nil, owner); err != nil {
		t.Fatalf("OwnerIs(match) = %v, want nil", err)
	}
	if err := auth.OwnerIs(subject("u2"), byID)(nil, owner); err == nil {
		t.Fatal("OwnerIs(mismatch) must deny, got nil")
	}
	if err := auth.OwnerIs(subject("u1"), byID)(nil, nil); err == nil {
		t.Fatal("OwnerIs(nil principal) must deny, got nil")
	}
	if err := auth.OwnerIs(nil, byID)(nil, owner); err == nil {
		t.Fatal("OwnerIs(nil subject) must deny, got nil")
	}
	if err := auth.OwnerIs(subject("u1"), nil)(nil, owner); err == nil {
		t.Fatal("OwnerIs(nil owner) must deny, got nil")
	}
}

func TestPolicyComposition(t *testing.T) {
	adminReader := &auth.User{Subject: "u1", Roles: []string{"admin"}, Scopes: []string{"read"}}
	adminOnly := &auth.User{Subject: "u2", Roles: []string{"admin"}}
	readerOnly := &auth.User{Subject: "u3", Scopes: []string{"read"}}

	composed := auth.All(rolePolicy("admin"), auth.Any(scopePolicy("read"), scopePolicy("write")))
	if err := composed(nil, adminReader); err != nil {
		t.Fatalf("composed(admin+read) = %v, want nil", err)
	}
	if err := composed(nil, adminOnly); err == nil {
		t.Fatal("composed(admin, no scopes) must deny, got nil")
	}
	if err := composed(nil, readerOnly); err == nil {
		t.Fatal("composed(no admin role) must deny, got nil")
	}
	negated := auth.All(rolePolicy("admin"), auth.Not(scopePolicy("suspended")))
	if err := negated(nil, adminReader); err != nil {
		t.Fatalf("All(admin, Not(suspended)) = %v, want nil", err)
	}
	suspended := &auth.User{Subject: "u4", Roles: []string{"admin"}, Scopes: []string{"suspended"}}
	if err := negated(nil, suspended); err == nil {
		t.Fatal("All(admin, Not(suspended)) with suspended scope must deny, got nil")
	}
}

func TestPolicyDenialsMapToForbidden(t *testing.T) {
	app := torgetest.NewApp(t)
	authed := auth.Required(auth.Bearer(verifyToken))
	app.GET("/combo", func(c *torge.Context) error { return c.NoContent(204) },
		authed, auth.Require(auth.All(rolePolicy("admin"), auth.Any(scopePolicy("read"), scopePolicy("write")))))
	app.GET("/neg", func(c *torge.Context) error { return c.NoContent(204) },
		authed, auth.Require(auth.Not(scopePolicy("write"))))
	app.GET("/users/:id", func(c *torge.Context) error { return c.NoContent(204) },
		authed, auth.Require(auth.RequireOwnerID("id")))
	app.GET("/owner-fn", func(c *torge.Context) error { return c.NoContent(204) },
		authed, auth.Require(auth.OwnerIs(
			func(c *torge.Context) string { return c.Query("as") },
			func(p torge.Principal) string { return p.ID() },
		)))
	tc := torgetest.New(t, app)

	//Composition: admin-token has admin role plus read+write scopes.
	tc.GET("/combo").BearerToken("admin-token").Do().ExpectStatus(204)
	// reader-token lacks the admin role, so the composed All denies with 403 FORBIDDEN.
	tc.GET("/combo").BearerToken("reader-token").Do().ExpectStatus(403).ExpectErrorCode(auth.CodeForbidden)

	// Not: reader-token has only "read", so Not(write) allows; admin-token
	// has "write", so Not(write) denies with 403.
	tc.GET("/neg").BearerToken("reader-token").Do().ExpectStatus(204)
	tc.GET("/neg").BearerToken("admin-token").Do().ExpectStatus(403).ExpectErrorCode(auth.CodeForbidden)

	// Ownership via path param: admin-token is u1.
	tc.GET("/users/u1").BearerToken("admin-token").Do().ExpectStatus(204)
	tc.GET("/users/u2").BearerToken("admin-token").Do().ExpectStatus(403).ExpectErrorCode(auth.CodeForbidden)

	// Ownership via custom extractors.
	tc.GET("/owner-fn?as=u1").BearerToken("admin-token").Do().ExpectStatus(204)
	tc.GET("/owner-fn?as=u2").BearerToken("admin-token").Do().ExpectStatus(403).ExpectErrorCode(auth.CodeForbidden)

	// Unauthenticated requests still get 401 before any policy runs.
	tc.GET("/combo").Do().ExpectStatus(401).ExpectErrorCode(auth.CodeAuthenticationRequired)
}
