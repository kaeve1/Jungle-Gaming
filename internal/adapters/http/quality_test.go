package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type reconciliationReader struct {
	application.Reader
	result application.Reconciliation
}

func (r reconciliationReader) Reconcile(context.Context, domain.ID) (application.Reconciliation, error) {
	return r.result, nil
}

type internalVerifier struct{}

func (internalVerifier) Verify(context.Context, string) (application.Principal, error) {
	return application.Principal{Internal: true, Permissions: []string{"wallet:admin"}}, nil
}

type fixedIDs struct{ id domain.ID }

func (f fixedIDs) NewID() domain.ID { return f.id }

type diagnosticCounter struct{ count int }

func (*diagnosticCounter) Observe(context.Context, application.Observation) {}
func (d *diagnosticCounter) Count(name string) {
	if name == "reconciliation_divergence" {
		d.count++
	}
}

func TestMetricsWrapperKeepsReconciliationDiagnostics(t *testing.T) {
	id, err := domain.ParseID("0192f291-27dd-7d3f-8071-5f8685deef37")
	if err != nil {
		t.Fatal(err)
	}
	zero, _ := money.Parse("0.00", "BRL")
	one, _ := money.Parse("1.00", "BRL")
	d := &diagnosticCounter{}
	// This http.Handler has no Diagnostics interface. The explicit dependency
	// must still receive divergence counts, independent of metrics middleware.
	wrappedMetrics := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := NewHandler(nil, reconciliationReader{result: application.Reconciliation{WalletID: id, StoredBalance: one, CalculatedBalance: zero, Difference: one}}, internalVerifier{}, nil, fixedIDs{id}, slog.New(slog.NewJSONHandler(io.Discard, nil)), wrappedMetrics, d)
	req := httptest.NewRequest("POST", "/wallets/"+id.String()+"/reconciliation", nil)
	req.Header.Set("Authorization", "Bearer test")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || d.count != 1 {
		t.Fatalf("status=%d divergence counts=%d", rec.Code, d.count)
	}
	req = httptest.NewRequest("GET", "/metrics", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("metrics lost authentication")
	}
}

func TestTypedResponsesPreserveNullsAndOpeningMetadata(t *testing.T) {
	id, _ := domain.ParseID("0192f291-27dd-7d3f-8071-5f8685deef37")
	amount, _ := money.Parse("1000.00", "BRL")
	at := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	b, err := json.Marshal(transactionJSON(domain.TransactionSnapshot{ID: id, Origin: domain.OriginInternal, WalletID: id, PlayerID: id, Kind: domain.KindOpening, Money: amount, Status: domain.StatusProcessed, ResultBalance: &amount, CreatedAt: at, UpdatedAt: at}))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 19 {
		t.Fatalf("unexpected transaction field count %d", len(out))
	}
	for _, key := range []string{"providerId", "externalTransactionId", "roundId", "gameId", "referenceExternalTransactionId", "referenceTransactionId", "failureCode", "nextReferenceAttemptAt"} {
		if string(out[key]) != "null" {
			t.Fatalf("%s must be present as null", key)
		}
	}
	if string(out["money"]) != `{"amount":"1000.00","currency":"BRL"}` {
		t.Fatal("monetary representation changed")
	}
	if string(out["createdAt"]) != `"2026-09-08T12:00:00.000Z"` {
		t.Fatal("timestamp changed")
	}
	b, err = json.Marshal(ledgerResponse{WalletID: id, Entries: []ledgerEntryResponse{}})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if string(out["entries"]) != "[]" || string(out["nextCursor"]) != "null" {
		t.Fatal("empty ledger contract changed")
	}
}
