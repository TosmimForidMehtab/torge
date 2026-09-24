// Package session provides server-side sessions referenced by a secure
// cookie. Session data lives in a cache.Store (use a shared store such as
// Redis when running several instances); the cookie carries only a random
// 256-bit ID.
//
//	sessions := session.New(session.Config{Store: store})
//	app.Use(sessions.Middleware())
//
//	func Login(c *torge.Context) error {
//	    s := session.Get(c)
//	    s.Regenerate() // prevent session fixation
//	    return s.Set("user_id", user.ID)
//	}
//
// Changes are saved automatically just before the response headers are
// written, so handlers never need to call Save.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/auth"
	"github.com/TosmimForidMehtab/torge/cache"
)

// Config configures sessions.
type Config struct {
	// Store holds session data. Required.
	Store cache.Store
	// CookieName defaults to "torge_session".
	CookieName string
	// TTL is the idle timeout (default 24h). Active sessions are extended.
	TTL time.Duration
	// Path and Domain scope the cookie (Path defaults to "/").
	Path   string
	Domain string
	// SameSite defaults to Lax.
	SameSite http.SameSite
	// Insecure omits the Secure attribute, for plain-HTTP development
	// outside localhost. Never enable it in production.
	Insecure bool
}

// Manager creates session middleware.
type Manager struct {
	cfg   Config
	store cache.Store
}

// New returns a Manager.
func New(cfg Config) *Manager {
	if cfg.Store == nil {
		panic(&torge.Diagnostic{Code: torge.DiagInvalidConfig, What: "session.Config.Store is nil",
			Why: "sessions cannot be stored", Fix: "pass a cache.Store (shared across instances in production)"})
	}
	if cfg.CookieName == "" {
		cfg.CookieName = "torge_session"
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.Path == "" {
		cfg.Path = "/"
	}
	if cfg.SameSite == 0 {
		cfg.SameSite = http.SameSiteLaxMode
	}
	return &Manager{cfg: cfg, store: cache.Namespace(cfg.Store, "torge:session:")}
}

type record struct {
	Values  map[string]json.RawMessage `json:"v"`
	Expires time.Time                  `json:"e"`
}

// Session is one client's session. It is request-scoped and not safe for
// concurrent use.
type Session struct {
	id         string
	values     map[string]json.RawMessage
	expires    time.Time
	modified   bool
	destroyed  bool
	regenerate bool
	oldID      string
}

type ctxKey struct{}

// Get returns the request's session. It panics if the session middleware is
// not installed, which is a setup error.
func Get(c *torge.Context) *Session {
	v, ok := c.Get(ctxKey{})
	if !ok {
		panic("session: middleware not installed; add sessions.Middleware() before using session.Get")
	}
	return v.(*Session)
}

// ID returns the session ID, or "" for a new session not yet saved.
func (s *Session) ID() string { return s.id }

// IsNew reports whether the session did not exist before this request.
func (s *Session) IsNew() bool { return s.id == "" }

// Get decodes the value stored under key into dst and reports whether it was
// present.
func (s *Session) Get(key string, dst any) (bool, error) {
	raw, ok := s.values[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dst)
}

// Set stores v (JSON-encoded) under key.
func (s *Session) Set(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("session: encode %q: %w", key, err)
	}
	s.values[key] = raw
	s.modified = true
	return nil
}

// Delete removes key.
func (s *Session) Delete(key string) {
	if _, ok := s.values[key]; ok {
		delete(s.values, key)
		s.modified = true
	}
}

// Regenerate issues a new session ID while keeping the data. Call it when the
// privilege level changes (login, logout, role change) to prevent session
// fixation.
func (s *Session) Regenerate() {
	if s.id != "" && s.oldID == "" {
		s.oldID = s.id
	}
	s.id = ""
	s.regenerate = true
	s.modified = true
}

// Destroy deletes the session and its cookie.
func (s *Session) Destroy() {
	s.destroyed = true
	s.values = map[string]json.RawMessage{}
}

// Middleware loads the session and saves changes before headers are written.
func (m *Manager) Middleware() torge.Middleware {
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			s := m.load(c)
			c.Set(ctxKey{}, s)
			c.Response().Before(func() { m.commit(c, s) })
			return next(c)
		}
	}
}

func (m *Manager) load(c *torge.Context) *Session {
	s := &Session{values: map[string]json.RawMessage{}}
	ck, err := c.Cookie(m.cfg.CookieName)
	if err != nil || !validID(ck.Value) {
		return s
	}
	data, err := m.store.Get(c.Context(), ck.Value)
	if err != nil {
		if !errors.Is(err, cache.ErrNotFound) {
			c.Logger().Error("session: load failed", "error", err)
		}
		return s
	}
	var rec record
	if json.Unmarshal(data, &rec) != nil || time.Now().After(rec.Expires) {
		return s
	}
	s.id, s.values, s.expires = ck.Value, rec.Values, rec.Expires
	if s.values == nil {
		s.values = map[string]json.RawMessage{}
	}
	return s
}

func (m *Manager) commit(c *torge.Context, s *Session) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), 5*time.Second)
	defer cancel()
	if s.destroyed {
		for _, id := range []string{s.id, s.oldID} {
			if id != "" {
				_ = m.store.Delete(ctx, id)
			}
		}
		m.setCookie(c, "", -1)
		return
	}
	// Extend active sessions when less than half of the TTL remains.
	touch := s.id != "" && time.Until(s.expires) < m.cfg.TTL/2
	if !s.modified && !touch {
		return
	}
	if s.oldID != "" {
		_ = m.store.Delete(ctx, s.oldID)
	}
	if s.id == "" {
		s.id = newID()
	}
	s.expires = time.Now().Add(m.cfg.TTL)
	data, err := json.Marshal(record{Values: s.values, Expires: s.expires})
	if err == nil {
		err = m.store.Set(ctx, s.id, data, m.cfg.TTL)
	}
	if err != nil {
		c.Logger().Error("session: save failed", "error", err)
		return
	}
	m.setCookie(c, s.id, int(m.cfg.TTL.Seconds()))
}

func (m *Manager) setCookie(c *torge.Context, value string, maxAge int) {
	c.SetCookie(&http.Cookie{
		Name:     m.cfg.CookieName,
		Value:    value,
		Path:     m.cfg.Path,
		Domain:   m.cfg.Domain,
		MaxAge:   maxAge,
		Secure:   !m.cfg.Insecure,
		HttpOnly: true,
		SameSite: m.cfg.SameSite,
	})
}

func newID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func validID(id string) bool {
	if len(id) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil
}

// Authenticator returns an auth.Authenticator that reads a user ID stored in
// the session under key and loads the principal with load.
func Authenticator(key string, load func(ctx context.Context, userID string) (torge.Principal, error)) auth.Authenticator {
	return auth.Func(func(c *torge.Context) (torge.Principal, error) {
		v, ok := c.Get(ctxKey{})
		if !ok {
			return nil, auth.ErrNoCredentials
		}
		var id string
		if found, err := v.(*Session).Get(key, &id); err != nil || !found || id == "" {
			return nil, auth.ErrNoCredentials
		}
		return load(c.Context(), id)
	})
}
