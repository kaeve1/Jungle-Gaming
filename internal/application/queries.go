package application

import (
	"context"
	"errors"

	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
)

var (
	ErrTransactionNotFound = errors.New("transaction not found")
	ErrInvalidCursor       = errors.New("invalid ledger cursor")
	ErrUnauthenticated     = errors.New("unauthenticated")
)

type Principal struct {
	ProviderID  string
	Internal    bool
	Permissions []string
}

func (p Principal) Has(role string) bool {
	for _, r := range p.Permissions {
		if r == role {
			return true
		}
	}
	return false
}

type TokenVerifier interface {
	Verify(context.Context, string) (Principal, error)
}

type LedgerPage struct {
	Entries    []domain.LedgerEntryData
	NextCursor string
}

type Reconciliation struct {
	WalletID          domain.ID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

// Readers are called only after transport authorization. An empty provider on
// Transaction is reserved for the internal service, allowing internal OPENING.
type Reader interface {
	Reconcile(context.Context, domain.ID) (Reconciliation, error)
	Wallet(context.Context, domain.ID) (domain.WalletSnapshot, error)
	WalletForPlayer(context.Context, domain.ID, money.Currency) (domain.WalletSnapshot, error)
	Transaction(context.Context, domain.ID, string) (domain.TransactionSnapshot, error)
	ExternalTransaction(context.Context, string, string) (domain.TransactionSnapshot, error)
	Ledger(context.Context, domain.ID, string, int) (LedgerPage, error)
}
