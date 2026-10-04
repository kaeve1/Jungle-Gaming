//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"jungle-gaming-backend/internal/adapters/postgres"
	appRuntime "jungle-gaming-backend/internal/adapters/runtime"
	"jungle-gaming-backend/internal/bootstrap"
)

type apiFixture struct {
	url    string
	client *http.Client
	pool   *pgxpool.Pool
	app    *bootstrap.Instance
}

func realAPI(t *testing.T, overrides ...map[string]string) apiFixture {
	t.Helper()
	db := scratchDatabase(t, true)
	settings := map[string]string{"DATABASE_URL": fmt.Sprintf("postgres://wallet_app:%s@%s:5432/%s?sslmode=disable", url.QueryEscape(env(t, "TEST_PG_APP_PASSWORD")), env(t, "TEST_PG_HOST"), db),
		"APP_ROLES": "api", "HTTP_ADDRESS": "127.0.0.1:0", "ALLOW_INSECURE_LOCAL": "true", "OIDC_ISSUER": env(t, "TEST_OIDC_ISSUER"), "OIDC_JWKS_URL": env(t, "TEST_OIDC_JWKS_URL"),
		"SQS_ENDPOINT": env(t, "TEST_SQS_ENDPOINT"), "SQS_CREDENTIALS_FILE": env(t, "TEST_SQS_SERVICE_CREDENTIALS_FILE"), "INSTANCE_ID": "api-integration"}
	for _, override := range overrides {
		for k, v := range override {
			settings[k] = v
		}
	}
	app, err := bootstrap.NewInstance(func(k string) string { return settings[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err = app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("API shutdown: %v", err)
		}
	})
	pool, err := postgres.OpenPool(t.Context(), settings["DATABASE_URL"], postgres.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return apiFixture{"http://" + app.Address(), &http.Client{Timeout: 25 * time.Second}, pool, app}
}
func accessToken(t *testing.T, client, secretVar string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {env(t, secretVar)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, env(t, "TEST_OIDC_TOKEN_URL"), strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("IdP status %d", resp.StatusCode)
	}
	var body struct {
		Token string `json:"access_token"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Token
}
func apiCall(t *testing.T, f apiFixture, method, path, token, key, body string, want int) map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, f.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Correlation-Id", "http-stage6")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	d := json.NewDecoder(resp.Body)
	d.UseNumber()
	if err = d.Decode(&result); err != nil {
		t.Fatalf("response JSON (status %d): %v", resp.StatusCode, err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s status %d want %d response=%v", method, path, resp.StatusCode, want, result)
	}
	if want == 503 && path != "/health/ready" && resp.Header.Get("Retry-After") != "1" {
		t.Fatal("transient response missing Retry-After")
	}
	if resp.Header.Get("X-Correlation-Id") != "http-stage6" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("response metadata missing")
	}
	return result
}
func apiOpen(t *testing.T, f apiFixture, token, amount string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":%q,"currency":"BRL"}}`, appRuntime.IDs{}.NewID().String(), amount)
	return apiCall(t, f, "POST", "/wallets", token, "", body, 201)
}
func wagerBody(wallet map[string]any, provider, ext, amount, kind string) string {
	return fmt.Sprintf(`{"providerId":%q,"externalTransactionId":%q,"playerId":%q,"walletId":%q,"roundId":"round-1","gameId":"game-1","kind":%q,"money":{"amount":%q,"currency":"BRL"}}`, provider, ext, wallet["playerId"], wallet["id"], kind, amount)
}
func apiCode(t *testing.T, result map[string]any, want string) {
	t.Helper()
	if result["error"].(map[string]any)["code"] != want {
		t.Fatalf("error code: %v want %s", result, want)
	}
}

func TestStage6AuthenticatedHTTPAndProviderIsolation(t *testing.T) {
	f := realAPI(t)
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	b := accessToken(t, "provider-b", "KC_SECRET_PROVIDER_B")
	noRoles := accessToken(t, "no-roles", "KC_SECRET_NO_ROLES")
	apiCall(t, f, "GET", "/health/live", "", "", "", 200)
	apiCall(t, f, "GET", "/health/ready", "", "", "", 200)
	apiCode(t, apiCall(t, f, "POST", "/wallets", "", "", `{}`, 401), "UNAUTHENTICATED")
	apiCall(t, f, "POST", "/wallets", "invalid", "", `{}`, 401)
	apiCall(t, f, "POST", "/wallets", noRoles, "", `{}`, 403)
	wallet := apiOpen(t, f, internal, "100.00")
	id := wallet["id"].(string)
	for _, path := range []string{"/wallets/" + id, "/wallets/" + id + "/ledger"} {
		apiCall(t, f, "GET", path, a, "", "", 403)
	}
	apiCall(t, f, "POST", "/wallets", a, "", `{}`, 403)
	apiCall(t, f, "POST", "/wallets/"+id+"/reconciliation", a, "", "", 403)
	apiCall(t, f, "POST", "/wallets/"+id+"/reconciliation", internal, "", "", 200)
	apiCall(t, f, "GET", "/wallets/"+id, internal, "", "", 200)
	openingPage := apiCall(t, f, "GET", "/wallets/"+id+"/ledger", internal, "", "", 200)
	openingID := openingPage["entries"].([]any)[0].(map[string]any)["transactionId"].(string)
	apiCall(t, f, "GET", "/wagering/transactions/"+openingID, a, "", "", 404)
	opening := apiCall(t, f, "GET", "/wagering/transactions/"+openingID, internal, "", "", 200)
	if opening["origin"] != "INTERNAL" || opening["providerId"] != nil {
		t.Fatal("internal opening detail corrupted")
	}
	duplicate := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":"0.00","currency":"BRL"}}`, wallet["playerId"])
	apiCode(t, apiCall(t, f, "POST", "/wallets", internal, "", duplicate, 409), "WALLET_ALREADY_EXISTS")
	body := wagerBody(wallet, "provider-a", "bet-1", "25.00", "BET")
	apiCode(t, apiCall(t, f, "POST", "/wagering/transactions", a, "", body, 400), "MISSING_IDEMPOTENCY_KEY")
	apiCode(t, apiCall(t, f, "POST", "/wagering/transactions", b, "key-1", body, 403), "PROVIDER_MISMATCH")
	result := apiCall(t, f, "POST", "/wagering/transactions", a, "key-1", body, 201)
	txID := result["transactionId"].(string)
	apiCall(t, f, "GET", "/wagering/transactions/"+txID, a, "", "", 200)
	denied := apiCall(t, f, "GET", "/wagering/transactions/"+txID, b, "", "", 404)
	apiCode(t, denied, "TRANSACTION_NOT_FOUND")
	if strings.Contains(fmt.Sprint(denied), "balance") {
		t.Fatal("cross-provider read exposed balance")
	}
	apiCall(t, f, "GET", "/wagering/transactions/"+txID, internal, "", "", 200)
	apiCall(t, f, "GET", "/providers/provider-a/wagering/transactions/bet-1", a, "", "", 200)
	apiCode(t, apiCall(t, f, "GET", "/providers/provider-a/wagering/transactions/bet-1", b, "", "", 403), "PROVIDER_MISMATCH")
	apiCall(t, f, "GET", "/providers/provider-b/wagering/transactions/bet-1", b, "", "", 404)
	apiCall(t, f, "POST", "/wagering/transactions", a, "win-key", wagerBody(wallet, "provider-a", "win-1", "10.00", "WIN"), 201)
	replayed := apiCall(t, f, "POST", "/wagering/transactions", a, "key-1", body, 200)
	if replayed["idempotentReplay"] != true || replayed["balance"].(map[string]any)["amount"] != "75.00" {
		t.Fatal("HTTP replay did not preserve original balance")
	}
	apiCode(t, apiCall(t, f, "POST", "/wagering/transactions", a, "key-1", wagerBody(wallet, "provider-a", "bet-1", "26.00", "BET"), 409), "IDEMPOTENCY_KEY_REUSED")
	apiCode(t, apiCall(t, f, "POST", "/wagering/transactions", a, "other-key", body, 409), "EXTERNAL_TRANSACTION_CONFLICT")
	rejected := apiCall(t, f, "POST", "/wagering/transactions", a, "rejected", wagerBody(wallet, "provider-a", "rejected", "100.00", "BET"), 422)
	if rejected["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatal("wrong rejection code")
	}
	apiCall(t, f, "POST", "/wagering/transactions", a, "rejected", wagerBody(wallet, "provider-a", "rejected", "100.00", "BET"), 422)
	privateWallet := map[string]any{"id": wallet["id"], "playerId": appRuntime.IDs{}.NewID().String()}
	private := apiCall(t, f, "POST", "/wagering/transactions", a, "private", wagerBody(privateWallet, "provider-a", "private", "1.00", "BET"), 422)
	if private["balance"] != nil || private["failureCode"] != "WALLET_PLAYER_MISMATCH" {
		t.Fatal("mismatched player exposed balance")
	}
	apiCode(t, apiCall(t, f, "POST", "/wagering/transactions", a, "opening", wagerBody(wallet, "provider-a", "opening", "1.00", "OPENING"), 400), "INVALID_KIND")
	var balance, entries, events int64
	if err := f.pool.QueryRow(t.Context(), `SELECT balance_amount,(SELECT count(*) FROM wallet.ledger_entries WHERE wallet_id=$1),(SELECT count(*) FROM wallet.outbox_events WHERE partition_key=$1) FROM wallet.wallets WHERE id=$1`, id).Scan(&balance, &entries, &events); err != nil {
		t.Fatal(err)
	}
	if balance != 8500 || entries != 3 || events != 8 {
		t.Fatalf("unauthorized or replay effect: balance=%d entries=%d events=%d", balance, entries, events)
	}
}

func TestStage6StrictJSONAndLedgerPagination(t *testing.T) {
	f := realAPI(t)
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wallet := apiOpen(t, f, internal, "100.00")
	base := wagerBody(wallet, "provider-a", "invalid", "1.00", "BET")
	for _, body := range []string{
		strings.Replace(base, `"providerId"`, `"ProviderId"`, 1), strings.Replace(base, `"money"`, `"Money"`, 1),
		strings.Replace(base, `"kind":"BET"`, `"kind":"BET","kind":"WIN"`, 1),
		strings.Replace(base, `"amount":"1.00"`, `"amount":"1.00","amount":"99.00"`, 1),
		strings.Replace(base, `"amount":"1.00"`, `"amount":1.00`, 1), strings.Replace(base, `"amount":"1.00"`, `"amount":"1e2"`, 1),
		strings.Replace(base, `"currency":"BRL"`, `"currency":"BRL","Currency":"USD"`, 1),
		strings.Replace(base, `"providerId"`, `"unexpected":"value","providerId"`, 1), base + `{}`, `null`, `[]`,
	} {
		apiCall(t, f, "POST", "/wagering/transactions", a, "invalid-key", body, 400)
	}
	apiCall(t, f, "POST", "/wagering/transactions", a, "huge", "{\"padding\":\""+strings.Repeat("a", 70<<10)+"\"}", 413)
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.wager_transactions WHERE origin='EXTERNAL'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid JSON persisted %d %v", count, err)
	}
	for i := 0; i < 4; i++ {
		apiCall(t, f, "POST", "/wagering/transactions", a, fmt.Sprintf("win-%d", i), wagerBody(wallet, "provider-a", fmt.Sprintf("win-%d", i), "1.00", "WIN"), 201)
	}
	id := wallet["id"].(string)
	cursor := ""
	seen := map[string]bool{}
	versions := []int64{}
	for {
		path := "/wallets/" + id + "/ledger?limit=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		page := apiCall(t, f, "GET", path, internal, "", "", 200)
		for _, raw := range page["entries"].([]any) {
			entry := raw.(map[string]any)
			entryID := entry["id"].(string)
			if seen[entryID] {
				t.Fatal("duplicate paginated ledger entry")
			}
			seen[entryID] = true
			v, err := entry["walletVersion"].(json.Number).Int64()
			if err != nil {
				t.Fatal(err)
			}
			versions = append(versions, v)
		}
		if page["nextCursor"] == nil {
			break
		}
		cursor = page["nextCursor"].(string)
	}
	if fmt.Sprint(versions) != "[1 2 3 4 5]" {
		t.Fatalf("ledger order: %v", versions)
	}
	for _, query := range []string{"limit=0", "limit=201", "limit=abc", "limit=1&limit=2", "cursor=garbage", "unknown=1"} {
		apiCall(t, f, "GET", "/wallets/"+id+"/ledger?"+query, internal, "", "", 400)
	}
	first := apiCall(t, f, "GET", "/wallets/"+id+"/ledger?limit=1", internal, "", "", 200)
	other := apiOpen(t, f, internal, "0.00")
	apiCode(t, apiCall(t, f, "GET", "/wallets/"+other["id"].(string)+"/ledger?cursor="+url.QueryEscape(first["nextCursor"].(string)), internal, "", "", 400), "INVALID_CURSOR")
	apiCall(t, f, "GET", "/wallets/"+other["id"].(string)+"/ledger", internal, "", "", 200)
}

func TestStage6ExpiredAndForgedKeycloakTokens(t *testing.T) {
	f := realAPI(t)
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	parts := strings.Split(a, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	payload = bytes.ReplaceAll(payload, []byte("provider-a"), []byte("provider-b"))
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	apiCall(t, f, "GET", "/wagering/transactions/"+appRuntime.IDs{}.NewID().String(), strings.Join(parts, "."), "", "", 401)
	short := accessToken(t, "provider-a-short-lived", "KC_SECRET_PROVIDER_A_SHORT_LIVED")
	parts = strings.Split(short, ".")
	payload, _ = base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	wait := time.Until(time.Unix(claims.Exp, 0).Add(100 * time.Millisecond))
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal("test canceled")
		}
	}
	apiCall(t, f, "POST", "/wagering/transactions", short, "expired", `{}`, 401)
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.wager_transactions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unauthenticated requests persisted data")
	}
}

func TestStage6TransientHTTPFailureAndLifecycle(t *testing.T) {
	f := realAPI(t, map[string]string{"DB_LOCK_TIMEOUT": "30ms", "DB_STATEMENT_TIMEOUT": "1s", "DB_OPERATION_TIMEOUT": "2s", "DB_MAX_RETRIES": "0"})
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wallet := apiOpen(t, f, internal, "100.00")
	id := wallet["id"].(string)
	blocker, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), `SELECT id FROM wallet.wallets WHERE id=$1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	body := wagerBody(wallet, "provider-a", "retry", "25.00", "BET")
	apiCode(t, apiCall(t, f, "POST", "/wagering/transactions", a, "retry", body, 503), "SERVICE_UNAVAILABLE")
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var count int
	var balance int64
	if err = f.pool.QueryRow(t.Context(), `SELECT balance_amount,(SELECT count(*) FROM wallet.wager_transactions WHERE origin='EXTERNAL') FROM wallet.wallets WHERE id=$1`, id).Scan(&balance, &count); err != nil || balance != 10000 || count != 0 {
		t.Fatalf("transient failure persisted partial data: %d/%d %v", balance, count, err)
	}
	apiCall(t, f, "POST", "/wagering/transactions", a, "retry", body, 201)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = f.app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := f.client.Get(f.url + "/health/live")
	if err == nil {
		resp.Body.Close()
		t.Fatal("listener remained open after Fx stop")
	}
	var ownConnections int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND usename='wallet_app'`).Scan(&ownConnections); err != nil {
		t.Fatal(err)
	}
	if ownConnections > int(f.pool.Stat().TotalConns()) {
		t.Fatalf("application pool survived shutdown: %d connections", ownConnections)
	}
}

func TestStage6AmbiguousHeadersRejected(t *testing.T) {
	f := realAPI(t)
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	wallet := apiOpen(t, f, internal, "100.00")
	for _, tc := range []struct {
		name, extra string
		status      int
	}{{"Authorization", "Bearer invalid", 401}, {"Idempotency-Key", "other-key", 400}, {"X-Correlation-Id", "other-correlation", 400}, {"Content-Type", "text/plain", 400}} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), "POST", f.url+"/wagering/transactions", strings.NewReader(wagerBody(wallet, "provider-a", "headers", "1.00", "BET")))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+a)
			req.Header.Set("Idempotency-Key", "header-key")
			req.Header.Set("X-Correlation-Id", "header-correlation")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Add(tc.name, tc.extra)
			resp, err := f.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("duplicate %s: status %d", tc.name, resp.StatusCode)
			}
		})
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM wallet.wager_transactions WHERE origin='EXTERNAL'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ambiguous headers persisted %d %v", count, err)
	}
}

func TestStage7FxHTTPReferenceRecovery(t *testing.T) {
	f := realAPI(t, map[string]string{"APP_ROLES": "api,reference-worker", "REFERENCE_TTL": "3s", "REFERENCE_BASE_DELAY": "10ms", "REFERENCE_MAX_DELAY": "20ms"})
	internal := accessToken(t, "wallet-internal", "KC_SECRET_WALLET_INTERNAL")
	a := accessToken(t, "provider-a", "KC_SECRET_PROVIDER_A")
	b := accessToken(t, "provider-b", "KC_SECRET_PROVIDER_B")
	w := apiOpen(t, f, internal, "100.00")
	body := wagerBody(w, "provider-a", "refund", "10.00", "REFUND")
	body = strings.TrimSuffix(body, "}") + `,"referenceExternalTransactionId":"bet"}`
	pending := apiCall(t, f, "POST", "/wagering/transactions", a, "refund", body, 202)
	id := pending["transactionId"].(string)
	apiCall(t, f, "POST", "/wagering/transactions", a, "bet", wagerBody(w, "provider-a", "bet", "10.00", "BET"), 201)
	deadline := time.Now().Add(4 * time.Second)
	for {
		got := apiCall(t, f, "GET", "/wagering/transactions/"+id, a, "", "", 200)
		if got["status"] == "PROCESSED" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Fx worker did not recover")
		}
		time.Sleep(30 * time.Millisecond)
	}
	apiCall(t, f, "GET", "/wagering/transactions/"+id, b, "", "", 404)
	apiCall(t, f, "POST", "/wagering/transactions", b, "refund", body, 403)
	replay := apiCall(t, f, "POST", "/wagering/transactions", a, "refund", body, 200)
	if replay["idempotentReplay"] != true || replay["balance"].(map[string]any)["amount"] != "100.00" {
		t.Fatal("resolved HTTP replay")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := f.app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
