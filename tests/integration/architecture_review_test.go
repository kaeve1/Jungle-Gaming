//go:build integration

package integration

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-backend/internal/adapters/postgres"
	"jungle-gaming-backend/internal/config"
)

// The original failing audit reproduction is now a normal regression.
// No connection is opened, no credential is used and no DSN is logged.
func TestArchitectureReviewRejectsEffectiveAdministrativeUser(t *testing.T) {
	for _, user := range []string{"wallet_migrator", "postgres"} {
		t.Run(user, func(t *testing.T) {
			dsn := "postgres://wallet_app:unused-local-example@postgres/wallet?user=" + user
			cfg, err := config.Load(func(key string) string {
				switch key {
				case "DATABASE_URL":
					return dsn
				case "ALLOW_INSECURE_LOCAL":
					return "true"
				default:
					return ""
				}
			})
			if err != nil {
				t.Fatal("failed to construct audit configuration")
			}
			parsed, err := pgxpool.ParseConfig(dsn)
			if err != nil || parsed.ConnConfig.User != user {
				t.Fatal("audit prerequisite: query must override effective connection user")
			}
			if cfg.ValidateRuntime() == nil {
				t.Fatalf("runtime accepted effective database user %s; expected rejection before opening pool", user)
			}
			if _, err := postgres.AppPoolConfig(dsn, postgres.DefaultOptions()); err == nil {
				t.Fatal("adapter accepted administrative database user")
			}
		})
	}
}
