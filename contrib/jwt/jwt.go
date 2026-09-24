// Package torgejwt verifies JSON Web Tokens for auth.Bearer using
// github.com/golang-jwt/jwt/v5. Algorithms must be listed explicitly, and
// issuer, audience and expiry are enforced.
//
//	verifier, err := torgejwt.New(torgejwt.Config{
//	    Key:        []byte(cfg.JWTSecret.Value()),
//	    Algorithms: []string{"HS256"},
//	    Issuer:     "https://auth.example.com",
//	    Audience:   "api",
//	})
//	api.Use(auth.Required(auth.Bearer(verifier)))
//
// For OIDC providers publishing a JWKS, pass a Keyfunc from a JWKS library
// such as github.com/MicahParks/keyfunc.
package torgejwt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/auth"
)

// Config configures token verification.
type Config struct {
	// Key is the verification key: []byte for HMAC, or a public key
	// (*rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey). Ignored when
	// Keyfunc is set.
	Key any
	// Keyfunc selects the key per token (for key rotation or JWKS).
	Keyfunc jwt.Keyfunc
	// Algorithms lists accepted signing algorithms. Required: accepting
	// whatever the token declares enables algorithm-confusion attacks.
	Algorithms []string
	// Issuer, if set, must match the iss claim.
	Issuer string
	// Audience, if set, must be in the aud claim.
	Audience string
	// Leeway tolerates clock skew for exp, nbf and iat (default 30s).
	Leeway time.Duration
	// Principal converts verified claims into a principal. The default
	// builds an *auth.User from sub, name, email, roles and scope/scp.
	Principal func(claims jwt.MapClaims) (torge.Principal, error)
}

// New returns a verifier for auth.Bearer.
func New(cfg Config) (auth.TokenVerifier, error) {
	if len(cfg.Algorithms) == 0 {
		return nil, errors.New("torgejwt: Algorithms must be set explicitly")
	}
	if cfg.Key == nil && cfg.Keyfunc == nil {
		return nil, errors.New("torgejwt: Key or Keyfunc is required")
	}
	keyfunc := cfg.Keyfunc
	if keyfunc == nil {
		keyfunc = func(*jwt.Token) (any, error) { return cfg.Key, nil }
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 30 * time.Second
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods(cfg.Algorithms),
		jwt.WithLeeway(cfg.Leeway),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	}
	if cfg.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(cfg.Issuer))
	}
	if cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(cfg.Audience))
	}
	parser := jwt.NewParser(opts...)
	toPrincipal := cfg.Principal
	if toPrincipal == nil {
		toPrincipal = DefaultPrincipal
	}
	return func(_ context.Context, token string) (torge.Principal, error) {
		claims := jwt.MapClaims{}
		if _, err := parser.ParseWithClaims(token, claims, keyfunc); err != nil {
			return nil, fmt.Errorf("%w: %w", auth.ErrInvalidCredentials, err)
		}
		return toPrincipal(claims)
	}, nil
}

// DefaultPrincipal maps standard claims onto an *auth.User.
func DefaultPrincipal(claims jwt.MapClaims) (torge.Principal, error) {
	sub, err := claims.GetSubject()
	if err != nil || sub == "" {
		return nil, fmt.Errorf("%w: token has no subject", auth.ErrInvalidCredentials)
	}
	u := &auth.User{Subject: sub, Claims: claims}
	u.Name, _ = claims["name"].(string)
	u.Email, _ = claims["email"].(string)
	u.Roles = stringList(claims["roles"])
	if scope, ok := claims["scope"].(string); ok {
		u.Scopes = strings.Fields(scope)
	} else {
		u.Scopes = stringList(claims["scp"])
	}
	return u, nil
}

func stringList(v any) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return strings.Fields(x)
	}
	return nil
}
