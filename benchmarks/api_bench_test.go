package benchmarks

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"

	"github.com/TosmimForidMehtab/torge"
)

// A realistic API request: path parameter, JSON body decoded into a struct,
// validation rules on three fields, JSON response. Gin and Echo validate with
// go-playground/validator, the validator both ecosystems use.

const apiBody = `{"name":"Ada Lovelace","email":"ada@example.com","age":36}`

type apiUser struct {
	ID    string `json:"id"`
	Name  string `json:"name" validate:"required,min=3,max=64" binding:"required,min=3,max=64"`
	Email string `json:"email" validate:"required,email" binding:"required,email"`
	Age   int    `json:"age" validate:"gte=0,lte=150" binding:"gte=0,lte=150"`
}

func runAPI(b *testing.B, h http.Handler) {
	b.Helper()
	body := []byte(apiBody)
	newReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/users/42", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq())
	var got apiUser
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID != "42" || got.Email != "ada@example.com" {
		b.Fatalf("unexpected response %d %q", rec.Code, rec.Body)
	}
	// Invalid input must be rejected by every framework.
	bad := httptest.NewRequest(http.MethodPost, "/users/42", bytes.NewReader([]byte(`{"name":"A","email":"x"}`)))
	bad.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code < 400 {
		b.Fatalf("invalid input accepted: %d", rec.Code)
	}

	req := newReq()
	reader := bytes.NewReader(body)
	w := &sink{h: make(http.Header)}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		reader.Reset(body)
		req.Body = io.NopCloser(reader)
		clear(w.h)
		h.ServeHTTP(w, req)
	}
}

type apiInput struct {
	ID   string `path:"id"`
	Body apiUser
}

func BenchmarkAPITorge(b *testing.B) {
	app := torge.New(torge.WithEnv(torge.Production), torge.WithoutDefaults(),
		torge.WithLogger(slog.New(slog.NewJSONHandler(io.Discard, nil))))
	torge.Post(app, "/users/:id", func(c *torge.Context, in *apiInput) (*apiUser, error) {
		u := in.Body
		u.ID = in.ID
		return &u, nil
	})
	if err := app.Start(b.Context()); err != nil {
		b.Fatal(err)
	}
	runAPI(b, app)
}

func BenchmarkAPIGin(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.POST("/users/:id", func(c *gin.Context) {
		var u apiUser
		if err := c.ShouldBindJSON(&u); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
			return
		}
		u.ID = c.Param("id")
		c.JSON(http.StatusOK, u)
	})
	runAPI(b, e)
}

type echoValidator struct{ v *validator.Validate }

func (ev echoValidator) Validate(i any) error { return ev.v.Struct(i) }

func BenchmarkAPIEcho(b *testing.B) {
	e := echo.New()
	e.Validator = echoValidator{validator.New()}
	e.POST("/users/:id", func(c echo.Context) error {
		var u apiUser
		if err := c.Bind(&u); err != nil {
			return err
		}
		if err := c.Validate(&u); err != nil {
			return echo.NewHTTPError(http.StatusUnprocessableEntity, err.Error())
		}
		u.ID = c.Param("id")
		return c.JSON(http.StatusOK, u)
	})
	runAPI(b, e)
}
