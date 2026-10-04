//go:build integration && distributed

package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Run only through scripts/test-distributed.ps1. The host supervisor verifies
// Compose identity and pauses only the named test containers, never development.
func stage11Control(t *testing.T, service, action string) {
	t.Helper()
	dir := os.Getenv("STAGE11_CONTROL_DIR")
	if dir == "" {
		t.Fatal("container outages require scripts/test-distributed.ps1")
	}
	if e := os.WriteFile(filepath.Join(dir, service+"."+action), []byte("requested"), 0600); e != nil {
		t.Fatal(e)
	}
	ack := "paused"
	if action == "resume" {
		ack = "resumed"
	}
	stage11Wait(t, 15*time.Second, func() bool { _, e := os.Stat(filepath.Join(dir, service+"."+ack)); return e == nil })
}
func TestStage11RealContainerOutagesAndRecovery(t *testing.T) {
	defer func() {
		dir := os.Getenv("STAGE11_CONTROL_DIR")
		for _, service := range []string{"postgres", "sqs"} {
			if _, e := os.Stat(filepath.Join(dir, service+".paused")); e == nil {
				_ = os.WriteFile(filepath.Join(dir, service+".resume"), []byte("cleanup"), 0600)
			}
		}
	}()
	f := financialCore(t)
	input := stage8Queue(t)
	output := stage9Queue(t)
	var ps []*stage11Process
	// Delay reference recovery deliberately while both the pending request and its
	// reference are committed. Restart a recovery worker after the DB pause/TTL.
	for range 3 {
		ps = append(ps, stage11Start(t, f.db, input.name, output, "api,consumer,outbox-publisher", "", "2s"))
	}
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wm := apiOpen(t, ps[0].api, internal, "100.00")
	w := stage11Wallet(t, f, wm)
	apiCall(t, ps[0].api, "POST", "/wagering/transactions", a, "outage-refund11", stage11PendingBody(wm, "outage-refund11", "outage-bet11", "REFUND", "5.00"), 202)
	apiCall(t, ps[1].api, "POST", "/wagering/transactions", a, "outage-bet11", wagerBody(wm, "provider-a", "outage-bet11", "5.00", "BET"), 201)
	stage11Published(t, f)
	stage11Control(t, "postgres", "pause")
	for _, p := range ps {
		apiCall(t, p.api, "GET", "/health/ready", "", "", "", 503)
		apiCall(t, p.api, "GET", "/health/live", "", "", "", 200)
	}
	// Valid input waits in SQS while known DB outage prevents taking new work.
	req := stage5Request(t, w, "provider-a", "outage-win11", "outage-win11", "WIN", "1.00")
	stage8Send(t, stage8Body(t, req, "outage-message11"), w.ID.String())
	stage11Control(t, "postgres", "resume")
	for _, p := range ps {
		apiCall(t, p.api, "GET", "/health/ready", "", "", "", 200)
	}
	ps[0].stop(t, false)
	ps[0] = stage11Start(t, f.db, input.name, output, stage11Roles, "", "2s")
	stage11Status(t, f, "outage-refund11", "PROCESSED")
	stage11Status(t, f, "outage-win11", "PROCESSED")
	// Retry through HTTP after SQS resumes; no extra credit.
	apiCall(t, ps[2].api, "POST", "/wagering/transactions", a, "outage-win11", wagerBody(wm, "provider-a", "outage-win11", "1.00", "WIN"), 200)
	stage5HistoryWithoutTrace(t, f, w, 10100, 4, 4, 4, 9)
	stage11Published(t, f)
	stage11Control(t, "sqs", "pause")
	for _, p := range ps {
		apiCall(t, p.api, "GET", "/health/ready", "", "", "", 503)
		apiCall(t, p.api, "GET", "/health/live", "", "", "", 200)
	}
	// HTTP financial commit is possible while SQS is temporarily unavailable.
	apiCall(t, ps[1].api, "POST", "/wagering/transactions", a, "broker-down11", wagerBody(wm, "provider-a", "broker-down11", "2.00", "WIN"), 201)
	var unpublished int
	if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.outbox_events WHERE published_at IS NULL`).Scan(&unpublished); e != nil || unpublished != 2 {
		t.Fatal("outbox did not preserve unsent events", e)
	}
	stage11Control(t, "sqs", "resume")
	stage11Published(t, f)
	for _, p := range ps {
		apiCall(t, p.api, "GET", "/health/ready", "", "", "", 200)
	}
	stage5HistoryWithoutTrace(t, f, w, 10300, 5, 5, 5, 11)
	t.Log("real test PostgreSQL and SQS paused/resumed; three processes remained alive; reference past TTL recovered; no duplicate credit or loss of unpublished events")
}
