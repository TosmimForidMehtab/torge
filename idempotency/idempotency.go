// Package idempotency makes non-idempotent operations (payments, orders,
// registrations) safe to retry. Clients send an Idempotency-Key header; the
// first request with a key is executed and its response stored, and later
// requests with the same key replay that response instead of executing again.
//
// Requests are fingerprinted (method, path and body), so reusing a key for a
// different request is rejected with 422, and concurrent requests with the
// same key get 409 while the first is in flight. Only successful handler
// executions with a status below 500 are stored; failures release the key so
// the client can retry.
//
// State lives in a cache.Store that supports atomic Add. Use a shared store
// (contrib/redis) when running several instances.
//
//	app.POST("/payments", createPayment, idempotency.Middleware(idempotency.Config{
//	    Store: idempotency.NewStore(redisStore),
//	}))
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/internal/capture"
)

// Error codes.
const (
	CodeKeyRequired = "IDEMPOTENCY_KEY_REQUIRED"
	CodeKeyInvalid  = "IDEMPOTENCY_KEY_INVALID"
	CodeKeyReused   = "IDEMPOTENCY_KEY_REUSED"
	CodeInProgress  = "IDEMPOTENCY_REQUEST_IN_PROGRESS"
	CodeUnavailable = "IDEMPOTENCY_UNAVAILABLE"
)

// Record is the persisted state of an idempotency key.
type Record struct {
	Fingerprint string            `json:"fingerprint"`
	Done        bool              `json:"done"`
	Response    *capture.Response `json:"response,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

// Store persists idempotency records.
type Store interface {
	// Reserve creates a pending record for key if none exists and returns
	// nil, or returns the existing record.
	Reserve(ctx context.Context, key string, rec *Record, ttl time.Duration) (*Record, error)
	// Complete stores the final record.
	Complete(ctx context.Context, key string, rec *Record, ttl time.Duration) error
	// Release deletes a pending record so the request can be retried.
	Release(ctx context.Context, key string) error
}

// NewStore returns a Store backed by a cache.Store implementing
// cache.Adder, such as cache.Memory or the contrib Redis store.
func NewStore(s cache.Store) Store {
	return &cacheStore{s: cache.Namespace(s, "torge:idempotency:")}
}

type cacheStore struct{ s cache.Store }

func (c *cacheStore) Reserve(ctx context.Context, key string, rec *Record, ttl time.Duration) (*Record, error) {
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	for range 2 {
		ok, err := cache.Add(ctx, c.s, key, data, ttl)
		if err != nil {
			return nil, err
		}
		if ok {
			return nil, nil
		}
		raw, err := c.s.Get(ctx, key)
		if errors.Is(err, cache.ErrNotFound) {
			continue // expired between Add and Get
		}
		if err != nil {
			return nil, err
		}
		var existing Record
		if err := json.Unmarshal(raw, &existing); err != nil {
			return nil, fmt.Errorf("idempotency: corrupt record: %w", err)
		}
		return &existing, nil
	}
	return nil, errors.New("idempotency: could not reserve key")
}

func (c *cacheStore) Complete(ctx context.Context, key string, rec *Record, ttl time.Duration) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return c.s.Set(ctx, key, data, ttl)
}

func (c *cacheStore) Release(ctx context.Context, key string) error {
	return c.s.Delete(ctx, key)
}

// Config configures the middleware.
type Config struct {
	// Store persists records. Required.
	Store Store
	// Header carries the key (default Idempotency-Key).
	Header string
	// Methods are protected (default POST and PATCH).
	Methods []string
	// Required rejects protected requests without a key.
	Required bool
	// TTL is how long completed responses are replayable (default 24h).
	TTL time.Duration
	// LockTTL bounds how long an in-flight request holds its key, in case
	// the process dies mid-request (default 1m).
	LockTTL time.Duration
	// MaxBodySize bounds the fingerprinted request body (default 1 MiB).
	MaxBodySize int64
	// MaxResponseSize bounds stored responses (default 1 MiB). Larger
	// responses are served but not stored.
	MaxResponseSize int
	// Scope partitions keys, by default per authenticated user so one user
	// can never replay another user's response.
	Scope func(c *torge.Context) string
}

// Middleware enforces idempotency for requests carrying a key.
func Middleware(cfg Config) torge.Middleware {
	if cfg.Store == nil {
		panic(&torge.Diagnostic{Code: torge.DiagInvalidConfig, What: "idempotency.Config.Store is nil",
			Why: "responses cannot be stored", Fix: "use idempotency.NewStore(cacheStore)"})
	}
	if cfg.Header == "" {
		cfg.Header = "Idempotency-Key"
	}
	if cfg.Methods == nil {
		cfg.Methods = []string{http.MethodPost, http.MethodPatch}
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = time.Minute
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = 1 << 20
	}
	if cfg.MaxResponseSize <= 0 {
		cfg.MaxResponseSize = 1 << 20
	}
	if cfg.Scope == nil {
		cfg.Scope = func(c *torge.Context) string {
			if u := c.User(); u != nil {
				return "user:" + u.ID()
			}
			return "anonymous"
		}
	}
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			if !slices.Contains(cfg.Methods, c.Method()) {
				return next(c)
			}
			key := c.GetHeader(cfg.Header)
			if key == "" {
				if cfg.Required {
					return torge.BadRequest(CodeKeyRequired, "The "+cfg.Header+" header is required")
				}
				return next(c)
			}
			if len(key) > 255 {
				return torge.BadRequest(CodeKeyInvalid, "The idempotency key must be at most 255 characters")
			}
			body, err := readBody(c, cfg.MaxBodySize)
			if err != nil {
				return err
			}
			fp := fingerprint(c.Method(), c.Path(), body)
			fullKey := cfg.Scope(c) + ":" + key
			ctx := c.Context()

			existing, err := cfg.Store.Reserve(ctx, fullKey, &Record{Fingerprint: fp, CreatedAt: time.Now().UTC()}, cfg.LockTTL)
			if err != nil {
				return torge.ServiceUnavailable(CodeUnavailable, "Idempotency is temporarily unavailable").Wrap(err)
			}
			if existing != nil {
				switch {
				case existing.Fingerprint != fp:
					return torge.UnprocessableEntity(CodeKeyReused, "The idempotency key was already used for a different request")
				case !existing.Done || existing.Response == nil:
					c.Header("Retry-After", "1")
					return torge.Conflict(CodeInProgress, "A request with this idempotency key is in progress")
				default:
					return existing.Response.WriteTo(c.Response(), map[string]string{"Idempotent-Replayed": "true"})
				}
			}

			rec := capture.New(c.Response(), cfg.MaxResponseSize)
			restore := c.SetResponseWriter(rec)
			err = next(c)
			restore()
			// Persistence must not be skipped because the client went away.
			storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			resp, ok := rec.Result()
			if err != nil || !ok || resp.Status >= 500 {
				if rerr := cfg.Store.Release(storeCtx, fullKey); rerr != nil {
					c.Logger().Error("idempotency: release failed", "error", rerr)
				}
				return err
			}
			done := &Record{Fingerprint: fp, Done: true, Response: &resp, CreatedAt: time.Now().UTC()}
			if cerr := cfg.Store.Complete(storeCtx, fullKey, done, cfg.TTL); cerr != nil {
				c.Logger().Error("idempotency: storing response failed", "error", cerr)
			}
			return nil
		}
	}
}

func readBody(c *torge.Context, limit int64) ([]byte, error) {
	r := c.Request()
	if r.Body == nil || r.Body == http.NoBody {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, torge.AsError(err)
	}
	if int64(len(body)) > limit {
		return nil, torge.AsError(&http.MaxBytesError{Limit: limit})
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func fingerprint(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}
