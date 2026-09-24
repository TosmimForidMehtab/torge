// Package torgemongo adapts the official MongoDB driver (v2) to Torge's
// database lifecycle: startup ping, readiness check, graceful disconnect and
// context-carried transactions. Queries stay plain driver calls.
//
//	db, err := torgemongo.Open(cfg.MongoURI.Value(), "shop")
//	app.Database(db)
//
//	orders := db.Collection("orders")
//	err = app.Transaction(ctx, func(ctx context.Context) error {
//	    if _, err := orders.InsertOne(ctx, order); err != nil { // joins the transaction via ctx
//	        return err
//	    }
//	    _, err := db.Collection("stock").UpdateOne(ctx, filter, update)
//	    return err
//	})
//
// Transactions require a replica set or sharded cluster. For command
// monitoring and tracing, set a monitor (for example otelmongo) on the client
// options passed to OpenClient.
package torgemongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// DB wraps a *mongo.Client and a default database.
type DB struct {
	client *mongo.Client
	db     *mongo.Database
	// CloseTimeout bounds Disconnect when the application stops (default 10s).
	CloseTimeout time.Duration
}

// Open connects to uri and uses database as the default database. Connecting
// is lazy; the application pings the server at startup.
func Open(uri, database string) (*DB, error) {
	return OpenClient(options.Client().ApplyURI(uri), database)
}

// OpenClient connects with explicit client options.
func OpenClient(opts *options.ClientOptions, database string) (*DB, error) {
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("torgemongo: connect: %w", err)
	}
	return Wrap(client, database), nil
}

// Wrap adapts an existing client. The application takes ownership and
// disconnects it at shutdown.
func Wrap(client *mongo.Client, database string) *DB {
	return &DB{client: client, db: client.Database(database), CloseTimeout: 10 * time.Second}
}

// Client returns the underlying client.
func (d *DB) Client() *mongo.Client { return d.client }

// Database returns the default database.
func (d *DB) Database() *mongo.Database { return d.db }

// Collection returns a collection of the default database.
func (d *DB) Collection(name string, opts ...options.Lister[options.CollectionOptions]) *mongo.Collection {
	return d.db.Collection(name, opts...)
}

// Ping checks connectivity to the primary. It implements torge.Database.
func (d *DB) Ping(ctx context.Context) error {
	return d.client.Ping(ctx, readpref.Primary())
}

// Close disconnects the client. It implements torge.Database.
func (d *DB) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), d.CloseTimeout)
	defer cancel()
	return d.client.Disconnect(ctx)
}

// Transaction implements torge.Transactor. fn receives a context carrying the
// session; driver operations that use it take part in the transaction, which
// commits when fn returns nil and aborts otherwise. A nested call joins the
// outer transaction.
//
// The driver retries fn on transient transaction errors, so fn may run more
// than once: keep side effects outside the database idempotent.
func (d *DB) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return d.TransactionWith(ctx, nil, fn)
}

// TransactionWith is Transaction with explicit transaction options (read and
// write concerns, read preference).
func (d *DB) TransactionWith(ctx context.Context, opts *options.TransactionOptionsBuilder, fn func(ctx context.Context) error) error {
	if mongo.SessionFromContext(ctx) != nil {
		return fn(ctx)
	}
	sess, err := d.client.StartSession()
	if err != nil {
		return fmt.Errorf("torgemongo: start session: %w", err)
	}
	defer sess.EndSession(context.WithoutCancel(ctx))
	var txOpts []options.Lister[options.TransactionOptions]
	if opts != nil {
		txOpts = append(txOpts, opts)
	}
	_, err = sess.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		return nil, fn(ctx)
	}, txOpts...)
	return err
}
