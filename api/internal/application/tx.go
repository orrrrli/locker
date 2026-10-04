package application

import "context"

// TxRunner runs fn inside a database transaction. Repositories called with the
// ctx passed to fn take part in that transaction. If fn returns an error or
// panics, the transaction is rolled back; otherwise it is committed.
type TxRunner interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}
