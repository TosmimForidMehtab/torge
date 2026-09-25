package main

// Templates use [[ ]] delimiters so generated code can contain {{ }}.

const goModTpl = `module [[.Module]]

go 1.26
[[- if .LocalDir]]

replace [[.Torge]] => [[.LocalDir]]
[[- end]]
`

const mainTpl = `package main

import (
	"log"

	"[[.Torge]]"
	"[[.Torge]]/config"
	"[[.Torge]]/openapi"

	appconfig "[[.Module]]/internal/config"
	"[[.Module]]/internal/users"
)

func main() {
	// Local development reads .env; real environment variables always win,
	// and nothing is loaded when TORGE_ENV=production is set.
	if err := config.LoadDotEnv(); err != nil {
		log.Fatal(err)
	}
	var cfg appconfig.Config
	if err := config.Load(&cfg); err != nil {
		log.Fatal(err)
	}

	app := torge.New()
	app.Config(&cfg)
	app.OpenAPI(torge.OpenAPIConfig{Info: openapi.Info{Title: "[[.Module]]", Version: "0.1.0"}})

	app.GET("/", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"message": "hello"})
	})
	app.Register(users.Module{})

	if err := app.Listen(cfg.Addr); err != nil {
		log.Fatal(err)
	}
}
`

const dotEnvTpl = `# Local development settings. Copy to .env (git-ignored) and adjust.
# Real environment variables always override these values.
TORGE_ENV=development
PORT=8080

# Optional infrastructure (uncomment to enable):
# DB_URL=postgres://app:secret@localhost:5432/[[.Module]]?sslmode=disable
# REDIS_URL=redis://localhost:6379/0
`

const configTpl = `// Package config defines the application's configuration, loaded from
// environment variables.
package config

// Config is the application configuration.
type Config struct {
	// Addr is the listen address; empty means ":$PORT", or ":8080" when
	// PORT is unset (the convention of Render, Heroku and most platforms).
	Addr string ` + "`" + `env:"ADDR"` + "`" + `
}
`

const moduleTpl = `// Package [[.Package]] implements the [[.Name]] module.
package [[.Package]]

import "[[.Torge]]"

// Module registers the [[.Name]] routes and dependencies.
type Module struct{}

// Name implements torge.NamedModule.
func (Module) Name() string { return "[[.Name]]" }

// Register implements torge.Module.
func (Module) Register(app *torge.App) error {
	g := app.Group("/[[.Path]]", torge.Tags("[[.Name]]"))
	g.GET("", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"module": "[[.Name]]"})
	})
	return nil
}
`

const resourceTpl = `// Package [[.Package]] implements the [[.Name]] resource.
package [[.Package]]

import (
	"context"
	"crypto/rand"
	"net/http"
	"sort"
	"sync"

	"[[.Torge]]"
)

// [[.Type]] is the resource representation.
type [[.Type]] struct {
	ID   string ` + "`" + `json:"id" readonly:"true"` + "`" + `
	Name string ` + "`" + `json:"name" validate:"required,min=2,max=100" example:"Example"` + "`" + `
}

// Store persists [[.Name]]. Replace MemoryStore with a database-backed
// implementation.
type Store interface {
	List(ctx context.Context) ([][[.Type]], error)
	Get(ctx context.Context, id string) ([[.Type]], bool, error)
	Save(ctx context.Context, v [[.Type]]) error
	Delete(ctx context.Context, id string) (bool, error)
}

// ErrNotFound is returned when a [[.Type]] does not exist.
var ErrNotFound = torge.NotFound("[[upper .Type]]_NOT_FOUND", "[[.Type]] not found")

// Service contains the business logic.
type Service struct{ store Store }

// NewService constructs a Service.
func NewService(store Store) *Service { return &Service{store: store} }

type idInput struct {
	ID string ` + "`" + `path:"id"` + "`" + `
}

type createInput struct {
	Body [[.Type]]
}

type updateInput struct {
	ID   string ` + "`" + `path:"id"` + "`" + `
	Body [[.Type]]
}

type listOutput struct {
	Items []` + "[[.Type]]" + ` ` + "`" + `json:"items"` + "`" + `
}

func (s *Service) list(c *torge.Context, _ *torge.Empty) (*listOutput, error) {
	items, err := s.store.List(c.Context())
	if err != nil {
		return nil, err
	}
	return &listOutput{Items: items}, nil
}

func (s *Service) get(c *torge.Context, in *idInput) (*[[.Type]], error) {
	v, ok, err := s.store.Get(c.Context(), in.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return &v, nil
}

func (s *Service) create(c *torge.Context, in *createInput) (*[[.Type]], error) {
	v := in.Body
	v.ID = rand.Text()
	if err := s.store.Save(c.Context(), v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Service) update(c *torge.Context, in *updateInput) (*[[.Type]], error) {
	if _, err := s.get(c, &idInput{ID: in.ID}); err != nil {
		return nil, err
	}
	v := in.Body
	v.ID = in.ID
	if err := s.store.Save(c.Context(), v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Service) delete(c *torge.Context, in *idInput) (*torge.Empty, error) {
	ok, err := s.store.Delete(c.Context(), in.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return nil, nil
}

// Module registers the [[.Name]] resource.
type Module struct{}

// Name implements torge.NamedModule.
func (Module) Name() string { return "[[.Name]]" }

// Register implements torge.Module.
func (Module) Register(app *torge.App) error {
	app.Provide(NewMemoryStore)
	app.Provide(NewService)
	app.Invoke(func(s *Service) {
		g := app.Group("/[[.Path]]", torge.Tags("[[.Name]]"))
		torge.Get(g, "", s.list, torge.Summary("List [[.Name]]"))
		torge.Get(g, "/:id", s.get, torge.Summary("Get a [[.Type]]"))
		torge.Post(g, "", s.create, torge.Summary("Create a [[.Type]]"), torge.Status(http.StatusCreated))
		torge.Put(g, "/:id", s.update, torge.Summary("Replace a [[.Type]]"))
		torge.Delete(g, "/:id", s.delete, torge.Summary("Delete a [[.Type]]"))
	})
	return nil
}

// MemoryStore is an in-memory Store for development and tests.
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string][[.Type]]
}

// NewMemoryStore returns an empty MemoryStore as a Store.
func NewMemoryStore() Store { return &MemoryStore{items: map[string][[.Type]]{}} }

func (m *MemoryStore) List(context.Context) ([][[.Type]], error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([][[.Type]], 0, len(m.items))
	for _, v := range m.items {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, id string) ([[.Type]], bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.items[id]
	return v, ok, nil
}

func (m *MemoryStore) Save(_ context.Context, v [[.Type]]) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[v.ID] = v
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.items[id]
	delete(m.items, id)
	return ok, nil
}
`

const resourceTestTpl = `package [[.Package]]_test

import (
	"testing"

	"[[.Torge]]/torgetest"

	"[[.Module]]/internal/[[.Package]]"
)

func TestCRUD(t *testing.T) {
	app := torgetest.NewApp(t)
	app.Register([[.Package]].Module{})
	tc := torgetest.New(t, app)

	created := tc.POST("/[[.Path]]").JSON(map[string]string{"name": "First"}).Do().ExpectStatus(201)
	id, _ := created.JSONPath("id")

	tc.GET("/[[.Path]]/" + id.(string)).Do().ExpectStatus(200).ExpectJSONPath("name", "First")
	tc.PUT("/[[.Path]]/" + id.(string)).JSON(map[string]string{"name": "Renamed"}).Do().ExpectStatus(200)
	tc.GET("/[[.Path]]").Do().ExpectStatus(200).ExpectJSONPath("items.0.name", "Renamed")
	tc.POST("/[[.Path]]").JSON(map[string]string{"name": "x"}).Do().ExpectStatus(422)
	tc.DELETE("/[[.Path]]/" + id.(string)).Do().ExpectStatus(204)
	tc.GET("/[[.Path]]/" + id.(string)).Do().ExpectStatus(404)
}
`

const middlewareTpl = `package middleware

import "[[.Torge]]"

// [[.Type]] is a middleware skeleton. Code before next runs on the way in;
// code after it runs on the way out. Return without calling next to
// short-circuit the request.
func [[.Type]]() torge.Middleware {
	return func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error {
			// before
			err := next(c)
			// after
			return err
		}
	}
}
`

const dockerfileTpl = `# syntax=docker/dockerfile:1
# Multi-stage build: a static binary in a minimal, non-root image.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
# Configuration comes from the platform's environment (Render, Kubernetes,
# docker run -e / --env-file); .env files are excluded from the image.
ENV TORGE_ENV=production
EXPOSE 8080
ENTRYPOINT ["/app"]
`

const dockerignoreTpl = `.env
.env.*
!.env.example
.git
bin/
*.test
`
