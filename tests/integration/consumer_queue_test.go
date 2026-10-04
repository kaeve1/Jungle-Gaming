//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type stage8Queues struct{ name, dlq string }

var stage8QueueFixtures sync.Map

func stage8Queue(t *testing.T) stage8Queues {
	t.Helper()
	if name := os.Getenv("STAGE8_CHILD_QUEUE"); name != "" {
		return stage8Queues{name, ""}
	}
	if v, ok := stage8QueueFixtures.Load(t); ok {
		return v.(stage8Queues)
	}
	suffix := strings.ToLower(rand.Text()[:12])
	q := stage8Queues{"stage8-" + suffix + ".fifo", "stage8-" + suffix + "-dlq.fifo"}
	admin := sqsClientWithKeys(t, env(t, "TEST_SQS_ADMIN_ACCESS_KEY_ID"), env(t, "TEST_SQS_ADMIN_SECRET_ACCESS_KEY"))
	attrs := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false", "DeduplicationScope": "messageGroup", "FifoThroughputLimit": "perMessageGroupId"}
	dlq, err := admin.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String(q.dlq), Attributes: attrs})
	if err != nil {
		t.Fatal(err)
	}
	redrive, _ := json.Marshal(map[string]string{"deadLetterTargetArn": "arn:aws:sqs:us-east-1:000000000000:" + q.dlq, "maxReceiveCount": "10"})
	attrs["RedrivePolicy"] = string(redrive)
	attrs["VisibilityTimeout"] = "30"
	attrs["ReceiveMessageWaitTimeSeconds"] = "20"
	input, err := admin.CreateQueue(t.Context(), &sqs.CreateQueueInput{QueueName: aws.String(q.name), Attributes: attrs})
	if err != nil {
		t.Fatal(err)
	}
	// Separate additive IAM policy for only this test's queue. Runtime identity and
	// production policies stay unchanged. Tests alone receive explicit admin keys.
	policyName := "stage8-" + suffix
	for _, user := range []string{"wallet-service", "wager-producer"} {
		actions := []string{"sqs:GetQueueUrl", "sqs:GetQueueAttributes"}
		if user == "wallet-service" {
			actions = append(actions, "sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility")
		} else {
			actions = append(actions, "sqs:SendMessage")
		}
		policy, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Allow", "Action": actions, "Resource": "arn:aws:sqs:us-east-1:000000000000:" + q.name}}})
		stage8IAM(t, t.Context(), url.Values{"Action": {"PutUserPolicy"}, "UserName": {user}, "PolicyName": {policyName}, "PolicyDocument": {string(policy)}})
	}
	stage8QueueFixtures.Store(t, q)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, queue := range []*string{input.QueueUrl, dlq.QueueUrl} {
			if _, err := admin.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue}); err != nil {
				t.Error("test queue cleanup failed")
			}
		}
		for _, user := range []string{"wallet-service", "wager-producer"} {
			stage8IAM(t, ctx, url.Values{"Action": {"DeleteUserPolicy"}, "UserName": {user}, "PolicyName": {policyName}})
		}
		stage8QueueFixtures.Delete(t)
	})
	return q
}
func stage8IAM(t *testing.T, ctx context.Context, values url.Values) {
	t.Helper()
	values.Set("Version", "2010-05-08")
	body := values.Encode()
	r, err := http.NewRequestWithContext(ctx, "POST", env(t, "TEST_SQS_ENDPOINT")+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hash := sha256.Sum256([]byte(body))
	creds := aws.Credentials{AccessKeyID: env(t, "TEST_SQS_ADMIN_ACCESS_KEY_ID"), SecretAccessKey: env(t, "TEST_SQS_ADMIN_SECRET_ACCESS_KEY")}
	if err = v4.NewSigner().SignHTTP(ctx, creds, r, hex.EncodeToString(hash[:]), "iam", "us-east-1", time.Now()); err != nil {
		t.Fatal("test policy signing failed")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal("test policy provisioning failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		t.Fatal(fmt.Sprintf("test IAM policy status %d", resp.StatusCode))
	}
}
