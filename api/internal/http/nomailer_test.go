package http

import (
	"context"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/auth"
	"github.com/orrrrli/locker/api/internal/infrastructure/password"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

// Without Resend configured nothing is stored or sent: registration and
// reset requests still answer, and no email link exists to leak.
func TestNoMailerStoresNoTokens(t *testing.T) {
	pool := testdb.New(t)
	svc := auth.NewService(auth.Deps{
		Tx:          postgres.NewTxRunner(pool),
		Users:       postgres.NewUsers(pool),
		Sessions:    postgres.NewSessions(pool),
		Hasher:      password.NewHasher(password.Params{MemoryKiB: 64, Time: 1, Threads: 1}),
		EmailTokens: postgres.NewEmailTokens(pool),
		Now:         func() time.Time { return today },
	})
	ctx := context.Background()
	birth := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := svc.Register(ctx, auth.RegisterInput{Name: "Ana", Email: "ana@example.com", Password: "correct horse", BirthDate: birth}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RequestPasswordReset(ctx, "ana@example.com"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_token`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d email tokens stored without a mailer", n)
	}
}
