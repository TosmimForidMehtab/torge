// Package auth provides authentication primitives: an Authenticator contract,
// middleware that runs one or more authenticators and exposes the principal
// through c.User(), and ready-made bearer token, API key and basic
// authenticators. Token formats (JWT, OIDC) plug in as bearer verifiers; see
// contrib/jwt.
//
//	app.Use(auth.Required(
//	    auth.Bearer(jwtVerifier),
//	    auth.APIKey(auth.APIKeyConfig{Lookup: keys.Lookup}),
//	))
//
//	func Handler(c *torge.Context) error {
//	    user := c.User()
//	    ...
//	}
//
// Authorization stays application-specific; Require, RequireRoles and
// RequireScopes cover the common checks.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"slices"
	"strings"

	"github.com/TosmimForidMehtab/torge"
)

// ErrNoCredentials is returned by an Authenticator when the request carries
// no credentials it understands, so the next authenticator is tried.
var ErrNoCredentials = errors.New("auth: no credentials")

// ErrInvalidCredentials is a convenience error for verifiers.
var ErrInvalidCredentials = errors.New("auth: invalid credentials")

// Error codes.
const (
	CodeAuthenticationRequired = "AUTHENTICATION_REQUIRED"
	CodeInvalidCredentials     = "INVALID_CREDENTIALS"
	CodeForbidden              = "FORBIDDEN"
)

// Authenticator identifies the caller of a request.
type Authenticator interface {
	// Authenticate returns the principal, ErrNoCredentials if the request has
	// no credentials for this authenticator, or another error if credentials
	// are present but invalid. Returning a *torge.Error controls the
	// response exactly.
	Authenticate(c *torge.Context) (torge.Principal, error)
}

// Challenger is implemented by authenticators that contribute a
// WWW-Authenticate challenge.
type Challenger interface {
	Challenge() string
}

// Func adapts a function to Authenticator.
type Func func(c *torge.Context) (torge.Principal, error)

// Authenticate implements Authenticator.
func (f Func) Authenticate(c *torge.Context) (torge.Principal, error) { return f(c) }

// User is a general-purpose Principal.
type User struct {
	Subject string         `json:"sub"`
	Name    string         `json:"name,omitempty"`
	Email   string         `json:"email,omitempty"`
	Roles   []string       `json:"roles,omitempty"`
	Scopes  []string       `json:"scopes,omitempty"`
	Claims  map[string]any `json:"claims,omitempty"`
}

// ID implements torge.Principal.
func (u *User) ID() string { return u.Subject }

// HasRole reports whether the user has role.
func (u *User) HasRole(role string) bool { return slices.Contains(u.Roles, role) }

// HasScope reports whether the user has scope.
func (u *User) HasScope(scope string) bool { return slices.Contains(u.Scopes, scope) }

// Required returns middleware that authenticates every request with the
// first authenticator that recognizes its credentials, and rejects requests
// without valid credentials with 401.
func Required(authenticators ...Authenticator) torge.Middleware {
	return middleware(false, authenticators)
}

// Optional is like Required but lets requests without credentials through
// unauthenticated. Invalid credentials are still rejected.
func Optional(authenticators ...Authenticator) torge.Middleware {
	return middleware(true, authenticators)
}

func middleware(optional bool, authenticators []Authenticator) torge.Middleware {
	var challenges []string
	for _, a := range authenticators {
		if ch, ok := a.(Challenger); ok && ch.Challenge() != "" {
			challenges = append(challenges, ch.Challenge())
		}
	}
	challenge := strings.Join(challenges, ", ")
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			if c.User() != nil {
				return next(c)
			}
			for _, a := range authenticators {
				p, err := a.Authenticate(c)
				if errors.Is(err, ErrNoCredentials) {
					continue
				}
				if err != nil {
					var te *torge.Error
					if errors.As(err, &te) {
						return err
					}
					if challenge != "" {
						c.Header("WWW-Authenticate", challenge)
					}
					return torge.Unauthorized(CodeInvalidCredentials, "Invalid credentials").Wrap(err)
				}
				if p == nil {
					continue
				}
				c.SetUser(p)
				return next(c)
			}
			if optional {
				return next(c)
			}
			if challenge != "" {
				c.Header("WWW-Authenticate", challenge)
			}
			return torge.Unauthorized(CodeAuthenticationRequired, "Authentication required")
		}
	}
}

// TokenVerifier validates a bearer token and returns its principal.
type TokenVerifier func(ctx context.Context, token string) (torge.Principal, error)

type bearer struct{ verify TokenVerifier }

// Bearer authenticates "Authorization: Bearer <token>" headers with verify.
func Bearer(verify TokenVerifier) Authenticator { return bearer{verify} }

func (b bearer) Authenticate(c *torge.Context) (torge.Principal, error) {
	scheme, token, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return nil, ErrNoCredentials
	}
	return b.verify(c.Context(), strings.TrimSpace(token))
}

func (bearer) Challenge() string { return "Bearer" }

// APIKeyConfig configures API key authentication.
type APIKeyConfig struct {
	// Header carries the key (default X-API-Key).
	Header string
	// Query optionally accepts the key from a query parameter. Keys in URLs
	// end up in logs and browser history; prefer the header.
	Query string
	// Lookup returns the principal owning key. Required.
	Lookup func(ctx context.Context, key string) (torge.Principal, error)
}

type apiKey struct{ cfg APIKeyConfig }

// APIKey authenticates requests carrying an API key.
func APIKey(cfg APIKeyConfig) Authenticator {
	if cfg.Header == "" {
		cfg.Header = "X-API-Key"
	}
	if cfg.Lookup == nil {
		panic("auth: APIKeyConfig.Lookup is required")
	}
	return apiKey{cfg}
}

func (a apiKey) Authenticate(c *torge.Context) (torge.Principal, error) {
	key := c.GetHeader(a.cfg.Header)
	if key == "" && a.cfg.Query != "" {
		key = c.Query(a.cfg.Query)
	}
	if key == "" {
		return nil, ErrNoCredentials
	}
	return a.cfg.Lookup(c.Context(), key)
}

// StaticKeys returns an API key lookup over a fixed key set. Keys are
// compared in constant time (see newComparer).
func StaticKeys(keys map[string]torge.Principal) func(context.Context, string) (torge.Principal, error) {
	tag := newComparer()
	type entry struct {
		tag []byte
		p   torge.Principal
	}
	entries := make([]entry, 0, len(keys))
	for k, p := range keys {
		entries = append(entries, entry{tag(k), p})
	}
	return func(_ context.Context, key string) (torge.Principal, error) {
		t := tag(key)
		var found torge.Principal
		for _, e := range entries {
			if subtle.ConstantTimeCompare(t, e.tag) == 1 {
				found = e.p
			}
		}
		if found == nil {
			return nil, ErrInvalidCredentials
		}
		return found, nil
	}
}

// BasicVerifier validates a username and password.
type BasicVerifier func(ctx context.Context, username, password string) (torge.Principal, error)

type basic struct {
	realm  string
	verify BasicVerifier
}

// Basic authenticates HTTP Basic credentials. Only use it over HTTPS.
func Basic(realm string, verify BasicVerifier) Authenticator {
	if realm == "" {
		realm = "restricted"
	}
	return basic{realm: realm, verify: verify}
}

func (b basic) Authenticate(c *torge.Context) (torge.Principal, error) {
	user, pass, ok := c.Request().BasicAuth()
	if !ok {
		return nil, ErrNoCredentials
	}
	return b.verify(c.Context(), user, pass)
}

func (b basic) Challenge() string {
	return `Basic realm="` + strings.ReplaceAll(b.realm, `"`, `'`) + `", charset="UTF-8"`
}

// BasicUsers returns a verifier over fixed credentials, compared in constant
// time (see newComparer).
func BasicUsers(users map[string]string) BasicVerifier {
	tag := newComparer()
	type entry struct{ user, pass []byte }
	entries := make([]entry, 0, len(users))
	for u, p := range users {
		entries = append(entries, entry{tag(u), tag(p)})
	}
	return func(_ context.Context, username, password string) (torge.Principal, error) {
		u, p := tag(username), tag(password)
		ok := 0
		for _, e := range entries {
			ok |= subtle.ConstantTimeCompare(u, e.user) & subtle.ConstantTimeCompare(p, e.pass)
		}
		if ok != 1 {
			return nil, ErrInvalidCredentials
		}
		return &User{Subject: username}, nil
	}
}

// newComparer returns a function mapping a secret to a fixed-length tag for
// constant-time comparison: HMAC-SHA256 under a random key created for this
// comparer. Equal inputs give equal tags; comparing tags instead of the raw
// values takes the same time wherever, and whether, the inputs differ, and
// does not reveal their length. The key never leaves the process and tags are
// never stored or exposed, so this is not password storage; secrets that
// must be stored should use a password hashing function instead.
func newComparer() func(string) []byte {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // crypto/rand.Read never fails; it aborts the program instead
	return func(s string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(s))
		return m.Sum(nil)
	}
}

// BasicAuth is shorthand for Required(Basic(realm, BasicUsers(users))).
func BasicAuth(realm string, users map[string]string) torge.Middleware {
	return Required(Basic(realm, BasicUsers(users)))
}

// Require returns middleware that authorizes the authenticated principal with
// policy. Requests without a principal get 401; a policy error gets 403
// unless it is a *torge.Error.
func Require(policy func(c *torge.Context, p torge.Principal) error) torge.Middleware {
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			p := c.User()
			if p == nil {
				return torge.Unauthorized(CodeAuthenticationRequired, "Authentication required")
			}
			if err := policy(c, p); err != nil {
				var te *torge.Error
				if errors.As(err, &te) {
					return err
				}
				return torge.Forbidden(CodeForbidden, "You do not have permission to perform this action").Wrap(err)
			}
			return next(c)
		}
	}
}

var errMissingRole = errors.New("missing required role")

// RequireRoles requires a principal with at least one of roles. The principal
// must implement HasRole(string) bool (as *User does).
func RequireRoles(roles ...string) torge.Middleware {
	return Require(func(_ *torge.Context, p torge.Principal) error {
		r, ok := p.(interface{ HasRole(string) bool })
		if !ok {
			return errMissingRole
		}
		if slices.ContainsFunc(roles, r.HasRole) {
			return nil
		}
		return errMissingRole
	})
}

var errMissingScope = errors.New("missing required scope")

// RequireScopes requires a principal holding all scopes. The principal must
// implement HasScope(string) bool (as *User does).
func RequireScopes(scopes ...string) torge.Middleware {
	return Require(func(_ *torge.Context, p torge.Principal) error {
		s, ok := p.(interface{ HasScope(string) bool })
		if !ok {
			return errMissingScope
		}
		for _, scope := range scopes {
			if !s.HasScope(scope) {
				return errMissingScope
			}
		}
		return nil
	})
}
