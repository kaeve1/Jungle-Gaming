package postgres

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"testing"
)

type erroredWalletRow struct{ err error }

func (r erroredWalletRow) Scan(...any) error { return r.err }

func TestWalletMapperPreservesKnownAbortForTransactionRetry(t *testing.T) {
	for _, code := range []string{"55P03", "40P01", "40001"} {
		original := &pgconn.PgError{Code: code, Message: "private database detail"}
		row := erroredWalletRow{original}
		_, err := readWallet(row)
		if !errors.Is(err, original) || !retryable(err) {
			t.Fatalf("SQLSTATE %s lost retry classification", code)
		}
		_, err = walletRow(row)
		var publicError *DatabaseError
		if !errors.As(err, &publicError) || publicError.SQLState != code {
			t.Fatal("public reader did not sanitize error")
		}
	}
}
