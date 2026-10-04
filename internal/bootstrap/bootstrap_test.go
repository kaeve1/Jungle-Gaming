package bootstrap

import (
	"bytes"
	"testing"

	"go.uber.org/fx"
	"go.uber.org/goleak"

	"jungle-gaming-backend/internal/config"
)

// Nenhum teste deste pacote pode deixar goroutines vivas: é assim que
// verificamos que o encerramento libera workers e recursos (R03).
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func testEnv(k string) string {
	return map[string]string{"INSTANCE_ID": "test-1"}[k]
}

func TestGraphIsValid(t *testing.T) {
	cfg, err := config.Load(testEnv)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.ValidateApp(Options(cfg, &bytes.Buffer{})); err != nil {
		t.Fatalf("invalid Fx graph: %v", err)
	}
}

func TestMissingRuntimeConfigPreventsStart(t *testing.T) {
	var logs bytes.Buffer
	// Success/shutdown with real dependencies is now covered by integration.
	if _, err := New(testEnv, &logs); err == nil {
		t.Fatal("API accepted missing database configuration")
	}
}

func TestInvalidConfigPreventsStart(t *testing.T) {
	bad := func(k string) string { return map[string]string{"APP_ROLES": "admin"}[k] }
	if _, err := New(bad, &bytes.Buffer{}); err == nil {
		t.Fatal("expected configuration error")
	}
}
