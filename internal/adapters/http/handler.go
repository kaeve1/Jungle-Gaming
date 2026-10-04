// Package httpapi exposes the authenticated HTTP contract without depending on Fx.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jungle-gaming-backend/internal/application"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
)

type Readiness interface {
	Check(context.Context) map[string]string
}
type Handler struct {
	metrics     http.Handler
	diagnostics application.Diagnostics
	service     *application.Service
	reader      application.Reader
	auth        application.TokenVerifier
	ready       Readiness
	ids         domain.IDGenerator
	log         *slog.Logger
}

var externalID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func NewHandler(service *application.Service, reader application.Reader, auth application.TokenVerifier, ready Readiness, ids domain.IDGenerator, log *slog.Logger, metrics http.Handler, diagnostics application.Diagnostics) http.Handler {
	h := &Handler{service: service, reader: reader, auth: auth, ready: ready, ids: ids, log: log}
	h.metrics = metrics
	h.diagnostics = diagnostics
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /health/ready", h.readiness)
	mux.HandleFunc("POST /wallets", h.guard("wallet:admin", true, h.open))
	mux.HandleFunc("GET /wallets/{walletId}", h.guard("wallet:admin", true, h.wallet))
	mux.HandleFunc("GET /wallets/{walletId}/ledger", h.guard("wallet:admin", true, h.ledger))
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", h.guard("wallet:admin", true, h.reconciliation))
	mux.HandleFunc("GET /metrics", h.guard("wallet:admin", true, func(w http.ResponseWriter, r *http.Request, p application.Principal) {
		if h.metrics == nil {
			fail(w, 503, "METRICS_UNAVAILABLE", "metrics unavailable")
			return
		}
		h.metrics.ServeHTTP(w, r)
	}))
	mux.HandleFunc("POST /wagering/transactions", h.guard("wagering:write", false, h.wager))
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", h.guard("wagering:read", false, h.transaction))
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", h.guard("wagering:read", false, h.externalTransaction))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlation := r.Header.Get("X-Correlation-Id")
		if correlation == "" {
			correlation = h.ids.NewID().String()
		}
		w.Header().Set("X-Correlation-Id", correlation)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if len(r.Header.Values("X-Correlation-Id")) > 1 || !externalID.MatchString(correlation) {
			fail(w, 400, "VALIDATION_ERROR", "invalid correlation header")
			return
		}
		defer func() {
			if recover() != nil {
				h.log.ErrorContext(r.Context(), "HTTP handler panic", "correlationId", correlation)
				fail(w, 500, "INTERNAL_ERROR", "unexpected error")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

func (h *Handler) reconciliation(w http.ResponseWriter, r *http.Request, p application.Principal) {
	id, err := domain.ParseID(r.PathValue("walletId"))
	if err != nil {
		h.inputError(w, err)
		return
	}
	out, err := h.reader.Reconcile(r.Context(), id)
	if err != nil {
		h.error(w, r, err)
		return
	}
	fields := []any{"walletId", id.String(), "correlationId", w.Header().Get("X-Correlation-Id"), "consistent", out.Consistent, "checkedEntries", out.CheckedEntries}
	if !out.Consistent {
		h.log.ErrorContext(r.Context(), "wallet reconciliation divergence", fields...)
		if h.diagnostics != nil {
			h.diagnostics.Count("reconciliation_divergence")
		}
	} else {
		h.log.InfoContext(r.Context(), "wallet reconciliation checked", fields...)
	}
	writeJSON(w, 200, out)
}

func (h *Handler) guard(role string, internal bool, next func(http.ResponseWriter, *http.Request, application.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			unauthenticated(w)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			unauthenticated(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		p, err := h.auth.Verify(ctx, parts[1])
		if err != nil {
			unauthenticated(w)
			return
		}
		if !p.Has(role) || (internal && !p.Internal) || (!internal && !p.Internal && p.ProviderID == "") || (!internal && role == "wagering:write" && p.ProviderID == "") {
			fail(w, 403, "FORBIDDEN", "permission denied")
			return
		}
		next(w, r, p)
	}
}
func unauthenticated(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="wallet-api"`)
	fail(w, 401, "UNAUTHENTICATED", "valid access token required")
}
func trace(w http.ResponseWriter) application.Trace {
	return application.Trace{CorrelationID: w.Header().Get("X-Correlation-Id")}
}

func (h *Handler) open(w http.ResponseWriter, r *http.Request, p application.Principal) {
	var body struct {
		PlayerID string      `json:"playerId"`
		Initial  money.Money `json:"initialBalance"`
	}
	if err := strictJSON(w, r, &body, "playerId", "initialBalance"); err != nil {
		h.inputError(w, err)
		return
	}
	player, err := domain.ParseID(body.PlayerID)
	if err != nil {
		h.inputError(w, err)
		return
	}
	result, err := h.service.OpenWallet(r.Context(), player, body.Initial, trace(w))
	if errors.Is(err, application.ErrWalletConflict) {
		existing, readErr := h.reader.WalletForPlayer(r.Context(), player, body.Initial.Currency())
		if readErr != nil {
			h.error(w, r, readErr)
			return
		}
		writeJSON(w, 409, map[string]any{"error": map[string]any{"code": "WALLET_ALREADY_EXISTS", "message": "wallet already exists", "details": []map[string]string{{"walletId": existing.ID.String()}}}})
		return
	}
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, 201, walletJSON(result))
}
func (h *Handler) wallet(w http.ResponseWriter, r *http.Request, p application.Principal) {
	id, err := domain.ParseID(r.PathValue("walletId"))
	if err != nil {
		h.inputError(w, err)
		return
	}
	result, err := h.reader.Wallet(r.Context(), id)
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, 200, walletJSON(result))
}
func (h *Handler) wager(w http.ResponseWriter, r *http.Request, p application.Principal) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) == 0 || keys[0] == "" {
		fail(w, 400, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key is required")
		return
	}
	if len(keys) != 1 {
		fail(w, 400, "VALIDATION_ERROR", "exactly one Idempotency-Key is required")
		return
	}
	var body struct {
		Provider  string      `json:"providerId"`
		External  string      `json:"externalTransactionId"`
		Player    string      `json:"playerId"`
		Wallet    string      `json:"walletId"`
		Round     string      `json:"roundId"`
		Game      string      `json:"gameId"`
		Kind      string      `json:"kind"`
		Money     money.Money `json:"money"`
		Reference string      `json:"referenceExternalTransactionId"`
	}
	if err := strictJSON(w, r, &body, "providerId", "externalTransactionId", "playerId", "walletId", "roundId", "gameId", "kind", "money", "referenceExternalTransactionId"); err != nil {
		h.inputError(w, err)
		return
	}
	if body.Provider != p.ProviderID {
		fail(w, 403, "PROVIDER_MISMATCH", "provider differs from authenticated identity")
		return
	}
	req, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: p.ProviderID, ExternalTransactionID: body.External, IdempotencyKey: keys[0], PlayerID: body.Player, WalletID: body.Wallet, RoundID: body.Round, GameID: body.Game, Kind: body.Kind, Money: body.Money, ReferenceExternalTransactionID: body.Reference})
	if err != nil {
		h.inputError(w, err)
		return
	}
	result, err := h.service.ProcessWager(r.Context(), req, trace(w))
	if err != nil {
		h.error(w, r, err)
		return
	}
	status := 201
	switch result.Transaction.Status {
	case domain.StatusProcessed:
		if result.IdempotentReplay {
			status = 200
		}
	case domain.StatusRejected:
		status = 422
	case domain.StatusPendingReference:
		status = 202
	case domain.StatusFailed:
		status = 500
	default:
		status = 500
	}
	writeJSON(w, status, wagerResponse{TransactionID: result.Transaction.ID, Status: result.Transaction.Status, Balance: result.Transaction.ResultBalance, FailureCode: stringPointer(string(result.Transaction.FailureCode)), IdempotentReplay: result.IdempotentReplay})
}
func (h *Handler) transaction(w http.ResponseWriter, r *http.Request, p application.Principal) {
	id, err := domain.ParseID(r.PathValue("transactionId"))
	if err != nil {
		h.inputError(w, err)
		return
	}
	provider := p.ProviderID
	if p.Internal {
		provider = ""
	}
	s, err := h.reader.Transaction(r.Context(), id, provider)
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, 200, transactionJSON(s))
}
func (h *Handler) externalTransaction(w http.ResponseWriter, r *http.Request, p application.Principal) {
	provider, ext := r.PathValue("providerId"), r.PathValue("externalTransactionId")
	if !p.Internal && provider != p.ProviderID {
		fail(w, 403, "PROVIDER_MISMATCH", "provider differs from authenticated identity")
		return
	}
	if !externalID.MatchString(provider) || !externalID.MatchString(ext) {
		h.inputError(w, errJSON)
		return
	}
	s, err := h.reader.ExternalTransaction(r.Context(), provider, ext)
	if err != nil {
		h.error(w, r, err)
		return
	}
	writeJSON(w, 200, transactionJSON(s))
}
func (h *Handler) ledger(w http.ResponseWriter, r *http.Request, p application.Principal) {
	id, err := domain.ParseID(r.PathValue("walletId"))
	if err != nil {
		h.inputError(w, err)
		return
	}
	query, err := parseQuery(r)
	if err != nil {
		h.inputError(w, err)
		return
	}
	limit := 50
	if value := query["limit"]; value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 200 {
			h.inputError(w, errJSON)
			return
		}
	}
	page, err := h.reader.Ledger(r.Context(), id, query["cursor"], limit)
	if err != nil {
		h.error(w, r, err)
		return
	}
	entries := make([]ledgerEntryResponse, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, ledgerEntryResponse{ID: e.ID, TransactionID: e.TransactionID, Direction: e.Direction, Money: e.Amount, BalanceBefore: e.BalanceBefore, BalanceAfter: e.BalanceAfter, WalletVersion: e.WalletVersion, CreatedAt: stamp(e.CreatedAt)})
	}
	writeJSON(w, 200, ledgerResponse{WalletID: id, Entries: entries, NextCursor: stringPointer(page.NextCursor)})
}
func parseQuery(r *http.Request) (map[string]string, error) {
	if len(r.URL.RawQuery) > 2048 {
		return nil, errJSON
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errJSON
	}
	out := map[string]string{}
	for k, v := range values {
		if (k != "limit" && k != "cursor") || len(v) != 1 || v[0] == "" {
			return nil, errJSON
		}
		out[k] = v[0]
	}
	return out, nil
}
func (h *Handler) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := h.ready.Check(ctx)
	status, state := 200, "ok"
	for _, v := range checks {
		if v != "ok" {
			status, state = 503, "unavailable"
		}
	}
	writeJSON(w, status, map[string]any{"status": state, "checks": checks})
}
