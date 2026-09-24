// Package webhook verifies incoming webhooks: signature verification over the
// exact raw body, timestamp validation, replay protection and structured
// errors, integrated with request IDs and logging.
//
//	app.POST("/webhooks/stripe", handleStripe, webhook.Middleware(webhook.Config{
//	    Verifier: webhook.Stripe(cfg.StripeSecret.Value()),
//	    Replay:   store, // cache.Store; shared across instances in production
//	}))
//
//	func handleStripe(c *torge.Context) error {
//	    body := webhook.RawBody(c) // the verified bytes
//	    ...
//	}
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
)

// Error codes.
const (
	CodeSignatureMissing = "WEBHOOK_SIGNATURE_MISSING"
	CodeSignatureInvalid = "WEBHOOK_SIGNATURE_INVALID"
	CodeTimestamp        = "WEBHOOK_TIMESTAMP_INVALID"
)

// Verification errors returned by verifiers.
var (
	ErrMissingSignature = errors.New("webhook: missing signature")
	ErrInvalidSignature = errors.New("webhook: invalid signature")
	ErrTimestamp        = errors.New("webhook: timestamp outside tolerance")
)

// Event is metadata extracted from a verified webhook.
type Event struct {
	// ID identifies the delivery, used for replay protection. When empty,
	// the signature is used instead.
	ID string
	// Timestamp is the signed send time, if the scheme has one.
	Timestamp time.Time
	// Signature is the verified signature value.
	Signature string
}

// Verifier verifies a webhook request against its raw body.
type Verifier interface {
	Verify(r *http.Request, body []byte, now time.Time) (Event, error)
}

// Config configures the middleware.
type Config struct {
	// Verifier checks signatures. Required.
	Verifier Verifier
	// MaxBodySize bounds the body (default 1 MiB).
	MaxBodySize int64
	// Replay, if set, remembers processed deliveries so duplicates are
	// acknowledged without running the handler again. Deliveries are
	// recorded only after the handler succeeds, so failed deliveries are
	// retried by the sender.
	Replay cache.Store
	// ReplayTTL is how long deliveries are remembered (default 24h).
	ReplayTTL time.Duration
	// Now returns the current time (for tests).
	Now func() time.Time
}

type ctxKey struct{}

type verified struct {
	body  []byte
	event Event
}

// RawBody returns the verified raw request body.
func RawBody(c *torge.Context) []byte {
	v, _ := c.Get(ctxKey{})
	if vv, ok := v.(*verified); ok {
		return vv.body
	}
	return nil
}

// EventFrom returns the verified event metadata.
func EventFrom(c *torge.Context) Event {
	v, _ := c.Get(ctxKey{})
	if vv, ok := v.(*verified); ok {
		return vv.event
	}
	return Event{}
}

// Decode unmarshals the verified body into v.
func Decode(c *torge.Context, v any) error {
	if err := json.Unmarshal(RawBody(c), v); err != nil {
		return torge.BadRequest(torge.CodeInvalidJSON, "Webhook payload is not valid JSON").Wrap(err)
	}
	return nil
}

// Middleware verifies webhooks before the handler runs. The raw body stays
// readable through RawBody and c.Body().
func Middleware(cfg Config) torge.Middleware {
	if cfg.Verifier == nil {
		panic(&torge.Diagnostic{Code: torge.DiagInvalidConfig, What: "webhook.Config.Verifier is nil",
			Why: "unsigned webhooks would be accepted", Fix: "use webhook.HMAC, webhook.Stripe or webhook.GitHub"})
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = 1 << 20
	}
	if cfg.ReplayTTL <= 0 {
		cfg.ReplayTTL = 24 * time.Hour
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	var replay cache.Store
	if cfg.Replay != nil {
		replay = cache.Namespace(cfg.Replay, "torge:webhook:")
	}
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			r := c.Request()
			body, err := io.ReadAll(io.LimitReader(r.Body, cfg.MaxBodySize+1))
			if err != nil {
				return torge.AsError(err)
			}
			if int64(len(body)) > cfg.MaxBodySize {
				return torge.AsError(&http.MaxBytesError{Limit: cfg.MaxBodySize})
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			ev, err := cfg.Verifier.Verify(r, body, cfg.Now())
			if err != nil {
				c.Logger().Warn("webhook verification failed", "error", err, "path", c.Path())
				switch {
				case errors.Is(err, ErrMissingSignature):
					return torge.Unauthorized(CodeSignatureMissing, "Webhook signature is missing").Wrap(err)
				case errors.Is(err, ErrTimestamp):
					return torge.Unauthorized(CodeTimestamp, "Webhook timestamp is outside the allowed tolerance").Wrap(err)
				default:
					return torge.Unauthorized(CodeSignatureInvalid, "Webhook signature is invalid").Wrap(err)
				}
			}
			c.Set(ctxKey{}, &verified{body: body, event: ev})
			id := ev.ID
			if id == "" {
				id = "sig:" + ev.Signature
			}
			if replay != nil {
				if _, err := replay.Get(c.Context(), id); err == nil {
					c.Header("Webhook-Duplicate", "true")
					return c.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
				}
			}
			if err := next(c); err != nil {
				return err
			}
			if replay != nil && c.StatusCode() < 300 {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Context()), 5*time.Second)
				defer cancel()
				if err := replay.Set(ctx, id, []byte{1}, cfg.ReplayTTL); err != nil {
					c.Logger().Error("webhook: recording delivery failed", "error", err)
				}
			}
			return nil
		}
	}
}

// Encoding is how a signature is encoded.
type Encoding int

// Signature encodings.
const (
	Hex Encoding = iota
	Base64
)

// HMACConfig configures a generic HMAC-SHA256 verifier.
type HMACConfig struct {
	// Secrets are accepted signing secrets; several allow rotation.
	Secrets [][]byte
	// Header carries the signature (default X-Signature).
	Header string
	// Prefix is stripped from the signature, for example "sha256=".
	Prefix string
	// Encoding of the signature (default Hex).
	Encoding Encoding
	// TimestampHeader, if set, carries a Unix timestamp that is signed as
	// "<timestamp>.<body>" and checked against Tolerance.
	TimestampHeader string
	// Tolerance bounds timestamp skew (default 5m).
	Tolerance time.Duration
	// IDHeader carries a delivery ID for replay protection.
	IDHeader string
}

type hmacVerifier struct{ cfg HMACConfig }

// HMAC returns a generic HMAC-SHA256 verifier.
func HMAC(cfg HMACConfig) Verifier {
	if cfg.Header == "" {
		cfg.Header = "X-Signature"
	}
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = 5 * time.Minute
	}
	return hmacVerifier{cfg}
}

func (v hmacVerifier) Verify(r *http.Request, body []byte, now time.Time) (Event, error) {
	sig := r.Header.Get(v.cfg.Header)
	if sig == "" {
		return Event{}, ErrMissingSignature
	}
	sig = strings.TrimPrefix(sig, v.cfg.Prefix)
	ev := Event{Signature: sig}
	if v.cfg.IDHeader != "" {
		ev.ID = r.Header.Get(v.cfg.IDHeader)
	}
	payload := body
	if v.cfg.TimestampHeader != "" {
		raw := r.Header.Get(v.cfg.TimestampHeader)
		ts, err := checkTimestamp(raw, now, v.cfg.Tolerance)
		if err != nil {
			return Event{}, err
		}
		ev.Timestamp = ts
		payload = append([]byte(raw+"."), body...)
	}
	want, err := decodeSig(sig, v.cfg.Encoding)
	if err != nil {
		return Event{}, ErrInvalidSignature
	}
	for _, secret := range v.cfg.Secrets {
		if hmac.Equal(want, sign(sha256.New, secret, payload)) {
			return ev, nil
		}
	}
	return Event{}, ErrInvalidSignature
}

// GitHub verifies GitHub webhooks (X-Hub-Signature-256). GitHub signatures
// have no timestamp; enable Config.Replay to reject replays by delivery ID.
func GitHub(secret string) Verifier {
	return HMAC(HMACConfig{
		Secrets:  [][]byte{[]byte(secret)},
		Header:   "X-Hub-Signature-256",
		Prefix:   "sha256=",
		IDHeader: "X-GitHub-Delivery",
	})
}

type stripeVerifier struct {
	secrets   [][]byte
	tolerance time.Duration
}

// Stripe verifies Stripe webhooks (Stripe-Signature: t=...,v1=...). Several
// secrets may be given during rotation.
func Stripe(secrets ...string) Verifier {
	v := stripeVerifier{tolerance: 5 * time.Minute}
	for _, s := range secrets {
		v.secrets = append(v.secrets, []byte(s))
	}
	return v
}

func (v stripeVerifier) Verify(r *http.Request, body []byte, now time.Time) (Event, error) {
	header := r.Header.Get("Stripe-Signature")
	if header == "" {
		return Event{}, ErrMissingSignature
	}
	var ts string
	var sigs []string
	for part := range strings.SplitSeq(header, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "t":
			ts = val
		case "v1":
			sigs = append(sigs, val)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return Event{}, ErrInvalidSignature
	}
	when, err := checkTimestamp(ts, now, v.tolerance)
	if err != nil {
		return Event{}, err
	}
	payload := append([]byte(ts+"."), body...)
	for _, secret := range v.secrets {
		expected := sign(sha256.New, secret, payload)
		for _, s := range sigs {
			got, err := hex.DecodeString(s)
			if err == nil && hmac.Equal(got, expected) {
				var meta struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(body, &meta)
				return Event{ID: meta.ID, Timestamp: when, Signature: s}, nil
			}
		}
	}
	return Event{}, ErrInvalidSignature
}

type standardVerifier struct {
	secrets   [][]byte
	tolerance time.Duration
}

// StandardWebhooks verifies webhooks following the Standard Webhooks
// specification (webhook-id, webhook-timestamp, webhook-signature headers),
// used by Svix and others. Secrets may carry the "whsec_" prefix.
func StandardWebhooks(secrets ...string) (Verifier, error) {
	v := standardVerifier{tolerance: 5 * time.Minute}
	for _, s := range secrets {
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "whsec_"))
		if err != nil {
			return nil, errors.New("webhook: standard webhook secrets must be base64 (optionally prefixed with whsec_)")
		}
		v.secrets = append(v.secrets, key)
	}
	return v, nil
}

func (v standardVerifier) Verify(r *http.Request, body []byte, now time.Time) (Event, error) {
	id, ts, header := r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature")
	if id == "" || ts == "" || header == "" {
		return Event{}, ErrMissingSignature
	}
	when, err := checkTimestamp(ts, now, v.tolerance)
	if err != nil {
		return Event{}, err
	}
	payload := append([]byte(id+"."+ts+"."), body...)
	for _, secret := range v.secrets {
		expected := sign(sha256.New, secret, payload)
		for entry := range strings.FieldsSeq(header) {
			version, sig, ok := strings.Cut(entry, ",")
			if !ok || version != "v1" {
				continue
			}
			got, err := base64.StdEncoding.DecodeString(sig)
			if err == nil && hmac.Equal(got, expected) {
				return Event{ID: id, Timestamp: when, Signature: sig}, nil
			}
		}
	}
	return Event{}, ErrInvalidSignature
}

func checkTimestamp(raw string, now time.Time, tolerance time.Duration) (time.Time, error) {
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, ErrTimestamp
	}
	ts := time.Unix(secs, 0)
	if d := now.Sub(ts); d > tolerance || d < -tolerance {
		return time.Time{}, ErrTimestamp
	}
	return ts, nil
}

func sign(h func() hash.Hash, secret, payload []byte) []byte {
	m := hmac.New(h, secret)
	m.Write(payload)
	return m.Sum(nil)
}

func decodeSig(sig string, enc Encoding) ([]byte, error) {
	if enc == Base64 {
		return base64.StdEncoding.DecodeString(sig)
	}
	return hex.DecodeString(sig)
}

// Sign computes an HMAC-SHA256 signature in the given encoding. It is useful
// for tests and for sending webhooks.
func Sign(secret, payload []byte, enc Encoding) string {
	mac := sign(sha256.New, secret, payload)
	if enc == Base64 {
		return base64.StdEncoding.EncodeToString(mac)
	}
	return hex.EncodeToString(mac)
}
