//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"jungle-gaming-backend/internal/application"
)

func TestDatabaseClockSamplesWallTimeInsideTransaction(t *testing.T) {
	f := financialCore(t)
	err := f.uow.Within(t.Context(), func(ctx context.Context, session application.Session) error {
		first, err := session.Now(ctx)
		if err != nil {
			return err
		}
		// Real database delay distinguishes clock_timestamp from now(), which
		// remains fixed for the transaction. No host clock is used as the oracle.
		if _, err := f.pool.Exec(ctx, `SELECT pg_sleep(0.03)`); err != nil {
			return err
		}
		second, err := session.Now(ctx)
		if err != nil {
			return err
		}
		if second.Sub(first) < 20*time.Millisecond || second.Location() != time.UTC {
			t.Fatal("database time must advance during transaction and be UTC")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
