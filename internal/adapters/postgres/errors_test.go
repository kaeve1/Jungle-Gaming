package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"jungle-gaming-backend/internal/application"
)

func TestDatabaseErrorsDoNotLeakInput(t *testing.T) {
	for _, code := range []string{"23000", "23514", "23505", "57014"} {
		err := safeError(&pgconn.PgError{Code: code, ConstraintName: "financial_rule", Message: "private payload", Detail: "private password"})
		if strings.Contains(err.Error(), "private") {
			t.Fatalf("database error leaked input: %v", err)
		}
		if code == "57014" && !errors.Is(err, application.ErrUnavailable) {
			t.Fatal("query cancellation must be transient")
		}
	}
	if !errors.Is(safeError(&pgconn.PgError{Code: "23505", ConstraintName: "wallets_player_currency_unique"}), application.ErrWalletConflict) {
		t.Fatal("wallet conflict not classified")
	}
	if errors.Is(safeError(&pgconn.PgError{Code: "23000", ConstraintName: "tx_result_matches_ledger"}), application.ErrWalletConflict) {
		t.Fatal("invariant treated as business conflict")
	}
}

func TestRetryOnlyKnownAborts(t *testing.T) {
	for _, code := range []string{"40P01", "40001", "55P03"} {
		if !retryable(&pgconn.PgError{Code: code}) {
			t.Fatalf("abort %s not retried", code)
		}
	}
	for _, code := range []string{"23000", "23505", "57014", "08006"} {
		if retryable(&pgconn.PgError{Code: code}) {
			t.Fatalf("error %s retried unsafely", code)
		}
	}
	if retryable(errors.New("connection lost during commit")) {
		t.Fatal("ambiguous commit retried")
	}
	if !retryable(application.ErrConcurrentDuplicate) {
		t.Fatal("known rolled-back duplicate not retried")
	}
}
