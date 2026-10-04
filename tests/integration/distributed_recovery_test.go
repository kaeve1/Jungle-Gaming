//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStage11FxConsumerKilledAfterCommitBeforeDelete(t *testing.T) {
	f := financialCore(t)
	input := stage8Queue(t)
	output := stage9Queue(t)
	w := stage5Wallet(t, f, "100.00")
	req := stage5Request(t, w, "provider-a", "consume-crash11", "consume-crash11", "BET", "20.00")
	p := stage11Start(t, f.db, input.name, output, "consumer", "consume-after", "")
	stage8Send(t, stage8Body(t, req, "consume-crash11"), w.ID.String())
	p.barrier(t)
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
	var inbox int
	if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.inbox_messages WHERE message_id='consume-crash11'`).Scan(&inbox); e != nil || inbox != 1 {
		t.Fatal("barrier before commit")
	}
	p.stop(t, true)
	a := stage11Start(t, f.db, input.name, output, "consumer", "", "")
	b := stage11Start(t, f.db, input.name, output, "consumer", "", "")
	stage11Wait(t, 45*time.Second, func() bool {
		for _, p := range []*stage11Process{a, b} {
			v, e := os.ReadFile(filepath.Join(p.dir, "delivered"))
			if e == nil && string(v) == "2" {
				return true
			}
		}
		return false
	})
	client := sqsClient(t, "wallet-service")
	qurl := queueURL(t, client, input.name)
	stage11Wait(t, 10*time.Second, func() bool {
		out, e := client.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(qurl), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
		return e == nil && out.Attributes["ApproximateNumberOfMessages"] == "0" && out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0"
	})
	stage5HistoryWithoutTrace(t, f, w, 8000, 2, 2, 2, 4)
}

func TestStage11FxPublishersKilledBeforeSendAndBeforeConfirmation(t *testing.T) {
	for _, mode := range []string{"publish-before", "publish-after"} {
		t.Run(mode, func(t *testing.T) {
			f := financialCore(t)
			input := stage8Queue(t)
			output := stage9Queue(t)
			w := stage5Wallet(t, f, "100.00")
			p := stage11Start(t, f.db, input.name, output, "outbox-publisher", mode, "")
			p.barrier(t)
			var id, token, payload string
			var published *time.Time
			if e := f.pool.QueryRow(t.Context(), `SELECT event_id::text,claimed_by,payload::text,published_at FROM wallet.outbox_events WHERE claimed_by IS NOT NULL`).Scan(&id, &token, &payload, &published); e != nil || published != nil {
				t.Fatal("crash window not reserved/unpublished", e)
			}
			reader := sqsClient(t, "events-reader")
			qurl := queueURL(t, reader, output)
			out, e := reader.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(qurl), MaxNumberOfMessages: 10})
			if e != nil {
				t.Fatal(e)
			}
			if mode == "publish-before" && len(out.Messages) != 0 {
				t.Fatal("event sent before barrier")
			}
			if mode == "publish-after" {
				if len(out.Messages) != 1 || aws.ToString(out.Messages[0].Body) != payload {
					t.Fatal("successful first publication not observed")
				}
				_, _ = reader.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(qurl), ReceiptHandle: out.Messages[0].ReceiptHandle})
			}
			p.stop(t, true)
			// Supply enough work and hold all rows while both independent replacements start.
			for range 8 {
				stage5Wallet(t, f, "1.00")
			}
			lock, e := f.pool.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer lock.Rollback(context.Background())
			rows, e := lock.Query(t.Context(), `SELECT event_id FROM wallet.outbox_events FOR UPDATE`)
			if e != nil {
				t.Fatal(e)
			}
			for rows.Next() {
			}
			rows.Close()
			a := stage11Start(t, f.db, input.name, output, "outbox-publisher", "", "")
			b := stage11Start(t, f.db, input.name, output, "outbox-publisher", "", "")
			_ = lock.Rollback(t.Context())
			stage11Published(t, f)
			var attempts int
			var currentPayload string
			if e = f.pool.QueryRow(t.Context(), `SELECT attempts,payload::text FROM wallet.outbox_events WHERE event_id=$1`, id).Scan(&attempts, &currentPayload); e != nil || attempts != 2 || currentPayload != payload {
				t.Fatal("crashed claim not safely recovered", attempts, e)
			}
			for _, peer := range []*stage11Process{a, b} {
				peer.stop(t, false)
				log, e := os.ReadFile(filepath.Join(peer.dir, "runtime.jsonl"))
				if e != nil || !strings.Contains(string(log), `"msg":"outbox event published"`) {
					t.Fatal("both publishers did not exercise send")
				}
			}
			seen := map[string]bool{}
			if mode == "publish-after" {
				seen[id] = true
			}
			stage11Wait(t, 20*time.Second, func() bool {
				out, e := reader.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(qurl), MaxNumberOfMessages: 10})
				if e != nil {
					t.Error(e)
					return false
				}
				for _, m := range out.Messages {
					var v struct {
						ID string `json:"eventId"`
					}
					_ = json.Unmarshal([]byte(aws.ToString(m.Body)), &v)
					var body string
					if e = f.pool.QueryRow(t.Context(), `SELECT payload::text FROM wallet.outbox_events WHERE event_id=$1`, v.ID).Scan(&body); e != nil || body != aws.ToString(m.Body) {
						t.Error("published snapshot mismatch")
					}
					seen[v.ID] = true
					_, _ = reader.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(qurl), ReceiptHandle: m.ReceiptHandle})
				}
				return len(seen) == 18
			})
			stage5HistoryWithoutTrace(t, f, w, 10000, 1, 1, 1, 2)
			t.Logf("SIGKILL window=%s original claim recovered with attempts=%d; two independent publishers delivered all18 unique events", mode, attempts)
		})
	}
}

func TestStage11ReferencesExpireAndInvalidAcrossProcesses(t *testing.T) {
	f := financialCore(t)
	input := stage8Queue(t)
	output := stage9Queue(t)
	var ps []*stage11Process
	for range 3 {
		ps = append(ps, stage11Start(t, f.db, input.name, output, stage11Roles, "", "1s"))
	}
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wm := apiOpen(t, ps[0].api, internal, "100.00")
	w := stage11Wallet(t, f, wm)
	apiCall(t, ps[0].api, "POST", "/wagering/transactions", a, "expire11", stage11PendingBody(wm, "expire11", "never11", "REFUND", "5.00"), 202)
	stage11Status(t, f, "expire11", "REJECTED")
	apiCall(t, ps[1].api, "POST", "/wagering/transactions", a, "reject-ref11", wagerBody(wm, "provider-a", "reject-ref11", "101.00", "BET"), 422)
	apiCall(t, ps[2].api, "POST", "/wagering/transactions", a, "invalid11", stage11PendingBody(wm, "invalid11", "reject-ref11", "REFUND", "101.00"), 422)
	stage5HistoryWithoutTrace(t, f, w, 10000, 1, 4, 1, 6)
	stage11Published(t, f)
}
