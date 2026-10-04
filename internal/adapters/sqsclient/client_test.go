package sqsclient

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClientUsesOnlyRuntimeProfile(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-admin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_PROFILE", "admin")
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte("[wallet-service]\naws_access_key_id = wallet-service-key\naws_secret_access_key = wallet-service-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, closeClient, err := New(t.Context(), "http://localhost:4566", "us-east-1", path, 25*time.Second)
	if err != nil {
		t.Fatal("runtime profile rejected")
	}
	defer closeClient()
	creds, err := client.Options().Credentials.Retrieve(t.Context())
	if err != nil || creds.AccessKeyID != "wallet-service-key" || creds.SecretAccessKey != "wallet-service-secret" {
		t.Fatal("ambient credentials overrode service identity")
	}
	if _, _, err = New(t.Context(), "http://localhost:4566", "us-east-1", path+"-missing", 25*time.Second); err == nil {
		t.Fatal("missing profile must not fall back to admin environment")
	}
}
