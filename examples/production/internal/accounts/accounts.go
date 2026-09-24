// Package accounts manages customer accounts.
package accounts

import (
	"context"
	"crypto/rand"
	"net/http"
	"sync"
	"time"

	"github.com/TosmimForidMehtab/torge"
)

// Account is a customer account.
type Account struct {
	ID        string    `json:"id" readonly:"true"`
	Name      string    `json:"name" validate:"required,min=2,max=80" example:"Ada Lovelace"`
	Email     string    `json:"email" validate:"required,email" example:"ada@example.com"`
	CreatedAt time.Time `json:"created_at" readonly:"true"`
}

// ErrNotFound is returned for unknown accounts.
var ErrNotFound = torge.NotFound("ACCOUNT_NOT_FOUND", "Account does not exist")

// ErrEmailTaken is returned when an email is already registered.
var ErrEmailTaken = torge.Conflict("EMAIL_TAKEN", "An account with this email already exists")

// Store persists accounts. Production code would implement it with sqldb or
// pgx; the application does not care which.
type Store interface {
	Create(ctx context.Context, a *Account) error
	Get(ctx context.Context, id string) (*Account, error)
}

// Service implements account operations.
type Service struct{ store Store }

// NewService constructs a Service.
func NewService(store Store) *Service { return &Service{store: store} }

// Get returns an account.
func (s *Service) Get(ctx context.Context, id string) (*Account, error) {
	return s.store.Get(ctx, id)
}

type createInput struct{ Body Account }

type getInput struct {
	ID string `path:"id" validate:"required"`
}

func (s *Service) create(c *torge.Context, in *createInput) (*Account, error) {
	a := in.Body
	a.ID, a.CreatedAt = "acc_"+rand.Text()[:12], time.Now().UTC()
	if err := s.store.Create(c.Context(), &a); err != nil {
		return nil, err
	}
	c.Logger().Info("account created", "account_id", a.ID)
	return &a, nil
}

func (s *Service) get(c *torge.Context, in *getInput) (*Account, error) {
	return s.store.Get(c.Context(), in.ID)
}

// Module registers the accounts feature.
type Module struct{}

// Name implements torge.NamedModule.
func (Module) Name() string { return "accounts" }

// Register implements torge.Module.
func (Module) Register(app *torge.App) error {
	app.Provide(NewMemoryStore)
	app.Provide(NewService)
	app.Invoke(func(s *Service, api *torge.Group) {
		g := api.Group("/accounts", torge.Tags("accounts"), torge.Security("apiToken"))
		torge.Post(g, "", s.create, torge.Summary("Create an account"), torge.Status(http.StatusCreated),
			torge.Returns[torge.ErrorBody](http.StatusConflict, "Email already registered"))
		torge.Get(g, "/:id", s.get, torge.Summary("Get an account"),
			torge.Returns[torge.ErrorBody](http.StatusNotFound, "Unknown account"))
	})
	return nil
}

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu      sync.RWMutex
	byID    map[string]*Account
	byEmail map[string]string
}

// NewMemoryStore returns a MemoryStore as a Store.
func NewMemoryStore() Store {
	return &MemoryStore{byID: map[string]*Account{}, byEmail: map[string]string{}}
}

// Create implements Store.
func (m *MemoryStore) Create(_ context.Context, a *Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.byEmail[a.Email]; taken {
		return ErrEmailTaken
	}
	cp := *a
	m.byID[a.ID], m.byEmail[a.Email] = &cp, a.ID
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, id string) (*Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}
