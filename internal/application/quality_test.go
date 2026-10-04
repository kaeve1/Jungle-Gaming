package application

import (
	"context"
	"errors"
	"jungle-gaming-backend/internal/domain"
	"jungle-gaming-backend/internal/domain/money"
	"testing"
	"time"
)

type incompatibleAuditUOW struct {
	first ReferenceSession
	calls int
}
type basicSession struct{ Session }

func (u *incompatibleAuditUOW) Within(ctx context.Context, fn func(context.Context, Session) error) error {
	u.calls++
	if u.calls == 1 {
		return fn(ctx, u.first)
	}
	return fn(ctx, basicSession{})
}

type failedClaim struct {
	Session
	wallet *domain.Wallet
	tx     *domain.WagerTransaction
}

func (f failedClaim) ClaimReference(context.Context) (*domain.Wallet, *domain.WagerTransaction, Trace, error) {
	return f.wallet, f.tx, Trace{}, domain.ErrInvariant
}
func (failedClaim) UpdateReference(context.Context, *domain.WagerTransaction) error {
	return domain.ErrInvariant
}
func (failedClaim) RecordReferenceFailure(context.Context, domain.ID, domain.ID, time.Duration) (domain.Status, error) {
	return "", domain.ErrInvariant
}

func TestReferenceAuditRejectsIncompatibleSessionWithoutPanic(t *testing.T) {
	id, err := domain.ParseID("0192f291-27dd-7d3f-8071-5f8685deef37")
	if err != nil {
		t.Fatal(err)
	}
	amount, err := money.Parse("10.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	wallet, err := domain.RehydrateWallet(domain.WalletSnapshot{ID: id, PlayerID: id, Balance: amount, Version: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	req, err := domain.NewExternalRequest(domain.RawExternalRequest{ProviderID: "provider-a", ExternalTransactionID: "refund", IdempotencyKey: "key", PlayerID: id.String(), WalletID: id.String(), RoundID: "round", GameID: "game", Kind: "REFUND", Money: amount, ReferenceExternalTransactionID: "bet"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := domain.NewExternalTransaction(id, req, now)
	if err != nil {
		t.Fatal(err)
	}
	uow := &incompatibleAuditUOW{first: failedClaim{wallet: wallet, tx: tx}}
	s := Service{uow: uow, policy: domain.DefaultReferencePolicy}
	worked, err := s.ResumeReference(t.Context())
	if worked || !errors.Is(err, domain.ErrInvariant) || uow.calls != 2 {
		t.Fatalf("worked=%v err=%v calls=%d", worked, err, uow.calls)
	}
}
