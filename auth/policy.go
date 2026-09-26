package auth

import (
	"errors"

	"github.com/TosmimForidMehtab/torge"
)

// Policy authorizes the authenticated principal for a request. It is the
// same signature Require accepts: return nil to allow, any other error to
// deny. Non-*torge.Error denials become 403 FORBIDDEN with CodeForbidden
// when run through Require, exactly like RequireRoles and RequireScopes.
//
//	allowAdmin := auth.Policy(func(_ *torge.Context, p torge.Principal) error {
//	    if u, ok := p.(*auth.User); ok && u.HasRole("admin") {
//	        return nil
//	    }
//	    return errors.New("not an admin")
//	})
type Policy func(c *torge.Context, p torge.Principal) error

// errPolicyDenied backs Any with no passing policy and Not over an
// allowing policy. It is a plain error so Require maps it to 403
// FORBIDDEN with CodeForbidden, matching RequireRoles/RequireScopes.
var errPolicyDenied = errors.New("policy denied")

// errNotOwner backs the ownership helpers.
var errNotOwner = errors.New("not the resource owner")

// All returns a Policy that allows only when every policy allows. The
// first denial wins and is returned; an empty All allows.
//
//	mw := auth.Require(auth.All(
//	    auth.OwnerIs(
//	        func(c *torge.Context) string { return c.Param("id") },
//	        func(p torge.Principal) string { return p.ID() },
//	    ),
//	    allowAdmin,
//	))
func All(policies ...Policy) Policy {
	return func(c *torge.Context, p torge.Principal) error {
		for _, policy := range policies {
			if policy == nil {
				return errPolicyDenied
			}
			if err := policy(c, p); err != nil {
				return err
			}
		}
		return nil
	}
}

// Any returns a Policy that allows when at least one policy allows. The
// first success wins; when every policy denies, the last denial is
// returned. An empty Any denies.
//
//	mw := auth.Require(auth.Any(scopeRead, scopeWrite))
func Any(policies ...Policy) Policy {
	return func(c *torge.Context, p torge.Principal) error {
		if len(policies) == 0 {
			return errPolicyDenied
		}
		var err error
		for _, policy := range policies {
			if policy == nil {
				err = errPolicyDenied
				continue
			}
			if perr := policy(c, p); perr == nil {
				return nil
			} else {
				err = perr
			}
		}
		if err == nil {
			err = errPolicyDenied
		}
		return err
	}
}

// Not returns a Policy that inverts policy: it denies when policy allows
// and allows when policy denies. A nil policy counts as denying, so
// Not(nil) allows.
//
//	mw := auth.Require(auth.Not(banned))
func Not(policy Policy) Policy {
	return func(c *torge.Context, p torge.Principal) error {
		if policy == nil {
			return nil
		}
		if err := policy(c, p); err != nil {
			return nil
		}
		return errPolicyDenied
	}
}

// OwnerIs returns a Policy that allows only when the request subject
// equals the resource owner. subject extracts the subject from the
// request (for example c.Param("id")); owner extracts the owner from the
// principal (usually p.ID()). A mismatch, a nil principal, or a nil
// extractor denies.
//
//	mw := auth.Require(auth.OwnerIs(
//	    func(c *torge.Context) string { return c.Param("id") },
//	    func(p torge.Principal) string { return p.ID() },
//	))
func OwnerIs(subject func(*torge.Context) string, owner func(torge.Principal) string) Policy {
	return func(c *torge.Context, p torge.Principal) error {
		if subject == nil || owner == nil || p == nil {
			return errNotOwner
		}
		if subject(c) != owner(p) {
			return errNotOwner
		}
		return nil
	}
}

// RequireOwnerID returns a Policy that allows only when the principal's
// ID equals the path parameter param.
//
//	app.GET("/users/:id", handler,
//	    auth.Required(auth.Bearer(verify)),
//	    auth.Require(auth.RequireOwnerID("id")),
func RequireOwnerID(param string) Policy {
	return OwnerIs(
		func(c *torge.Context) string { return c.Param(param) },
		func(p torge.Principal) string { return p.ID() },
	)
}
