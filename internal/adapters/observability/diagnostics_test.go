package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"jungle-gaming-backend/internal/application"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLogsAreSanitizedAndMetricLabelsBounded(t *testing.T) {
	var log bytes.Buffer
	d := New(nil, nil, slog.New(slog.NewJSONHandler(&log, nil)), "")
	for range 100 {
		d.Observe(t.Context(), application.Observation{Source: "unbounded-source", Status: "unbounded-status", CorrelationID: "corr", MessageID: "msg", TransactionID: "tx", WalletID: "wallet", ProviderID: "provider", Error: errors.New("secret-password payload 80.00"), Duration: time.Millisecond})
		d.Count("unbounded-event")
	}
	if strings.Contains(log.String(), "secret-password") || strings.Contains(log.String(), "80.00") {
		t.Fatal("error payload leaked")
	}
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		var entry map[string]json.RawMessage
		if e := json.Unmarshal([]byte(line), &entry); e != nil {
			t.Fatal(e)
		}
		for _, id := range []string{"correlationId", "messageId", "transactionId", "walletId", "providerId"} {
			if entry[id] == nil {
				t.Fatal("missing available ID", id)
			}
		}
	}
	recorder := httptest.NewRecorder()
	d.ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `wallet_operations_total{source="other",status="ERROR"} 100`) {
		t.Fatal("unknown labels not bounded")
	}
	for _, v := range []string{"unbounded", `walletId=`, `providerId=`, `transactionId=`} {
		if strings.Contains(body, v) {
			t.Fatal("unbounded metric label")
		}
	}
}
