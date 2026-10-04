package config

import (
	"strings"
	"testing"
)

func TestRuntimeRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://wallet_app:local-example-app@postgres/wallet", "ALLOW_INSECURE_LOCAL": "true"}
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", ""}, {"DATABASE_URL", "postgres://postgres:private-password@postgres/wallet"},
		{"DATABASE_URL", "postgres://wallet_app:private-password@postgres/wallet?user=postgres"},
		{"DATABASE_URL", "postgres://wallet_app:private-password@postgres/wallet?user=wallet_app&user=wallet_migrator"},
		{"HTTP_ADDRESS", "invalid"}, {"OIDC_ISSUER", "http://private-password@issuer/realm"},
		{"OIDC_JWKS_URL", "file:///keys"}, {"ALLOW_INSECURE_LOCAL", "false"}, {"OIDC_AUDIENCE", "another-api"},
		{"PUBLISHER_QUEUE", "invalid"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			cfg, err := Load(func(k string) string {
				if k == tc.key {
					return tc.value
				}
				return base[k]
			})
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ValidateRuntime()
			if err == nil {
				t.Fatal("unsafe runtime accepted")
			}
			if strings.Contains(err.Error(), "private-password") {
				t.Fatal("runtime error disclosed secret")
			}
		})
	}
	cfg, err := Load(func(k string) string { return base[k] })
	if err != nil || cfg.ValidateRuntime() != nil {
		t.Fatal("valid local runtime rejected")
	}
}

func TestReferenceRuntimeConfiguration(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://wallet_app:local-example-app@postgres/wallet", "ALLOW_INSECURE_LOCAL": "true", "APP_ROLES": "reference-worker"}
	cfg, err := Load(func(k string) string { return base[k] })
	if err != nil || cfg.ValidateRuntime() != nil {
		t.Fatalf("worker rejected: %v", err)
	}
	for _, override := range []map[string]string{{"REFERENCE_TTL": "25h"}, {"REFERENCE_BASE_DELAY": "2m"}, {"REFERENCE_MAX_DELAY": "2h"}, {"REFERENCE_TTL": "500ms"}} {
		if _, err := Load(func(k string) string {
			if v, ok := override[k]; ok {
				return v
			}
			return base[k]
		}); err == nil {
			t.Fatalf("invalid reference policy accepted: %v", override)
		}
	}
}

func TestConsumerRuntimeConfiguration(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://wallet_app:local-example-app@postgres/wallet", "ALLOW_INSECURE_LOCAL": "true", "APP_ROLES": "consumer"}
	cfg, err := Load(func(k string) string { return base[k] })
	if err != nil || cfg.ValidateRuntime() != nil {
		t.Fatal("consumer runtime rejected")
	}
	for key, value := range map[string]string{"CONSUMER_QUEUE": "invalid", "CONSUMER_NAME": "spaces not allowed", "CONSUMER_PROVIDERS": "provider-a,", "CONSUMER_CONCURRENCY": "17"} {
		cfg, err := Load(func(k string) string {
			if k == key {
				return value
			}
			return base[k]
		})
		if err == nil && cfg.ValidateRuntime() == nil {
			t.Fatalf("invalid consumer %s accepted", key)
		}
	}
}
