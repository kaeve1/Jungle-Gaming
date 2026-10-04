//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

// Prova via rede: adulteração do corpo/alvo, ausência de assinatura e reset não
// chegam ao emulador. Controle positivo usa exatamente o mesmo caminho assinado.
func TestBrokerSignatureAndAdminBoundary(t *testing.T) {
	cfg, err := config.LoadDefaultConfig(t.Context(), config.WithRegion("us-east-1"),
		config.WithSharedCredentialsFiles([]string{env(t, "TEST_SQS_CREDENTIALS_FILE")}), config.WithSharedConfigProfile("wager-producer"))
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.Credentials.Retrieve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"valid", "unsigned", "tampered body", "tampered action", "expired", "admin route"} {
		t.Run(name, func(t *testing.T) {
			body := `{"QueueName":"wager-transactions.fifo"}`
			req, err := http.NewRequestWithContext(t.Context(), "POST", env(t, "TEST_SQS_ENDPOINT")+"/", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/x-amz-json-1.0")
			req.Header.Set("X-Amz-Target", "AmazonSQS.GetQueueUrl")
			at := time.Now()
			if name == "expired" {
				at = at.Add(-time.Hour)
			}
			digest := sha256.Sum256([]byte(body))
			if name != "unsigned" {
				if err := v4.NewSigner().SignHTTP(t.Context(), creds, req, hex.EncodeToString(digest[:]), "sqs", "us-east-1", at); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "tampered body":
				req.Body = io.NopCloser(strings.NewReader(body + " "))
				req.ContentLength = int64(len(body) + 1)
			case "tampered action":
				req.Header.Set("X-Amz-Target", "AmazonSQS.ListQueues")
			case "admin route":
				req.URL.Path = "/_ministack/reset"
			}
			client := http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			want := http.StatusForbidden
			if name == "valid" {
				want = http.StatusOK
			}
			if resp.StatusCode != want {
				t.Fatalf("status=%d want=%d", resp.StatusCode, want)
			}
		})
	}
}
