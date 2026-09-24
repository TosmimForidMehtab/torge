package idempotency_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/idempotency"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

func TestIdempotency(t *testing.T) {
	store := cache.NewMemory()
	defer store.Close()
	var charges atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	app := torgetest.NewApp(t)
	mw := idempotency.Middleware(idempotency.Config{Store: idempotency.NewStore(store)})
	app.POST("/payments", func(c *torge.Context) error {
		var in struct {
			Amount int `json:"amount"`
		}
		if err := c.BindJSON(&in); err != nil {
			return err
		}
		n := charges.Add(1)
		if in.Amount == 999 {
			started <- struct{}{}
			<-release
		}
		if in.Amount < 0 {
			return torge.BadRequest("NEGATIVE", "negative amount")
		}
		c.Header("Location", "/payments/1")
		return c.JSON(201, map[string]int{"charge": int(n), "amount": in.Amount})
	}, mw)
	tc := torgetest.New(t, app)

	first := tc.POST("/payments").Header("Idempotency-Key", "k1").JSON(map[string]int{"amount": 10}).Do().
		ExpectStatus(201).ExpectJSONPath("charge", 1)
	tc.POST("/payments").Header("Idempotency-Key", "k1").JSON(map[string]int{"amount": 10}).Do().
		ExpectStatus(201).ExpectBody(string(first.Body)).
		ExpectHeader("Idempotent-Replayed", "true").ExpectHeader("Location", "/payments/1")
	if charges.Load() != 1 {
		t.Fatalf("replay must not execute the handler, charges=%d", charges.Load())
	}

	tc.POST("/payments").Header("Idempotency-Key", "k1").JSON(map[string]int{"amount": 20}).Do().
		ExpectStatus(422).ExpectErrorCode(idempotency.CodeKeyReused)

	// Failures release the key so the client can retry.
	tc.POST("/payments").Header("Idempotency-Key", "k2").JSON(map[string]int{"amount": -1}).Do().ExpectStatus(400)
	tc.POST("/payments").Header("Idempotency-Key", "k2").JSON(map[string]int{"amount": -1}).Do().ExpectStatus(400)
	if charges.Load() != 3 {
		t.Fatalf("failed requests must be retryable, charges=%d", charges.Load())
	}

	// Concurrent duplicates are rejected while the first is in flight.
	done := make(chan *torgetest.Response)
	go func() {
		done <- tc.POST("/payments").Header("Idempotency-Key", "k3").JSON(map[string]int{"amount": 999}).Do()
	}()
	<-started
	tc.POST("/payments").Header("Idempotency-Key", "k3").JSON(map[string]int{"amount": 999}).Do().
		ExpectStatus(409).ExpectErrorCode(idempotency.CodeInProgress)
	close(release)
	(<-done).ExpectStatus(201)

	// Overlong keys are rejected; requests without a key pass through.
	tc.POST("/payments").Header("Idempotency-Key", strings.Repeat("x", 300)).JSON(map[string]int{"amount": 1}).Do().
		ExpectStatus(400).ExpectErrorCode(idempotency.CodeKeyInvalid)
	tc.POST("/payments").JSON(map[string]int{"amount": 1}).Do().ExpectStatus(201)
}

func TestIdempotencyRequiredKey(t *testing.T) {
	store := cache.NewMemory()
	defer store.Close()
	app := torgetest.NewApp(t)
	app.POST("/orders", func(c *torge.Context) error { return c.NoContent(201) },
		idempotency.Middleware(idempotency.Config{Store: idempotency.NewStore(store), Required: true}))
	torgetest.New(t, app).POST("/orders").Do().ExpectStatus(400).ExpectErrorCode(idempotency.CodeKeyRequired)
}
