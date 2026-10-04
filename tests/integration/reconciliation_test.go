//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"jungle-gaming-backend/internal/adapters/postgres"
	"jungle-gaming-backend/internal/domain/money"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStage10ReconciliationExactSnapshotAndLargeTurnover(t *testing.T) {
	f := financialCore(t)
	reader := postgres.NewReader(f.pool, f.options.OperationTimeout)
	zero := stage5Wallet(t, f, "0.00")
	z, e := reader.Reconcile(t.Context(), zero.ID)
	if e != nil || !z.Consistent || z.CheckedEntries != 0 || z.CalculatedBalance.Amount() != "0.00" {
		t.Fatal("zero opening reconciliation", e)
	}
	w := stage5Wallet(t, f, "92233720368547758.07")
	for _, req := range []struct{ kind, ext string }{{"BET", "max-bet"}, {"WIN", "max-win"}, {"BET", "max-bet-2"}} {
		processStage5(t, f, stage5Request(t, w, "provider-a", req.ext, req.ext, req.kind, "92233720368547758.07"))
	}
	r, e := reader.Reconcile(t.Context(), w.ID)
	if e != nil || !r.Consistent || r.CheckedEntries != 4 || r.CalculatedBalance.Amount() != "0.00" {
		t.Fatal("gross turnover overflowed", e)
	}
	// Repeated concurrent commits would reveal a split wallet/ledger snapshot.
	var wg sync.WaitGroup
	wg.Go(func() {
		for n := range 50 {
			req := stage5Request(t, zero, "provider-a", strings.Repeat("w", n+1), strings.Repeat("w", n+1), "WIN", "1.00")
			if _, e := f.service.ProcessWager(t.Context(), req, stage5Trace); e != nil {
				t.Error(e)
			}
		}
	})
	for range 100 {
		r, e := reader.Reconcile(t.Context(), zero.ID)
		if e != nil || !r.Consistent {
			t.Error("false divergence under concurrent commit", e)
		}
	}
	wg.Wait()
	r, e = reader.Reconcile(t.Context(), zero.ID)
	if e != nil || !r.Consistent || r.StoredBalance.Amount() != "50.00" || r.CheckedEntries != 50 {
		t.Fatal("final reconciliation", e)
	}
	ctx, c := context.WithCancel(t.Context())
	c()
	if _, e = reader.Reconcile(ctx, w.ID); e == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestStage10FxAllRolesPublishesAndShutsDown(t *testing.T) {
	input := stage8Queue(t)
	output := stage9Queue(t)
	f := realAPI(t, map[string]string{"APP_ROLES": "api,reference-worker,consumer,outbox-publisher", "CONSUMER_QUEUE": input.name, "PUBLISHER_QUEUE": output, "PUBLISHER_CONCURRENCY": "2", "CONSUMER_CONCURRENCY": "2"})
	token := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	apiOpen(t, f, token, "1.00")
	eventuallyStage8(t, func() bool {
		var n int
		_ = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events WHERE published_at IS NOT NULL`).Scan(&n)
		return n == 2
	})
	ctx, c := context.WithTimeout(context.Background(), 15*time.Second)
	defer c()
	if e := f.app.Stop(ctx); e != nil {
		t.Fatal("Fx shutdown failed", e)
	}
	f.client.CloseIdleConnections()
	if _, e := f.client.Get(f.url + "/health/live"); e == nil {
		t.Fatal("listener survived shutdown")
	}
}
func stage10Metrics(t *testing.T, f apiFixture, token string, want int) string {
	t.Helper()
	req, e := http.NewRequestWithContext(t.Context(), "GET", f.url+"/metrics", nil)
	if e != nil {
		t.Fatal(e)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, e := f.client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(resp.Body)
	if e != nil || resp.StatusCode != want {
		t.Fatalf("metrics status %d want %d: %s", resp.StatusCode, want, body)
	}
	return string(body)
}
func TestStage10RealAuthorizationMetricsAndDivergence(t *testing.T) {
	f := realAPI(t)
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	stage10Metrics(t, f, "", 401)
	stage10Metrics(t, f, a, 403)
	w := apiOpen(t, f, internal, "100.00")
	id := w["id"].(string)
	body := wagerBody(w, "provider-a", "stage10-bet", "80.00", "BET")
	apiCall(t, f, "POST", "/wagering/transactions", a, "stage10-key", body, 201)
	apiCall(t, f, "POST", "/wagering/transactions", a, "stage10-key", body, 200)
	apiCall(t, f, "POST", "/wagering/transactions", a, "stage10-key", wagerBody(w, "provider-a", "stage10-bet", "1.00", "BET"), 409)
	apiCall(t, f, "POST", "/wagering/transactions", a, "stage10-reject", wagerBody(w, "provider-a", "stage10-reject", "80.00", "BET"), 422)
	r := apiCall(t, f, "POST", "/wallets/"+id+"/reconciliation", internal, "", "", 200)
	if r["consistent"] != true || r["checkedEntries"] != json.Number("2") || r["difference"].(map[string]any)["amount"] != "0.00" {
		t.Fatal("API reconciliation mismatch")
	}
	// Administrative corruption is confined to this disposable scratch database.
	var db string
	if e := f.pool.QueryRow(t.Context(), `SELECT current_database()`).Scan(&db); e != nil {
		t.Fatal(e)
	}
	admin := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", db)
	if _, e := admin.Exec(t.Context(), `SET session_replication_role=replica`); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(t.Context(), `UPDATE wallet.wallets SET balance_amount=balance_amount+1 WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(t.Context(), `SET session_replication_role=origin`); e != nil {
		t.Fatal(e)
	}
	r = apiCall(t, f, "POST", "/wallets/"+id+"/reconciliation", internal, "", "", 200)
	if r["consistent"] != false || r["difference"].(map[string]any)["amount"] != "0.01" {
		t.Fatal("corruption not reported")
	}
	wallet := apiCall(t, f, "GET", "/wallets/"+id, internal, "", "", 200)
	if wallet["balance"].(map[string]any)["amount"] != "20.01" {
		t.Fatal("reconciliation modified money")
	}
	metrics := stage10Metrics(t, f, internal, 200)
	for _, want := range []string{`wallet_operations_total{source="http",status="PROCESSED"} 2`, `wallet_operations_total{source="http",status="REJECTED"} 1`, `wallet_events_total{event="duplicates"} 1`, `wallet_events_total{event="payload_conflicts"} 1`, `wallet_events_total{event="reconciliation_divergence"} 1`, `wallet_outbox_pending 5`, `wallet_dlq_visible `, `wallet_operation_duration_seconds_count 5`} {
		if !strings.Contains(metrics, want) {
			t.Errorf("missing metric %s", want)
		}
	}
	for _, secret := range []string{id, "stage10-key", a, internal, "80.00"} {
		if strings.Contains(metrics, secret) {
			t.Fatal("metrics contains sensitive/high cardinality values")
		}
	}
}
func TestStage10ReconciliationFinalOverflowIsExplicit(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	processStage5(t, f, stage5Request(t, w, "provider-a", "overflow-test", "overflow-test", "WIN", "1.00"))
	admin := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", f.db)
	if _, e := admin.Exec(t.Context(), `SET session_replication_role=replica`); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(t.Context(), `UPDATE wallet.ledger_entries SET amount=9223372036854775807,balance_before=0,balance_after=9223372036854775807 WHERE wallet_id=$1`, w.ID.String()); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.Exec(t.Context(), `SET session_replication_role=origin`); e != nil {
		t.Fatal(e)
	}
	reader := postgres.NewReader(f.pool, f.options.OperationTimeout)
	if _, e := reader.Reconcile(t.Context(), w.ID); !errors.Is(e, money.ErrOverflow) {
		t.Fatal("overflow silently truncated", e)
	}
}
