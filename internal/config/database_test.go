package config

import (
	"strings"
	"testing"
	"time"
)

func TestDatabaseConfiguration(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 10 || cfg.DBMaxRetries != 3 || cfg.DBOperationTimeout != 20*time.Second || cfg.DBLockTimeout != 3*time.Second || cfg.DBStatementTimeout != 5*time.Second {
		t.Fatalf("unexpected database defaults")
	}
	for _, tc := range []struct{ key, value string }{
		{"DB_MAX_CONNS", "0"}, {"DB_MAX_CONNS", "101"}, {"DB_MAX_CONNS", "secret-value"},
		{"DB_MAX_RETRIES", "4"}, {"DB_MAX_RETRIES", "-1"}, {"DB_LOCK_TIMEOUT", "6s"},
		{"DB_STATEMENT_TIMEOUT", "21s"}, {"DB_OPERATION_TIMEOUT", "2m"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			_, err := Load(func(key string) string {
				if key == tc.key {
					return tc.value
				}
				return ""
			})
			if err == nil {
				t.Fatal("invalid database settings accepted")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("configuration echoed input")
			}
		})
	}
}
