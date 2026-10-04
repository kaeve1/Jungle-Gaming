package httpapi

import (
	"encoding/json"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
	"net/http"
	"time"
)

// Pointer fields intentionally encode null when absent: the external contract
// distinguishes absence from an empty string. Do not use omitempty here.
type walletResponse struct {
	ID        domain.ID   `json:"id"`
	PlayerID  domain.ID   `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt string      `json:"createdAt"`
	UpdatedAt string      `json:"updatedAt"`
}

type wagerResponse struct {
	TransactionID    domain.ID     `json:"transactionId"`
	Status           domain.Status `json:"status"`
	Balance          *money.Money  `json:"balance"`
	FailureCode      *string       `json:"failureCode"`
	IdempotentReplay bool          `json:"idempotentReplay"`
}

type transactionResponse struct {
	TransactionID                  domain.ID     `json:"transactionId"`
	Origin                         domain.Origin `json:"origin"`
	ProviderID                     *string       `json:"providerId"`
	ExternalTransactionID          *string       `json:"externalTransactionId"`
	WalletID                       domain.ID     `json:"walletId"`
	PlayerID                       domain.ID     `json:"playerId"`
	RoundID                        *string       `json:"roundId"`
	GameID                         *string       `json:"gameId"`
	Kind                           domain.Kind   `json:"kind"`
	Money                          money.Money   `json:"money"`
	ReferenceExternalTransactionID *string       `json:"referenceExternalTransactionId"`
	ReferenceTransactionID         *domain.ID    `json:"referenceTransactionId"`
	Status                         domain.Status `json:"status"`
	FailureCode                    *string       `json:"failureCode"`
	ResultBalance                  *money.Money  `json:"resultBalance"`
	ReferenceAttempts              int           `json:"referenceAttempts"`
	NextReferenceAttemptAt         *string       `json:"nextReferenceAttemptAt"`
	CreatedAt                      string        `json:"createdAt"`
	UpdatedAt                      string        `json:"updatedAt"`
}

type ledgerEntryResponse struct {
	ID            domain.ID        `json:"id"`
	TransactionID domain.ID        `json:"transactionId"`
	Direction     domain.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
	CreatedAt     string           `json:"createdAt"`
}

type ledgerResponse struct {
	WalletID   domain.ID             `json:"walletId"`
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor *string               `json:"nextCursor"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func stringPointer(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func idPointer(id domain.ID) *domain.ID {
	if id.IsZero() {
		return nil
	}
	return &id
}
func timePointer(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	return stringPointer(stamp(t))
}

func walletJSON(w domain.WalletSnapshot) walletResponse {
	return walletResponse{ID: w.ID, PlayerID: w.PlayerID, Balance: w.Balance, Version: w.Version, CreatedAt: stamp(w.CreatedAt), UpdatedAt: stamp(w.UpdatedAt)}
}

func transactionJSON(s domain.TransactionSnapshot) transactionResponse {
	out := transactionResponse{TransactionID: s.ID, Origin: s.Origin, WalletID: s.WalletID, PlayerID: s.PlayerID, Kind: s.Kind, Money: s.Money, ReferenceTransactionID: idPointer(s.ReferenceTransactionID), Status: s.Status, FailureCode: stringPointer(string(s.FailureCode)), ResultBalance: s.ResultBalance, ReferenceAttempts: s.ReferenceAttempts, NextReferenceAttemptAt: timePointer(s.NextReferenceAttemptAt), CreatedAt: stamp(s.CreatedAt), UpdatedAt: stamp(s.UpdatedAt)}
	if e := s.External; e != nil {
		out.ProviderID = stringPointer(e.ProviderID)
		out.ExternalTransactionID = stringPointer(e.ExternalTransactionID)
		out.RoundID = stringPointer(e.RoundID)
		out.GameID = stringPointer(e.GameID)
		out.ReferenceExternalTransactionID = stringPointer(e.ReferenceExternalTransactionID)
	}
	return out
}

func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	b, err := json.Marshal(value)
	if err != nil {
		status = 500
		b = []byte(`{"error":{"code":"INTERNAL_ERROR","message":"response unavailable"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}
