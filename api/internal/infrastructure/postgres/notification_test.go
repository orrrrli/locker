package postgres

import (
	"context"
	"errors"
	"testing"
)

// A token APNs rejected is deleted whoever holds it; other tokens stay.
func TestDeleteDeviceTokenByValue(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
		db := Conn(ctx, pool)
		var ana, beto int64
		for _, u := range []struct {
			dest *int64
			name string
		}{{&ana, "Ana"}, {&beto, "Beto"}} {
			if err := db.QueryRow(ctx, `INSERT INTO "user" (name, birth_date) VALUES ($1, '2000-01-01') RETURNING id`, u.name).Scan(u.dest); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(ctx, `INSERT INTO device_token (user_id, token) VALUES ($1, 'aa'), ($1, 'bb'), ($2, 'cc')`, ana, beto); err != nil {
			t.Fatal(err)
		}

		repo := NewNotifications(pool)
		if err := repo.DeleteDeviceToken(ctx, "cc"); err != nil {
			t.Fatal(err)
		}
		if err := repo.DeleteDeviceToken(ctx, "unknown"); err != nil {
			t.Fatalf("unknown token: %v, want a no-op", err)
		}
		var left []string
		rows, err := db.Query(ctx, `SELECT token FROM device_token WHERE user_id IN ($1, $2) ORDER BY token`, ana, beto)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var tok string
			if err := rows.Scan(&tok); err != nil {
				t.Fatal(err)
			}
			left = append(left, tok)
		}
		if rows.Err() != nil || len(left) != 2 || left[0] != "aa" || left[1] != "bb" {
			t.Fatalf("tokens left %v (err %v), want [aa bb]", left, rows.Err())
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
}
