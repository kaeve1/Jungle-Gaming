// Package observability exposes bounded-cardinality metrics and sanitized logs.
package observability

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-backend/internal/application"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Diagnostics struct {
	log     *slog.Logger
	pool    *pgxpool.Pool
	client  *sqs.Client
	dlq     string
	mu      sync.Mutex
	results map[string]uint64
	counts  map[string]uint64
	buckets [7]uint64
	total   uint64
	sum     time.Duration
	scrape  chan struct{}
}

var limits = [6]time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 500 * time.Millisecond, time.Second, 5 * time.Second}
var allowedCounts = map[string]bool{"db_retry": true, "concurrency_conflict": true, "consumer_retry": true, "consumer_permanent": true, "consumer_exhausted": true, "publisher_retry": true, "reconciliation_divergence": true}

func New(pool *pgxpool.Pool, client *sqs.Client, log *slog.Logger, dlq string) *Diagnostics {
	return &Diagnostics{pool: pool, client: client, log: log, dlq: dlq, results: map[string]uint64{}, counts: map[string]uint64{}, scrape: make(chan struct{}, 1)}
}
func (d *Diagnostics) Count(name string) {
	if !allowedCounts[name] {
		return
	}
	d.mu.Lock()
	d.counts[name]++
	d.mu.Unlock()
}
func (d *Diagnostics) Observe(ctx context.Context, o application.Observation) {
	source := o.Source
	if source != "http" && source != "sqs" && source != "reference" && source != "opening" {
		source = "other"
	}
	status := o.Status
	// FAILED can include the original attempt error after its terminal audit
	// state was committed. Other failed attempts have no confirmed outcome.
	if o.Error != nil && status != "FAILED" {
		status = "ERROR"
	}
	switch status {
	case "PROCESSED", "REJECTED", "PENDING_REFERENCE", "FAILED":
	default:
		status = "ERROR"
	}
	category := application.ErrorCategory(o.Error)
	d.mu.Lock()
	d.results[source+"|"+status]++
	if o.Replay {
		d.counts["duplicates"]++
	}
	if category == "conflict" {
		d.counts["payload_conflicts"]++
	}
	d.total++
	d.sum += o.Duration
	for i, limit := range limits {
		if o.Duration <= limit {
			d.buckets[i]++
		}
	}
	d.buckets[6]++
	d.mu.Unlock()
	fields := []any{"source", source, "status", status, "errorCategory", category, "idempotentReplay", o.Replay, "durationMs", o.Duration.Milliseconds()}
	for _, v := range []struct{ k, v string }{{"correlationId", o.CorrelationID}, {"messageId", o.MessageID}, {"transactionId", o.TransactionID}, {"walletId", o.WalletID}, {"providerId", o.ProviderID}} {
		if v.v != "" {
			fields = append(fields, v.k, v.v)
		}
	}
	if o.Error != nil {
		d.log.WarnContext(ctx, "operation failed", fields...)
	} else {
		d.log.InfoContext(ctx, "operation committed", fields...)
	}
}
func seconds(d time.Duration) string { return fmt.Sprintf("%d.%09d", d/time.Second, d%time.Second) }
func (d *Diagnostics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case d.scrape <- struct{}{}:
		defer func() { <-d.scrape }()
	default:
		http.Error(w, "metrics busy", 503)
		return
	}
	var b strings.Builder
	b.WriteString("# TYPE wallet_operations_total counter\n# TYPE wallet_events_total counter\n# TYPE wallet_operation_duration_seconds histogram\n")
	d.mu.Lock()
	for _, source := range []string{"http", "sqs", "reference", "opening", "other"} {
		for _, status := range []string{"PROCESSED", "REJECTED", "PENDING_REFERENCE", "FAILED", "ERROR"} {
			fmt.Fprintf(&b, "wallet_operations_total{source=%q,status=%q} %d\n", source, status, d.results[source+"|"+status])
		}
	}
	for _, name := range []string{"duplicates", "payload_conflicts", "db_retry", "concurrency_conflict", "consumer_retry", "consumer_permanent", "consumer_exhausted", "publisher_retry", "reconciliation_divergence"} {
		fmt.Fprintf(&b, "wallet_events_total{event=%q} %d\n", name, d.counts[name])
	}
	for i, limit := range limits {
		fmt.Fprintf(&b, "wallet_operation_duration_seconds_bucket{le=%q} %d\n", seconds(limit), d.buckets[i])
	}
	fmt.Fprintf(&b, "wallet_operation_duration_seconds_bucket{le=\"+Inf\"} %d\nwallet_operation_duration_seconds_count %d\nwallet_operation_duration_seconds_sum %s\n", d.buckets[6], d.total, seconds(d.sum))
	d.mu.Unlock()
	ctx, c := context.WithTimeout(r.Context(), 3*time.Second)
	defer c()
	var pending, lag int64
	if d.pool != nil {
		err := d.pool.QueryRow(ctx, `SELECT count(*),COALESCE(floor(extract(epoch FROM clock_timestamp()-min(created_at))),0)::bigint FROM wallet.outbox_events WHERE published_at IS NULL`).Scan(&pending, &lag)
		if err == nil {
			fmt.Fprintf(&b, "wallet_metrics_dependency_up{dependency=\"postgres\"} 1\nwallet_outbox_pending %d\nwallet_outbox_oldest_age_seconds %d\n", pending, max(lag, 0))
		} else {
			b.WriteString("wallet_metrics_dependency_up{dependency=\"postgres\"} 0\n")
		}
	}
	if d.client != nil {
		q, err := d.client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(d.dlq)})
		if err == nil && q.QueueUrl != nil {
			var out *sqs.GetQueueAttributesOutput
			out, err = d.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: q.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible}})
			if err == nil {
				visible, e1 := strconv.ParseInt(out.Attributes["ApproximateNumberOfMessages"], 10, 64)
				flight, e2 := strconv.ParseInt(out.Attributes["ApproximateNumberOfMessagesNotVisible"], 10, 64)
				if e1 == nil && e2 == nil {
					fmt.Fprintf(&b, "wallet_metrics_dependency_up{dependency=\"sqs\"} 1\nwallet_dlq_visible %d\nwallet_dlq_inflight %d\n", visible, flight)
				} else {
					err = fmt.Errorf("invalid depth")
				}
			}
		}
		if err != nil {
			b.WriteString("wallet_metrics_dependency_up{dependency=\"sqs\"} 0\n")
		}
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
