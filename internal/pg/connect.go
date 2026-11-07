package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Connect opens a read-only connection to the DSN the user gave. It sets
// default_transaction_read_only=on and statement_timeout=30000 as session
// parameters at connect time (so a DSN that tries to turn read-only off
// cannot win), then verifies the server really is in read-only mode.
// The returned connection must be closed by the caller.
func Connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN: %w", err)
	}
	cfg.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.RuntimeParams["statement_timeout"] = "30000"
	cfg.ConnectTimeout = 10 * time.Second

	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var readOnly string
	if err := conn.QueryRow(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil {
		conn.Close(ctx)
		return nil, fmt.Errorf("could not verify read-only mode: %w", err)
	}
	if readOnly != "on" {
		conn.Close(ctx)
		return nil, fmt.Errorf("server refused read-only mode (default_transaction_read_only=%s)", readOnly)
	}
	return conn, nil
}

// ConnectAdmin opens a plain connection with no read-only parameters. It is
// used only by --self-test to create and drop its temporary schemas in a
// disposable database. It must never be used for user-supplied SQL.
func ConnectAdmin(ctx context.Context, dsn string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN: %w", err)
	}
	cfg.ConnectTimeout = 10 * time.Second
	return pgx.ConnectConfig(ctx, cfg)
}

// BeginReadTx starts a READ ONLY transaction. Every user query runs inside
// one, so even a statement that passed validation cannot write: the server
// rejects the write inside the transaction.
func BeginReadTx(ctx context.Context, conn *pgx.Conn) (pgx.Tx, error) {
	return conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
}
