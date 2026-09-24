package torge_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

type fakeDB struct {
	mu      sync.Mutex
	pingErr error
	closed  atomic.Bool
}

func (d *fakeDB) setPingErr(err error) {
	d.mu.Lock()
	d.pingErr = err
	d.mu.Unlock()
}

func (d *fakeDB) Ping(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pingErr
}

func (d *fakeDB) Close() error { d.closed.Store(true); return nil }

func (d *fakeDB) Transaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func TestHealthEndpoints(t *testing.T) {
	var blocked atomic.Bool
	app := torgetest.NewApp(t, torge.WithHealth(&torge.HealthConfig{Timeout: 50 * time.Millisecond}))
	db := &fakeDB{}
	app.Database(db)
	app.HealthCheck("slow", func(ctx context.Context) error {
		if blocked.Load() {
			<-ctx.Done() // honors the timeout
		}
		return nil
	})
	app.HealthCheck("search", func(context.Context) error { return errors.New("degraded") }, torge.Optional())
	app.HealthCheck("worker", func(context.Context) error { return nil }, torge.Liveness())
	// Global middleware must not block probes.
	app.Use(func(next torge.Handler) torge.Handler {
		return func(c *torge.Context) error { return torge.Unauthorized("AUTH", "no") }
	})
	tc := torgetest.New(t, app)

	tc.GET("/live").Do().ExpectStatus(200).ExpectJSON(map[string]any{"status": "up"})
	tc.GET("/ready").Do().ExpectStatus(200)
	tc.GET("/health").Do().ExpectStatus(200).
		ExpectJSONPath("checks.database.status", "up").
		ExpectJSONPath("checks.search.status", "down").
		ExpectJSONPath("checks.search.optional", true)

	db.setPingErr(errors.New("connection reset"))
	tc.GET("/ready").Do().ExpectStatus(503).ExpectJSONPath("status", "down")
	tc.GET("/live").Do().ExpectStatus(200)

	db.setPingErr(nil)
	blocked.Store(true)
	start := time.Now()
	tc.GET("/health").Do().ExpectStatus(503).ExpectJSONPath("checks.slow.status", "down")
	if time.Since(start) > time.Second {
		t.Fatal("health checks must time out")
	}
	tc.GET("/anything").Do().ExpectStatus(401)
}

func TestReadinessDuringShutdown(t *testing.T) {
	app := torgetest.NewApp(t, torge.WithServer(torge.ServerConfig{DrainDelay: 100 * time.Millisecond}))
	tc := torgetest.New(t, app)
	tc.GET("/ready").Do().ExpectStatus(200)
	done := make(chan error)
	go func() { done <- app.Shutdown(context.Background()) }()
	time.Sleep(30 * time.Millisecond)
	tc.GET("/ready").Do().ExpectStatus(503).ExpectJSONPath("status", "stopping")
	tc.GET("/live").Do().ExpectStatus(200)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseLifecycle(t *testing.T) {
	db := &fakeDB{}
	db.setPingErr(errors.New("dial tcp: connection refused"))
	app := torgetest.NewApp(t)
	app.Database(db)
	err := app.Start(context.Background())
	if diagCodes(err) != torge.DiagDatabaseUnreachable {
		t.Fatalf("expected unreachable database diagnostic, got %v", err)
	}

	db2 := &fakeDB{}
	app = torgetest.NewApp(t)
	app.Database(db2)
	app.Invoke(func(d *fakeDB) error {
		if d != db2 {
			return errors.New("database must be injectable")
		}
		return nil
	})
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := app.Transaction(context.Background(), func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("transaction: %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !db2.closed.Load() {
		t.Fatal("database must be closed at shutdown")
	}
}
