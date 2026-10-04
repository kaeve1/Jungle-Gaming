package postgres

import (
	"context"
	"strings"
	"testing"
)

func TestEffectiveDatabaseIdentityIsRestrictedBeforeConnecting(t *testing.T) {
	t.Setenv("PGUSER", "postgres")
	for _, dsn := range []string{
		"postgres://wallet_migrator:private-password@127.0.0.1:1/wallet",
		"postgres://wallet_app:private-password@127.0.0.1:1/wallet?user=postgres",
		"postgres://wallet_app:private-password@127.0.0.1:1/wallet?user=wallet_migrator",
	} {
		_, err := AppPoolConfig(dsn, DefaultOptions())
		if err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatal("unsafe identity accepted or secret disclosed")
		}
		_, err = OpenPool(context.Background(), dsn, DefaultOptions())
		if err == nil || err.Error() != "effective database user must be wallet_app" {
			t.Fatal("OpenPool must reject effective identity before network access")
		}
	}
	cfg, err := AppPoolConfig("postgres://wallet_app:unused-example@postgres/wallet?user=wallet_app", DefaultOptions())
	if err != nil || cfg.ConnConfig.User != "wallet_app" {
		t.Fatal("explicit application identity must override PGUSER")
	}
}
