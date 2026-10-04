package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// DBTX is what repositories run queries on: either the pool or the current
// transaction. It aliases the sqlc interface so Conn's result always fits
// sqlcdb.New, even when sqlc adds methods (CopyFrom for :copyfrom queries).
type DBTX = sqlcdb.DBTX

type txKey struct{}

// TxRunner implements the application TxRunner on a pgx pool. The transaction
// travels in the context, so use cases and repositories only take a ctx.
type TxRunner struct {
	pool *pgxpool.Pool
}

func NewTxRunner(pool *pgxpool.Pool) *TxRunner {
	return &TxRunner{pool: pool}
}

// InTx commits when fn returns nil and rolls back when it returns an error or
// panics. If ctx already carries a transaction, fn joins it instead of opening
// a second one, so a use case calling another stays in a single transaction.
func (r *TxRunner) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// Conn returns the transaction carried by ctx, or the pool when there is none.
func Conn(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}
