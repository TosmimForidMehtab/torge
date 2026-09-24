// Package payments creates payments idempotently, processes provider
// webhooks and sends receipts in the background.
package payments

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/accounts"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/platform"
	"github.com/TosmimForidMehtab/torge/idempotency"
	"github.com/TosmimForidMehtab/torge/jobs"
	"github.com/TosmimForidMehtab/torge/webhook"
)

// Payment is a charge against an account.
type Payment struct {
	ID        string    `json:"id" readonly:"true"`
	AccountID string    `json:"account_id" validate:"required"`
	Amount    int64     `json:"amount" validate:"gt=0,lte=1000000" doc:"Amount in cents"`
	Currency  string    `json:"currency" validate:"oneof=EUR USD GBP"`
	Status    string    `json:"status" readonly:"true"`
	CreatedAt time.Time `json:"created_at" readonly:"true"`
}

// Receipt is the payload of the send-receipt job.
type Receipt struct {
	PaymentID string `json:"payment_id"`
	Email     string `json:"email"`
}

// Service creates and settles payments.
type Service struct {
	accounts *accounts.Service
	jobs     *jobs.Manager
	log      *slog.Logger

	mu       sync.Mutex
	payments map[string]*Payment
}

// NewService constructs a Service.
func NewService(a *accounts.Service, j *jobs.Manager, log *slog.Logger) *Service {
	return &Service{accounts: a, jobs: j, log: log, payments: map[string]*Payment{}}
}

type createInput struct{ Body Payment }

func (s *Service) create(c *torge.Context, in *createInput) (*Payment, error) {
	acct, err := s.accounts.Get(c.Context(), in.Body.AccountID)
	if err != nil {
		return nil, err
	}
	p := in.Body
	p.ID, p.Status, p.CreatedAt = "pay_"+rand.Text()[:12], "pending", time.Now().UTC()
	s.mu.Lock()
	s.payments[p.ID] = &p
	s.mu.Unlock()
	// The receipt is sent in the background; the job carries this request's
	// ID so its logs correlate with the request.
	if err := s.jobs.Dispatch(c.Context(), "send-receipt", Receipt{PaymentID: p.ID, Email: acct.Email}); err != nil {
		return nil, err
	}
	return &p, nil
}

// Status returns a payment's status.
func (s *Service) Status(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.payments[id]; ok {
		return p.Status
	}
	return ""
}

type providerEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		PaymentID string `json:"payment_id"`
	} `json:"data"`
}

// webhook handles signed provider notifications.
func (s *Service) webhook(c *torge.Context) error {
	var ev providerEvent
	if err := webhook.Decode(c, &ev); err != nil {
		return err
	}
	if ev.Type == "payment.succeeded" {
		s.mu.Lock()
		if p, ok := s.payments[ev.Data.PaymentID]; ok {
			p.Status = "succeeded"
		}
		s.mu.Unlock()
	}
	return c.NoContent(http.StatusNoContent)
}

// sendReceipt is the background job handler.
func (s *Service) sendReceipt(ctx context.Context, r Receipt) error {
	// A real implementation would call an email provider through
	// httpclient; errors are retried with backoff by the job manager.
	s.log.InfoContext(ctx, "receipt sent", "payment_id", r.PaymentID)
	return nil
}

// Module registers the payments feature.
type Module struct{}

// Name implements torge.NamedModule.
func (Module) Name() string { return "payments" }

// Register implements torge.Module.
func (Module) Register(app *torge.App) error {
	app.Provide(NewService)
	app.Invoke(func(s *Service, api *torge.Group, store cache.Store, cfg *platform.Config, m *jobs.Manager) error {
		if err := m.Register("send-receipt", jobs.Typed(s.sendReceipt)); err != nil {
			return err
		}
		g := api.Group("/payments", torge.Tags("payments"), torge.Security("apiToken"))
		torge.Post(g, "", s.create,
			torge.Summary("Create a payment"),
			torge.Description("Send an Idempotency-Key header; retries with the same key return the original response."),
			torge.Status(http.StatusCreated),
			idempotency.Middleware(idempotency.Config{Store: idempotency.NewStore(store), Required: true}))

		app.POST("/webhooks/payments", s.webhook,
			torge.Tags("webhooks"), torge.Public(), torge.Summary("Payment provider webhook"),
			webhook.Middleware(webhook.Config{
				Verifier: webhook.HMAC(webhook.HMACConfig{
					Secrets:         [][]byte{[]byte(cfg.WebhookSecret.Value())},
					Header:          "X-Signature",
					TimestampHeader: "X-Timestamp",
					IDHeader:        "X-Event-Id",
				}),
				Replay: store,
			}))
		return nil
	})
	return nil
}
