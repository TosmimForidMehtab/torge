package torgemongo_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/TosmimForidMehtab/torge"
	torgemongo "github.com/TosmimForidMehtab/torge/contrib/mongo"
	"github.com/TosmimForidMehtab/torge/torgetest"
)

// Compile-time checks that DB satisfies the framework contracts.
var (
	_ torge.Database   = (*torgemongo.DB)(nil)
	_ torge.Transactor = (*torgemongo.DB)(nil)
)

func TestOpenRejectsInvalidURI(t *testing.T) {
	if _, err := torgemongo.Open("not-a-mongo-uri", "db"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestUnreachableServerFailsStartup(t *testing.T) {
	db, err := torgemongo.Open("mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=200&connectTimeoutMS=200", "db")
	if err != nil {
		t.Fatal(err)
	}
	app := torgetest.NewApp(t)
	app.Database(db, torge.DatabasePingTimeout(time.Second))
	err = app.Start(context.Background())
	var d *torge.Diagnostic
	if !errors.As(err, &d) || d.Code != torge.DiagDatabaseUnreachable {
		t.Fatalf("expected DATABASE_UNREACHABLE, got %v", err)
	}
}

// TestTransactions runs against a real replica set when TORGE_TEST_MONGO_URI
// is set, for example mongodb://localhost:27017/?replicaSet=rs0.
func TestTransactions(t *testing.T) {
	uri := os.Getenv("TORGE_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TORGE_TEST_MONGO_URI (a replica set) to run MongoDB integration tests")
	}
	db, err := torgemongo.Open(uri, "torge_test")
	if err != nil {
		t.Fatal(err)
	}
	app := torgetest.NewApp(t)
	app.Database(db)
	torgetest.Start(t, app)

	ctx := context.Background()
	coll := db.Collection("items")
	_ = coll.Drop(ctx)
	if err := db.Database().CreateCollection(ctx, "items"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = app.Transaction(ctx, func(ctx context.Context) error {
		if _, err := coll.InsertOne(ctx, bson.D{{Key: "v", Value: 1}}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if n, _ := coll.CountDocuments(ctx, bson.D{}); n != 0 {
		t.Fatalf("aborted transaction left %d documents", n)
	}
	if err := app.Transaction(ctx, func(ctx context.Context) error {
		_, err := coll.InsertOne(ctx, bson.D{{Key: "v", Value: 2}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n, _ := coll.CountDocuments(ctx, bson.D{}); n != 1 {
		t.Fatalf("committed transaction: %d documents", n)
	}
}
