//go:build integration

package integration

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
)

const (
	wagerQueue  = "wager-transactions.fifo"
	dlqQueue    = "wager-transactions-dlq.fifo"
	eventsQueue = "wallet-events.fifo"
)

// sqsClient usa um perfil do arquivo de credenciais gerado por deploy/sqs/init.sh.
func sqsClient(t *testing.T, profile string) *sqs.Client {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithSharedCredentialsFiles([]string{env(t, "TEST_SQS_CREDENTIALS_FILE")}),
		config.WithSharedConfigProfile(profile),
	)
	if err != nil {
		t.Fatal(err)
	}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(env(t, "TEST_SQS_ENDPOINT")) })
}

// sqsClientWithKeys usa credenciais explícitas (para testar chaves inválidas).
func sqsClientWithKeys(t *testing.T, key, secret string) *sqs.Client {
	t.Helper()
	return sqs.New(sqs.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(env(t, "TEST_SQS_ENDPOINT")),
		Credentials:  credentials.NewStaticCredentialsProvider(key, secret, ""),
	})
}

func profileKey(t *testing.T, profile string) string {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(t.Context(),
		config.WithRegion("us-east-1"),
		config.WithSharedCredentialsFiles([]string{env(t, "TEST_SQS_CREDENTIALS_FILE")}),
		config.WithSharedConfigProfile(profile))
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cfg.Credentials.Retrieve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return creds.AccessKeyID
}

func queueURL(t *testing.T, c *sqs.Client, name string) string {
	t.Helper()
	out, err := c.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		t.Fatalf("GetQueueUrl %s: %v", name, err)
	}
	return *out.QueueUrl
}

func errorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func isDenied(err error) bool {
	code := errorCode(err)
	return strings.Contains(code, "AccessDenied") || code == "UnrecognizedClientException" || code == "InvalidClientTokenId"
}

// Filas FIFO, redrive para a DLQ e prazos provisionados conforme
// ARCHITECTURE.md §13.
func TestQueuesAreProvisioned(t *testing.T) {
	c := sqsClient(t, "wallet-service")
	out, err := c.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL(t, c, wagerQueue)),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := out.Attributes
	for k, want := range map[string]string{
		"FifoQueue": "true", "VisibilityTimeout": "30", "ContentBasedDeduplication": "false",
		"ReceiveMessageWaitTimeSeconds": "20", "DeduplicationScope": "messageGroup",
		"FifoThroughputLimit": "perMessageGroupId",
	} {
		if a[k] != want {
			t.Errorf("%s = %q, want %q", k, a[k], want)
		}
	}
	var redrive struct {
		DeadLetterTargetArn string `json:"deadLetterTargetArn"`
		MaxReceiveCount     any    `json:"maxReceiveCount"`
	}
	if err := json.Unmarshal([]byte(a["RedrivePolicy"]), &redrive); err != nil {
		t.Fatalf("RedrivePolicy %q: %v", a["RedrivePolicy"], err)
	}
	if !strings.HasSuffix(redrive.DeadLetterTargetArn, ":"+dlqQueue) || toString(redrive.MaxReceiveCount) != "10" {
		t.Errorf("redrive %+v", redrive)
	}
	queueURL(t, c, eventsQueue) // existe e wallet-service tem acesso
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}

// Menor privilégio por identidade (enunciado §2: acesso à mensageria por
// credenciais e políticas do broker). O que o emulador NÃO garante está em
// TestKnownEmulatorLimitations.
func TestLeastPrivilegePolicies(t *testing.T) {
	ctx := t.Context()
	producer := sqsClient(t, "wager-producer")
	service := sqsClient(t, "wallet-service")
	reader := sqsClient(t, "events-reader")
	wagerURL := queueURL(t, service, wagerQueue)
	eventsURL := queueURL(t, service, eventsQueue)

	group := "probe-" + rand.Text()
	body := "least-privilege-probe-" + rand.Text()
	if _, err := producer.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(wagerURL), MessageBody: aws.String(body),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(rand.Text()),
	}); err != nil {
		t.Fatalf("producer must send to %s: %v", wagerQueue, err)
	}

	denied := map[string]error{}
	_, denied["producer receives wager"] = producer.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(wagerURL)})
	_, denied["producer sends to events"] = producer.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(eventsURL), MessageBody: aws.String("x"),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(rand.Text()),
	})
	_, denied["reader receives wager"] = reader.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(wagerURL)})
	_, denied["reader sends to events"] = reader.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(eventsURL), MessageBody: aws.String("x"),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(rand.Text()),
	})
	_, denied["service receives events"] = service.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(eventsURL)})
	_, denied["service sends wager"] = service.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(wagerURL), MessageBody: aws.String("must-not-be-published"),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(rand.Text()),
	})
	_, denied["unknown key"] = sqsClientWithKeys(t, "AKIAUNKNOWNKEY000000", "x").GetQueueAttributes(ctx,
		&sqs.GetQueueAttributesInput{QueueUrl: aws.String(wagerURL)})
	for name, err := range denied {
		if !isDenied(err) {
			t.Errorf("%s: want access denied, got %v", name, err)
		}
	}

	// O serviço consome e apaga a mensagem de teste (limpeza incluída).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out, err := service.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(wagerURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
		})
		if err != nil {
			t.Fatalf("wallet-service must receive: %v", err)
		}
		for _, m := range out.Messages {
			if *m.Body != body {
				// Não é nossa: devolve imediatamente para não atrapalhar outros consumidores.
				_, _ = service.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
					QueueUrl: aws.String(wagerURL), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 0})
				continue
			}
			if _, err := service.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(wagerURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
				t.Fatalf("wallet-service must delete: %v", err)
			}
			return
		}
	}
	t.Fatal("probe message not received by wallet-service")
}

// Regressão VULN-007: a entrada estrita do broker deve recusar os bypasses
// que a imagem original do MiniStack aceitava. Nenhuma chamada altera filas.
func TestBrokerRejectsAuthenticationBypass(t *testing.T) {
	ctx := t.Context()
	wagerURL := queueURL(t, sqsClient(t, "wallet-service"), wagerQueue)
	attrs := &sqs.GetQueueAttributesInput{QueueUrl: aws.String(wagerURL)}

	t.Run("wrong secret is rejected", func(t *testing.T) {
		// A assinatura precisa comprovar conhecimento do segredo correto.
		c := sqsClientWithKeys(t, profileKey(t, "wager-producer"), "wrong-secret")
		if _, err := c.GetQueueAttributes(ctx, attrs); !isDenied(err) {
			t.Errorf("wrong secret must be denied: %v", err)
		}
	})
	t.Run("built-in root keys are rejected", func(t *testing.T) {
		for _, key := range []string{"test", "000000000000"} {
			if _, err := sqsClientWithKeys(t, key, "anything").GetQueueAttributes(ctx, attrs); !isDenied(err) {
				t.Errorf("root alias must be denied: %v", err)
			}
		}
	})
}
