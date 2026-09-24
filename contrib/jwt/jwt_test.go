package torgejwt_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/auth"
	torgejwt "github.com/TosmimForidMehtab/torge/contrib/jwt"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

var secret = []byte("test-secret-at-least-32-bytes-long!!")

func sign(t *testing.T, method jwt.SigningMethod, claims jwt.MapClaims, key any) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifier(t *testing.T) {
	verify, err := torgejwt.New(torgejwt.Config{
		Key: secret, Algorithms: []string{"HS256"}, Issuer: "issuer", Audience: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := torgetest.NewApp(t)
	app.GET("/me", func(c *torge.Context) error {
		u, _ := torge.UserAs[*auth.User](c)
		return c.JSON(200, u)
	}, auth.Required(auth.Bearer(verify)), auth.RequireScopes("read"))
	tc := torgetest.New(t, app)

	now := time.Now()
	valid := jwt.MapClaims{
		"sub": "u1", "iss": "issuer", "aud": "api", "exp": now.Add(time.Hour).Unix(), "iat": now.Unix(),
		"email": "ada@example.com", "roles": []string{"admin"}, "scope": "read write",
	}
	tc.GET("/me").BearerToken(sign(t, jwt.SigningMethodHS256, valid, secret)).Do().
		ExpectStatus(200).ExpectJSONPath("sub", "u1").ExpectJSONPath("roles.0", "admin").ExpectJSONPath("scopes.1", "write")

	for name, claims := range map[string]jwt.MapClaims{
		"expired":      {"sub": "u1", "iss": "issuer", "aud": "api", "exp": now.Add(-time.Hour).Unix()},
		"no expiry":    {"sub": "u1", "iss": "issuer", "aud": "api"},
		"wrong issuer": {"sub": "u1", "iss": "other", "aud": "api", "exp": now.Add(time.Hour).Unix()},
		"wrong aud":    {"sub": "u1", "iss": "issuer", "aud": "web", "exp": now.Add(time.Hour).Unix()},
		"no subject":   {"iss": "issuer", "aud": "api", "exp": now.Add(time.Hour).Unix()},
	} {
		t.Run(name, func(t *testing.T) {
			tc.GET("/me").BearerToken(sign(t, jwt.SigningMethodHS256, claims, secret)).Do().
				ExpectStatus(401).ExpectErrorCode(auth.CodeInvalidCredentials)
		})
	}
	tc.GET("/me").BearerToken(sign(t, jwt.SigningMethodHS512, valid, secret)).Do().ExpectStatus(401)
	tc.GET("/me").BearerToken(sign(t, jwt.SigningMethodHS256, valid, []byte("other-secret-other-secret-other!!"))).Do().ExpectStatus(401)
	unsigned := sign(t, jwt.SigningMethodNone, valid, jwt.UnsafeAllowNoneSignatureType)
	tc.GET("/me").BearerToken(unsigned).Do().ExpectStatus(401)
}

func TestConfigValidation(t *testing.T) {
	if _, err := torgejwt.New(torgejwt.Config{Key: secret}); err == nil {
		t.Fatal("algorithms must be required")
	}
	if _, err := torgejwt.New(torgejwt.Config{Algorithms: []string{"HS256"}}); err == nil {
		t.Fatal("a key must be required")
	}
}
