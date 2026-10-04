package config

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"INSTANCE_ID": "i1"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(cfg.Roles, []Role{RoleAPI}) {
		t.Errorf("Roles = %v, want api", cfg.Roles)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.ShutdownTimeout != 20*time.Second || cfg.StartTimeout != 30*time.Second {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadRoles(t *testing.T) {
	cfg, err := Load(env(map[string]string{"INSTANCE_ID": "i1", "APP_ROLES": " api, consumer ,api"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(cfg.Roles, []Role{RoleAPI, RoleConsumer}) {
		t.Errorf("Roles = %v", cfg.Roles)
	}
	if cfg.Has(RoleOutboxPublisher) {
		t.Error("unexpected outbox-publisher role")
	}
}

func TestInstanceIDDefaultsToHostnameAndProcessIDIsUnique(t *testing.T) {
	a, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, _ := Load(env(nil))
	if a.InstanceID == "" {
		t.Error("InstanceID should default to the hostname")
	}
	if a.ProcessID == b.ProcessID || !strings.HasPrefix(a.ProcessID, a.InstanceID+"/") {
		t.Errorf("ProcessID must be unique per process: %q, %q", a.ProcessID, b.ProcessID)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{
		"APP_ROLES":        "api,admin",
		"LOG_LEVEL":        "loud",
		"SHUTDOWN_TIMEOUT": "-1s",
		"START_TIMEOUT":    "soon",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"APP_ROLES", "LOG_LEVEL", "SHUTDOWN_TIMEOUT", "START_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
