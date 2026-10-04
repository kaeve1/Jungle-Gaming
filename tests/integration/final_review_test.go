//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"jungle-gaming-backend/internal/adapters/observability"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
)

func TestStage12ReferenceLogKeepsInputMessageIdentity(t *testing.T) {
	f := financialCore(t)
	var logs bytes.Buffer
	d := observability.New(nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)), "")
	var err error
	f.service, err = application.NewService(f.uow, appRuntime.IDs{}, domain.ReferencePolicy{TTL: time.Second, BaseDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}, d)
	if err != nil {
		t.Fatal(err)
	}
	w := stage5Wallet(t, f, "100.00")
	const message = "message-pending12"
	if err = stage8Consume(t, f, refRequest(t, w, "trace-refund", "REFUND", "10.00", "trace-bet"), message); err != nil {
		t.Fatal(err)
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "trace-bet", "trace-bet", "BET", "10.00"))
	time.Sleep(15 * time.Millisecond)
	if worked, err := f.service.ResumeReference(t.Context()); !worked || err != nil {
		t.Fatalf("reference resolution: %v, %v", worked, err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if err = json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry["source"] == "reference" {
			found = true
			if entry["messageId"] != message || entry["correlationId"] != message || entry["status"] != "PROCESSED" {
				t.Fatal("reference log confused input message identity with pending event")
			}
		}
	}
	if !found {
		t.Fatal("missing reference diagnostic")
	}
	var linked int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events final JOIN wallet.outbox_events pending ON final.payload->>'causationId'=pending.event_id::text WHERE final.event_type='WagerTransactionProcessed' AND pending.event_type='WagerTransactionPendingReference'`).Scan(&linked); err != nil || linked != 1 {
		t.Fatal("event causation must still link to durable pending event")
	}
	stage5HistoryWithoutTrace(t, f, w, 10000, 3, 3, 3, 7)
}

func TestStage12FailedMetricRequiresConfirmedAudit(t *testing.T) {
	for _, rollbackAudit := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback-audit-%v", rollbackAudit), func(t *testing.T) {
			f := financialCore(t)
			d := observability.New(nil, nil, slog.New(slog.NewJSONHandler(io.Discard, nil)), "")
			var err error
			f.service, err = application.NewService(f.uow, appRuntime.IDs{}, domain.ReferencePolicy{TTL: time.Second, BaseDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}, d)
			if err != nil {
				t.Fatal(err)
			}
			w := stage5Wallet(t, f, "100.00")
			processStage5(t, f, refRequest(t, w, "metric-refund", "REFUND", "10.00", "metric-bet"))
			processStage5(t, f, stage5Request(t, w, "provider-a", "metric-bet", "metric-bet", "BET", "10.00"))
			admin := connect(t, "postgres", "TEST_PG_SUPERUSER_PASSWORD", f.db)
			_, err = admin.Exec(t.Context(), `CREATE FUNCTION wallet.test_metric_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test' USING ERRCODE='23000',CONSTRAINT='test_metric_failure'; END $$; CREATE TRIGGER test_metric_failure BEFORE INSERT ON wallet.ledger_entries FOR EACH ROW EXECUTE FUNCTION wallet.test_metric_failure()`)
			if err != nil {
				t.Fatal(err)
			}
			if rollbackAudit {
				_, err = admin.Exec(t.Context(), `CREATE FUNCTION wallet.test_audit_rollback() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='FAILED' THEN RAISE EXCEPTION 'test' USING ERRCODE='23000',CONSTRAINT='test_audit_rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_audit_rollback BEFORE UPDATE ON wallet.wager_transactions FOR EACH ROW EXECUTE FUNCTION wallet.test_audit_rollback()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			for range 3 {
				time.Sleep(15 * time.Millisecond)
				if worked, err := f.service.ResumeReference(t.Context()); worked || err == nil {
					t.Fatal("expected original permanent attempt error")
				}
			}
			var status string
			if err = f.pool.QueryRow(t.Context(), `SELECT status FROM wallet.wager_transactions WHERE external_transaction_id='metric-refund'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			failed, errored := 1, 2
			if rollbackAudit {
				failed, errored = 0, 3
				if status != "PENDING_REFERENCE" {
					t.Fatal("rolled-back audit cannot change durable status")
				}
			} else if status != "FAILED" {
				t.Fatal("terminal failure audit was not committed")
			}
			recorder := httptest.NewRecorder()
			d.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
			for _, want := range []string{
				fmt.Sprintf(`wallet_operations_total{source="reference",status="FAILED"} %d`, failed),
				fmt.Sprintf(`wallet_operations_total{source="reference",status="ERROR"} %d`, errored),
			} {
				if !strings.Contains(recorder.Body.String(), want) {
					t.Fatalf("metric must reflect confirmed outcome: %s", want)
				}
			}
			stage5HistoryWithoutTrace(t, f, w, 9000, 2, 3, 2, 5)
		})
	}
}

func TestStage12ThreeProcessesContestReplays(t *testing.T) {
	f := financialCore(t)
	input := stage8Queue(t)
	output := stage9Queue(t)
	var ps []*stage11Process
	for range 3 {
		ps = append(ps, stage11Start(t, f.db, input.name, output, "api", "", ""))
	}
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	provider := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wm := apiOpen(t, ps[0].api, internal, "100.00")
	w := stage11Wallet(t, f, wm)
	type response struct {
		status int
		body   map[string]any
	}
	var replies [2]response
	var wg sync.WaitGroup
	start := make(chan struct{})
	for n := range 2 {
		wg.Go(func() {
			<-start
			ext := fmt.Sprintf("contest12-%d", n)
			s, body, err := stage11Call(t, ps[n], provider, ext, wagerBody(wm, "provider-a", ext, "80.00", "BET"), ext)
			if err != nil {
				t.Error(err)
			}
			replies[n] = response{s, body}
		})
	}
	close(start)
	wg.Wait()
	processed, rejected := 0, 0
	for _, reply := range replies {
		switch reply.status {
		case 201:
			processed++
			if reply.body["status"] != "PROCESSED" {
				t.Fatal("winner did not persist PROCESSED")
			}
		case 422:
			rejected++
			if reply.body["status"] != "REJECTED" || reply.body["failureCode"] != "INSUFFICIENT_FUNDS" {
				t.Fatal("loser must persist the precise insufficient-funds code")
			}
		default:
			t.Fatalf("unexpected contest response: %d", reply.status)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatal("contest must have exactly one winner and one loser")
	}
	stage5HistoryWithoutTrace(t, f, w, 2000, 2, 3, 2, 5)
	apiCall(t, ps[2].api, "POST", "/wagering/transactions", provider, "later12", wagerBody(wm, "provider-a", "later12", "1.00", "WIN"), 201)
	for n, first := range replies {
		ext := fmt.Sprintf("contest12-%d", n)
		want := 200
		if first.status == 422 {
			want = 422
		}
		replay := apiCall(t, ps[(n+1)%3].api, "POST", "/wagering/transactions", provider, ext, wagerBody(wm, "provider-a", ext, "80.00", "BET"), want)
		balance, ok := replay["balance"].(map[string]any)
		if !ok || balance["amount"] != "20.00" || replay["idempotentReplay"] != true || replay["transactionId"] != first.body["transactionId"] || replay["failureCode"] != first.body["failureCode"] {
			t.Fatal("winner and loser replay must preserve original identity, balance and code")
		}
	}
	stage5HistoryWithoutTrace(t, f, w, 2100, 3, 4, 3, 7)
}

// Complements type/value tests with real provider-scoped SQL lookup and
// cross-wallet/currency/round boundaries, always checking financial effects.
func TestStage12ReferenceMetadataBoundaries(t *testing.T) {
	f := financialCore(t)
	w := stage5Wallet(t, f, "100.00")
	other := stage5Wallet(t, f, "100.00")
	usd, err := f.service.OpenWallet(t.Context(), w.PlayerID, stage5Money(t, "100.00", "USD"), stage5Trace)
	if err != nil {
		t.Fatal(err)
	}
	processStage5(t, f, stage5Request(t, w, "provider-a", "original", "original", "BET", "10.00"))
	for _, tc := range []struct {
		name, provider, round, currency string
		wallet                          domain.WalletSnapshot
		status                          domain.Status
		code                            domain.FailureCode
	}{
		{"other-wallet-player", "provider-a", "round-1", "BRL", other, domain.StatusRejected, domain.CodeReferenceMismatch},
		{"other-currency", "provider-a", "round-1", "USD", usd, domain.StatusRejected, domain.CodeReferenceMismatch},
		{"other-round", "provider-a", "round-2", "BRL", w, domain.StatusRejected, domain.CodeReferenceMismatch},
		{"other-provider", "provider-b", "round-1", "BRL", w, domain.StatusPendingReference, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: tc.provider, ExternalTransactionID: tc.name, IdempotencyKey: tc.name, PlayerID: tc.wallet.PlayerID.String(), WalletID: tc.wallet.ID.String(), RoundID: tc.round, GameID: "game-1", Kind: "REFUND", Money: stage5Money(t, "10.00", tc.currency), ReferenceExternalTransactionID: "original"})
			if err != nil {
				t.Fatal(err)
			}
			got := processStage5(t, f, req)
			if got.Transaction.Status != tc.status || got.Transaction.FailureCode != tc.code {
				t.Fatalf("boundary result: %s/%s", got.Transaction.Status, got.Transaction.FailureCode)
			}
			if tc.provider == "provider-b" && !got.Transaction.ReferenceTransactionID.IsZero() {
				t.Fatal("another provider's reference must not be resolved or exposed")
			}
			var entries int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.ledger_entries`).Scan(&entries); err != nil || entries != 4 {
				t.Fatalf("invalid reference changed ledger: %d, %v", entries, err)
			}
		})
	}
	for _, check := range []struct {
		wallet domain.WalletSnapshot
		units  int64
	}{{w, 9000}, {other, 10000}, {usd, 10000}} {
		var balance, calculated int64
		if err := f.pool.QueryRow(t.Context(), `SELECT balance_amount,(SELECT SUM(CASE direction WHEN 'CREDIT' THEN amount ELSE -amount END)::bigint FROM wallet.ledger_entries WHERE wallet_id=$1) FROM wallet.wallets WHERE id=$1`, check.wallet.ID.String()).Scan(&balance, &calculated); err != nil || balance != check.units || calculated != balance {
			t.Fatalf("financial history changed across reference boundary: %d/%d, %v", balance, calculated, err)
		}
	}
}
